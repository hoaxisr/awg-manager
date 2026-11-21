package phphost

import (
	"context"
	"net"
	"sort"
	"sync"
	"time"
)

// A phone's (or a router's, or an ISP's) DNS can keep answering with an address that is not the site's: a free host
// whose domain was created a moment ago is often a wildcard "parking" server for a while, and the resolver in front
// of the device keeps handing that out long after the real record exists. Seen live: the same domain resolved to
// 185.27.134.216 (the real host: answers the node) on a Mac and on every public resolver, and to 185.27.134.24 (some
// other nginx that has never heard of the domain: a plain 404) on the phone. Everything the wizard asked of the site
// then looked like "the site is not a node", for good. So the site is looked up on public resolvers too, theirs
// first, and every address is a candidate (see Site.getValid).
var publicDNS = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}

// lookupHost resolves host through server ("" = the system resolver). A variable so tests need no network.
var lookupHost = func(ctx context.Context, server, host string) []string {
	r := net.DefaultResolver
	if server != "" {
		r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", server)
		}}
	}
	ips, err := r.LookupHost(ctx, host)
	if err != nil {
		return nil
	}
	return ips
}

// candidateIPs is every address host resolves to - public resolvers' answers first, then the system's - each once,
// IPv4 before IPv6 within a source. Looked up once per Site.
func (s *Site) candidateIPs(ctx context.Context, host string) []string {
	if net.ParseIP(host) != nil {
		return []string{host}
	}
	s.mu.Lock()
	if got, ok := s.ips[host]; ok {
		s.mu.Unlock()
		return got
	}
	s.mu.Unlock()

	servers := append(append([]string{}, publicDNS...), "") // "" = system, last
	answers := make([][]string, len(servers))
	lctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		go func(i int, srv string) {
			defer wg.Done()
			answers[i] = lookupHost(lctx, srv, host)
		}(i, srv)
	}
	wg.Wait()

	seen := map[string]bool{}
	var out []string
	for _, a := range answers {
		v4 := append([]string{}, a...)
		sort.SliceStable(v4, func(i, j int) bool { return isV4(v4[i]) && !isV4(v4[j]) })
		for _, ip := range v4 {
			if !seen[ip] {
				seen[ip] = true
				out = append(out, ip)
			}
		}
	}
	s.mu.Lock()
	if s.ips == nil {
		s.ips = map[string][]string{}
	}
	s.ips[host] = out
	s.mu.Unlock()
	return out
}

func isV4(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && p.To4() != nil
}

// ordered is candidateIPs with the address that last proved right (if any) in front.
func (s *Site) ordered(ctx context.Context, host string) []string {
	ips := s.candidateIPs(ctx, host)
	s.mu.Lock()
	pin := s.pin
	s.mu.Unlock()
	if pin == "" {
		return ips
	}
	out := []string{pin}
	for _, ip := range ips {
		if ip != pin {
			out = append(out, ip)
		}
	}
	return out
}

func (s *Site) setPin(ip string) {
	s.mu.Lock()
	s.pin = ip
	s.mu.Unlock()
}

// dialIP connects to one address of the site (the dial hook is for tests).
func (s *Site) dialIP(ctx context.Context, network, ip, port string) (net.Conn, error) {
	if s.dialHook != nil {
		return s.dialHook(ctx, network, ip, port)
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(ip, port))
}

// dialContext is the transport's dial: the site's addresses in order, the first that connects wins. A name that
// resolves nowhere is dialled as it is, leaving the verdict to the system.
func (s *Site) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	ips := s.ordered(ctx, host)
	if len(ips) == 0 {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	var lastErr error
	for _, ip := range ips {
		c, err := s.dialIP(ctx, network, ip, port)
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// dialFixed always connects to ip (the request still names the site, so the Host header and TLS name are right).
func (s *Site) dialFixed(ip string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		return s.dialIP(ctx, network, ip, port)
	}
}
