// fmbridge: local HTTP bridge for the webOS File Manager.
// It lists and streams files from USB drives, SMB shares and SFTP servers,
// and makes thumbnails, stream info and subtitles with ffmpeg.
// It only listens on 127.0.0.1 and only answers the TV app.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

const version = "0.6.0"

type server struct {
	root    string
	port    int
	started time.Time
	cfg     *configStore
	keys    *keyStore
	media   *media
	lan     discovery
	mu      sync.Mutex
	conns   map[string]FS // network sources by id
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	debug.SetMemoryLimit(96 << 20)
	root := env("FM_ROOT", "/media/developer/smb-browser")
	listen := env("FM_LISTEN", "127.0.0.1:31231")
	_, portStr, _ := net.SplitHostPort(listen)
	port, _ := strconv.Atoi(portStr)
	exe, _ := os.Executable()
	toolDirs := []string{env("FM_TOOLS", filepath.Join(root, "bin")), filepath.Dir(exe)}

	os.MkdirAll(filepath.Join(root, "cache"), 0755)
	s := &server{
		root: root, port: port, started: time.Now(),
		cfg:   &configStore{path: filepath.Join(root, "sources.json"), legacy: filepath.Join(root, "config.json")},
		keys:  &keyStore{dir: filepath.Join(root, "ssh")},
		conns: map[string]FS{},
		media: &media{
			ffmpeg: findTool("ffmpeg", toolDirs...), ffprobe: findTool("ffprobe", toolDirs...), nice: findTool("nice"),
			cache: filepath.Join(root, "cache"), port: port,
			imgSem: make(chan struct{}, 1), vidSem: make(chan struct{}, 1), probes: map[string]*Probe{},
		},
	}
	s.cfg.load()
	go func() {
		for {
			s.media.pruneCache(250 << 20)
			time.Sleep(6 * time.Hour)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.health)
	mux.HandleFunc("/api/sources", s.sources)
	mux.HandleFunc("/api/sources/save", s.saveSource)
	mux.HandleFunc("/api/sources/delete", s.deleteSource)
	mux.HandleFunc("/api/sources/test", s.testSource)
	mux.HandleFunc("/api/smb/shares", s.listShares)
	mux.HandleFunc("/api/discover", s.discover)
	mux.HandleFunc("/api/sshkey", s.sshKey)
	mux.HandleFunc("/api/list", s.list)
	mux.HandleFunc("/api/file", s.file)
	mux.HandleFunc("/api/thumb", s.thumb)
	mux.HandleFunc("/api/probe", s.probe)
	mux.HandleFunc("/api/subs", s.subs)
	mux.HandleFunc("/api/restart", s.restart)

	log.Printf("fmbridge %s listening on %s (ffmpeg: %q)", version, listen, s.media.ffmpeg)
	srv := &http.Server{Addr: listen, Handler: s.guard(mux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// guard only lets the TV app in: the Host must be loopback (stops DNS
// rebinding) and a browser Origin, if sent, must be the app's file:// page.
func (s *server) guard(next http.Handler) http.Handler {
	okHost := map[string]bool{fmt.Sprintf("127.0.0.1:%d", s.port): true, fmt.Sprintf("localhost:%d", s.port): true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !okHost[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "null" && !strings.HasPrefix(o, "file://") {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Headers", "Content-Type, Range")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readBody(r *http.Request, v interface{}) error {
	if r.Method != http.MethodPost {
		return errors.New("POST required")
	}
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

// fsFor resolves a source id to its filesystem.
func (s *server) fsFor(id string) (FS, bool, error) {
	if strings.HasPrefix(id, "usb:") {
		for _, d := range discoverUSB() {
			if d.ID == id {
				return &localFS{root: d.Mount}, false, nil
			}
		}
		return nil, false, errors.New("USB drive not connected")
	}
	if host, ok := strings.CutPrefix(id, "lan:"); ok {
		// a discovered server: use a saved login for it if there is one
		for _, c := range s.cfg.sources() {
			if c.Kind == "smb" && c.Share == "" && strings.EqualFold(c.Host, host) {
				return s.fsFor(c.ID)
			}
		}
		if !hostRe.MatchString(host) {
			return nil, false, errBadPath
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if fs, ok := s.conns[id]; ok {
			return fs, true, nil
		}
		fs := &smbFS{cfg: SourceConfig{Kind: "smb", Host: host}}
		s.conns[id] = fs
		return fs, true, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if fs, ok := s.conns[id]; ok {
		return fs, true, nil
	}
	c, ok := s.cfg.find(id)
	if !ok {
		return nil, false, errors.New("unknown location")
	}
	var fs FS
	if c.Kind == "sftp" {
		fs = &sftpFS{cfg: c, keys: s.keys}
	} else {
		fs = &smbFS{cfg: c}
	}
	s.conns[id] = fs
	return fs, true, nil
}

func (s *server) dropConn(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fs, ok := s.conns[id]; ok {
		fs.Close()
		delete(s.conns, id)
	}
}

// target parses ?src=&path= into a filesystem and a clean relative path.
func (s *server) target(r *http.Request) (string, FS, string, bool, error) {
	q := r.URL.Query()
	src := q.Get("src")
	rel, err := cleanRel(q.Get("path"))
	if err != nil {
		return "", nil, "", false, err
	}
	fs, network, err := s.fsFor(src)
	return src, fs, rel, network, err
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound
	case errors.Is(err, os.ErrPermission):
		return http.StatusForbidden
	case errors.Is(err, errBadPath):
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

// ---------- handlers ----------

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "version": version, "uptime": int(time.Since(s.started).Seconds()),
		"ffmpeg": s.media.ffmpeg != "", "ffprobe": s.media.ffprobe != "",
	})
}

func (s *server) sources(w http.ResponseWriter, r *http.Request) {
	var net []publicSource
	for _, c := range s.cfg.sources() {
		net = append(net, publicSource{SourceConfig: c, HasPassword: c.Password != ""})
	}
	usb := discoverUSB()
	if usb == nil {
		usb = []usbDrive{}
	}
	if net == nil {
		net = []publicSource{}
	}
	writeJSON(w, 200, map[string]interface{}{"usb": usb, "network": net})
}

type sourceReq struct {
	SourceConfig
	KeepPassword bool `json:"keepPassword"`
}

func (s *server) saveSource(w http.ResponseWriter, r *http.Request) {
	var req sourceReq
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	c, err := s.cfg.normalize(req.SourceConfig, req.KeepPassword)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if c, err = s.cfg.save(c); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.dropConn(c.ID)
	s.dropConn("lan:" + c.Host)
	writeJSON(w, 200, map[string]string{"id": c.ID})
}

func (s *server) deleteSource(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string }
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.dropConn(req.ID)
	if err := s.cfg.remove(req.ID); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) testSource(w http.ResponseWriter, r *http.Request) {
	var req sourceReq
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	c, err := s.cfg.normalize(req.SourceConfig, req.KeepPassword)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var fs FS
	if c.Kind == "sftp" {
		fs = &sftpFS{cfg: c, keys: s.keys}
	} else {
		fs = &smbFS{cfg: c}
	}
	defer fs.Close()
	es, err := fs.List(ctx, "")
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "items": len(es)})
}

func (s *server) listShares(w http.ResponseWriter, r *http.Request) {
	var req sourceReq
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	c := req.SourceConfig
	if req.KeepPassword && c.Password == "" && c.ID != "" {
		if old, ok := s.cfg.find(c.ID); ok {
			c.Password = old.Password
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	names, err := smbShares(ctx, c)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"shares": names})
}

func (s *server) discover(w http.ResponseWriter, r *http.Request) {
	servers, scanning := s.lan.get(r.URL.Query().Get("refresh") == "1")
	type out struct {
		lanServer
		Login string `json:"login,omitempty"` // id of a saved login for this server
	}
	res := []out{}
	for _, sv := range servers {
		o := out{lanServer: sv}
		for _, c := range s.cfg.sources() {
			if c.Kind == "smb" && c.Share == "" && strings.EqualFold(c.Host, sv.Host) {
				o.Login = c.ID
			}
		}
		res = append(res, o)
	}
	writeJSON(w, 200, map[string]interface{}{"servers": res, "scanning": scanning})
}

func (s *server) sshKey(w http.ResponseWriter, r *http.Request) {
	k, err := s.keys.publicKey()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"publicKey": k})
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	_, fs, rel, _, err := s.target(r)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	es, err := fs.List(ctx, rel)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"path": rel, "entries": es})
}

func contentType(name string) string {
	switch ext := extOf(name); ext {
	case ".mkv":
		return "video/x-matroska"
	case ".mka":
		return "audio/x-matroska"
	case ".ts", ".m2ts", ".mts":
		return "video/mp2t"
	case ".srt":
		return "text/plain; charset=utf-8"
	default:
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
	}
	return "application/octet-stream"
}

func (s *server) file(w http.ResponseWriter, r *http.Request) {
	_, fs, rel, _, err := s.target(r)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	st, err := fs.Stat(r.Context(), rel)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	if st.Dir {
		writeErr(w, 400, errors.New("is a folder"))
		return
	}
	f, err := fs.Open(r.Context(), rel)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentType(rel))
	http.ServeContent(w, r, "", time.Unix(st.Mtime, 0), newBufReader(f, st.Size))
}

