package dns

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// dnsHandler answers parsed wire queries for the test servers.
type dnsHandler func(query []byte) (response []byte)

// testUDPServer serves one wire-protocol DNS server on the loopback.
type testUDPServer struct {
	t       *testing.T
	conn    *net.UDPConn
	handler dnsHandler
	wg      sync.WaitGroup
}

func newTestUDPServer(t *testing.T, handler dnsHandler) *testUDPServer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	s := &testUDPServer{t: t, conn: conn, handler: handler}
	s.wg.Add(1)
	go s.loop()
	t.Cleanup(func() {
		conn.Close()
		s.wg.Wait()
	})
	return s
}

func (s *testUDPServer) loop() {
	defer s.wg.Done()
	buf := make([]byte, maxUDPReply)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if resp := s.handler(buf[:n]); resp != nil {
			if _, err := s.conn.WriteToUDP(resp, from); err != nil {
				return
			}
		}
	}
}

func (s *testUDPServer) addr() Server {
	return Server{Type: TypeUDP, Server: "127.0.0.1", ServerPort: uint16(s.conn.LocalAddr().(*net.UDPAddr).Port)}
}

// buildAAnswer builds a NOERROR response with one A or AAAA record. The IP is
// encoded at its natural wire length: 4 bytes for A, 16 for AAAA.
func buildAAnswer(id uint16, name string, qtype int, addr net.IP) []byte {
	q := encodeQuestionForTest(name, qtype)
	rd := addr.To16()
	if qtype == typeA {
		rd = addr.To4()
	}
	msg := []byte{}
	msg = binary.BigEndian.AppendUint16(msg, id)
	msg = binary.BigEndian.AppendUint16(msg, 0x8180) // QR=1, RD=1, RA=1
	msg = binary.BigEndian.AppendUint16(msg, 1)      // QDCOUNT
	msg = binary.BigEndian.AppendUint16(msg, 1)      // ANCOUNT
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = append(msg, q...)
	msg = binary.BigEndian.AppendUint16(msg, 0xC00C) // pointer to question name
	msg = binary.BigEndian.AppendUint16(msg, uint16(qtype))
	msg = binary.BigEndian.AppendUint16(msg, classIN)
	msg = binary.BigEndian.AppendUint32(msg, 60)
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(rd)))
	msg = append(msg, rd...)
	return msg
}

// buildNODATAAnswer builds a NOERROR response with no answer records.
func buildNODATAAnswer(id uint16, name string, qtype int) []byte {
	q := encodeQuestionForTest(name, qtype)
	msg := []byte{}
	msg = binary.BigEndian.AppendUint16(msg, id)
	msg = binary.BigEndian.AppendUint16(msg, 0x8180)
	msg = binary.BigEndian.AppendUint16(msg, 1) // QDCOUNT
	msg = binary.BigEndian.AppendUint16(msg, 0) // ANCOUNT
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = append(msg, q...)
	return msg
}

// buildRCODEAnswer builds a response carrying the given RCODE.
func buildRCODEAnswer(id uint16, name string, qtype int, rcode int) []byte {
	q := encodeQuestionForTest(name, qtype)
	msg := []byte{}
	msg = binary.BigEndian.AppendUint16(msg, id)
	msg = binary.BigEndian.AppendUint16(msg, uint16(0x8180|rcode))
	msg = binary.BigEndian.AppendUint16(msg, 1)
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = append(msg, q...)
	return msg
}

// encodeQuestionForTest encodes the question section of name/qtype.
func encodeQuestionForTest(name string, qtype int) []byte {
	msg := []byte{}
	for _, label := range strings.Split(name, ".") {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, uint16(qtype))
	msg = binary.BigEndian.AppendUint16(msg, classIN)
	return msg
}

