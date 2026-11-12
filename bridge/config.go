package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
)

type SourceConfig struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // "smb" or "sftp"
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	Share    string `json:"share,omitempty"`
	Path     string `json:"path,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	Domain   string `json:"domain,omitempty"`
}

// publicSource is what the UI gets: everything except the password.
type publicSource struct {
	SourceConfig
	Password    string `json:"password,omitempty"`
	HasPassword bool   `json:"hasPassword"`
}

type Config struct {
	Version int            `json:"version"`
	Sources []SourceConfig `json:"sources"`
}

type configStore struct {
	path   string
	legacy string // the 0.4 Python bridge's config.json
	mu     sync.Mutex
	c      Config
}

func (cs *configStore) load() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if b, err := os.ReadFile(cs.path); err == nil && json.Unmarshal(b, &cs.c) == nil {
		return
	}
	cs.c = Config{Version: 2}
	var old struct {
		Host, Share, Subpath, Username, Password string
	}
	if b, err := os.ReadFile(cs.legacy); err == nil && json.Unmarshal(b, &old) == nil && old.Host != "" {
		user := old.Username
		if strings.EqualFold(user, "anonymous") {
			user = ""
		}
		cs.c.Sources = append(cs.c.Sources, SourceConfig{
			ID: newID(), Kind: "smb", Name: old.Host + " · " + old.Share, Host: old.Host,
			Share: old.Share, Path: old.Subpath, User: user, Password: old.Password,
		})
		cs.saveLocked()
	}
}

func (cs *configStore) saveLocked() error {
	b, _ := json.MarshalIndent(cs.c, "", "  ")
	tmp := cs.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, cs.path)
}

func (cs *configStore) sources() []SourceConfig {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return append([]SourceConfig(nil), cs.c.Sources...)
}

func (cs *configStore) find(id string) (SourceConfig, bool) {
	for _, s := range cs.sources() {
		if s.ID == id {
			return s, true
		}
	}
	return SourceConfig{}, false
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9._:\-\[\]]+$`)

// normalize validates a source from the UI. keepPassword reuses the stored
// password when the UI left the field empty while editing.
func (cs *configStore) normalize(in SourceConfig, keepPassword bool) (SourceConfig, error) {
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	in.Host = strings.TrimSpace(in.Host)
	in.Share = strings.Trim(strings.TrimSpace(in.Share), `/\`)
	in.Path = strings.TrimSpace(in.Path)
	in.Name = strings.TrimSpace(in.Name)
	in.User = strings.TrimSpace(in.User)
	if in.Kind != "smb" && in.Kind != "sftp" {
		return in, errors.New("choose SMB or SFTP")
	}
	if in.Host == "" || !hostRe.MatchString(in.Host) {
		return in, errors.New("enter a valid server name or IP address")
	}
	if in.Port < 0 || in.Port > 65535 {
		return in, errors.New("invalid port")
	}
	if in.Kind == "sftp" && in.User == "" {
		return in, errors.New("SFTP needs a user name")
	}
	if in.Kind == "smb" {
		in.Path = strings.Trim(strings.ReplaceAll(in.Path, `\`, "/"), "/")
	}
	if keepPassword && in.Password == "" && in.ID != "" {
		if old, ok := cs.find(in.ID); ok {
			in.Password = old.Password
		}
	}
	if in.Name == "" {
		if in.Kind == "smb" && in.Share == "" {
			in.Name = in.Host
		} else if in.Kind == "smb" {
			in.Name = in.Host + " · " + in.Share
		} else {
			in.Name = in.User + "@" + in.Host
		}
	}
	return in, nil
}

func (cs *configStore) save(s SourceConfig) (SourceConfig, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if s.ID == "" {
		s.ID = newID()
		cs.c.Sources = append(cs.c.Sources, s)
	} else {
		found := false
		for i := range cs.c.Sources {
			if cs.c.Sources[i].ID == s.ID {
				cs.c.Sources[i] = s
				found = true
			}
		}
		if !found {
			return s, errors.New("unknown location")
		}
	}
	return s, cs.saveLocked()
}

func (cs *configStore) remove(id string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := cs.c.Sources[:0]
	for _, s := range cs.c.Sources {
		if s.ID != id {
			out = append(out, s)
		}
	}
	cs.c.Sources = out
	return cs.saveLocked()
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
