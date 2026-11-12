package main

// LAN discovery of SMB servers: probe TCP 445 across the TV's own /24,
// then name each host by reverse DNS or a NetBIOS node-status query.

import (
	"context"
	"encoding/binary"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

type lanServer struct {
	Host string `json:"host"` // IP address
	Name string `json:"name"`
}

type discovery struct {
	mu       sync.Mutex
	servers  []lanServer
	at       time.Time
	scanning bool
}

func localIPv4Nets() []*net.IPNet {
	var out []*net.IPNet
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil || !n.IP.IsPrivate() {
				continue
			}
			out = append(out, n)
		}
	}
	return out
}

// get returns the last scan and starts a new one when it is stale.
func (d *discovery) get(force bool) ([]lanServer, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.scanning && (force || time.Since(d.at) > 2*time.Minute) {
		d.scanning = true
		go d.scan()
	}
	return append([]lanServer{}, d.servers...), d.scanning
}

func (d *discovery) scan() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var mu sync.Mutex
	var found []lanServer
	sem := make(chan struct{}, 48)
	var wg sync.WaitGroup
	for _, n := range localIPv4Nets() {
		self := n.IP.To4()
		for i := 1; i < 255; i++ {
			ip := net.IPv4(self[0], self[1], self[2], byte(i))
			if ip.Equal(self) {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(ip string) {
				defer wg.Done()
				defer func() { <-sem }()
				c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "445"), 600*time.Millisecond)
				if err != nil {
					return
				}
				c.Close()
				s := lanServer{Host: ip, Name: hostName(ctx, ip)}
				mu.Lock()
				found = append(found, s)
				mu.Unlock()
			}(ip.String())
		}
	}
	wg.Wait()
	sort.Slice(found, func(i, j int) bool { return strings.ToLower(found[i].Name) < strings.ToLower(found[j].Name) })
	d.mu.Lock()
	d.servers, d.at, d.scanning = found, time.Now(), false
	d.mu.Unlock()
}

func hostName(ctx context.Context, ip string) string {
	if n := netbiosName(ip); n != "" {
		return n
	}
	rctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	if names, err := net.DefaultResolver.LookupAddr(rctx, ip); err == nil && len(names) > 0 {
		n := strings.TrimSuffix(names[0], ".")
		if i := strings.IndexByte(n, '.'); i > 0 {
			n = n[:i]
		}
		if strings.Trim(n, "0123456789-") == "" {
			return ip // a router's generated "192-168-10-1" name says nothing
		}
		return strings.ToUpper(n)
	}
	return ip
}

// netbiosName sends a NetBIOS node-status request (UDP 137) and returns the
// machine's file-server name, as Windows' "network" view does.
func netbiosName(ip string) string {
	c, err := net.DialTimeout("udp", net.JoinHostPort(ip, "137"), time.Second)
	if err != nil {
		return ""
	}
	defer c.Close()
	q := []byte{0x13, 0x37, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0x20}
	// encoded "*" name: '*' = 0x2A -> "CK", then 15 zero bytes -> "AA" each
	q = append(q, 'C', 'K')
	for i := 0; i < 15; i++ {
		q = append(q, 'A', 'A')
	}
	q = append(q, 0, 0, 0x21, 0, 1) // NBSTAT, IN
	c.SetDeadline(time.Now().Add(800 * time.Millisecond))
	if _, err := c.Write(q); err != nil {
		return ""
	}
	b := make([]byte, 1024)
	n, err := c.Read(b)
	if err != nil || n < 57 {
		return ""
	}
	// header(12) + name(34) + type/class/ttl(8) + rdlength(2) + count(1)
	off := 12 + 34 + 8 + 2
	count := int(b[off])
	off++
	for i := 0; i < count && off+18 <= n; i++ {
		name := strings.TrimSpace(string(b[off : off+15]))
		suffix := b[off+15]
		flags := binary.BigEndian.Uint16(b[off+16 : off+18])
		off += 18
		if suffix == 0x20 && flags&0x8000 == 0 { // unique file-server name
			return name
		}
	}
	return ""
}
