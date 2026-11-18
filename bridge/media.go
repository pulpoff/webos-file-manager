package main

// Media helpers: stream info, thumbnails and subtitles, from ffmpeg, and video
// thumbnails from webOS's own thumbnailer when it can. Every ffmpeg job runs at
// nice 19 so it never competes with the UI or playback, and nothing here touches
// the TV's hardware decoders.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/text/encoding/charmap"
)

var (
	videoExt = set(".mp4", ".mkv", ".avi", ".mov", ".m4v", ".ts", ".m2ts", ".mts", ".wmv", ".flv", ".webm", ".mpg", ".mpeg", ".vob", ".3gp")
	imageExt = set(".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp", ".heic", ".tif", ".tiff")
	audioExt = set(".mp3", ".wav", ".flac", ".aac", ".ogg", ".m4a", ".wma", ".opus", ".ac3", ".dts", ".mka")
	subExt   = set(".srt", ".ass", ".ssa", ".vtt")
)

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func extOf(name string) string { return strings.ToLower(path.Ext(name)) }

type media struct {
	ffmpeg, ffprobe, nice string
	luna                  string // luna-send, for webOS's own video thumbnailer
	cache                 string
	port                  int
	imgSem, vidSem        chan struct{}
	probeMu               sync.Mutex
	probes                map[string]*Probe
	subMu                 sync.Mutex
	subKey                string
	subCancel             context.CancelFunc
}

