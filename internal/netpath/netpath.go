// Package netpath says why a network share a job writes to is not there:
// is the box's Tailscale down, is the other machine offline, does it answer
// at all — or is the network fine and only the mount is missing?
//
// Measured on the test NAS (16.10.2026): four jobs wrote to SMB shares Files had
// mounted under /media/<name>.ts.net/<share>. They failed for twelve days
// with "target unavailable"; the owner took Tailscale for the cause, while
// both Tailscale peers stood "active; direct" and only the mounts were
// missing. The message has to tell these cases apart.
package netpath

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"time"
)

// Reasons, each one sentence the UI translates.
const (
	TailscaleStopped = "ts_stopped"  // this box is not connected to Tailscale
	TailscaleNoPeer  = "ts_no_peer"  // the name is not in this box's tailnet
	TailscaleOffline = "ts_offline"  // the peer is offline in Tailscale
	Reachable        = "reachable"   // the machine answers on the SMB port: only the mount is missing
	Unreachable      = "unreachable" // the machine does not answer on the SMB port
)

// Tailscale states of our own; otherwise State is tailscaled's BackendState
// (Stopped, NeedsLogin, …).
const (
	StateNotInstalled = "not_installed" // no tailscale binary on the box
	StateNotRunning   = "not_running"   // the binary is there, tailscaled gave no state
)

// Network labels, as the discovery uses them.
const (
	NetLAN       = "lan"
	NetTailscale = "tailscale"
	NetZimaNet   = "zimanet"
	NetZeroTier  = "zerotier"
)

// Reason is the verdict for one host.
type Reason struct {
	Kind     string    `json:"kind"`
	Net      string    `json:"net"`
	Host     string    `json:"host"`
	State    string    `json:"state,omitempty"`     // not_installed, not_running, or Tailscale's BackendState
	LastSeen time.Time `json:"last_seen,omitempty"` // offline peer
}

// Prober holds what Explain asks the world; the zero value asks the real
// one (tailscale at /usr/bin/tailscale, the box's interfaces, DNS, TCP).
type Prober struct {
	Tailscale  string                                                       // binary; empty: /usr/bin/tailscale
	Status     func(ctx context.Context) ([]byte, error)                    // tailscale status --json
	Interfaces func() []Iface                                               // local prefixes
	Lookup     func(ctx context.Context, host string) ([]netip.Addr, error) // DNS
	Dial       func(ctx context.Context, addr string) error                 // TCP connect
}

// Iface is one local interface prefix.
type Iface struct {
	Name   string
	Prefix netip.Prefix
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// label names the mesh an interface belongs to (ZimaOS: tun0 is ZimaNet,
// zt* ZeroTier, tailscale0 Tailscale — KB §55, measured on 1.7.1).
func label(iface string) string {
	switch {
	case strings.HasPrefix(iface, "tailscale"):
		return NetTailscale
	case strings.HasPrefix(iface, "zt"):
		return NetZeroTier
	case strings.HasPrefix(iface, "tun"):
		return NetZimaNet
	}
	return NetLAN
}

type peer struct {
	HostName     string    `json:"HostName"`
	DNSName      string    `json:"DNSName"`
	TailscaleIPs []string  `json:"TailscaleIPs"`
	Online       bool      `json:"Online"`
	LastSeen     time.Time `json:"LastSeen"`
}

type status struct {
	BackendState string          `json:"BackendState"`
	Peer         map[string]peer `json:"Peer"`
}

// findPeer matches the host as Files has it in the mount point: a MagicDNS
// name (with or without the tailnet suffix) or a Tailscale address.
func (s status) findPeer(host string) (peer, bool) {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range s.Peer {
		dns := strings.ToLower(strings.TrimSuffix(p.DNSName, "."))
		if dns != "" && (dns == h || strings.SplitN(dns, ".", 2)[0] == h) {
			return p, true
		}
		for _, ip := range p.TailscaleIPs {
			if ip == h {
				return p, true
			}
		}
	}
	return peer{}, false
}

// status asks tailscaled. installed is false when the binary is missing
// (measured on the second test NAS, 16.10.2026: ZimaOS without Tailscale has none).
func (p Prober) status(ctx context.Context) (st status, ok, installed bool) {
	get := p.Status
	if get == nil {
		bin := p.Tailscale
		if bin == "" {
			bin = "/usr/bin/tailscale"
		}
		get = func(ctx context.Context) ([]byte, error) {
			return exec.CommandContext(ctx, bin, "status", "--json").Output()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// the exit code is not judged: what counts is whether a BackendState
	// came back (how tailscale exits while logged out is not measured)
	out, err := get(ctx)
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return status{}, false, false
	}
	var s status
	if json.Unmarshal(out, &s) != nil || s.BackendState == "" {
		return status{}, false, true
	}
	return s, true, true
}

func (p Prober) interfaces() []Iface {
	if p.Interfaces != nil {
		return p.Interfaces()
	}
	var out []Iface
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if pf, err := netip.ParsePrefix(ipn.String()); err == nil {
					out = append(out, Iface{Name: ifc.Name, Prefix: pf.Masked()})
				}
			}
		}
	}
	return out
}