// handlerFor returns a handler answering both families from the given map:
// v4 for A and v6 for AAAA, NODATA for a nil IP.
func handlerFor(name string, v4, v6 net.IP) dnsHandler {
	return func(query []byte) []byte {
		if len(query) < hdrLen+4 {
			return nil
		}
		id := binary.BigEndian.Uint16(query[0:2])
		off := hdrLen
		for off < len(query) {
			l := int(query[off])
			if l == 0 {
				off++
				break
			}
			off += 1 + l
		}
		if off+4 > len(query) {
			return nil
		}
		qtype := int(binary.BigEndian.Uint16(query[off:]))
		switch qtype {
		case typeA:
			if v4 == nil {
				return buildNODATAAnswer(id, name, qtype)
			}
			return buildAAnswer(id, name, qtype, v4)
		case typeAAAA:
			if v6 == nil {
				return buildNODATAAnswer(id, name, qtype)
			}
			return buildAAnswer(id, name, qtype, v6)
		default:
			return buildRCODEAnswer(id, name, qtype, rcodeRef)
		}
	}
}

// resolveWith runs Resolve against the UDP test server.
func resolveWith(t *testing.T, s *testUDPServer, domain string) ([]string, error) {
	t.Helper()
	r, err := NewResolver(s.addr())
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	addrs, err := r.Resolve(context.Background(), domain)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out, nil
}

func TestResolveAAndAAAAOverUDP(t *testing.T) {
	s := newTestUDPServer(t, handlerFor("example.com", net.ParseIP("93.184.216.34"), net.ParseIP("2606:2800:220:1:248:1893:25c8:1946")))
	got, err := resolveWith(t, s, "example.com")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 2 || got[0] != "93.184.216.34" || got[1] != "2606:2800:220:1:248:1893:25c8:1946" {
		t.Fatalf("unexpected addresses: %v", got)
	}
}

func TestResolveNODATAOneFamily(t *testing.T) {
	s := newTestUDPServer(t, handlerFor("v4only.example.com", net.ParseIP("192.0.2.1"), nil))
	got, err := resolveWith(t, s, "v4only.example.com")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 1 || got[0] != "192.0.2.1" {
		t.Fatalf("unexpected addresses: %v", got)
	}
}

func TestResolveEmptyResultIsError(t *testing.T) {
	s := newTestUDPServer(t, handlerFor("empty.example.com", nil, nil))
	if _, err := resolveWith(t, s, "empty.example.com"); err == nil {
		t.Fatalf("expected error for empty combined result")
	}
}

func TestResolveServfailRefusedNXDOMAINFailWholeResult(t *testing.T) {
	cases := map[string]int{
		"SERVFAIL": rcodeServ,
		"REFUSED":  rcodeRef,
		"NXDOMAIN": rcodeNX,
	}
	for label, rcode := range cases {
		t.Run(label, func(t *testing.T) {
			// Even when the other family would answer, an RCODE on the first
			// (A) query must fail the whole result.
			base := handlerFor("x.example.com", net.ParseIP("192.0.2.7"), net.ParseIP("2001:db8::1"))
			s := newTestUDPServer(t, func(query []byte) []byte {
				if len(query) >= hdrLen+4 {
					off := hdrLen
					for off < len(query) {
						l := int(query[off])
						if l == 0 {
							off++
							break
						}
						off += 1 + l
					}
					qtype := int(binary.BigEndian.Uint16(query[off:]))
					if qtype == typeA {
						id := binary.BigEndian.Uint16(query[0:2])
						return buildRCODEAnswer(id, "x.example.com", qtype, rcode)
					}
				}
				return base(query)
			})
			if _, err := resolveWith(t, s, "x.example.com"); err == nil {
				t.Fatalf("expected error for %s", label)
			}
		})
	}
}

func TestResolveWrongTransactionID(t *testing.T) {
	name := "wid.example.com"
	s := newTestUDPServer(t, func(query []byte) []byte {
		id := binary.BigEndian.Uint16(query[0:2]) + 1
		return buildAAnswer(id, name, typeA, net.ParseIP("192.0.2.9"))
	})
	if _, err := resolveWith(t, s, name); err == nil || !strings.Contains(err.Error(), "transaction ID") {
		t.Fatalf("expected transaction ID error, got %v", err)
	}
}

func TestResolveWrongQuestion(t *testing.T) {
	name := "asked.example.com"
	other := "other.example.com"
	s := newTestUDPServer(t, func(query []byte) []byte {
		id := binary.BigEndian.Uint16(query[0:2])
		return buildAAnswer(id, other, typeA, net.ParseIP("192.0.2.10"))
	})
	if _, err := resolveWith(t, s, name); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected question mismatch error, got %v", err)
	}
}

