package main

// Sources: USB drives found in /proc/mounts, plus SMB and SFTP locations from the config.
// Every source is browsed through the same small FS interface.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hirochachacha/go-smb2"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type Entry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

type RFile interface {
	io.ReaderAt
	io.Closer
}

type FS interface {
	List(ctx context.Context, rel string) ([]Entry, error)
	Stat(ctx context.Context, rel string) (Entry, error)
	Open(ctx context.Context, rel string) (RFile, error)
	// LocalPath returns a filesystem path ffmpeg can read directly (USB only).
	LocalPath(rel string) (string, bool)
	Close()
}

var errBadPath = errors.New("invalid path")

// cleanRel turns a client path into a relative path without "." or ".." parts.
func cleanRel(p string) (string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	var out []string
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", errBadPath
		}
		if strings.ContainsRune(part, 0) {
			return "", errBadPath
		}
		out = append(out, part)
	}
	return strings.Join(out, "/"), nil
}

func sortEntries(es []Entry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Dir != es[j].Dir {
			return es[i].Dir
		}
		return strings.ToLower(es[i].Name) < strings.ToLower(es[j].Name)
	})
}

// ---------- USB (local mount) ----------

type localFS struct{ root string }

func (l *localFS) abs(rel string) string { return filepath.Join(l.root, filepath.FromSlash(rel)) }

func (l *localFS) List(ctx context.Context, rel string) ([]Entry, error) {
	des, err := os.ReadDir(l.abs(rel))
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			continue // keep browsing inside the drive
		}
		out = append(out, Entry{Name: de.Name(), Dir: fi.IsDir(), Size: fi.Size(), Mtime: fi.ModTime().Unix()})
	}
	sortEntries(out)
	return out, nil
}

func (l *localFS) Stat(ctx context.Context, rel string) (Entry, error) {
	fi, err := os.Lstat(l.abs(rel))
	if err != nil {
		return Entry{}, err
	}
	return Entry{Name: fi.Name(), Dir: fi.IsDir(), Size: fi.Size(), Mtime: fi.ModTime().Unix()}, nil
}

func (l *localFS) Open(ctx context.Context, rel string) (RFile, error) { return os.Open(l.abs(rel)) }
func (l *localFS) LocalPath(rel string) (string, bool)                 { return l.abs(rel), true }
func (l *localFS) Close()                                               {}

type usbDrive struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Dev   string `json:"dev"`
	FS    string `json:"fstype"`
	Mount string `json:"-"`
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
}

var blkidLine = regexp.MustCompile(`^(\S+):(.*)$`)
var blkidKV = regexp.MustCompile(`(\w+)="([^"]*)"`)

func blkidInfo() map[string]map[string]string {
	out := map[string]map[string]string{}
	b, err := exec.Command("blkid").Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		m := blkidLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		kv := map[string]string{}
		for _, p := range blkidKV.FindAllStringSubmatch(m[2], -1) {
			kv[p[1]] = p[2]
		}
		out[m[1]] = kv
	}
	return out
}

func unescapeMount(s string) string {
	// /proc/mounts escapes spaces and tabs as octal (\040).
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func statfs(p string) (total, free int64) {
	var st syscall.Statfs_t
	if syscall.Statfs(p, &st) == nil {
		total = int64(st.Blocks) * int64(st.Bsize)
		free = int64(st.Bavail) * int64(st.Bsize)
	}
	return
}

// discoverUSB lists mounted USB partitions. FM_USB_DIRS (colon separated) adds
// plain directories, which is how the bridge is tested off the TV.
func discoverUSB() []usbDrive {
	var drives []usbDrive
	if extra := os.Getenv("FM_USB_DIRS"); extra != "" {
		for i, d := range strings.Split(extra, ":") {
			t, f := statfs(d)
			drives = append(drives, usbDrive{ID: fmt.Sprintf("usb:test%d", i), Name: filepath.Base(d), Dev: d, FS: "dir", Mount: d, Total: t, Free: f})
		}
		return drives
	}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	var info map[string]map[string]string
	sc := bufio.NewScanner(f)
	seen := map[string]bool{}
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		dev, mnt, fstype := fields[0], unescapeMount(fields[1]), fields[2]
		if !strings.HasPrefix(mnt, "/tmp/usb/") || !strings.HasPrefix(dev, "/dev/") || seen[mnt] {
			continue
		}
		seen[mnt] = true
		if info == nil {
			info = blkidInfo()
		}
		kv := info[dev]
		name := kv["LABEL"]
		if name == "" {
			name = "USB " + path.Base(dev)
		}
		id := "usb:" + path.Base(dev)
		if u := kv["UUID"]; u != "" {
			id = "usb:" + u
		}
		t, fr := statfs(mnt)
		drives = append(drives, usbDrive{ID: id, Name: name, Dev: path.Base(dev), FS: fstype, Mount: mnt, Total: t, Free: fr})
	}
	return drives
}

