package main

// The bridge's own SSH identity (for key login to SFTP servers) and a
// trust-on-first-use store of server host keys.

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

type keyStore struct {
	dir string
	mu  sync.Mutex
	s   ssh.Signer
}

func (k *keyStore) signer() (ssh.Signer, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.s != nil {
		return k.s, nil
	}
	p := filepath.Join(k.dir, "id_ed25519")
	if b, err := os.ReadFile(p); err == nil {
		s, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return nil, err
		}
		k.s = s
		return s, nil
	}
	if err := os.MkdirAll(k.dir, 0700); err != nil {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "webos-file-manager")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0600); err != nil {
		return nil, err
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	k.s = s
	return s, nil
}

func (k *keyStore) publicKey() (string, error) {
	s, err := k.signer()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey()))) + " webos-file-manager", nil
}

var errHostKeyChanged = errors.New("the server's host key changed; remove it under Settings if this is expected")

func (k *keyStore) hostKeyCallback(addr string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		k.mu.Lock()
		defer k.mu.Unlock()
		p := filepath.Join(k.dir, "known_hosts")
		line := addr + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		if f, err := os.Open(p); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				t := sc.Text()
				if strings.HasPrefix(t, addr+" "+key.Type()+" ") {
					f.Close()
					if t == line {
						return nil
					}
					return errHostKeyChanged
				}
			}
			f.Close()
		}
		os.MkdirAll(k.dir, 0700)
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintln(f, line)
		return err
	}
}

func (k *keyStore) forgetHost(addr string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	p := filepath.Join(k.dir, "known_hosts")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" && !strings.HasPrefix(l, addr+" ") {
			keep = append(keep, l)
		}
	}
	return os.WriteFile(p, []byte(strings.Join(keep, "\n")+"\n"), 0600)
}
