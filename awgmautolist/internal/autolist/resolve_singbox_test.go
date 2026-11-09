package autolist

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveSingboxDomainUDP(t *testing.T) {
	host, port := startSingboxTestUDP(t)
	client := newSingboxTestClient(t, map[string]any{
		"tag": "plain", "type": "udp", "server": host, "server_port": port,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := client.ResolveSingboxDomain(ctx, "Example.COM", "sb:plain")
	if err != nil {
		t.Fatalf("ResolveSingboxDomain: %v", err)
	}
	assertSingboxTestIPs(t, got, "192.0.2.10", "2001:db8::10")
}

func TestResolveSingboxDomainHTTPS(t *testing.T) {
	certificate := newSingboxTestCertificate(t)
	sni := make(chan string, 2)
	var queries atomic.Int32
	doh := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/dns-query" {
			t.Errorf("DoH request = %s %s", r.Method, r.URL.Path)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if accept := r.Header.Get("Accept"); accept != "application/dns-message" {
			t.Errorf("DoH Accept = %q", accept)
		}
		query, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		if err != nil {
			t.Errorf("DoH dns parameter: %v", err)
			http.Error(w, "bad dns parameter", http.StatusBadRequest)
			return
		}
		response, err := singboxTestDNSResponse(query)
		if err != nil {
			t.Errorf("DoH query: %v", err)
			http.Error(w, "bad DNS query", http.StatusBadRequest)
			return
		}
		queries.Add(1)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(response)
	}))
	doh.TLS = &tls.Config{
		Certificates: []tls.Certificate{certificate},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			sni <- hello.ServerName
			return nil, nil
		},
	}
	doh.StartTLS()
	t.Cleanup(doh.Close)
	host, port := splitSingboxTestAddress(t, strings.TrimPrefix(doh.URL, "https://"))
	client := newSingboxTestClient(t, map[string]any{
		"tag": "secure", "type": "https", "server": host, "server_port": port,
		"tls": map[string]any{"server_name": "dns.internal", "insecure": true},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := client.ResolveSingboxDomain(ctx, "example.com", "sb:secure")
	if err != nil {
		t.Fatalf("ResolveSingboxDomain: %v", err)
	}
	assertSingboxTestIPs(t, got, "192.0.2.10", "2001:db8::10")
	if queries.Load() != 2 {
		t.Fatalf("DoH queries = %d, want A and AAAA", queries.Load())
	}
	select {
	case gotSNI := <-sni:
		if gotSNI != "dns.internal" {
			t.Fatalf("DoH SNI = %q, want dns.internal", gotSNI)
		}
	default:
		t.Fatal("DoH server did not receive a ClientHello")
	}
}

func TestResolveSingboxDomainTLS(t *testing.T) {
	certificate := newSingboxTestCertificate(t)
	sni := make(chan string, 2)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen DoT: %v", err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			sni <- hello.ServerName
			return nil, nil
		},
	}
	serverErrors := make(chan error, 4)
	var connections sync.WaitGroup
	connections.Add(1)
	go func() {
		defer connections.Done()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer connections.Done()
				conn := tls.Server(raw, tlsConfig)
				defer conn.Close()
				query, err := readSingboxTestFrame(conn)
				if err != nil {
					serverErrors <- err
					return
				}
				response, err := singboxTestDNSResponse(query)
				if err != nil {
					serverErrors <- err
					return
				}
				if err := writeSingboxTestFrame(conn, response); err != nil {
					serverErrors <- err
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		connections.Wait()
	})
	host, port := splitSingboxTestAddress(t, listener.Addr().String())
	client := newSingboxTestClient(t, map[string]any{
		"tag": "dot", "type": "tls", "server": host, "server_port": port,
		"tls": map[string]any{"server_name": "dot.internal", "insecure": true},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := client.ResolveSingboxDomain(ctx, "example.com", "sb:dot")
	if err != nil {
		t.Fatalf("ResolveSingboxDomain: %v", err)
	}
	assertSingboxTestIPs(t, got, "192.0.2.10", "2001:db8::10")
	for range 2 {
		select {
		case gotSNI := <-sni:
			if gotSNI != "dot.internal" {
				t.Errorf("DoT SNI = %q, want dot.internal", gotSNI)
			}
		case err := <-serverErrors:
			t.Fatalf("DoT server: %v", err)
		default:
			t.Fatal("DoT server did not receive both ClientHellos")
		}
	}
}

func TestResolveSingboxDomainRejectsUnsupportedConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		server map[string]any
		want   string
	}{
		{
			name: "detour",
			server: map[string]any{
				"tag": "routed", "type": "udp", "server": "192.0.2.1", "detour": "vpn",
			},
			want: "detour",
		},
		{
			name:   "local type",
			server: map[string]any{"tag": "local", "type": "local"},
			want:   "unsupported type",
		},
		{
			name:   "quic type",
			server: map[string]any{"tag": "quic", "type": "quic", "server": "192.0.2.1"},
			want:   "unsupported type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newSingboxTestClient(t, tt.server)
			_, err := client.ResolveSingboxDomain(context.Background(), "example.com", "sb:"+tt.server["tag"].(string))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestResolveSingboxDomainValidatesViaAndTag(t *testing.T) {
	client := NewRuleClient("http://127.0.0.1:1", "")
	for _, via := range []string{"", "plain", "sb:", " SB:missing"} {
		if _, err := client.ResolveSingboxDomain(context.Background(), "example.com", via); err == nil {
			t.Errorf("ResolveSingboxDomain via %q: expected error", via)
		}
	}

	client = newSingboxTestClient(t)
	_, err := client.ResolveSingboxDomain(context.Background(), "example.com", "sb:missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing tag error = %v", err)
	}
}

func TestNewSingboxDNSQueryBuildsBoundedWireNames(t *testing.T) {
	query, err := newSingboxDNSQuery("WWW.Example.COM.", singboxDNSQueryA)
	if err != nil {
		t.Fatalf("newSingboxDNSQuery: %v", err)
	}
	if query.name != "www.example.com" {
		t.Fatalf("normalized name = %q", query.name)
	}
	if got := binary.BigEndian.Uint16(query.wire[:2]); got != query.id {
		t.Fatalf("wire ID = %#x, query ID = %#x", got, query.id)
	}
	if got := binary.BigEndian.Uint16(query.wire[2:4]); got != 0x0100 {
		t.Fatalf("flags = %#x, want recursion desired", got)
	}
	if got := binary.BigEndian.Uint16(query.wire[4:6]); got != 1 {
		t.Fatalf("QDCOUNT = %d", got)
	}
	wantName := []byte{3, 'w', 'w', 'w', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0}
	if got := query.wire[12 : 12+len(wantName)]; string(got) != string(wantName) {
		t.Fatalf("QNAME = %v, want %v", got, wantName)
	}
	end := 12 + len(wantName)
	if got := binary.BigEndian.Uint16(query.wire[end : end+2]); got != singboxDNSQueryA {
		t.Fatalf("QTYPE = %d", got)
	}
	if got := binary.BigEndian.Uint16(query.wire[end+2 : end+4]); got != 1 {
		t.Fatalf("QCLASS = %d", got)
	}

	invalid := []string{
		"", ".", "a..example", strings.Repeat("a", 64) + ".example", "exämple.com",
		strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." +
			strings.Repeat("c", 63) + "." + strings.Repeat("d", 63),
	}
	for _, domain := range invalid {
		if _, err := newSingboxDNSQuery(domain, singboxDNSQueryA); err == nil {
			t.Errorf("domain %q: expected error", domain)
		}
	}
}

func newSingboxTestClient(t *testing.T, servers ...map[string]any) *RuleClient {
	t.Helper()
	if servers == nil {
		servers = []map[string]any{}
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/singbox/router/dns/servers/list" {
			t.Errorf("unexpected AWGM request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("AWGM Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": servers})
	}))
	t.Cleanup(api.Close)
	return NewRuleClient(api.URL, "test-key")
}