func TestResolveInvalidHostname(t *testing.T) {
	s := newTestUDPServer(t, handlerFor("unused", net.ParseIP("192.0.2.1"), nil))
	for _, bad := range []string{"", "foo..bar", "-leading.example.com", "under_score.example.com", "xn--bad..example.com", "192.0.2.1"} {
		if _, err := resolveWith(t, s, bad); err == nil {
			t.Fatalf("expected error for invalid domain %q", bad)
		}
	}
}

func TestResolverRejectsHostnameServer(t *testing.T) {
	_, err := NewResolver(Server{Type: TypeUDP, Server: "dns.example.com"})
	if err == nil || !strings.Contains(err.Error(), "numeric IP") {
		t.Fatalf("expected numeric IP error, got %v", err)
	}
}

func TestResolveTimeout(t *testing.T) {
	s := newTestUDPServer(t, func(query []byte) []byte { return nil }) // never answers
	r, err := NewResolver(Server{Type: TypeUDP, Server: "127.0.0.1", ServerPort: uint16(s.conn.LocalAddr().(*net.UDPAddr).Port), Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	start := time.Now()
	_, err = r.Resolve(context.Background(), "slow.example.com")
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestResolveCanceledContext(t *testing.T) {
	s := newTestUDPServer(t, handlerFor("cancel.example.com", net.ParseIP("192.0.2.1"), nil))
	r, err := NewResolver(s.addr())
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Resolve(ctx, "cancel.example.com"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestDefaultPorts(t *testing.T) {
	cases := map[ServerType]uint16{TypeUDP: 53, TypeHTTPS: 443, TypeTLS: 853}
	for typ, want := range cases {
		r, err := NewResolver(Server{Type: typ, Server: "127.0.0.1"})
		if err != nil {
			t.Fatalf("new resolver %s: %v", typ, err)
		}
		if got := r.server.ServerPort; got != want {
			t.Fatalf("%s default port: got %d, want %d", typ, got, want)
		}
	}
}

func TestDetourBindingUnsupportedOSErrors(t *testing.T) {
	saved := bindToDeviceSupported
	savedGOOS := runtimeGOOS
	defer func() {
		bindToDeviceSupported = saved
		runtimeGOOS = savedGOOS
	}()
	// Force the "binding not available" path regardless of the build OS.
	bindToDeviceSupported = false
	runtimeGOOS = "plan9"
	_, err := NewResolver(Server{Type: TypeUDP, Server: "127.0.0.1", DetourInterface: "eth0"})
	if err == nil || !strings.Contains(err.Error(), "not supported on plan9") {
		t.Fatalf("expected explicit unsupported-binding error, got %v", err)
	}
}

// --- DoH tests ---

func TestResolveOverDoH(t *testing.T) {
	name := "doh.example.com"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dnsQuery, err := decodeDoHRequest(t, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id := binary.BigEndian.Uint16(dnsQuery[0:2])
		qtype := questionTypeForTest(t, dnsQuery)
		w.Header().Set("Content-Type", "application/dns-message")
		if qtype == typeA {
			w.Write(buildAAnswer(id, name, qtype, net.ParseIP("192.0.2.33")))
		} else {
			w.Write(buildAAnswer(id, name, qtype, net.ParseIP("2001:db8::33")))
		}
	}))
	defer srv.Close()
	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	r, err := NewResolver(Server{
		Type:       TypeHTTPS,
		Server:     host,
		ServerPort: uint16(port),
		Insecure:   true,
	})
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	addrs, err := r.Resolve(context.Background(), name)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(addrs) != 2 || addrs[0].String() != "192.0.2.33" || addrs[1].String() != "2001:db8::33" {
		t.Fatalf("unexpected addresses: %v", addrs)
	}
}

func TestDoHRejectsProxyEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("https_proxy", "http://127.0.0.1:1")
	name := "noproxy.example.com"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dnsQuery, err := decodeDoHRequest(t, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id := binary.BigEndian.Uint16(dnsQuery[0:2])
		qtype := questionTypeForTest(t, dnsQuery)
		w.Header().Set("Content-Type", "application/dns-message")
		if qtype == typeA {
			w.Write(buildAAnswer(id, name, qtype, net.ParseIP("192.0.2.44")))
		} else {
			w.Write(buildNODATAAnswer(id, name, qtype))
		}
	}))
	defer srv.Close()
	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	r, err := NewResolver(Server{Type: TypeHTTPS, Server: host, ServerPort: uint16(port), Insecure: true})
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	// With Proxy disabled, the request must succeed even though the proxy
	// environment points at a dead address.
	if _, err := r.Resolve(context.Background(), name); err != nil {
		t.Fatalf("resolve should bypass proxy env: %v", err)
	}
}