// ---------- SMB ----------

// smbFS browses one share (cfg.Share set) or a whole server: with no share
// configured, the top level lists the server's shares as folders.
//
// go-smb2 leaks the SMB credits of any request whose context is cancelled
// while it is in flight: the late reply is dropped as "unknown message id"
// before its credits are counted. The TV's player cancels range requests all
// the time (seeking, closing, reloading), so once the credits run out every
// call on that connection waits forever. So nothing here hands go-smb2 a
// request's context. Calls run on a context that lives as long as the
// connection, and a call that gets no answer in time drops the whole
// connection (cancelling its context and closing the socket) so the next call
// dials a fresh one.
type smbFS struct {
	cfg SourceConfig
	mu  sync.Mutex
	cur *smbConn
}

type smbConn struct {
	conn   net.Conn
	sess   *smb2.Session
	ctx    context.Context // cancelled only when the connection is dropped
	cancel context.CancelFunc
	mu     sync.Mutex
	shares map[string]*smb2.Share
}

const (
	smbCallTimeout = 20 * time.Second // directory listings, stat, open, close
	smbReadTimeout = 30 * time.Second // one ReadAt of up to a few MiB
)

func (c *smbConn) kill() {
	c.cancel()
	c.conn.Close()
}

// mount returns the share bound to the connection's context.
func (c *smbConn) mount(share string) (*smb2.Share, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sh, ok := c.shares[strings.ToLower(share)]; ok {
		return sh, nil
	}
	sh, err := c.sess.WithContext(c.ctx).Mount(share)
	if err != nil {
		return nil, fmt.Errorf("share %q: %w", share, err)
	}
	sh = sh.WithContext(c.ctx)
	c.shares[strings.ToLower(share)] = sh
	return sh, nil
}

// split maps a client path to (share, path inside the share).
func (s *smbFS) split(rel string) (string, string) {
	if s.cfg.Share != "" {
		return s.cfg.Share, strings.Trim(path.Join(s.cfg.Path, rel), "/")
	}
	share, inner, _ := strings.Cut(rel, "/")
	return share, inner
}

func smbSession(ctx context.Context, c SourceConfig) (net.Conn, *smb2.Session, error) {
	port := c.Port
	if port == 0 {
		port = 445
	}
	d := net.Dialer{Timeout: 8 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(port)))
	if err != nil {
		return nil, nil, err
	}
	user := c.User
	if user == "" {
		user = "guest"
	}
	dialer := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: user, Password: c.Password, Domain: c.Domain}}
	sess, err := dialer.DialContext(ctx, conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("SMB login failed: %w", err)
	}
	return conn, sess, nil
}

// get returns the live connection, dialling one if there is none. The dial
// and login get their own time limit, not the caller's context: a connection
// is shared, so one impatient request must not cut it off for the others.
func (s *smbFS) get() (*smbConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		return s.cur, nil
	}
	dctx, dcancel := context.WithTimeout(context.Background(), smbCallTimeout)
	defer dcancel()
	conn, sess, err := smbSession(dctx, s.cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cur = &smbConn{conn: conn, sess: sess, ctx: ctx, cancel: cancel, shares: map[string]*smb2.Share{}}
	return s.cur, nil
}

// drop discards c, if it is still the live connection, and unblocks
// everything waiting on it.
func (s *smbFS) drop(c *smbConn) {
	s.mu.Lock()
	if s.cur == c {
		s.cur = nil
	}
	s.mu.Unlock()
	c.kill()
}

// watch runs op and drops c if op has not finished after d.
func (s *smbFS) watch(c *smbConn, d time.Duration, op func() error) error {
	t := time.AfterFunc(d, func() {
		fmt.Fprintf(os.Stderr, "%s smb %s: no answer in %s, reconnecting\n", time.Now().Format("2006/01/02 15:04:05"), s.cfg.Host, d)
		s.drop(c)
	})
	defer t.Stop()
	return op()
}

func isSMBStatus(err error) bool {
	var re *smb2.ResponseError
	return errors.As(err, &re) || errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission)
}