func startSingboxTestUDP(t *testing.T) (string, int) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen UDP DNS: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 65535)
		for {
			n, peer, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			response, err := singboxTestDNSResponse(buffer[:n])
			if err == nil {
				_, _ = conn.WriteTo(response, peer)
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
	return splitSingboxTestAddress(t, conn.LocalAddr().String())
}

func singboxTestDNSResponse(query []byte) ([]byte, error) {
	questionEnd, queryType, err := singboxTestQuestionEnd(query)
	if err != nil {
		return nil, err
	}
	response := append([]byte(nil), query[:questionEnd]...)
	binary.BigEndian.PutUint16(response[2:4], 0x8180)
	binary.BigEndian.PutUint16(response[4:6], 1)
	binary.BigEndian.PutUint16(response[6:8], 1)
	binary.BigEndian.PutUint16(response[8:12], 0)

	var address netip.Addr
	switch queryType {
	case singboxDNSQueryA:
		address = netip.MustParseAddr("192.0.2.10")
	case singboxDNSQueryAAAA:
		address = netip.MustParseAddr("2001:db8::10")
	default:
		return nil, fmt.Errorf("unexpected QTYPE %d", queryType)
	}
	response = append(response, 0xc0, 0x0c)
	response = binary.BigEndian.AppendUint16(response, queryType)
	response = binary.BigEndian.AppendUint16(response, 1)
	response = binary.BigEndian.AppendUint32(response, 60)
	response = binary.BigEndian.AppendUint16(response, uint16(len(address.AsSlice())))
	response = append(response, address.AsSlice()...)
	return response, nil
}

func singboxTestQuestionEnd(query []byte) (int, uint16, error) {
	if len(query) < 17 || binary.BigEndian.Uint16(query[4:6]) != 1 {
		return 0, 0, fmt.Errorf("invalid DNS query header")
	}
	offset := 12
	for {
		if offset >= len(query) {
			return 0, 0, fmt.Errorf("unterminated QNAME")
		}
		length := int(query[offset])
		offset++
		if length == 0 {
			break
		}
		if length > 63 || offset+length > len(query) {
			return 0, 0, fmt.Errorf("invalid QNAME label")
		}
		offset += length
	}
	if offset+4 > len(query) {
		return 0, 0, fmt.Errorf("truncated query question")
	}
	// Go's UDP resolver may append an EDNS0 additional record. The response
	// needs only the original question, so ignore that optional tail.
	return offset + 4, binary.BigEndian.Uint16(query[offset : offset+2]), nil
}

func readSingboxTestFrame(reader io.Reader) ([]byte, error) {
	var prefix [2]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return nil, err
	}
	payload := make([]byte, int(binary.BigEndian.Uint16(prefix[:])))
	_, err := io.ReadFull(reader, payload)
	return payload, err
}

func writeSingboxTestFrame(writer io.Writer, payload []byte) error {
	frame := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(payload)))
	copy(frame[2:], payload)
	for len(frame) > 0 {
		n, err := writer.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[n:]
	}
	return nil
}

func newSingboxTestCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate TLS key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sing-box test DNS"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"dns.internal", "dot.internal"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create TLS certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func splitSingboxTestAddress(t *testing.T, address string) (string, int) {
	t.Helper()
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split %q: %v", address, err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatalf("port in %q: %v", address, err)
	}
	return host, port
}

func assertSingboxTestIPs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("IPs = %v, want %v", got, want)
	}
	counts := make(map[string]int, len(got))
	for _, ip := range got {
		counts[ip]++
	}
	for _, ip := range want {
		if counts[ip] != 1 {
			t.Fatalf("IPs = %v, want %v", got, want)
		}
		counts[ip]--
	}
}
