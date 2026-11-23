package warpscout

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type writeRecorderConn struct {
	net.Conn
	mu     sync.Mutex
	writes [][]byte
}

func (c *writeRecorderConn) Write(p []byte) (int, error) {
	cp := append([]byte(nil), p...)
	c.mu.Lock()
	c.writes = append(c.writes, cp)
	c.mu.Unlock()
	return len(p), nil
}

func (c *writeRecorderConn) snapshot() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.writes))
	copy(out, c.writes)
	return out
}

type spyTapListener struct {
	net.Listener
	mu    sync.Mutex
	bytes []byte
}

func (l *spyTapListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &spyTapConn{Conn: conn, owner: l}, nil
}

type spyTapConn struct {
	net.Conn
	owner *spyTapListener
	mu    sync.Mutex
	reads int
}

func (c *spyTapConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.mu.Lock()
		c.reads++
		take := c.reads <= 4
		c.mu.Unlock()
		if take {
			c.owner.mu.Lock()
			if len(c.owner.bytes) < 4096 {
				c.owner.bytes = append(c.owner.bytes, p[:n]...)
			}
			c.owner.mu.Unlock()
		}
	}
	return n, err
}

func TestSplitHelloConn_SplitsSNIAcrossTwoTLSRecords(t *testing.T) {
	rec := &writeRecorderConn{}
	sni := "api.devices.cloudflare.com"
	helloPayload := append([]byte{0x01, 0x00, 0x00, 0x20}, bytes.Repeat([]byte{'x'}, 16)...)
	helloPayload = append(helloPayload, []byte(sni)...)
	helloPayload = append(helloPayload, bytes.Repeat([]byte{'y'}, 20)...)

	recPkt := make([]byte, 5+len(helloPayload))
	recPkt[0] = 0x16
	recPkt[1] = 0x03
	recPkt[2] = 0x01
	binary.BigEndian.PutUint16(recPkt[3:5], uint16(len(helloPayload)))
	copy(recPkt[5:], helloPayload)

	sc := &splitHelloConn{Conn: rec, serverName: sni, pause: 0}
	n, err := sc.Write(recPkt)
	if err != nil || n != len(recPkt) {
		t.Fatalf("Write: n=%d want=%d err=%v", n, len(recPkt), err)
	}

	writes := rec.snapshot()
	if len(writes) != 2 {
		t.Fatalf("ожидалось 2 записи TLS-фреймов, получили %d", len(writes))
	}
	for i, w := range writes {
		if len(w) <= 5 || w[0] != 0x16 {
			t.Fatalf("запись %d не является TLS handshake-фреймом: len=%d", i, len(w))
		}
		if gotLen := int(binary.BigEndian.Uint16(w[3:5])); gotLen != len(w)-5 {
			t.Fatalf("запись %d: неверная длина TLS-фрейма header=%d actual=%d", i, gotLen, len(w)-5)
		}
	}
	p1 := string(writes[0][5:])
	p2 := string(writes[1][5:])
	if strings.Contains(p1, sni) || strings.Contains(p2, sni) {
		t.Fatalf("SNI %q не был разрезан между двумя записями", sni)
	}
	if !strings.HasSuffix(p1, sni[:len(sni)/2]) || !strings.HasPrefix(p2, sni[len(sni)/2:]) {
		t.Fatalf("ожидался разрез точно посередине SNI %q: tail=%q head=%q", sni, p1[max(0, len(p1)-16):], p2[:min(len(p2), 16)])
	}
}

func TestSplitTLSDialer_MixedCaseHostSplitsSNI(t *testing.T) {
	rec := &writeRecorderConn{}
	dial := NewSplitTLSDialer(func(context.Context, string, string) (net.Conn, error) {
		return rec, nil
	})
	conn, err := dial(context.Background(), "tcp", "Relay.Example.COM:443")
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}

	sni := "relay.example.com"
	helloPayload := append([]byte{0x01, 0x00, 0x00, 0x20}, bytes.Repeat([]byte{'x'}, 16)...)
	helloPayload = append(helloPayload, []byte(sni)...)
	helloPayload = append(helloPayload, bytes.Repeat([]byte{'y'}, 20)...)

	recPkt := make([]byte, 5+len(helloPayload))
	recPkt[0] = 0x16
	recPkt[1] = 0x03
	recPkt[2] = 0x01
	binary.BigEndian.PutUint16(recPkt[3:5], uint16(len(helloPayload)))
	copy(recPkt[5:], helloPayload)

	if _, err := conn.Write(recPkt); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	writes := rec.snapshot()
	if len(writes) != 2 {
		t.Fatalf("expected 2 TLS records, got %d", len(writes))
	}
	p1 := string(writes[0][5:])
	p2 := string(writes[1][5:])
	if strings.Contains(p1, sni) || strings.Contains(p2, sni) {
		t.Fatalf("expected mixed-case host to split lowercased SNI %q across records", sni)
	}
	if !strings.HasSuffix(p1, sni[:len(sni)/2]) || !strings.HasPrefix(p2, sni[len(sni)/2:]) {
		t.Fatalf("expected SNI cut at midpoint, tail=%q head=%q", p1[max(0, len(p1)-12):], p2[:min(len(p2), 12)])
	}
}

func TestNewSplitTLSDialer_CompletesRealTLSHandshake(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sniHost := "api.devices.cloudflare.com"
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: sniHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{sniHost},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	pool := x509.NewCertPool()
	parsedCert, _ := x509.ParseCertificate(der)
	pool.AddCert(parsedCert)

	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tapLn := &spyTapListener{Listener: rawLn}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Listener = tapLn
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	srvAddr := srv.Listener.Addr().String()
	baseDial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srvAddr)
	}
	tr := &http.Transport{
		DialContext:     NewSplitTLSDialer(baseDial),
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Get("https://" + net.JoinHostPort(sniHost, "443") + "/reg")
	if err != nil {
		t.Fatalf("HTTPS GET через SplitTLSDialer: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("ответ %d %q", resp.StatusCode, string(body))
	}

	tapLn.mu.Lock()
	raw := append([]byte(nil), tapLn.bytes...)
	tapLn.mu.Unlock()

	if len(raw) < 12 || raw[0] != 0x16 {
		t.Fatalf("некорректный старт ClientHello: len=%d", len(raw))
	}
	l1 := int(binary.BigEndian.Uint16(raw[3:5]))
	off2 := 5 + l1
	if len(raw) <= off2+5 || raw[off2] != 0x16 {
		t.Fatalf("второй TLS-фрейм ClientHello не найден: len=%d off2=%d", len(raw), off2)
	}
	l2 := int(binary.BigEndian.Uint16(raw[off2+3 : off2+5]))
	rec1Body := string(raw[5:off2])
	rec2Body := string(raw[off2+5 : off2+5+l2])
	if strings.Contains(rec1Body, sniHost) || strings.Contains(rec2Body, sniHost) {
		t.Fatalf("SNI %q должен быть разрезан между двумя TLS-фреймами", sniHost)
	}
	if !strings.Contains(rec1Body+rec2Body, sniHost) {
		t.Fatalf("склейка двух TLS-фреймов не содержит SNI %q", sniHost)
	}
}