// run calls op on the live connection, and once more on a new connection if
// the first one died underneath it. ctx only decides whether to start.
func (s *smbFS) run(ctx context.Context, op func(*smbConn) error) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var c *smbConn
		if c, err = s.get(); err != nil {
			return err
		}
		err = s.watch(c, smbCallTimeout, func() error { return op(c) })
		if err == nil || isSMBStatus(err) {
			return err
		}
		s.drop(c)
	}
	return err
}

func (s *smbFS) do(ctx context.Context, share string, op func(*smb2.Share) error) error {
	return s.run(ctx, func(c *smbConn) error {
		sh, err := c.mount(share)
		if err != nil {
			return err
		}
		return op(sh)
	})
}

func smbPath(p string) string { return strings.ReplaceAll(p, "/", `\`) }

func (s *smbFS) List(ctx context.Context, rel string) ([]Entry, error) {
	share, inner := s.split(rel)
	var out []Entry
	if share == "" {
		err := s.run(ctx, func(c *smbConn) error {
			names, err := c.sess.WithContext(c.ctx).ListSharenames()
			if err != nil {
				return err
			}
			out = out[:0]
			for _, n := range names {
				if !strings.HasSuffix(n, "$") {
					out = append(out, Entry{Name: n, Dir: true})
				}
			}
			return nil
		})
		sortEntries(out)
		return out, err
	}
	err := s.do(ctx, share, func(sh *smb2.Share) error {
		fis, err := sh.ReadDir(smbPath(inner))
		if err != nil {
			return err
		}
		out = make([]Entry, 0, len(fis))
		for _, fi := range fis {
			out = append(out, Entry{Name: fi.Name(), Dir: fi.IsDir(), Size: fi.Size(), Mtime: fi.ModTime().Unix()})
		}
		return nil
	})
	sortEntries(out)
	return out, err
}

func (s *smbFS) Stat(ctx context.Context, rel string) (Entry, error) {
	share, inner := s.split(rel)
	if share == "" || (inner == "" && s.cfg.Share == "") {
		return Entry{Name: share, Dir: true}, nil
	}
	var e Entry
	err := s.do(ctx, share, func(sh *smb2.Share) error {
		fi, err := sh.Stat(smbPath(inner))
		if err != nil {
			return err
		}
		e = Entry{Name: fi.Name(), Dir: fi.IsDir(), Size: fi.Size(), Mtime: fi.ModTime().Unix()}
		return nil
	})
	return e, err
}

// smbFile is an open file whose reads are watched like any other call: a read
// that hangs drops the connection instead of the player's stream.
type smbFile struct {
	f *smb2.File
	s *smbFS
	c *smbConn
}

func (f *smbFile) ReadAt(p []byte, off int64) (int, error) {
	var n int
	err := f.s.watch(f.c, smbReadTimeout, func() error {
		var err error
		n, err = f.f.ReadAt(p, off)
		return err
	})
	return n, err
}

func (f *smbFile) Close() error {
	return f.s.watch(f.c, smbCallTimeout, f.f.Close)
}

func (s *smbFS) Open(ctx context.Context, rel string) (RFile, error) {
	share, inner := s.split(rel)
	if share == "" || inner == "" {
		return nil, os.ErrNotExist
	}
	var out *smbFile
	err := s.run(ctx, func(c *smbConn) error {
		sh, err := c.mount(share)
		if err != nil {
			return err
		}
		f, err := sh.Open(smbPath(inner))
		if err != nil {
			return err
		}
		out = &smbFile{f: f, s: s, c: c}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *smbFS) LocalPath(string) (string, bool) { return "", false }

func (s *smbFS) Close() {
	s.mu.Lock()
	c := s.cur
	s.cur = nil
	s.mu.Unlock()
	if c != nil {
		c.kill()
	}
}

func smbShares(ctx context.Context, c SourceConfig) ([]string, error) {
	conn, sess, err := smbSession(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	defer sess.Logoff()
	names, err := sess.ListSharenames()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if !strings.HasSuffix(n, "$") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ---------- SFTP ----------

type sftpFS struct {
	cfg    SourceConfig
	keys   *keyStore
	mu     sync.Mutex
	ssh    *ssh.Client
	client *sftp.Client
	home   string
}

func (s *sftpFS) base(rel string) string {
	p := s.cfg.Path
	if p == "" {
		p = s.home
	}
	return path.Join(p, rel)
}

func (s *sftpFS) get(ctx context.Context) (*sftp.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	port := s.cfg.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(port))
	var auth []ssh.AuthMethod
	if signer, err := s.keys.signer(); err == nil {
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if s.cfg.Password != "" {
		pw := s.cfg.Password
		auth = append(auth, ssh.Password(pw), ssh.KeyboardInteractive(func(name, instr string, qs []string, echos []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range qs {
				ans[i] = pw
			}
			return ans, nil
		}))
	}
	cfg := &ssh.ClientConfig{
		User:            s.cfg.User,
		Auth:            auth,
		HostKeyCallback: s.keys.hostKeyCallback(addr),
		Timeout:         10 * time.Second,
	}
	d := net.Dialer{Timeout: 8 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	cc, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("SSH login failed: %w", err)
	}
	sc := ssh.NewClient(cc, chans, reqs)
	cl, err := sftp.NewClient(sc, sftp.UseConcurrentReads(true), sftp.MaxConcurrentRequestsPerFile(16))
	if err != nil {
		sc.Close()
		return nil, fmt.Errorf("SFTP not available: %w", err)
	}
	if s.home == "" {
		if wd, err := cl.Getwd(); err == nil {
			s.home = wd
		} else {
			s.home = "/"
		}
	}
	s.ssh, s.client = sc, cl
	return cl, nil
}

func (s *sftpFS) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		s.client.Close()
	}
	if s.ssh != nil {
		s.ssh.Close()
	}
	s.ssh, s.client = nil, nil
}

func isSFTPStatus(err error) bool {
	var se *sftp.StatusError
	return errors.As(err, &se) || errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission)
}

func (s *sftpFS) do(ctx context.Context, op func(*sftp.Client) error) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var cl *sftp.Client
		cl, err = s.get(ctx)
		if err != nil {
			return err
		}
		err = op(cl)
		if err == nil || isSFTPStatus(err) || ctx.Err() != nil {
			return err
		}
		s.reset()
	}
	return err
}

func (s *sftpFS) List(ctx context.Context, rel string) ([]Entry, error) {
	var out []Entry
	err := s.do(ctx, func(cl *sftp.Client) error {
		fis, err := cl.ReadDir(s.base(rel))
		if err != nil {
			return err
		}
		out = make([]Entry, 0, len(fis))
		for _, fi := range fis {
			dir := fi.IsDir()
			if fi.Mode()&os.ModeSymlink != 0 {
				if t, err := cl.Stat(path.Join(s.base(rel), fi.Name())); err == nil {
					fi, dir = t, t.IsDir()
				}
			}
			out = append(out, Entry{Name: path.Base(fi.Name()), Dir: dir, Size: fi.Size(), Mtime: fi.ModTime().Unix()})
		}
		return nil
	})
	sortEntries(out)
	return out, err
}

func (s *sftpFS) Stat(ctx context.Context, rel string) (Entry, error) {
	var e Entry
	err := s.do(ctx, func(cl *sftp.Client) error {
		fi, err := cl.Stat(s.base(rel))
		if err != nil {
			return err
		}
		e = Entry{Name: fi.Name(), Dir: fi.IsDir(), Size: fi.Size(), Mtime: fi.ModTime().Unix()}
		return nil
	})
	return e, err
}

func (s *sftpFS) Open(ctx context.Context, rel string) (RFile, error) {
	var f *sftp.File
	err := s.do(ctx, func(cl *sftp.Client) error {
		var err error
		f, err = cl.Open(s.base(rel))
		return err
	})
	return f, err
}

func (s *sftpFS) LocalPath(string) (string, bool) { return "", false }
func (s *sftpFS) Close()                          { s.reset() }

// ---------- buffered reader for streaming ----------

// bufReader turns ReadAt into large sequential reads, so a network file is
// fetched in 1 MiB requests instead of the 32 KiB io.Copy would use.
type bufReader struct {
	f        io.ReaderAt
	size     int64
	pos      int64
	buf      []byte
	bufStart int64
	bufLen   int
}

func newBufReader(f io.ReaderAt, size int64) *bufReader {
	return &bufReader{f: f, size: size, buf: make([]byte, 1<<20)}
}

func (b *bufReader) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		off += b.pos
	case io.SeekEnd:
		off += b.size
	}
	if off < 0 {
		return 0, errors.New("negative seek")
	}
	b.pos = off
	return off, nil
}

func (b *bufReader) Read(p []byte) (int, error) {
	if b.pos >= b.size {
		return 0, io.EOF
	}
	if b.pos < b.bufStart || b.pos >= b.bufStart+int64(b.bufLen) {
		n, err := b.f.ReadAt(b.buf, b.pos)
		if n == 0 {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
		b.bufStart, b.bufLen = b.pos, n
	}
	n := copy(p, b.buf[b.pos-b.bufStart:b.bufLen])
	b.pos += int64(n)
	return n, nil
}