func decodeDoHRequest(t *testing.T, r *http.Request) ([]byte, error) {
	t.Helper()
	q := r.URL.Query().Get("dns")
	if q == "" {
		return nil, errors.New("missing dns parameter")
	}
	return base64.RawURLEncoding.DecodeString(q)
}

func questionTypeForTest(t *testing.T, query []byte) int {
	t.Helper()
	off := hdrLen
	for off < len(query) {
		l := int(query[off])
		if l == 0 {
			off++
			break
		}
		off += 1 + l
	}
	if off+4 > len(query) {
		t.Fatalf("query too short to hold a question")
	}
	return int(binary.BigEndian.Uint16(query[off:]))
}

// --- DoT tests ---

func TestResolveOverDoT(t *testing.T) {
	name := "dot.example.com"
	cert := testCert(t)
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	tlsListener := tls.NewListener(inner, &tls.Config{
		Certificates: []tls.Certificate{cert},
	})
	serveErr := make(chan error, 1)
	go func() {
		for {
			conn, err := tlsListener.Accept()
			if err != nil {
				serveErr <- nil
				return
			}
			go serveDoT(t, conn, name)
		}
	}()
	defer tlsListener.Close()
	host, portStr, err := net.SplitHostPort(inner.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	r, err := NewResolver(Server{Type: TypeTLS, Server: host, ServerPort: uint16(port), Insecure: true})
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	addrs, err := r.Resolve(context.Background(), name)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(addrs) != 2 || addrs[0].String() != "203.0.113.7" || addrs[1].String() != "2001:db8::7" {
		t.Fatalf("unexpected addresses: %v", addrs)
	}
}

// serveDoT answers framed DNS queries on one DoT connection.
func serveDoT(t *testing.T, conn net.Conn, name string) {
	t.Helper()
	defer conn.Close()
	for {
		var prefix [2]byte
		if _, err := ioReadFull(conn, prefix[:]); err != nil {
			return
		}
		length := int(binary.BigEndian.Uint16(prefix[:]))
		query := make([]byte, length)
		if _, err := ioReadFull(conn, query); err != nil {
			return
		}
		id := binary.BigEndian.Uint16(query[0:2])
		qtype := int(binary.BigEndian.Uint16(query[len(query)-4:]))
		var resp []byte
		if qtype == typeAAAA {
			resp = buildAAnswer(id, name, qtype, net.ParseIP("2001:db8::7"))
		} else {
			resp = buildAAnswer(id, name, qtype, net.ParseIP("203.0.113.7"))
		}
		frame := make([]byte, 2+len(resp))
		binary.BigEndian.PutUint16(frame[:2], uint16(len(resp)))
		copy(frame[2:], resp)
		if _, err := conn.Write(frame); err != nil {
			return
		}
	}
}

// testCert generates a throwaway TLS certificate for loopback tests.
func testCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}
}

// ioReadFull avoids importing io/ioutil-style helpers in tests.
func ioReadFull(r net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func TestParseResponseAcceptsCNAMEChain(t *testing.T) {
	const id = 42
	msg := []byte{}
	msg = binary.BigEndian.AppendUint16(msg, id)
	msg = binary.BigEndian.AppendUint16(msg, 0x8180)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	msg = binary.BigEndian.AppendUint16(msg, 2)
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = append(msg, encodeQuestionForTest("www.example.com", typeA)...)
	msg = binary.BigEndian.AppendUint16(msg, 0xc00c)
	msg = binary.BigEndian.AppendUint16(msg, typeCNAME)
	msg = binary.BigEndian.AppendUint16(msg, classIN)
	msg = binary.BigEndian.AppendUint32(msg, 60)
	alias := encodeNameForTest("cdn.example.net")
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(alias)))
	msg = append(msg, alias...)
	msg = append(msg, alias...)
	msg = binary.BigEndian.AppendUint16(msg, typeA)
	msg = binary.BigEndian.AppendUint16(msg, classIN)
	msg = binary.BigEndian.AppendUint32(msg, 60)
	msg = binary.BigEndian.AppendUint16(msg, 4)
	msg = append(msg, 192, 0, 2, 10)
	got, err := parseResponse(msg, id, "www.example.com", typeA)
	if err != nil || len(got) != 1 || got[0].String() != "192.0.2.10" {
		t.Fatalf("CNAME chain rejected: got=%v err=%v", got, err)
	}
}

