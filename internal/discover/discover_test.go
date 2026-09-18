package discover

import (
	"context"
	"encoding/hex"
	"net/netip"
	"os"
	"testing"
)

// The exact packet a ZimaOS 1.7.1 box answered to a _zimaos._tcp PTR
// query (captured 2026-09-18 with a raw UDP query to 224.0.0.251:5353):
// PTR, TXT os=ZimaOS, SRV port 80, AAAA (link-local), A. Only the four
// address bytes of the A record were replaced (192.168.77.143 here).
const zimaosAnswer = "000084000000000500000000075f7a696d616f73045f746370056c6f63616c00000c0001000011940009065a696d614f53c00cc02a0010800100001194000a096f733d5a696d614f53c02a0021800100000078000f000000000050065a696d614f53c019c05b001c8001000000780010fe800000000000003af4841a740a8735c05b00018001000000780004c0a84d8f"

func TestParseZimaOSAnnouncement(t *testing.T) {
	data, err := hex.DecodeString(zimaosAnswer)
	if err != nil {
		t.Fatal(err)
	}
	got := parseAnswers(data, zimaosService)
	if len(got) != 1 {
		t.Fatalf("services = %+v", got)
	}
	s := got[0]
	if s.Instance != "ZimaOS" || s.Target != "ZimaOS.local" || s.Port != 80 || s.TXT["os"] != "ZimaOS" {
		t.Fatalf("parsed %+v", s)
	}
	if len(s.Addrs) != 2 || s.Addrs[1] != netip.MustParseAddr("192.168.77.143") || !s.Addrs[0].Is6() {
		t.Fatalf("addresses %v", s.Addrs)
	}
	// a query (not a response) and a foreign service type yield nothing
	q, _ := buildQuery(zimaosService)
	if got := parseAnswers(q, zimaosService); len(got) != 0 {
		t.Fatalf("query parsed as answer: %+v", got)
	}
	if got := parseAnswers(data, "_smb._tcp.local."); len(got) != 0 {
		t.Fatalf("other service type matched: %+v", got)
	}
}

func TestClassifyUsesLocalRoutes(t *testing.T) {
	nets := []localNetwork{
		{netip.MustParsePrefix("192.168.77.0/24"), NetLAN, netip.MustParseAddr("192.168.77.143")},
		{netip.MustParsePrefix("10.211.0.0/16"), NetZeroTier, netip.MustParseAddr("10.211.0.1")},
		{netip.MustParsePrefix("10.200.7.0/24"), NetZimaNet, netip.MustParseAddr("10.200.7.1")},
		{netip.MustParsePrefix("100.110.212.65/32"), NetTailscale, netip.MustParseAddr("100.110.212.65")},
	}
	if !isSelf([]netip.Addr{netip.MustParseAddr("192.168.77.143")}, nets) || isSelf([]netip.Addr{netip.MustParseAddr("192.168.77.110")}, nets) {
		t.Fatal("isSelf must recognise exactly our own addresses")
	}
	cases := map[string]string{
		"192.168.77.110": NetLAN,
		"10.211.0.7":     NetZeroTier,
		"10.200.7.2":     NetZimaNet,
		"100.85.145.6":   NetTailscale, // not a local prefix, but Tailscale's CGNAT range
		"203.0.113.9":    NetLAN,
	}
	for addr, want := range cases {
		if got := classify(netip.MustParseAddr(addr), nets); got != want {
			t.Errorf("classify(%s) = %s, want %s", addr, got, want)
		}
	}
	for iface, want := range map[string]string{"eth0": NetLAN, "tailscale0": NetTailscale, "ztdf3gwnrs": NetZeroTier, "tun0": NetZimaNet} {
		if got := labelFor(iface); got != want {
			t.Errorf("labelFor(%s) = %s", iface, got)
		}
	}
}

// Shape of `tailscale status --json` 1.98 on ZimaOS, identities replaced:
// a funnel ingress node (no DNS name), an offline phone, an online box.
const tailscaleStatus = `{"Version":"1.98.8","BackendState":"Running","Self":{"HostName":"ZimaOS","DNSName":"zimaos-a.example.ts.net.","OS":"linux","TailscaleIPs":["100.110.0.1"]},
"Peer":{
 "nodekey:1":{"HostName":"funnel-ingress-node","DNSName":"","OS":"","TailscaleIPs":["fd7a::1"],"Online":true},
 "nodekey:2":{"HostName":"Pixel 7","DNSName":"pixel-7.example.ts.net.","OS":"android","TailscaleIPs":["100.66.32.52"],"Online":false},
 "nodekey:3":{"HostName":"ZimaBoard","DNSName":"zimaboard-b.example.ts.net.","OS":"linux","TailscaleIPs":["100.85.145.6","fd7a::3"],"Online":true}
}}`

func TestParseTailscaleKeepsOnlineNamedPeers(t *testing.T) {
	got := parseTailscale([]byte(tailscaleStatus))
	if len(got) != 1 {
		t.Fatalf("peers = %+v", got)
	}
	p := got[0]
	if p.Name != "ZimaBoard" || p.Host != "zimaboard-b.example.ts.net" || p.Network != NetTailscale || p.OS != "linux" || p.Addresses[0] != "100.85.145.6" {
		t.Fatalf("peer %+v", p)
	}
	if got := parseTailscale([]byte("not json")); got != nil {
		t.Fatal("garbage must yield nothing")
	}
}

func TestDedupePutsZimaOSFirst(t *testing.T) {
	hosts := dedupe([]Host{
		{Name: "box", Host: "100.1.1.1", OS: "linux"},
		{Name: "ZimaOS", Host: "192.168.77.5", OS: "ZimaOS"},
		{Name: "ZimaOS", Host: "192.168.77.5", OS: "ZimaOS"},
	})
	if len(hosts) != 2 || hosts[0].OS != "ZimaOS" {
		t.Fatalf("dedupe = %+v", hosts)
	}
	if got := dedupe(nil); got == nil || len(got) != 0 {
		t.Fatal("empty result must be an empty list, not null")
	}
}

// A slow tailscale must not be cut off by the mDNS window: with a fake
// binary that sleeps longer than the window, the peer still arrives.
func TestTailscaleRunsBesideTheMDNSWindow(t *testing.T) {
	dir := t.TempDir()
	fake := dir + "/tailscale"
	script := "#!/bin/sh\nsleep 2.5\ncat <<'EOF'\n" + tailscaleStatus + "\nEOF\n"
	if err := os.WriteFile(fake, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	hosts := Finder{Tailscale: fake}.Hosts(context.Background())
	for _, h := range hosts {
		if h.Source == "tailscale" && h.Name == "ZimaBoard" {
			return
		}
	}
	t.Fatalf("tailscale peer missing: %+v", hosts)
}

// Against the real network: needs a ZimaOS box on the LAN (set
// DISCOVER_LIVE=1); otherwise skipped loudly.
func TestLiveDiscoveryFindsAZimaOSBox(t *testing.T) {
	if os.Getenv("DISCOVER_LIVE") == "" {
		t.Skip("SKIPPED: set DISCOVER_LIVE=1 with a ZimaOS box on the LAN")
	}
	hosts := Finder{}.Hosts(context.Background())
	for _, h := range hosts {
		if h.OS == "ZimaOS" && h.Network == NetLAN {
			t.Logf("found %s at %s via %s", h.Name, h.Host, h.Source)
			return
		}
	}
	t.Fatalf("no ZimaOS box found: %+v", hosts)
}
