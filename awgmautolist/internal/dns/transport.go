package dns

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
)

// maxUDPReply is the largest UDP DNS message the resolver accepts. A larger
// reply would have been truncated; without EDNS there is no TCP fallback.
const maxUDPReply = 512
const maxDNSMessage = 65535

// queryUDP performs the plain UDP exchange: one datagram out, one datagram
// back, no TCP fallback. Validation of ID, question and RCODE belongs to
// parseResponse, so the first datagram is handed over unfiltered: a stray or
// mismatched reply then becomes an explicit error instead of a silent retry.
func (s Server) queryUDP(ctx context.Context, d *net.Dialer, query []byte) ([]byte, error) {
	conn, err := d.DialContext(ctx, "udp", s.addr())
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, fmt.Errorf("dial context has no deadline")
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}
	if _, err := conn.Write(query); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	buf := make([]byte, maxUDPReply)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("empty datagram")
	}
	return buf[:n], nil
}

// queryDoH performs a DNS-over-HTTPS GET exchange with the wire query
// base64url-encoded without padding. The transport deliberately ignores proxy
// environment variables: the coverage probe must never detour through a
// proxy by accident.
func (s Server) queryDoH(ctx context.Context, d *net.Dialer, query []byte) ([]byte, error) {
	url := "https://" + hostPortForURL(s) + s.Path +
		"?dns=" + base64.RawURLEncoding.EncodeToString(query)
	tlsConfig := &tls.Config{
		ServerName:         s.TLSServerName,
		InsecureSkipVerify: s.Insecure,
		NextProtos:         []string{"h2", "http/1.1"},
	}
	transport := &http.Transport{
		DialContext:         d.DialContext,
		DialTLSContext:      dialTLSContext(d, tlsConfig),
		Proxy:               nil, // proxy environment variables are deliberately ignored
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: s.Timeout,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("DoH server must not redirect")
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/dns-message")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/dns-message") {
		return nil, fmt.Errorf("content type %q is not application/dns-message", ct)
	}
	msg, err := readLimited(resp.Body, maxDNSMessage)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(msg) == 0 {
		return nil, fmt.Errorf("empty DoH body")
	}
	return msg, nil
}

// readLimited reads at most limit+1 bytes so an oversized body is detectable.
func readLimited(r io.Reader, limit int) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > limit {
		return nil, fmt.Errorf("body exceeds the %d byte DNS message limit", limit)
	}
	return buf, nil
}

// hostPortForURL renders the server for a URL authority; a numeric IPv6
// address needs brackets.
func hostPortForURL(s Server) string {
	if strings.Contains(s.Server, ":") {
		return "[" + s.Server + "]:" + strconv.Itoa(int(s.ServerPort))
	}
	return s.Server + ":" + strconv.Itoa(int(s.ServerPort))
}

// dialTLSContext returns a DialTLSContext that dials with the bound dialer
// and then performs the TLS handshake with the given configuration.
func dialTLSContext(d *net.Dialer, cfg *tls.Config) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		tlsConn := tls.Client(raw, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		return tlsConn, nil
	}
}

// queryDoT performs a DNS-over-TLS exchange: the wire query framed with a
// two-byte big-endian length prefix, one framed reply read back. There is no
// plaintext DNS-over-TCP fallback.
func (s Server) queryDoT(ctx context.Context, d *net.Dialer, query []byte) ([]byte, error) {
	tlsConfig := &tls.Config{
		ServerName:         s.TLSServerName,
		InsecureSkipVerify: s.Insecure,
	}
	conn, err := dialTLSContext(d, tlsConfig)(ctx, "tcp", s.addr())
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, fmt.Errorf("dial context has no deadline")
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}
	frame := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(query)))
	copy(frame[2:], query)
	if _, err := conn.Write(frame); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	msg, err := readDNSFrame(conn)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// readDNSFrame reads exactly one length-prefixed DNS message.
func readDNSFrame(r io.Reader) ([]byte, error) {
	var prefix [2]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, fmt.Errorf("read frame length: %w", err)
	}
	length := int(binary.BigEndian.Uint16(prefix[:]))
	if length == 0 {
		return nil, fmt.Errorf("empty DNS-over-TLS frame")
	}
	msg := make([]byte, length)
	if _, err := io.ReadFull(r, msg); err != nil {
		return nil, fmt.Errorf("read frame body: %w", err)
	}
	return msg, nil
}

// newBoundDialer returns a dialer bound to s.DetourInterface. Binding is
// implemented for Linux via SO_BINDTODEVICE; any other OS was already
// rejected by Server.validate, so this only wires the socket control.
func newBoundDialer(s Server) (*net.Dialer, error) {
	d := &net.Dialer{Timeout: s.Timeout}
	if s.DetourInterface == "" {
		return d, nil
	}
	if !bindToDeviceSupported {
		return nil, fmt.Errorf("interface binding is not supported on %s", runtimeGOOS)
	}
	d.Control = func(_ string, _ string, c syscall.RawConn) error {
		return bindToDeviceImpl(c, s.DetourInterface)
	}
	return d, nil
}

// dialer builds the dialer used by every transport of this server.
func (s Server) dialer() (*net.Dialer, error) {
	return newBoundDialer(s)
}