func (s *server) thumb(w http.ResponseWriter, r *http.Request) {
	src, fs, rel, _, err := s.target(r)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	width, _ := strconv.Atoi(r.URL.Query().Get("w"))
	if width < 64 || width > 1920 {
		width = 320
	}
	st, err := fs.Stat(r.Context(), rel)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	p, err := s.media.thumb(r.Context(), src, rel, fs, st, width)
	if err != nil {
		w.Header().Set("Cache-Control", "max-age=600")
		writeErr(w, 404, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=31536000, immutable") // the URL carries size and mtime
	http.ServeFile(w, r, p)
}

func (s *server) probe(w http.ResponseWriter, r *http.Request) {
	src, fs, rel, _, err := s.target(r)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	st, err := fs.Stat(r.Context(), rel)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	side := sidecarSubs(r.Context(), fs, rel)
	p, err := s.media.probe(r.Context(), src, rel, fs, st)
	if err != nil {
		// still offer subtitle files found next to the video
		writeJSON(w, 200, map[string]interface{}{"error": err.Error(), "audio": []AudioInfo{}, "subs": append([]SubInfo{}, side...)})
		return
	}
	out := *p
	// empty lists go out as [] rather than null: the player iterates them
	out.Subs = append(append([]SubInfo{}, p.Subs...), side...)
	if out.Audio == nil {
		out.Audio = []AudioInfo{}
	}
	writeJSON(w, 200, out)
}

// subs returns cues for one subtitle track. Embedded tracks are extracted in
// the background; until that finishes the reply has done=false and the client
// asks again with from= set to the number of cues it already has.
func (s *server) subs(w http.ResponseWriter, r *http.Request) {
	src, fs, rel, network, err := s.target(r)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	from, _ := strconv.Atoi(q.Get("from"))
	if strings.HasPrefix(id, "f:") {
		name := id[2:]
		if strings.ContainsAny(name, `/\`) {
			writeErr(w, 400, errBadPath)
			return
		}
		cues, err := readSidecar(r.Context(), fs, path.Join(path.Dir(rel), name))
		if err != nil {
			writeErr(w, statusFor(err), err)
			return
		}
		writeJSON(w, 200, map[string]interface{}{"done": true, "cues": tail(cues, from), "total": len(cues)})
		return
	}
	st, err := fs.Stat(r.Context(), rel)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	p, err := s.media.probe(r.Context(), src, rel, fs, st)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	var track *SubInfo
	for i := range p.Subs {
		if p.Subs[i].ID == id {
			track = &p.Subs[i]
		}
	}
	if track == nil || !track.Text {
		writeErr(w, 404, errors.New("this subtitle track is not text, so it cannot be shown"))
		return
	}
	dir, done, err := s.media.subsDir(src, rel, fs, st, p, network)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "done")); err == nil {
		done = true
	}
	b, _ := os.ReadFile(filepath.Join(dir, id+".srt"))
	cues := parseSRT(decodeText(b), done)
	writeJSON(w, 200, map[string]interface{}{"done": done, "cues": tail(cues, from), "total": len(cues)})
}

func tail(c []Cue, from int) []Cue {
	if from < 0 || from > len(c) {
		from = len(c)
	}
	out := c[from:]
	if out == nil {
		out = []Cue{}
	}
	return out
}

// restart lets the app's playback watchdog recycle a stuck bridge;
// run-bridge.sh starts it again within two seconds.
func (s *server) restart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, errors.New("POST required"))
		return
	}
	writeJSON(w, 200, map[string]bool{"restarting": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		log.Printf("restart requested by the app")
		os.Exit(3)
	}()
}
