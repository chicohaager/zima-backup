// Package discover finds machines a job could target: ZimaOS boxes that
// announce themselves with mDNS (on the LAN and on any mesh that carries
// multicast, e.g. ZeroTier), and Tailscale peers from the local daemon.
package discover

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Service is one mDNS service instance with everything its announcement
// carried (SRV target and port, TXT records, addresses).
type Service struct {
	Instance string            // e.g. "ZimaOS"
	Target   string            // SRV target, e.g. "ZimaOS.local"
	Port     int               // SRV port (the ZimaOS web UI port)
	TXT      map[string]string // e.g. os=ZimaOS
	Addrs    []netip.Addr
	Iface    string // interface the answer arrived on
}

var mdnsGroup = netip.MustParseAddrPort("224.0.0.251:5353")

// zimaosService is what every ZimaOS box announces (measured on 1.7.1:
// /etc/avahi/services/zimaos.service → _zimaos._tcp, TXT os=ZimaOS).
const zimaosService = "_zimaos._tcp.local."

// queryMDNS sends one PTR query for service on every multicast-capable
// interface and collects the answers until ctx ends.
func queryMDNS(ctx context.Context, service string) ([]Service, error) {
	q, err := buildQuery(service)
	if err != nil {
		return nil, err
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	type packet struct {
		data  []byte
		iface string
	}
	packets := make(chan packet, 64)
	var conns []*net.UDPConn
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagMulticast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		ifc := ifc
		conn, err := net.ListenMulticastUDP("udp4", &ifc, net.UDPAddrFromAddrPort(mdnsGroup))
		if err != nil {
			continue // an interface without IPv4 or without multicast support
		}
		conns = append(conns, conn)
		go func() {
			buf := make([]byte, 9000)
			for {
				n, _, err := conn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				packets <- packet{data: append([]byte(nil), buf[:n]...), iface: ifc.Name}
			}
		}()
		// send the query out of this interface (the socket is bound to it)
		if _, err := conn.WriteToUDP(q, net.UDPAddrFromAddrPort(mdnsGroup)); err != nil {
			// fall back to a plain socket: some interfaces refuse sends on the group socket
			if c, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(mdnsGroup)); err == nil {
				_, _ = c.Write(q)
				_ = c.Close()
			}
		}
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	if len(conns) == 0 {
		return nil, nil
	}
	seen := map[string]*Service{}
	var out []Service
	for {
		select {
		case <-ctx.Done():
			return out, nil
		case p := <-packets:
			for _, s := range parseAnswers(p.data, service) {
				key := s.Instance + "@" + p.iface
				if prev, ok := seen[key]; ok {
					prev.Addrs = mergeAddrs(prev.Addrs, s.Addrs)
					continue
				}
				s.Iface = p.iface
				out = append(out, s)
				seen[key] = &out[len(out)-1]
			}
		}
	}
}

func buildQuery(service string) ([]byte, error) {
	name, err := dnsmessage.NewName(service)
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return b.Finish()
}

// parseAnswers reads one response and returns the service instances it
// describes: the PTR names the instance, SRV/TXT/A/AAAA in the answer or
// additional sections fill it in (avahi puts them all in one packet,
// measured: 144 bytes with PTR, TXT, SRV, AAAA, A).
func parseAnswers(data []byte, service string) []Service {
	var p dnsmessage.Parser
	h, err := p.Start(data)
	if err != nil || !h.Response {
		return nil
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil
	}
	var rrs []dnsmessage.Resource
	for _, next := range []func() ([]dnsmessage.Resource, error){p.AllAnswers, p.AllAuthorities, p.AllAdditionals} {
		got, err := next()
		if err != nil {
			break
		}
		rrs = append(rrs, got...)
	}
	instances := map[string]*Service{}
	var order []string
	for _, rr := range rrs {
		if ptr, ok := rr.Body.(*dnsmessage.PTRResource); ok && strings.EqualFold(rr.Header.Name.String(), service) {
			full := ptr.PTR.String()
			if _, dup := instances[full]; !dup {
				instances[full] = &Service{Instance: instanceName(full, service), TXT: map[string]string{}}
				order = append(order, full)
			}
		}
	}
	addrs := map[string][]netip.Addr{}
	for _, rr := range rrs {
		name := rr.Header.Name.String()
		switch body := rr.Body.(type) {
		case *dnsmessage.SRVResource:
			if s, ok := instances[name]; ok {
				s.Target = strings.TrimSuffix(body.Target.String(), ".")
				s.Port = int(body.Port)
			}
		case *dnsmessage.TXTResource:
			if s, ok := instances[name]; ok {
				for _, t := range body.TXT {
					if k, v, found := strings.Cut(t, "="); found {
						s.TXT[k] = v
					}
				}
			}
		case *dnsmessage.AResource:
			addrs[strings.TrimSuffix(name, ".")] = append(addrs[strings.TrimSuffix(name, ".")], netip.AddrFrom4(body.A))
		case *dnsmessage.AAAAResource:
			addrs[strings.TrimSuffix(name, ".")] = append(addrs[strings.TrimSuffix(name, ".")], netip.AddrFrom16(body.AAAA))
		}
	}
	var out []Service
	for _, full := range order {
		s := instances[full]
		s.Addrs = addrs[s.Target]
		out = append(out, *s)
	}
	return out
}

// instanceName strips the service suffix and DNS escapes from a PTR
// target: "ZimaOS._zimaos._tcp.local." → "ZimaOS".
func instanceName(full, service string) string {
	name := strings.TrimSuffix(full, "."+service)
	name = strings.TrimSuffix(name, ".")
	return strings.ReplaceAll(name, `\ `, " ")
}

func mergeAddrs(a, b []netip.Addr) []netip.Addr {
	for _, x := range b {
		dup := false
		for _, y := range a {
			if x == y {
				dup = true
				break
			}
		}
		if !dup {
			a = append(a, x)
		}
	}
	return a
}

// mdnsWindow is how long a query listens for answers; avahi answers
// within a few hundred milliseconds, mesh peers may take longer.
const mdnsWindow = 2 * time.Second
