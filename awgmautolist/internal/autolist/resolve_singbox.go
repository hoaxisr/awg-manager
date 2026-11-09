package autolist

import (
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/dnsfeed"
)

const (
	singboxDNSQueryA    = 1
	singboxDNSQueryAAAA = 28
	maxDNSMessageSize   = 65535
)

type singboxDNSServer struct {
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port,omitempty"`
	Path       string `json:"path,omitempty"`
	Detour     string `json:"detour,omitempty"`
	TLS        *struct {
		ServerName string `json:"server_name,omitempty"`
		Insecure   bool   `json:"insecure,omitempty"`
	} `json:"tls,omitempty"`
}

type singboxDNSQuery struct {
	wire []byte
	id   uint16
	name string
}

// ResolveSingboxDomain resolves domain through a DNS server configured in
// sing-box. resolveVia must have the form "sb:<tag>". Only direct UDP, DoH and
// DoT servers are supported: a satellite cannot reproduce a configured detour
// through a sing-box outbound. DoH and DoT perform both A and AAAA queries.
func (r *RuleClient) ResolveSingboxDomain(ctx context.Context, domain, resolveVia string) ([]string, error) {
	via := strings.TrimSpace(resolveVia)
	if !strings.HasPrefix(via, "sb:") || strings.TrimSpace(strings.TrimPrefix(via, "sb:")) == "" {
		return nil, fmt.Errorf("sing-box resolver: expected sb:<tag>, got %q", resolveVia)
	}
	tag := strings.TrimSpace(strings.TrimPrefix(via, "sb:"))

	body, err := r.get(ctx, "/api/singbox/router/dns/servers/list")
	if err != nil {
		return nil, err
	}
	var env struct {
		Success bool               `json:"success"`
		Data    []singboxDNSServer `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("sing-box DNS servers list: %w", err)
	}
	if !env.Success {
		return nil, errors.New("sing-box DNS servers list: API reported failure")
	}

	var server *singboxDNSServer
	for i := range env.Data {
		if env.Data[i].Tag == tag {
			server = &env.Data[i]
			break
		}
	}
	if server == nil {
		return nil, fmt.Errorf("sing-box DNS server %q not found", tag)
	}
	if strings.TrimSpace(server.Detour) != "" {
		return nil, fmt.Errorf("sing-box DNS server %q: detour %q is unsupported", tag, server.Detour)
	}
	switch server.Type {
	case "udp", "https", "tls":
	default:
		return nil, fmt.Errorf("sing-box DNS server %q: unsupported type %q (want udp, https or tls)", tag, server.Type)
	}
	if strings.TrimSpace(server.Server) == "" {
		return nil, fmt.Errorf("sing-box DNS server %q: server is empty", tag)
	}

	switch server.Type {
	case "udp":
		return r.resolveSingboxUDP(ctx, domain, *server)
	case "https":
		return r.resolveSingboxHTTPS(ctx, domain, *server)
	default: // tls
		return r.resolveSingboxTLS(ctx, domain, *server)
	}
}

func (r *RuleClient) resolveSingboxUDP(ctx context.Context, domain string, server singboxDNSServer) ([]string, error) {
	address, err := singboxDNSServerAddress(server, 53)
	if err != nil {
		return nil, err
	}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, network, address)
		},
	}
	addresses, err := resolver.LookupHost(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("sing-box UDP DNS %q: %w", server.Tag, err)
	}
	return addresses, nil
}

func (r *RuleClient) resolveSingboxHTTPS(ctx context.Context, domain string, server singboxDNSServer) ([]string, error) {
	address, err := singboxDNSServerAddress(server, 443)
	if err != nil {
		return nil, err
	}
	path := server.Path
	if path == "" {
		path = "/dns-query"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	baseURL := url.URL{Scheme: "https", Host: address, Path: path}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext:       dialer.DialContext,
		TLSClientConfig:   singboxDNSTLSConfig(server),
		ForceAttemptHTTP2: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	if r.http != nil {
		client.Timeout = r.http.Timeout
	}

	return resolveSingboxWire(ctx, domain, func(ctx context.Context, query []byte) ([]byte, error) {
		u := baseURL
		u.RawQuery = url.Values{"dns": {base64.RawURLEncoding.EncodeToString(query)}}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/dns-message")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("DoH status %d", resp.StatusCode)
		}
		payload, err := io.ReadAll(io.LimitReader(resp.Body, maxDNSMessageSize+1))
		if err != nil {
			return nil, err
		}
		if len(payload) > maxDNSMessageSize {
			return nil, errors.New("DoH response exceeds 65535 bytes")
		}
		return payload, nil
	})
}

func (r *RuleClient) resolveSingboxTLS(ctx context.Context, domain string, server singboxDNSServer) ([]string, error) {
	address, err := singboxDNSServerAddress(server, 853)
	if err != nil {
		return nil, err
	}
	tlsConfig := singboxDNSTLSConfig(server)
	return resolveSingboxWire(ctx, domain, func(ctx context.Context, query []byte) ([]byte, error) {
		dialer := net.Dialer{Timeout: 5 * time.Second}
		rawConn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		conn := tls.Client(rawConn, tlsConfig)
		stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stopClose()
		defer conn.Close()
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		} else {
			_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
		}
		if err := conn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		if err := writeSingboxDNSFrame(conn, query); err != nil {
			return nil, fmt.Errorf("write DoT frame: %w", err)
		}
		payload, err := readSingboxDNSFrame(conn)
		if err != nil {
			return nil, fmt.Errorf("read DoT frame: %w", err)
		}
		return payload, nil
	})
}

func resolveSingboxWire(ctx context.Context, domain string, exchange func(context.Context, []byte) ([]byte, error)) ([]string, error) {
	addresses := make([]string, 0, 2)
	seen := make(map[string]struct{})
	var errs []error
	for _, queryType := range []uint16{singboxDNSQueryA, singboxDNSQueryAAAA} {
		query, err := newSingboxDNSQuery(domain, queryType)
		if err != nil {
			return nil, err
		}
		payload, err := exchange(ctx, query.wire)
		if err == nil {
			if len(payload) >= 2 && binary.BigEndian.Uint16(payload[:2]) != query.id {
				err = fmt.Errorf("DNS response ID mismatch")
			} else {
				var answer dnsfeed.Answer
				answer, err = dnsfeed.ParseResponse(payload)
				if err == nil {
					if answer.QueriedName != query.name {
						err = fmt.Errorf("DNS response question %q does not match %q", answer.QueriedName, query.name)
					} else {
						for _, address := range answer.RealIPs {
							value := address.String()
							if _, ok := seen[value]; ok {
								continue
							}
							seen[value] = struct{}{}
							addresses = append(addresses, value)
						}
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, dnsfeed.ErrNoUsableAnswers) {
				continue
			}
			errs = append(errs, fmt.Errorf("DNS query type %d: %w", queryType, err))
		}
	}
	if len(addresses) > 0 {
		return addresses, nil
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return []string{}, nil
}

func newSingboxDNSQuery(domain string, queryType uint16) (singboxDNSQuery, error) {
	if queryType != singboxDNSQueryA && queryType != singboxDNSQueryAAAA {
		return singboxDNSQuery{}, fmt.Errorf("unsupported DNS query type %d", queryType)
	}
	name := strings.ToLower(strings.TrimSpace(domain))
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return singboxDNSQuery{}, errors.New("DNS domain is empty")
	}
	labels := strings.Split(name, ".")
	wireNameLength := 1
	for _, label := range labels {
		if len(label) == 0 {
			return singboxDNSQuery{}, fmt.Errorf("DNS domain %q has an empty label", domain)
		}
		if len(label) > 63 {
			return singboxDNSQuery{}, fmt.Errorf("DNS domain %q has a label longer than 63 bytes", domain)
		}
		for i := 0; i < len(label); i++ {
			if label[i] < 0x21 || label[i] > 0x7e {
				return singboxDNSQuery{}, fmt.Errorf("DNS domain %q contains a non-ASCII or control character", domain)
			}
		}
		wireNameLength += 1 + len(label)
	}
	if wireNameLength > 255 {
		return singboxDNSQuery{}, fmt.Errorf("DNS domain %q exceeds 255 wire bytes", domain)
	}

	var id [2]byte
	if _, err := io.ReadFull(crand.Reader, id[:]); err != nil {
		return singboxDNSQuery{}, fmt.Errorf("DNS transaction ID: %w", err)
	}
	wire := make([]byte, 12, 12+wireNameLength+4)
	binary.BigEndian.PutUint16(wire[0:2], binary.BigEndian.Uint16(id[:]))
	binary.BigEndian.PutUint16(wire[2:4], 0x0100) // recursion desired
	binary.BigEndian.PutUint16(wire[4:6], 1)
	for _, label := range labels {
		wire = append(wire, byte(len(label)))
		wire = append(wire, label...)
	}
	wire = append(wire, 0, 0, 0, 0, 0)
	questionEnd := len(wire)
	binary.BigEndian.PutUint16(wire[questionEnd-4:questionEnd-2], queryType)
	binary.BigEndian.PutUint16(wire[questionEnd-2:questionEnd], 1) // IN
	return singboxDNSQuery{wire: wire, id: binary.BigEndian.Uint16(id[:]), name: name}, nil
}

func singboxDNSServerAddress(server singboxDNSServer, defaultPort int) (string, error) {
	port := server.ServerPort
	if port == 0 {
		port = defaultPort
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("sing-box DNS server %q: invalid server_port %d", server.Tag, server.ServerPort)
	}
	host := strings.TrimSpace(server.Server)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port)), nil
}

func singboxDNSTLSConfig(server singboxDNSServer) *tls.Config {
	serverName := strings.TrimSpace(server.Server)
	if strings.HasPrefix(serverName, "[") && strings.HasSuffix(serverName, "]") {
		serverName = strings.TrimSuffix(strings.TrimPrefix(serverName, "["), "]")
	}
	insecure := false
	if server.TLS != nil {
		if configured := strings.TrimSpace(server.TLS.ServerName); configured != "" {
			serverName = configured
		}
		insecure = server.TLS.Insecure
	}
	return &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: insecure, // #nosec G402 -- mirrors the configured sing-box DNS TLS policy.
	}
}

func writeSingboxDNSFrame(writer io.Writer, payload []byte) error {
	if len(payload) > maxDNSMessageSize {
		return errors.New("DNS message exceeds 65535 bytes")
	}
	frame := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(payload)))
	copy(frame[2:], payload)
	for len(frame) > 0 {
		n, err := writer.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

func readSingboxDNSFrame(reader io.Reader) ([]byte, error) {
	var prefix [2]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return nil, err
	}
	payload := make([]byte, int(binary.BigEndian.Uint16(prefix[:])))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}