func TestParseResponseAcceptsNODATAWithAuthorityAndOPT(t *testing.T) {
	const id = 43
	msg := buildNODATAAnswer(id, "example.com", typeAAAA)
	binary.BigEndian.PutUint16(msg[8:10], 1) // SOA authority
	binary.BigEndian.PutUint16(msg[10:12], 1)
	msg = append(msg, 0) // root owner
	msg = binary.BigEndian.AppendUint16(msg, 6)
	msg = binary.BigEndian.AppendUint16(msg, classIN)
	msg = binary.BigEndian.AppendUint32(msg, 60)
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = append(msg, 0) // OPT root owner
	msg = binary.BigEndian.AppendUint16(msg, 41)
	msg = binary.BigEndian.AppendUint16(msg, 4096)
	msg = binary.BigEndian.AppendUint32(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, 0)
	got, err := parseResponse(msg, id, "example.com", typeAAAA)
	if err != nil || len(got) != 0 {
		t.Fatalf("NODATA with SOA/OPT rejected: got=%v err=%v", got, err)
	}
}

func encodeNameForTest(name string) []byte {
	var out []byte
	for _, label := range strings.Split(name, ".") {
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0)
}

func TestParseResponseRejectsTruncatedAndMalformed(t *testing.T) {
	name := "mal.example.com"
	good := buildAAnswer(0x1234, name, typeA, net.ParseIP("192.0.2.55"))
	if _, err := parseResponse(good, 0x1234, name, typeA); err != nil {
		t.Fatalf("good answer must parse: %v", err)
	}
	if _, err := parseResponse(good[:hdrLen-1], 0x1234, name, typeA); err == nil {
		t.Fatalf("expected error for headerless message")
	}
	tc := buildAAnswer(0x1234, name, typeA, net.ParseIP("192.0.2.55"))
	binary.BigEndian.PutUint16(tc[2:4], uint16(0x8180|flagTC))
	if _, err := parseResponse(tc, 0x1234, name, typeA); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected truncated error, got %v", err)
	}
	// Wrong wire question type.
	wrong := buildAAnswer(0x1234, name, typeAAAA, net.ParseIP("2001:db8::9"))
	wrong[hdrLen-2] = byte(typeAAAA >> 8)
	binary.BigEndian.PutUint16(wrong[hdrLen-2:], typeAAAA)
	if _, err := parseResponse(wrong, 0x1234, name, typeA); err == nil {
		t.Fatalf("expected question type mismatch error")
	}
	// Trailing garbage.
	trailing := append(append([]byte{}, good...), 0x00)
	if _, err := parseResponse(trailing, 0x1234, name, typeA); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("expected trailing bytes error, got %v", err)
	}
}

func TestResolveWrongQuestionClass(t *testing.T) {
	name := "ch.example.com"
	s := newTestUDPServer(t, func(query []byte) []byte {
		id := binary.BigEndian.Uint16(query[0:2])
		resp := buildAAnswer(id, name, typeA, net.ParseIP("192.0.2.66"))
		// Locate the end of the question section: scan the encoded name.
		off := hdrLen
		for off < len(query) {
			l := int(query[off])
			if l == 0 {
				off++
				break
			}
			off += 1 + l
		}
		// off now points just past the question; class is the last 2 bytes.
		binary.BigEndian.PutUint16(resp[off-2:off], 3)
		// Answer RR: name ptr(2) + type(2) + class(2) + ttl(4) + rdlen(2).
		ansClassOff := off + 2 + 2
		binary.BigEndian.PutUint16(resp[ansClassOff:ansClassOff+2], 3)
		return resp
	})
	if _, err := resolveWith(t, s, name); err == nil {
		t.Fatalf("expected class mismatch error")
	}
}
