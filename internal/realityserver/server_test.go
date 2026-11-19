package realityserver

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
	utls "github.com/refraction-networking/utls"
	"github.com/xtls/reality"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/net/http2"
)

const (
	testUUID = "b831381d-6324-4d53-ad4f-8cda48b30811"
	testSNI  = "dest.test"
)

// startFakeDest runs a TLS 1.3 server that plays the camouflage site.
func startFakeDest(t *testing.T) (addr string, certDER []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: testSNI},
		DNSNames:     []string{testSNI},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2", "http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// Close shortly after the handshake so REALITY's post-handshake
				// record-length detection (ReadAll with a 5s deadline) finishes fast.
				_ = c.(*tls.Conn).Handshake()
				_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
				_, _ = io.Copy(io.Discard, c)
			}(c)
		}
	}()
	return ln.Addr().String(), der
}

func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

// waitRecordDetection blocks until REALITY finished probing dest's
// post-handshake records (otherwise the server handshake sleeps in 5s steps).
func waitRecordDetection(t *testing.T, dest string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		done := true
		for _, alpn := range []string{"0", "1", "2"} {
			v, ok := reality.GlobalPostHandshakeRecordsLens.Load(dest + " " + testSNI + " " + alpn)
			if !ok {
				done = false
				break
			}
			if _, pending := v.(bool); pending {
				done = false
				break
			}
		}
		if done {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("record length detection did not finish")
}

// realityDial performs a REALITY client handshake the same way Xray-core does.
func realityDial(t *testing.T, addr, sni, publicKey, shortID string) (*utls.UConn, bool) {
	t.Helper()
	raw, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	uConn := utls.UClient(raw, &utls.Config{
		ServerName:             sni,
		InsecureSkipVerify:     true,
		SessionTicketsDisabled: true,
		NextProtos:             []string{"h2", "http/1.1"},
	}, utls.HelloChrome_Auto)
	if err := uConn.BuildHandshakeState(); err != nil {
		t.Fatal(err)
	}
	hello := uConn.HandshakeState.Hello
	hello.SessionId = make([]byte, 32)
	copy(hello.Raw[39:], hello.SessionId)
	hello.SessionId[0], hello.SessionId[1], hello.SessionId[2] = 26, 3, 27
	binary.BigEndian.PutUint32(hello.SessionId[4:], uint32(time.Now().Unix()))
	sid, _ := hex.DecodeString(shortID)
	copy(hello.SessionId[8:], sid)

	pubRaw, err := DecodeKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ecdh.X25519().NewPublicKey(pubRaw)
	if err != nil {
		t.Fatal(err)
	}
	ecdhe := uConn.HandshakeState.State13.KeyShareKeys.Ecdhe
	if ecdhe == nil {
		ecdhe = uConn.HandshakeState.State13.KeyShareKeys.MlkemEcdhe
	}
	authKey, err := ecdhe.ECDH(pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hkdf.New(sha256.New, authKey, hello.Random[:20], []byte("REALITY")).Read(authKey); err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(authKey)
	aead, _ := cipher.NewGCM(block)
	aead.Seal(hello.SessionId[:0], hello.Random[20:], hello.SessionId[:16], hello.Raw)
	copy(hello.Raw[39:], hello.SessionId)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := uConn.HandshakeContext(ctx); err != nil {
		t.Fatalf("reality handshake: %v", err)
	}
	certs := uConn.ConnectionState().PeerCertificates
	verified := false
	if len(certs) > 0 {
		if edPub, ok := certs[0].PublicKey.(ed25519.PublicKey); ok {
			h := hmac.New(sha512.New, authKey)
			h.Write(edPub)
			verified = bytes.Equal(h.Sum(nil), certs[0].Signature)
		}
	}
	return uConn, verified
}

func vlessHeader(t *testing.T, target string) []byte {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portStr)
	u, err := xhttpserver.ParseUUID(testUUID)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	b.WriteByte(0)
	b.Write(u[:])
	b.WriteByte(0)                                       // addons
	b.WriteByte(VLESSCmdTCP)                             // command
	_ = binary.Write(&b, binary.BigEndian, uint16(port)) // port
	b.WriteByte(VLESSAddrIPv4)
	b.Write(net.ParseIP(host).To4())
	return b.Bytes()
}

func newTestConfig(t *testing.T, dest, transport string) (Config, string, string) {
	t.Helper()
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := GenerateShortID()
	return Config{
		ListenAddr:  "127.0.0.1:0",
		Transport:   transport,
		Dest:        dest,
		ServerNames: []string{testSNI},
		PrivateKey:  priv,
		ShortIDs:    []string{sid},
		Backend: xhttpserver.Config{
			Clients: []string{testUUID},
			Egress:  xhttpserver.EgressConfig{Mode: "direct", AllowPrivateDestinations: true},
		},
	}, pub, sid
}

func TestKeys(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	derived, err := PublicKeyFromPrivate(priv)
	if err != nil || derived != pub {
		t.Fatalf("derived=%q pub=%q err=%v", derived, pub, err)
	}
	if _, err := PublicKeyFromPrivate("nope"); err == nil {
		t.Fatal("expected error for invalid key")
	}
	if _, err := ParseShortID("abc"); err == nil {
		t.Fatal("odd-length short id must fail")
	}
	if _, err := ParseShortID("0123456789abcdef00"); err == nil {
		t.Fatal("too long short id must fail")
	}
}

func TestNormalize(t *testing.T) {
	cfg, _, _ := newTestConfig(t, "ya.ru", "")
	cfg.ListenAddr = ":2053"
	cfg.ServerNames = nil
	n, err := cfg.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if n.ListenAddr != "0.0.0.0:2053" || n.Dest != "ya.ru:443" || n.Transport != TransportTCP || n.ServerNames[0] != "ya.ru" {
		t.Fatalf("unexpected normalize result: %+v", n)
	}
	cfg.Transport = "xhttp"
	n, err = cfg.Normalize()
	if err != nil || n.XHTTPPath != DefaultXHTTPPath {
		t.Fatalf("xhttp path default: %q err=%v", n.XHTTPPath, err)
	}
	cfg.Transport = "grpc"
	if _, err := cfg.Normalize(); err == nil {
		t.Fatal("unsupported transport must fail")
	}
}

func TestRealityTCPEndToEnd(t *testing.T) {
	dest, _ := startFakeDest(t)
	echo := startEcho(t)
	cfg, pub, sid := newTestConfig(t, dest, TransportTCP)

	srv := New()
	if err := srv.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	waitRecordDetection(t, dest)

	conn, verified := realityDial(t, srv.Addr(), testSNI, pub, sid)
	defer conn.Close()
	if !verified {
		t.Fatal("server certificate was not REALITY-signed (client would fall into spider mode)")
	}
	payload := []byte("hello through reality")
	if _, err := conn.Write(append(vlessHeader(t, echo), payload...)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp := make([]byte, 2+len(payload))
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if resp[0] != 0 || resp[1] != 0 || !bytes.Equal(resp[2:], payload) {
		t.Fatalf("unexpected response %q", resp)
	}
	if st := srv.Status(); !st.Running || st.ActiveConns < 1 {
		t.Fatalf("status: %+v", st)
	}
}

func TestRealityUnauthenticatedFallsBackToDest(t *testing.T) {
	dest, destCert := startFakeDest(t)
	cfg, _, _ := newTestConfig(t, dest, TransportTCP)
	srv := New()
	if err := srv.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	// A regular TLS client (prober/browser) must see the genuine dest certificate.
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", srv.Addr(), &tls.Config{
		ServerName:         testSNI,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("fallback handshake: %v", err)
	}
	defer c.Close()
	certs := c.ConnectionState().PeerCertificates
	if len(certs) == 0 || !bytes.Equal(certs[0].Raw, destCert) {
		t.Fatal("prober did not receive the dest certificate")
	}
}

func TestRealityWrongShortIDRejected(t *testing.T) {
	dest, destCert := startFakeDest(t)
	cfg, pub, _ := newTestConfig(t, dest, TransportTCP)
	srv := New()
	if err := srv.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	conn, verified := realityDial(t, srv.Addr(), testSNI, pub, "deadbeefdeadbeef")
	defer conn.Close()
	if verified {
		t.Fatal("wrong short id must not authenticate")
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 || !bytes.Equal(certs[0].Raw, destCert) {
		t.Fatal("unauthenticated client should be proxied to dest")
	}
}

func TestRealityApplyHotUpdateAndRevoke(t *testing.T) {
	dest, _ := startFakeDest(t)
	cfg, _, _ := newTestConfig(t, dest, TransportTCP)
	srv := New()
	if err := srv.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	addr := srv.Addr()

	// Changing only clients must not rebind.
	cfg.Backend.Clients = []string{testUUID, "11111111-2222-3333-4444-555555555555"}
	if err := srv.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	if srv.Addr() != addr {
		t.Fatal("client-only change must keep the listener")
	}
	// No clients -> refuse.
	cfg.Backend.Clients = nil
	if err := srv.Apply(cfg); err == nil {
		t.Fatal("expected error without clients")
	}
	srv.Stop()
	if srv.IsRunning() {
		t.Fatal("server should be stopped")
	}
}

func TestRealityXHTTPStreamOne(t *testing.T) {
	dest, _ := startFakeDest(t)
	echo := startEcho(t)
	cfg, pub, sid := newTestConfig(t, dest, TransportXHTTP)
	cfg.XHTTPPath = "/rx-test"
	srv := New()
	if err := srv.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	waitRecordDetection(t, dest)

	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			c, verified := realityDial(t, srv.Addr(), testSNI, pub, sid)
			if !verified {
				return nil, fmt.Errorf("not verified")
			}
			return c, nil
		},
	}
	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodPost, "https://"+testSNI+"/rx-test/", pr)
	go func() {
		_, _ = pw.Write(append(vlessHeader(t, echo), []byte("xhttp-ping")...))
	}()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("xhttp request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	buf := make([]byte, 2+len("xhttp-ping"))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[2:]) != "xhttp-ping" {
		t.Fatalf("unexpected echo %q", buf)
	}
	_ = pw.Close()
}

func TestIsPortFree(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if IsPortFree(port) {
		t.Fatal("bound port reported as free")
	}
}