func findTool(name string, dirs ...string) string {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode()&0111 != 0 {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func fileKey(src, rel string, e Entry, extra string) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s", src, rel, e.Size, e.Mtime, extra)))
	return hex.EncodeToString(h[:])
}

// input is what ffmpeg reads: the file itself on USB, otherwise the bridge's own stream URL.
func (m *media) input(src, rel string, fs FS) string {
	if p, ok := fs.LocalPath(rel); ok {
		return p
	}
	// The trailing parameter keeps ffmpeg from treating ".jpg" URLs as image-sequence patterns.
	return fmt.Sprintf("http://127.0.0.1:%d/api/file?src=%s&path=%s&via=ffmpeg", m.port, url.QueryEscape(src), url.QueryEscape(rel))
}

func (m *media) command(ctx context.Context, tool string, args ...string) *exec.Cmd {
	if m.nice != "" {
		return exec.CommandContext(ctx, m.nice, append([]string{"-n", "19", tool}, args...)...)
	}
	return exec.CommandContext(ctx, tool, args...)
}

// ---------- probe ----------

type VideoInfo struct {
	Codec  string  `json:"codec"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	FPS    float64 `json:"fps,omitempty"`
	HDR    string  `json:"hdr,omitempty"`
}

type AudioInfo struct {
	Codec    string `json:"codec"`
	Lang     string `json:"lang,omitempty"`
	Title    string `json:"title,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

type SubInfo struct {
	ID       string `json:"id"`
	Codec    string `json:"codec"`
	Lang     string `json:"lang,omitempty"`
	Title    string `json:"title,omitempty"`
	Text     bool   `json:"text"`
	Forced   bool   `json:"forced,omitempty"`
	Default  bool   `json:"default,omitempty"`
	External bool   `json:"external,omitempty"`
	stream   int    // position among the file's subtitle streams (0:s:N)
}

type Probe struct {
	Duration float64     `json:"duration"`
	Bitrate  int64       `json:"bitrate,omitempty"`
	Video    *VideoInfo  `json:"video,omitempty"`
	Audio    []AudioInfo `json:"audio"`
	Subs     []SubInfo   `json:"subs"`
}

var textSubCodecs = set("subrip", "ass", "ssa", "webvtt", "mov_text", "text", "srt", "microdvd", "subviewer", "subviewer1", "sami", "realtext", "jacosub", "mpl2", "pjs", "vplayer", "stl")

func (m *media) probe(ctx context.Context, src, rel string, fs FS, st Entry) (*Probe, error) {
	key := fileKey(src, rel, st, "probe")
	m.probeMu.Lock()
	if p, ok := m.probes[key]; ok {
		m.probeMu.Unlock()
		return p, nil
	}
	m.probeMu.Unlock()
	if m.ffprobe == "" {
		return nil, fmt.Errorf("ffprobe is not installed")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	out, err := m.command(ctx, m.ffprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", m.input(src, rel, fs)).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %v", err)
	}
	var raw struct {
		Format struct {
			Duration string `json:"duration"`
			BitRate  string `json:"bit_rate"`
		} `json:"format"`
		Streams []struct {
			CodecType   string            `json:"codec_type"`
			CodecName   string            `json:"codec_name"`
			Width       int               `json:"width"`
			Height      int               `json:"height"`
			Channels    int               `json:"channels"`
			AvgRate     string            `json:"avg_frame_rate"`
			Transfer    string            `json:"color_transfer"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	p := &Probe{Audio: []AudioInfo{}, Subs: []SubInfo{}}
	p.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	p.Bitrate, _ = strconv.ParseInt(raw.Format.BitRate, 10, 64)
	subN := 0
	for _, s := range raw.Streams {
		lang, title := s.Tags["language"], s.Tags["title"]
		if lang == "und" {
			lang = ""
		}
		switch s.CodecType {
		case "video":
			if s.Disposition["attached_pic"] == 1 || p.Video != nil {
				continue
			}
			v := &VideoInfo{Codec: s.CodecName, Width: s.Width, Height: s.Height}
			if a, b, ok := strings.Cut(s.AvgRate, "/"); ok {
				x, _ := strconv.ParseFloat(a, 64)
				y, _ := strconv.ParseFloat(b, 64)
				if y > 0 {
					v.FPS = float64(int(x/y*100+0.5)) / 100
				}
			}
			switch s.Transfer {
			case "smpte2084":
				v.HDR = "HDR10"
			case "arib-std-b67":
				v.HDR = "HLG"
			}
			p.Video = v
		case "audio":
			p.Audio = append(p.Audio, AudioInfo{Codec: s.CodecName, Lang: lang, Title: title, Channels: s.Channels, Default: s.Disposition["default"] == 1})
		case "subtitle":
			p.Subs = append(p.Subs, SubInfo{
				ID: "s" + strconv.Itoa(subN), Codec: s.CodecName, Lang: lang, Title: title,
				Text: textSubCodecs[s.CodecName], Forced: s.Disposition["forced"] == 1, Default: s.Disposition["default"] == 1, stream: subN,
			})
			subN++
		}
	}
	m.probeMu.Lock()
	if len(m.probes) > 400 {
		m.probes = map[string]*Probe{}
	}
	m.probes[key] = p
	m.probeMu.Unlock()
	return p, nil
}

// sidecarSubs finds Movie.srt, Movie.rus.srt, Movie.en.forced.ass ... next to Movie.mkv.
func sidecarSubs(ctx context.Context, fs FS, rel string) []SubInfo {
	dir, name := path.Split(rel)
	stem := strings.ToLower(strings.TrimSuffix(name, path.Ext(name)))
	es, err := fs.List(ctx, dir)
	if err != nil {
		return nil
	}
	var out []SubInfo
	for _, e := range es {
		ext := extOf(e.Name)
		if e.Dir || !subExt[ext] {
			continue
		}
		lower := strings.ToLower(e.Name)
		if !strings.HasPrefix(lower, stem) {
			continue
		}
		mid := strings.Trim(strings.TrimSuffix(e.Name[len(stem):], e.Name[len(e.Name)-len(ext):]), ". _-")
		out = append(out, SubInfo{ID: "f:" + e.Name, Codec: strings.TrimPrefix(ext, "."), Lang: mid, Title: e.Name, Text: true, External: true, Forced: strings.Contains(strings.ToLower(mid), "forced")})
	}
	return out
}

// ---------- thumbnails ----------

// Frame positions for videos: slot 0 is a file's own thumbnail; a folder's preview
// takes more frames from a video when the folder has fewer than four.
var (
	// Seconds, as webOS's thumbnailer can't tell the length first. Some files have no
	// seek index, and their first thumbnail costs about 1 s per minute of seek on USB.
	lgSeeks   = [4]float64{120, 600, 1200, 1800}
	ffmpegPos = [4]float64{0.1, 0.3, 0.5, 0.7} // of the length, which ffprobe tells
)

// thumb returns the path of a cached JPEG thumbnail, creating it if needed: for
// videos with webOS's own thumbnailer, falling back to ffmpeg, which makes the
// photo and audio ones. slot picks a video's frame (see lgSeeks).
func (m *media) thumb(ctx context.Context, src, rel string, fs FS, st Entry, width, slot int) (string, error) {
	extra := "thumb" + strconv.Itoa(width)
	if slot > 0 {
		extra += "-" + strconv.Itoa(slot)
	}
	key := fileKey(src, rel, st, extra)
	dir := filepath.Join(m.cache, "thumbs", key[:2])
	out := filepath.Join(dir, key+".jpg")
	fail := out + ".fail"
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if fi, err := os.Stat(fail); err == nil && time.Since(fi.ModTime()) < 24*time.Hour {
		return "", fmt.Errorf("no preview")
	}
	ext := extOf(rel)
	lg := videoExt[ext] && m.luna != ""
	if m.ffmpeg == "" && !lg {
		return "", fmt.Errorf("ffmpeg is not installed")
	}
	sem := m.imgSem
	if !imageExt[ext] {
		sem = m.vidSem
	}
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-sem }()
	if _, err := os.Stat(out); err == nil {
		return out, nil // made while we waited
	}
	os.MkdirAll(dir, 0755)
	tmp := out + ".tmp.jpg"
	in := m.input(src, rel, fs)
	var lgErr error
	if lg {
		if lgErr = m.lgThumb(ctx, in, tmp, width, slot); lgErr == nil {
			return out, os.Rename(tmp, out)
		}
		os.Remove(tmp)
		if m.ffmpeg == "" || ctx.Err() != nil {
			if ctx.Err() == nil {
				os.WriteFile(fail, []byte(lgErr.Error()), 0644)
			}
			return "", fmt.Errorf("no preview: %v", lgErr)
		}
	}
	h := width * 9 / 16
	if imageExt[ext] {
		h = width
	}
	scale := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", width, h)
	args := []string{"-v", "error", "-nostdin", "-y"}
	switch {
	case imageExt[ext]:
		if (ext == ".jpg" || ext == ".jpeg") && width <= 480 && st.Size > 1<<20 {
			args = append(args, "-lowres", "2") // decode big JPEGs at quarter size: 10x faster
		}
		args = append(args, "-i", in)
	case videoExt[ext]:
		ss := 30.0 + 600*float64(slot)
		if p, err := m.probe(ctx, src, rel, fs, st); err == nil && p.Duration > 0 {
			ss = p.Duration * ffmpegPos[slot]
			if slot == 0 && ss > 600 {
				ss = 600
			}
		}
		args = append(args, "-threads", "2", "-ss", strconv.FormatFloat(ss, 'f', 1, 64), "-i", in, "-map", "0:V:0")
	default: // audio: embedded cover art
		args = append(args, "-i", in, "-map", "0:v:0")
	}
	args = append(args, "-frames:v", "1", "-vf", scale, "-q:v", "5", "-update", "1", "-f", "image2", tmp)
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := m.command(cctx, m.ffmpeg, args...)
	cmd.Stderr = &stderr
	err := cmd.Run()
	if fi, serr := os.Stat(tmp); err == nil && serr == nil && fi.Size() > 0 {
		return out, os.Rename(tmp, out)
	}
	os.Remove(tmp)
	if lgErr != nil {
		stderr.WriteString("\n" + lgErr.Error())
	}
	if ctx.Err() == nil { // remember real failures, not abandoned requests
		os.WriteFile(fail, stderr.Bytes(), 0644)
	}
	return "", fmt.Errorf("no preview: %v %s", err, strings.TrimSpace(stderr.String()))
}

// lgReply is the answer of webOS's video thumbnailer.
type lgReply struct {
	ReturnValue    bool   `json:"returnValue"`
	ErrorText      string `json:"errorText"`
	OriginalWidth  int    `json:"originalWidth"`
	OriginalHeight int    `json:"originalHeight"`
}

// lgThumb has webOS's video thumbnailer (com.webos.service.videothumbnailer, the
// one LG's Media Player uses) write a video's frame as a JPEG of at most
// width x width*9/16, in the video's own shape. It decodes with LG's own libav,
// in about 0.1 s for a file on USB; for SMB and SFTP it reads the bridge's stream
// URL. It can't tell a video's length, so a seek past the end is retried nearer
// the start.
func (m *media) lgThumb(ctx context.Context, in, out string, width, slot int) error {
	call := func(seek float64, w, h int) (lgReply, error) {
		var r lgReply
		var params bytes.Buffer
		enc := json.NewEncoder(&params)
		enc.SetEscapeHTML(false) // keep the stream URL's & as it is
		enc.Encode(map[string]interface{}{"videoPath": in, "outputPath": out, "imageType": "jpg",
			"width": w, "height": h, "seekTime": seek})
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		b, err := exec.CommandContext(cctx, m.luna, "-n", "1", "luna://com.webos.service.videothumbnailer/getThumbnail",
			strings.TrimSpace(params.String())).Output()
		if i := bytes.IndexByte(b, '{'); i >= 0 {
			if jerr := json.Unmarshal(b[i:], &r); jerr != nil && err == nil {
				err = jerr
			}
		} else if err == nil {
			err = fmt.Errorf("no answer from the thumbnailer")
		}
		if err == nil && !r.ReturnValue {
			err = fmt.Errorf("thumbnailer: %s", r.ErrorText)
		}
		return r, err
	}
	h := width * 9 / 16
	seek := lgSeeks[slot]
	for {
		r, err := call(seek, width, h)
		if err != nil && strings.Contains(r.ErrorText, "seek") && seek > 0 {
			if seek /= 4; seek < 8 {
				seek = 0
			}
			continue
		}
		if err != nil {
			return err
		}
		// It scales to exactly the size asked for, so other shapes than 16:9 are
		// asked again in their own; that's quick, as it keeps the decoder open.
		if ow, oh := r.OriginalWidth, r.OriginalHeight; ow > 0 && oh > 0 {
			w2, h2 := width, width*oh/ow
			if h2 > h {
				w2, h2 = h*ow/oh, h
			}
			w2, h2 = w2&^1, h2&^1
			if w2 < width-2 || h2 < h-2 {
				_, err = call(seek, w2, h2)
			}
		}
		return err
	}
}

// ---------- folder previews ----------

type pick struct {
	rel string
	e   Entry
}

// hiddenName tells the system and dot files and folders that previews skip.
func hiddenName(n string) bool {
	switch strings.ToLower(n) {
	case "$recycle.bin", "system volume information", "lost+found", "found.000":
		return true
	}
	return strings.HasPrefix(n, ".")
}

// mediaPicks chooses up to four of a folder's files for its preview: its videos
// in name order, then, when photos is set, its photos.
func mediaPicks(rel string, es []Entry, photos bool) []pick {
	var vids, pics []pick
	for _, e := range es {
		if e.Dir || hiddenName(e.Name) {
			continue
		}
		if x := extOf(e.Name); videoExt[x] {
			vids = append(vids, pick{path.Join(rel, e.Name), e})
		} else if photos && imageExt[x] {
			pics = append(pics, pick{path.Join(rel, e.Name), e})
		}
	}
	sort.SliceStable(vids, func(i, j int) bool { return strings.ToLower(vids[i].rel) < strings.ToLower(vids[j].rel) })
	sort.SliceStable(pics, func(i, j int) bool { return strings.ToLower(pics[i].rel) < strings.ToLower(pics[j].rel) })
	picks := append(vids, pics...)
	if len(picks) > 4 {
		picks = picks[:4]
	}
	return picks
}

// Folder previews of folders that only hold folders look this deep, and list at
// most this many of them: a series' seasons, or a photo library's years and albums.
const (
	previewDepth = 2
	previewLists = 12
)

// subfolders returns the first n subfolders of rel, in the order the app shows them.
func subfolders(rel string, es []Entry, n int) []string {
	sortEntries(es)
	var out []string
	for _, e := range es {
		if e.Dir && !hiddenName(e.Name) && len(out) < n {
			out = append(out, path.Join(rel, e.Name))
		}
	}
	return out
}

// folderPicks chooses the files of a folder's preview: its own videos, else its
// photos (photo thumbnails need ffmpeg). A folder with neither takes them from its
// subfolders, breadth first: four subfolders a level, previewDepth levels down,
// previewLists listings at most.
func (m *media) folderPicks(ctx context.Context, fs FS, rel string) ([]pick, error) {
	es, err := fs.List(ctx, rel)
	if err != nil {
		return nil, err
	}
	photos := m.ffmpeg != ""
	if picks := mediaPicks(rel, es, photos); len(picks) > 0 {
		return picks, nil
	}
	type dir struct {
		rel   string
		depth int
	}
	var queue []dir
	for _, s := range subfolders(rel, es, 4) {
		queue = append(queue, dir{s, 1})
	}
	var picks []pick
	for listed := 0; len(queue) > 0 && listed < previewLists; listed++ {
		d := queue[0]
		queue = queue[1:]
		ses, err := fs.List(ctx, d.rel)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		picks = append(picks, mediaPicks(d.rel, ses, photos)...)
		if len(picks) >= 4 {
			return picks[:4], nil
		}
		if d.depth < previewDepth {
			for _, s := range subfolders(d.rel, ses, 4) {
				queue = append(queue, dir{s, d.depth + 1})
			}
		}
	}
	return picks, nil
}

// folderThumb returns a folder's preview, as LG's Media Player shows folders: a
// 2x2 collage of frames from the files folderPicks chooses. With fewer than four
// files, the videos give more frames; a single photo fills the whole picture. It
// is cached under a key made of the files and frames it shows, so new or changed
// files make a new one.
func (m *media) folderThumb(ctx context.Context, src, rel string, fs FS, width int) (string, error) {
	picks, err := m.folderPicks(ctx, fs, rel)
	if err != nil {
		return "", err
	}
	if len(picks) == 0 {
		return "", fmt.Errorf("no videos or photos here")
	}
	type frame struct {
		p    pick
		slot int
	}
	var frames []frame
	for _, p := range picks {
		frames = append(frames, frame{p, 0})
	}
	for slot := 1; slot < 4 && len(frames) < 4; slot++ {
		for _, p := range picks {
			if videoExt[extOf(p.rel)] && len(frames) < 4 {
				frames = append(frames, frame{p, slot})
			}
		}
	}
	h := sha1.New()
	fmt.Fprintf(h, "folder\x00%s\x00%s\x00%d", src, rel, width)
	for _, f := range frames {
		fmt.Fprintf(h, "\x00%s\x00%d\x00%d\x00%d", f.p.rel, f.p.e.Size, f.p.e.Mtime, f.slot)
	}
	key := hex.EncodeToString(h.Sum(nil))
	dir := filepath.Join(m.cache, "thumbs", key[:2])
	out := filepath.Join(dir, key+".jpg")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	var imgs []image.Image
	for _, f := range frames {
		p, err := m.thumb(ctx, src, f.p.rel, fs, f.p.e, width, f.slot)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			continue
		}
		if img, err := decodeJPEG(p); err == nil {
			imgs = append(imgs, img)
		}
	}
	if len(imgs) == 0 {
		return "", fmt.Errorf("no preview")
	}
	W, H := width, width*9/16
	dst := image.NewRGBA(image.Rect(0, 0, W, H))
	if len(imgs) == 1 {
		cover(dst, dst.Bounds(), imgs[0])
	} else {
		xs, ys := [3]int{0, W / 2, W}, [3]int{0, H / 2, H}
		for i := 0; i < 4; i++ {
			cover(dst, image.Rect(xs[i%2], ys[i/2], xs[i%2+1], ys[i/2+1]), imgs[i%len(imgs)])
		}
	}
	os.MkdirAll(dir, 0755)
	tmp := out + ".tmp.jpg"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	err = jpeg.Encode(f, dst, &jpeg.Options{Quality: 82})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return out, os.Rename(tmp, out)
}

func decodeJPEG(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return jpeg.Decode(f)
}

// cover scales the middle of src that has r's shape onto r of dst.
func cover(dst *image.RGBA, r image.Rectangle, src image.Image) {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw*r.Dy() > sh*r.Dx() { // wider than r: crop the sides
		w := sh * r.Dx() / r.Dy()
		sb.Min.X += (sw - w) / 2
		sb.Max.X = sb.Min.X + w
	} else { // taller: crop top and bottom
		h := sw * r.Dy() / r.Dx()
		sb.Min.Y += (sh - h) / 2
		sb.Max.Y = sb.Min.Y + h
	}
	xdraw.CatmullRom.Scale(dst, r, src, sb, xdraw.Src, nil)
}

// pruneCache keeps the thumbnail cache under limit bytes, deleting oldest first.
func (m *media) pruneCache(limit int64) {
	type f struct {
		p string
		t time.Time
		n int64
	}
	var files []f
	var total int64
	filepath.Walk(filepath.Join(m.cache, "thumbs"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			files = append(files, f{p, fi.ModTime(), fi.Size()})
			total += fi.Size()
		}
		return nil
	})
	if total <= limit {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].t.Before(files[j].t) })
	for _, x := range files {
		if total <= limit*3/4 {
			break
		}
		if os.Remove(x.p) == nil {
			total -= x.n
		}
	}
}

// ---------- subtitles ----------

type Cue [3]interface{} // start seconds, end seconds, text

// subsDir returns the extraction directory for a file's embedded text
// subtitles. Extraction runs once per file in the background, writes one SRT
// per track as it goes, and marks the directory "done" at the end.
func (m *media) subsDir(src, rel string, fs FS, st Entry, p *Probe, network bool) (string, bool, error) {
	key := fileKey(src, rel, st, "subs")
	dir := filepath.Join(m.cache, "subs", key)
	if _, err := os.Stat(filepath.Join(dir, "done")); err == nil {
		return dir, true, nil
	}
	m.subMu.Lock()
	defer m.subMu.Unlock()
	if m.subKey == key {
		return dir, false, nil
	}
	if m.subCancel != nil {
		m.subCancel() // only one extraction at a time: the file being watched
	}
	os.RemoveAll(dir)
	os.MkdirAll(dir, 0755)
	args := []string{"-v", "error", "-nostdin", "-y"}
	if network {
		args = append(args, "-readrate", "12") // 12x real time leaves the 100 Mbit link to playback
	}
	args = append(args, "-i", m.input(src, rel, fs))
	n := 0
	for _, s := range p.Subs {
		if !s.Text || s.External {
			continue
		}
		args = append(args, "-map", "0:s:"+strconv.Itoa(s.stream), "-c:s", "srt", "-flush_packets", "1", "-f", "srt", filepath.Join(dir, s.ID+".srt"))
		n++
	}
	if n == 0 {
		os.WriteFile(filepath.Join(dir, "done"), nil, 0644)
		return dir, true, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.subKey, m.subCancel = key, cancel
	go func() {
		var stderr bytes.Buffer
		cmd := m.command(ctx, m.ffmpeg, args...)
		cmd.Stderr = &stderr
		err := cmd.Run()
		if ctx.Err() == nil {
			marker := "done"
			if err != nil {
				os.WriteFile(filepath.Join(dir, "error"), stderr.Bytes(), 0644)
			}
			os.WriteFile(filepath.Join(dir, marker), nil, 0644)
		}
		m.subMu.Lock()
		if m.subKey == key {
			m.subKey, m.subCancel = "", nil
		}
		m.subMu.Unlock()
	}()
	return dir, false, nil
}

var cueTime = regexp.MustCompile(`(?:(\d+):)?(\d{1,2}):(\d{2})[,.](\d{1,3})\s*-->\s*(?:(\d+):)?(\d{1,2}):(\d{2})[,.](\d{1,3})`)
var tagRe = regexp.MustCompile(`<[^>]*>|\{[^}]*\}`)

func secs(h, m, s, ms string) float64 {
	hh, _ := strconv.Atoi(h)
	mm, _ := strconv.Atoi(m)
	ss, _ := strconv.Atoi(s)
	for len(ms) < 3 {
		ms += "0"
	}
	mi, _ := strconv.Atoi(ms)
	return float64(hh*3600+mm*60+ss) + float64(mi)/1000
}

func cleanCueText(t string) string {
	t = strings.ReplaceAll(t, `\N`, "\n")
	t = strings.ReplaceAll(t, `\n`, "\n")
	t = strings.ReplaceAll(t, `\h`, " ")
	t = tagRe.ReplaceAllString(t, "")
	lines := strings.Split(t, "\n")
	out := lines[:0]
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// parseSRT parses SRT and WebVTT. When the file is still being written,
// the last cue is dropped unless it is followed by a blank line.
func parseSRT(data string, complete bool) []Cue {
	data = strings.ReplaceAll(strings.TrimPrefix(data, "\uFEFF"), "\r\n", "\n")
	lines := strings.Split(data, "\n")
	var cues []Cue
	for i := 0; i < len(lines); i++ {
		m := cueTime.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		var text []string
		j := i + 1
		for ; j < len(lines) && strings.TrimSpace(lines[j]) != ""; j++ {
			text = append(text, lines[j])
		}
		if j >= len(lines) && !complete {
			break
		}
		if t := cleanCueText(strings.Join(text, "\n")); t != "" {
			cues = append(cues, Cue{secs(m[1], m[2], m[3], m[4]), secs(m[5], m[6], m[7], m[8]), t})
		}
		i = j
	}
	return cues
}

var assTime = regexp.MustCompile(`^(\d+):(\d{2}):(\d{2})[.,](\d{1,3})$`)

func parseASS(data string) []Cue {
	data = strings.ReplaceAll(strings.TrimPrefix(data, "\uFEFF"), "\r\n", "\n")
	format := []string{"layer", "start", "end", "style", "name", "marginl", "marginr", "marginv", "effect", "text"}
	var cues []Cue
	inEvents := false
	for _, l := range strings.Split(data, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			inEvents = strings.EqualFold(l, "[Events]")
			continue
		}
		if !inEvents {
			continue
		}
		if strings.HasPrefix(strings.ToLower(l), "format:") {
			format = nil
			for _, f := range strings.Split(l[7:], ",") {
				format = append(format, strings.ToLower(strings.TrimSpace(f)))
			}
			continue
		}
		if !strings.HasPrefix(l, "Dialogue:") {
			continue
		}
		parts := strings.SplitN(strings.TrimSpace(l[9:]), ",", len(format))
		if len(parts) != len(format) {
			continue
		}
		var start, end float64
		var text string
		for i, f := range format {
			switch f {
			case "start", "end":
				m := assTime.FindStringSubmatch(strings.TrimSpace(parts[i]))
				if m == nil {
					continue
				}
				ms := m[4]
				if len(ms) == 2 {
					ms += "0"
				}
				v := secs(m[1], m[2], m[3], ms)
				if f == "start" {
					start = v
				} else {
					end = v
				}
			case "text":
				text = parts[i]
			}
		}
		if strings.Contains(text, `\p1`) || strings.Contains(text, `\p2`) {
			continue // vector drawing, not text
		}
		if t := cleanCueText(text); t != "" && end > start {
			cues = append(cues, Cue{start, end, t})
		}
	}
	sort.SliceStable(cues, func(i, j int) bool { return cues[i][0].(float64) < cues[j][0].(float64) })
	return cues
}

// decodeText handles the encodings subtitle files come in: UTF-8 (with or
// without BOM), UTF-16, and legacy Windows code pages (1251 for Cyrillic).
func decodeText(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:])
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		le := b[0] == 0xFF
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			if le {
				u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
			} else {
				u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
			}
		}
		return string(utf16.Decode(u))
	case utf8.Valid(b):
		return string(b)
	}
	high := 0
	letters := 0
	for _, c := range b {
		if c >= 0xC0 {
			high++
		}
		if c >= 'A' && c <= 'z' || c >= 0xC0 {
			letters++
		}
	}
	dec := charmap.Windows1252.NewDecoder()
	if letters > 0 && high*100/letters > 30 {
		dec = charmap.Windows1251.NewDecoder()
	}
	out, err := dec.Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(out)
}

func readSidecar(ctx context.Context, fs FS, rel string) ([]Cue, error) {
	f, err := fs.Open(ctx, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(io.NewSectionReader(f, 0, 8<<20), 8<<20))
	if err != nil {
		return nil, err
	}
	text := decodeText(b)
	switch extOf(rel) {
	case ".ass", ".ssa":
		return parseASS(text), nil
	default:
		return parseSRT(text, true), nil
	}
}
