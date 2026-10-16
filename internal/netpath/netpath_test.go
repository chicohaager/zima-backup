package netpath

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// statusJSON has the fields tailscale 1.98.8 printed on .143 (16.10.2026);
// names and addresses are invented.
const statusJSON = `{
 "BackendState": "Running",
 "Peer": {
  "a": {"HostName": "cube", "DNSName": "cube.tail0000.ts.net.", "TailscaleIPs": ["100.100.1.10", "fd7a::10"], "Online": true, "LastSeen": "2026-10-15T21:08:34.1Z"},
  "b": {"HostName": "board", "DNSName": "board.tail0000.ts.net.", "TailscaleIPs": ["100.100.1.11"], "Online": false, "LastSeen": "2026-10-05T03:00:00Z"}
 }
}`

type world struct {
	status  string
	addrs   map[string][]netip.Addr
	open    map[string]bool // "ip:445" answers
	ifaces  []Iface
	dialled []string
}

func (w *world) prober() Prober {
	return Prober{
		Status: func(context.Context) ([]byte, error) {
			if w.status == "" {
				return nil, errors.New("exec: tailscale: not found")
			}
			return []byte(w.status), nil
		},
		Interfaces: func() []Iface { return w.ifaces },
		Lookup: func(_ context.Context, h string) ([]netip.Addr, error) {
			if a, ok := w.addrs[h]; ok {
				return a, nil
			}
			return nil, errors.New("no such host")
		},
		Dial: func(_ context.Context, addr string) error {
			w.dialled = append(w.dialled, addr)
			if w.open[addr] {
				return nil
			}
			return errors.New("i/o timeout")
		},
	}
}

var ctx = context.Background()

func TestTailscalePeerOnlineButShareNotMounted(t *testing.T) {
	// the .143 case: Tailscale up, peer online, SMB answers — only the mount is gone
	w := &world{status: statusJSON, addrs: map[string][]netip.Addr{"cube.tail0000.ts.net": {netip.MustParseAddr("100.100.1.10")}}, open: map[string]bool{"100.100.1.10:445": true}}
	r := w.prober().Explain(ctx, "cube.tail0000.ts.net")
	if r.Kind != Reachable || r.Net != NetTailscale {
		t.Fatalf("got %+v", r)
	}
	if s := r.Sentence(); !strings.Contains(s, "answers over Tailscale") || !strings.Contains(s, "only the share is not mounted") {
		t.Errorf("sentence: %s", s)
	}
}

func TestTailscalePeerOffline(t *testing.T) {
	w := &world{status: statusJSON}
	r := w.prober().Explain(ctx, "board.tail0000.ts.net")
	if r.Kind != TailscaleOffline || !r.LastSeen.Equal(time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %+v", r)
	}
	if len(w.dialled) != 0 {
		t.Errorf("an offline peer is not dialled: %v", w.dialled)
	}
	if !strings.Contains(r.Sentence(), "offline in Tailscale (last seen 2026-10-05") {
		t.Errorf("sentence: %s", r.Sentence())
	}
}

func TestTailscaleNotRunningOnThisBox(t *testing.T) {
	for _, st := range []string{`{"BackendState": "Stopped", "Peer": {}}`, `{"BackendState": "NeedsLogin"}`} {
		w := &world{status: st}
		r := w.prober().Explain(ctx, "cube.tail0000.ts.net")
		if r.Kind != TailscaleStopped || r.State == "" || r.State == "Running" {
			t.Errorf("%s: got %+v", st, r)
		}
	}
	w := &world{} // the binary is there, tailscaled answers nothing
	if r := w.prober().Explain(ctx, "cube.tail0000.ts.net"); r.Kind != TailscaleStopped || r.State != StateNotRunning {
		t.Errorf("no answer: got %+v", r)
	}
	// .184 (16.10.2026): no tailscale on the box at all
	missing := Prober{Tailscale: "/nonexistent/tailscale", Lookup: w.prober().Lookup, Dial: w.prober().Dial, Interfaces: w.prober().Interfaces}
	if r := missing.Explain(ctx, "cube.tail0000.ts.net"); r.Kind != TailscaleStopped || r.State != StateNotInstalled {
		t.Errorf("not installed: got %+v", r)
	}
	if s := (Reason{Kind: TailscaleStopped, State: StateNotInstalled}).Sentence(); s != "Tailscale is not connected on this box (not installed)" {
		t.Errorf("sentence: %s", s)
	}
}

func TestTailscaleNameNotInTailnet(t *testing.T) {
	w := &world{status: statusJSON}
	if r := w.prober().Explain(ctx, "gone.tail0000.ts.net"); r.Kind != TailscaleNoPeer {
		t.Fatalf("got %+v", r)
	}
}

func TestShortMagicDNSNameAndTailscaleAddressAreTailscale(t *testing.T) {
	w := &world{status: statusJSON, open: map[string]bool{"100.100.1.10:445": true}}
	if r := w.prober().Explain(ctx, "cube"); r.Kind != Reachable || r.Net != NetTailscale {
		t.Errorf("short name: %+v", r)
	}
	if r := w.prober().Explain(ctx, "100.100.1.11"); r.Kind != TailscaleOffline {
		t.Errorf("address of the offline peer: %+v", r)
	}
}

func TestZimaNetAndLANByInterface(t *testing.T) {
	w := &world{
		ifaces: []Iface{{"eth0", netip.MustParsePrefix("192.0.2.0/24")}, {"tun0", netip.MustParsePrefix("198.51.100.0/24")}},
		open:   map[string]bool{"192.0.2.7:445": true},
	}
	if r := w.prober().Explain(ctx, "198.51.100.9"); r.Net != NetZimaNet || r.Kind != Unreachable || !strings.Contains(r.Sentence(), "over ZimaNet") {
		t.Errorf("zimanet: %+v %s", r, r.Sentence())
	}
	if r := w.prober().Explain(ctx, "192.0.2.7"); r.Net != NetLAN || r.Kind != Reachable {
		t.Errorf("lan: %+v", r)
	}
	if r := w.prober().Explain(ctx, "nas.example"); r.Kind != Unreachable || len(w.dialled) != 2 {
		t.Errorf("a name that does not resolve is unreachable, nothing dialled: %+v %v", r, w.dialled)
	}
}

// With Tailscale stopped there is no peer list: only the address range says
// that 100.64.0.0/10 is reached through Tailscale.
func TestTailscaleAddressWhileTailscaleIsDown(t *testing.T) {
	w := &world{status: `{"BackendState": "Stopped"}`}
	if r := w.prober().Explain(ctx, "100.100.1.99"); r.Kind != TailscaleStopped || r.Net != NetTailscale {
		t.Fatalf("got %+v", r)
	}
}
