// Package dns implements the dig-level resolver used for IP coverage: a
// single configured server, one A and one AAAA query per Resolve call, strict
// answer validation, and no cache, background jobs, retry policy, fallback
// servers or use of the system resolver (spec 4.2). The server must be a
// numeric IP; a hostname is an explicit error, never a bootstrap case.
package dns

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// ServerType selects the transport used to reach the DNS server.
type ServerType string

// Supported transports: plain UDP, DNS-over-HTTPS (GET) and DNS-over-TLS.
const (
	TypeUDP   ServerType = "udp"
	TypeHTTPS ServerType = "https"
	TypeTLS   ServerType = "tls"
)

// defaultTimeout bounds each of the two queries when Server.Timeout is zero.
const defaultTimeout = 5 * time.Second

// Server describes the one DNS server this resolver may talk to. It carries
// the sing-box UDP/DoH/DoT server fields the coverage check needs.
//
// Server must be a numeric IP: a hostname would require a bootstrap resolver,
// which this module refuses by design. ServerPort zero selects the type's
// default port (53, 443 or 853). Path is the DoH request path, "/dns-query"
// by default. TLSServerName and Insecure are the only TLS options implemented;
// anything fancier must be rejected by the caller, not ignored here.
// DetourInterface pins all sockets to that kernel interface (Linux only;
// requesting a binding on any other OS is an explicit error). Timeout bounds
// each single query; Resolve itself adds no further limit.
type Server struct {
	Type            ServerType
	Server          string
	ServerPort      uint16
	Path            string
	TLSServerName   string
	Insecure        bool
	DetourInterface string
	Timeout         time.Duration
}

// Resolver answers A and AAAA questions against one configured server. It is
// stateless: no cache, no background jobs and no retry policy.
type Resolver struct {
	server Server
}

// NewResolver validates server and returns a resolver for it.
func NewResolver(server Server) (*Resolver, error) {
	if err := server.validate(); err != nil {
		return nil, err
	}
	return &Resolver{server: server}, nil
}

// Resolve sends one A and one AAAA query to the configured server and returns
// the combined addresses. NODATA for a single address family is allowed;
// SERVFAIL, NXDOMAIN, REFUSED, any other RCODE, a transport error, an invalid
// answer or an empty combined result fail the whole call.
func (r *Resolver) Resolve(ctx context.Context, domain string) ([]netip.Addr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.server.validate(); err != nil {
		return nil, err
	}
	name, err := normalizeDomain(domain)
	if err != nil {
		return nil, err
	}
	dialer, err := r.server.dialer()
	if err != nil {
		return nil, fmt.Errorf("dns %s: %w", r.server.Type, err)
	}
	var addrs []netip.Addr
	for _, qtype := range []int{typeA, typeAAAA} {
		family, err := r.query(ctx, dialer, name, qtype)
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, family...)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("dns %s %s: no A or AAAA records for %q",
			r.server.Type, r.server.addr(), name)
	}
	return addrs, nil
}

// query performs one round trip for one question type and validates the reply.
func (r *Resolver) query(ctx context.Context, dialer *net.Dialer, name string, qtype int) ([]netip.Addr, error) {
	s := r.server
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("dns %s %s: %w", s.Type, s.addr(), err)
	}
	wireQuery, err := buildQuery(id, name, qtype)
	if err != nil {
		return nil, fmt.Errorf("dns %s %s: %w", s.Type, s.addr(), err)
	}
	qctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	var msg []byte
	switch s.Type {
	case TypeUDP:
		msg, err = s.queryUDP(qctx, dialer, wireQuery)
	case TypeHTTPS:
		msg, err = s.queryDoH(qctx, dialer, wireQuery)
	case TypeTLS:
		msg, err = s.queryDoT(qctx, dialer, wireQuery)
	default:
		err = fmt.Errorf("unsupported server type %q", s.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("dns %s %s: %w", s.Type, s.addr(), err)
	}
	addrs, err := parseResponse(msg, id, name, qtype)
	if err != nil {
		return nil, fmt.Errorf("dns %s %s: %w", s.Type, s.addr(), err)
	}
	return addrs, nil
}

// validate applies defaults and rejects unsupported or ambiguous settings. It
// mutates the receiver so the stored copy keeps the effective values.
func (s *Server) validate() error {
	switch s.Type {
	case TypeUDP, TypeHTTPS, TypeTLS:
	default:
		return fmt.Errorf("dns: unsupported server type %q", s.Type)
	}
	if _, err := netip.ParseAddr(s.Server); err != nil {
		return fmt.Errorf("dns: server must be a numeric IP, got %q", s.Server)
	}
	if s.ServerPort == 0 {
		switch s.Type {
		case TypeUDP:
			s.ServerPort = 53
		case TypeHTTPS:
			s.ServerPort = 443
		case TypeTLS:
			s.ServerPort = 853
		}
	}
	if s.Timeout == 0 {
		s.Timeout = defaultTimeout
	}
	if s.Timeout < 0 {
		return fmt.Errorf("dns: timeout must not be negative")
	}
	switch s.Type {
	case TypeUDP:
		if s.Path != "" {
			return fmt.Errorf("dns: path applies only to DoH servers")
		}
		if s.TLSServerName != "" || s.Insecure {
			return fmt.Errorf("dns: TLS options apply only to DoH/DoT servers")
		}
	case TypeHTTPS:
		if s.Path == "" {
			s.Path = "/dns-query"
		}
		if !strings.HasPrefix(s.Path, "/") {
			return fmt.Errorf("dns: DoH path must start with '/', got %q", s.Path)
		}
		if s.TLSServerName == "" {
			s.TLSServerName = s.Server
		}
	case TypeTLS:
		if s.Path != "" {
			return fmt.Errorf("dns: path applies only to DoH servers")
		}
		if s.TLSServerName == "" {
			s.TLSServerName = s.Server
		}
	}
	if strings.ContainsAny(s.DetourInterface, " \t\r\n") {
		return fmt.Errorf("dns: detour must be a kernel interface name")
	}
	if s.DetourInterface != "" && !bindToDeviceSupported {
		return fmt.Errorf("dns: interface binding is not supported on %s", runtimeGOOS)
	}
	return nil
}

// addr is the dial address of the server.
func (s Server) addr() string {
	return net.JoinHostPort(s.Server, strconv.Itoa(int(s.ServerPort)))
}

// normalizeDomain lowercases and validates the queried name. IP literals,
// underscores and other non-hostname characters are refused.
func normalizeDomain(domain string) (string, error) {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if name == "" {
		return "", fmt.Errorf("dns: empty domain")
	}
	if net.ParseIP(name) != nil {
		return "", fmt.Errorf("dns: %q is an IP literal, not a domain", domain)
	}
	if len(name) > 253 {
		return "", fmt.Errorf("dns: domain %q is too long", name)
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("dns: invalid domain %q", domain)
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return "", fmt.Errorf("dns: invalid domain %q", domain)
			}
		}
	}
	return name, nil
}

// randomID draws a fresh transaction ID for one query.
func randomID() (uint16, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("transaction ID: %w", err)
	}
	return binary.BigEndian.Uint16(b[:]), nil
}