func (p Prober) lookup(ctx context.Context, host string) []netip.Addr {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}
	}
	look := p.Lookup
	if look == nil {
		look = func(ctx context.Context, h string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, _ := look(ctx, host)
	return addrs
}

func (p Prober) dial(ctx context.Context, addr string) error {
	if p.Dial != nil {
		return p.Dial(ctx, addr)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err == nil {
		c.Close()
	}
	return err
}

// Explain judges the host of a share that is not mounted.
func (p Prober) Explain(ctx context.Context, host string) Reason {
	r := Reason{Host: host, Net: NetLAN}
	addrs := p.lookup(ctx, host)
	viaTailscale := strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".ts.net")
	for _, a := range addrs {
		if cgnat.Contains(a.Unmap()) {
			viaTailscale = true
		}
	}
	st, haveStatus, installed := p.status(ctx)
	if !viaTailscale && haveStatus {
		_, viaTailscale = st.findPeer(host) // a short MagicDNS name
	}
	if viaTailscale {
		r.Net = NetTailscale
		if !haveStatus || st.BackendState != "Running" {
			r.Kind, r.State = TailscaleStopped, st.BackendState
			switch {
			case !installed:
				r.State = StateNotInstalled
			case r.State == "":
				r.State = StateNotRunning
			}
			return r
		}
		pr, ok := st.findPeer(host)
		if !ok {
			r.Kind = TailscaleNoPeer
			return r
		}
		if !pr.Online {
			r.Kind, r.LastSeen = TailscaleOffline, pr.LastSeen
			return r
		}
		if len(addrs) == 0 && len(pr.TailscaleIPs) > 0 {
			if a, err := netip.ParseAddr(pr.TailscaleIPs[0]); err == nil {
				addrs = []netip.Addr{a}
			}
		}
	} else if len(addrs) > 0 {
		for _, ifc := range p.interfaces() {
			if ifc.Prefix.Contains(addrs[0]) {
				r.Net = label(ifc.Name)
				break
			}
		}
	}
	r.Kind = Unreachable
	for _, a := range addrs {
		if p.dial(ctx, netip.AddrPortFrom(a, 445).String()) == nil {
			r.Kind = Reachable
			break
		}
	}
	return r
}

// Sentence is the English form of the reason (logs, notifications, older
// clients); the UI writes its own from Kind.
func (r Reason) Sentence() string {
	via := map[string]string{NetTailscale: "Tailscale", NetZimaNet: "ZimaNet", NetZeroTier: "ZeroTier", NetLAN: "the local network"}[r.Net]
	switch r.Kind {
	case TailscaleStopped:
		return "Tailscale is not connected on this box (" + strings.ReplaceAll(r.State, "_", " ") + ")"
	case TailscaleNoPeer:
		return r.Host + " is not in this box's Tailscale network"
	case TailscaleOffline:
		s := r.Host + " is offline in Tailscale"
		if !r.LastSeen.IsZero() {
			s += " (last seen " + r.LastSeen.Local().Format("2006-01-02 15:04") + ")"
		}
		return s
	case Reachable:
		return r.Host + " answers over " + via + " — only the share is not mounted: connect it again in Files, or let the job connect to the share directly (SMB target)"
	case Unreachable:
		return r.Host + " does not answer on the SMB port over " + via
	}
	return ""
}
