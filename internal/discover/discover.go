package discover

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Host is one machine a job could target.
type Host struct {
	Name      string   `json:"name"`      // what the user recognises: mDNS instance or Tailscale host name
	Host      string   `json:"host"`      // what goes into the target: an address or a DNS name
	Addresses []string `json:"addresses"` // every address the machine announced
	Network   string   `json:"network"`   // lan, zerotier, zimanet, tailscale
	OS        string   `json:"os"`        // "ZimaOS" from the mDNS TXT record, Tailscale's OS otherwise
	Source    string   `json:"source"`    // mdns or tailscale
}

// Network labels.
const (
	NetLAN       = "lan"
	NetZeroTier  = "zerotier"
	NetZimaNet   = "zimanet"
	NetTailscale = "tailscale"
)

// Finder runs the discovery sources. Tailscale is the tailscale binary
// (empty: not looked for).
type Finder struct {
	Tailscale string
}

// Hosts returns everything found within the window, ZimaOS boxes first,
// deduplicated by name and address.
func (f Finder) Hosts(ctx context.Context) []Host {
	// tailscale is asked in parallel: the mDNS query uses its whole window,
	// so a sequential call would start with an expired context
	peers := make(chan []Host, 1)
	go func() {
		if f.Tailscale == "" {
			peers <- nil
			return
		}
		peers <- tailscalePeers(ctx, f.Tailscale)
	}()
	mctx, cancel := context.WithTimeout(ctx, mdnsWindow)
	defer cancel()
	services, _ := queryMDNS(mctx, zimaosService)
	nets := localNetworks()
	var hosts []Host
	for _, s := range services {
		if len(s.Addrs) == 0 || isSelf(s.Addrs, nets) {
			continue // a box hears its own announcement too
		}
		h := Host{Name: s.Instance, OS: s.TXT["os"], Source: "mdns", Network: NetLAN}
		if h.OS == "" {
			h.OS = "ZimaOS" // only ZimaOS announces this service type
		}
		for _, a := range s.Addrs {
			if a.Is4() { // an IPv4 address is what users type into the other fields
				h.Addresses = append(h.Addresses, a.String())
			}
		}
		for _, a := range s.Addrs {
			if !a.Is4() && !a.IsLinkLocalUnicast() {
				h.Addresses = append(h.Addresses, a.String())
			}
		}
		if len(h.Addresses) == 0 {
			continue
		}
		h.Host = h.Addresses[0]
		h.Network = classify(netip.MustParseAddr(h.Host), nets)
		hosts = append(hosts, h)
	}
	hosts = append(hosts, <-peers...)
	return dedupe(hosts)
}

// localNetwork is one local interface prefix with the label of the
// network it belongs to.
type localNetwork struct {
	prefix netip.Prefix
	label  string
	self   netip.Addr // our own address on that interface
}

// localNetworks reads the interfaces: on ZimaOS 1.7.1 the ZimaNet mesh is
// tun0, ZeroTier is zt<id>, Tailscale is tailscale0 (measured). Anything
// else is the LAN.
func localNetworks() []localNetwork {
	var out []localNetwork
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			p, err := netip.ParsePrefix(ipn.String())
			if err != nil {
				continue
			}
			out = append(out, localNetwork{prefix: p.Masked(), label: labelFor(ifc.Name), self: p.Addr()})
		}
	}
	return out
}

// isSelf says whether one of the addresses is our own.
func isSelf(addrs []netip.Addr, nets []localNetwork) bool {
	for _, a := range addrs {
		for _, n := range nets {
			if n.self == a {
				return true
			}
		}
	}
	return false
}

func labelFor(iface string) string {
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

// classify names the network an address is reached through.
func classify(addr netip.Addr, nets []localNetwork) string {
	for _, n := range nets {
		if n.prefix.Contains(addr) {
			return n.label
		}
	}
	if tailscaleCGNAT.Contains(addr) {
		return NetTailscale
	}
	return NetLAN
}

var tailscaleCGNAT = netip.MustParsePrefix("100.64.0.0/10")

// tailscalePeers lists the online peers of the local tailscaled through
// `tailscale status --json` (measured with 1.98: Peer map with HostName,
// DNSName, OS, TailscaleIPs, Online; Tailscale's own funnel ingress nodes
// appear as peers without a DNS name and are skipped).
func tailscalePeers(ctx context.Context, bin string) []Host {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
	if err != nil {
		return nil
	}
	return parseTailscale(out)
}

func parseTailscale(data []byte) []Host {
	var st struct {
		Peer map[string]struct {
			HostName     string   `json:"HostName"`
			DNSName      string   `json:"DNSName"`
			OS           string   `json:"OS"`
			TailscaleIPs []string `json:"TailscaleIPs"`
			Online       bool     `json:"Online"`
		} `json:"Peer"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return nil
	}
	var hosts []Host
	for _, p := range st.Peer {
		if !p.Online || p.DNSName == "" || p.HostName == "funnel-ingress-node" || len(p.TailscaleIPs) == 0 {
			continue
		}
		hosts = append(hosts, Host{
			Name: p.HostName, Host: strings.TrimSuffix(p.DNSName, "."),
			Addresses: p.TailscaleIPs, Network: NetTailscale, OS: p.OS, Source: "tailscale",
		})
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	return hosts
}

// dedupe keeps the first host per (name, first address) and puts ZimaOS
// boxes first.
func dedupe(hosts []Host) []Host {
	seen := map[string]bool{}
	var out []Host
	for _, h := range hosts {
		key := h.Name + "|" + h.Host
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool {
		zi, zj := out[i].OS == "ZimaOS", out[j].OS == "ZimaOS"
		if zi != zj {
			return zi
		}
		return out[i].Name < out[j].Name
	})
	if out == nil {
		out = []Host{}
	}
	return out
}
