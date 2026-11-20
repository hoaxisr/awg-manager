package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/config"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/frame"
)

func TestCaddyPublicParityAndCarriers(t *testing.T) {
	binary := os.Getenv("TPROXY_CADDY_BIN")
	if binary == "" {
		t.Skip("set TPROXY_CADDY_BIN to the pinned Caddy 2.11.4 binary")
	}
	version, err := exec.Command(binary, "version").Output()
	if err != nil || !strings.HasPrefix(string(version), "v2.11.4 ") {
		t.Fatalf("expected Caddy 2.11.4: %v", err)
	}
	public := httptest.NewServer(http.HandlerFunc(observingPublicHandler))
	defer public.Close()
	backend := startEchoBackend(t)
	application, _ := newConfiguredTestServer(t, backend, func(value *config.Config) {
		value.PublicDir = ""
		value.PublicUpstream = public.URL
		original := value.Profiles[0]
		value.Profiles = nil
		for i, mode := range []config.CarrierMode{config.CarrierHTTPS, config.CarrierHTTPSLanes, config.CarrierWebSocket, config.CarrierWebSocketLanes} {
			profile := original
			profile.Name = string(mode)
			profile.CarrierMode = mode
			profile.Capability[0] ^= byte(i + 1)
			value.Profiles = append(value.Profiles, profile)
		}
	})
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()
	control, relay, tlsConfig := caddyPair(t, binary, public.URL, hosted.URL)
	for _, http2 := range []bool{false, true} {
		name := "HTTP1"
		if http2 {
			name = "HTTP2"
		}
		t.Run(name, func(t *testing.T) {
			transport := &http.Transport{TLSClientConfig: tlsConfig.Clone(), ForceAttemptHTTP2: http2}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			probe := perform(t, client, request(t, "GET", relay+"/", nil, ""))
			_ = readResponse(t, probe)
			if (probe.ProtoMajor == 2) != http2 {
				t.Fatal("wrong negotiated HTTP version")
			}
			if probe.Header.Get("Via") != "" {
				t.Fatal("Caddy Via banner was not removed")
			}
			comparePublicRequests(t, control, relay, client, true)
		})
	}
	testPublicWebSockets(t, relay, &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig.Clone()}})
	testCaddyEdges(t, control, relay, tlsConfig)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig.Clone(), ForceAttemptHTTP2: true}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	for _, profile := range application.config.Profiles {
		mode := profile.CarrierMode
		t.Run(string(mode), func(t *testing.T) {
			capability := config.CapabilityString(profile.Capability)
			page := perform(t, client, request(t, "GET", relay+"/?bridge="+capability, nil, ""))
			body := string(readResponse(t, page))
			_, rest, found := strings.Cut(body, `bootstrap="`)
			if !found || len(rest) < 43 {
				t.Fatal("bridge page did not load through Caddy")
			}
			created := perform(t, client, apiRequest(t, "POST", relay+"/api/v1/session", rest[:43], frame.Encode(frame.Hello, 0, []byte{1})))
			_ = readResponse(t, created)
			token := created.Header.Get("X-Session-Token")
			if created.StatusCode != http.StatusOK || len(token) != 43 || created.Header.Get("X-Carrier-Mode") != string(mode) {
				t.Fatal("carrier creation failed through Caddy")
			}
			payload := append(frame.Encode(frame.Open, 17, nil), frame.Encode(frame.Data, 17, []byte("Caddy echo"))...)
			if mode == config.CarrierHTTPS || mode == config.CarrierHTTPSLanes {
				up := apiRequest(t, "POST", relay+"/api/v1/up", token, payload)
				up.Header.Set("X-Up-Seq", "1")
				if mode == config.CarrierHTTPSLanes {
					up.Header.Set("X-Lane-ID", "17")
				}
				response := perform(t, client, up)
				_ = readResponse(t, response)
				if response.StatusCode != http.StatusNoContent {
					t.Fatal("carrier uplink failed through Caddy")
				}
				cursor := "0"
				found := false
				for i := 0; i < 8 && !found; i++ {
					down := apiRequest(t, "POST", relay+"/api/v1/down", token, nil)
					down.Header.Set("X-Down-Cursor", cursor)
					if mode == config.CarrierHTTPSLanes {
						down.Header.Set("X-Lane-ID", "17")
					}
					response := perform(t, client, down)
					frames, err := frame.ParseAll(readResponse(t, response), frame.MaxPayload)
					if err != nil {
						t.Fatal(err)
					}
					cursor = response.Header.Get("X-Down-Cursor")
					for _, f := range frames {
						if f.Type == frame.Data && string(f.Payload) == "Caddy echo" {
							found = true
						}
					}
				}
				if !found {
					t.Fatal("carrier echo not received through Caddy")
				}
			} else {
				protocol := webSocketProtocolPrefix + token
				if mode == config.CarrierWebSocketLanes {
					protocol = webSocketLaneProtocolPrefix + token + ".17"
				}
				wsCtx, wsCancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer wsCancel()
				connection, _, err := websocket.Dial(wsCtx, "wss"+strings.TrimPrefix(relay, "https")+"/api/v1/ws", &websocket.DialOptions{
					HTTPClient:   &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig.Clone()}},
					Host:         testHost,
					Subprotocols: []string{protocol},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer connection.CloseNow()
				if err := connection.Write(wsCtx, websocket.MessageBinary, payload); err != nil {
					t.Fatal(err)
				}
				found := false
				for !found {
					_, body, err := connection.Read(wsCtx)
					if err != nil {
						t.Fatal(err)
					}
					frames, err := frame.ParseAll(body, frame.MaxPayload)
					if err != nil {
						t.Fatal(err)
					}
					for _, f := range frames {
						if f.Type == frame.Data && string(f.Payload) == "Caddy echo" {
							found = true
						}
					}
				}
			}
			response := perform(t, client, apiRequest(t, "DELETE", relay+"/api/v1/session", token, nil))
			_ = readResponse(t, response)
			if response.StatusCode != http.StatusNoContent {
				t.Fatal("carrier close failed through Caddy")
			}
		})
	}
	hosted.Close()
	var reference []byte
	for _, path := range []string{"/random", "/api/v1/session", "/api/v1/up", "/api/v1/down", "/api/v1/ws"} {
		response := perform(t, client, request(t, "GET", relay+path, nil, ""))
		body := readResponse(t, response)
		if response.StatusCode != http.StatusBadGateway || response.Header.Get("Content-Security-Policy") != "" || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("unexpected Caddy backend-down response")
		}
		if reference != nil && !bytes.Equal(reference, body) {
			t.Fatal("backend-down body differs on transport paths")
		}
		reference = body
	}
}

func testCaddyEdges(t *testing.T, control, relay string, tlsConfig *tls.Config) {
	t.Helper()
	transport := &http.Transport{TLSClientConfig: tlsConfig.Clone(), ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, encoding := range []string{"identity", "gzip", "zstd"} {
		for _, host := range []string{testHost, testHost + ":443", "wrong.example", "PROXY.EXAMPLE.COM", testHost + "."} {
			for _, malformedUpgrade := range []bool{false, true} {
				var bodies [2][]byte
				var responses [2]*http.Response
				for i, base := range []string{control, relay} {
					r := request(t, "GET", base+"/api/v1/ws", []byte(strings.Repeat("public content ", 100)), "")
					r.Host = host
					r.Header.Set("Accept-Encoding", encoding)
					if malformedUpgrade {
						r.Header.Set("Connection", "Upgrade")
						r.Header.Set("Upgrade", "websocket")
						r.Header.Set("Sec-WebSocket-Version", "0")
					}
					responses[i] = perform(t, client, r)
					bodies[i] = readResponse(t, responses[i])
					if responses[i].Header.Get("Content-Encoding") == "gzip" {
						reader, err := gzip.NewReader(bytes.NewReader(bodies[i]))
						if err != nil {
							t.Fatal(err)
						}
						bodies[i], err = io.ReadAll(reader)
						reader.Close()
						if err != nil {
							t.Fatal(err)
						}
					}
					responses[i].Header.Del("Date")
				}
				if (responses[0].Header.Get("Content-Encoding") != "zstd" && !bytes.Equal(bodies[0], bodies[1])) || responses[0].StatusCode != responses[1].StatusCode || !reflect.DeepEqual(responses[0].Header, responses[1].Header) {
					t.Fatalf("Caddy edge parity failed for encoding=%s host=%s upgrade=%t", encoding, host, malformedUpgrade)
				}
				if host == testHost && !malformedUpgrade && encoding != "identity" && responses[1].Header.Get("Content-Encoding") != encoding {
					t.Fatalf("Caddy did not exercise %s compression", encoding)
				}
			}
		}
	}
	var bodies [2][]byte
	for i, base := range []string{control, relay} {
		connection, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", strings.TrimPrefix(base, "https://"), tlsConfig.Clone())
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		_, err = fmt.Fprintf(connection, "GET /api/v1/up?bridge=wrong HTTP/1.0\r\nHost: %s\r\n\r\n", testHost)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: "GET"})
		if err != nil {
			t.Fatal(err)
		}
		bodies[i], err = io.ReadAll(response.Body)
		response.Body.Close()
		connection.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("HTTP/1.0 failed: %v", err)
		}
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatal("HTTP/1.0 request changed through relay")
	}
}

func caddyPair(t *testing.T, binary, public, relay string) (string, string, *tls.Config) {
	t.Helper()
	directory := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: testHost}, DNSNames: []string{testHost}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certPath, keyPath := filepath.Join(directory, "cert.pem"), filepath.Join(directory, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	adapt := exec.Command(binary, "adapt", "--config", "../../deploy/Caddyfile", "--adapter", "caddyfile")
	adapt.Env = append(os.Environ(), "TPROXY_HOSTNAME="+testHost, "ACME_EMAIL=operator@example.com")
	data, err := adapt.Output()
	if err != nil {
		t.Fatal(err)
	}
	var caddy map[string]any
	if err := json.Unmarshal(data, &caddy); err != nil {
		t.Fatal(err)
	}
	apps := caddy["apps"].(map[string]any)
	httpApp := apps["http"].(map[string]any)
	servers := httpApp["servers"].(map[string]any)
	template, err := json.Marshal(servers["srv0"])
	if err != nil {
		t.Fatal(err)
	}
	delete(servers, "srv0")
	addresses := []string{freeAddress(t), freeAddress(t)}
	for i, upstream := range []string{public, relay} {
		var server map[string]any
		data := strings.ReplaceAll(string(template), "127.0.0.1:8080", strings.TrimPrefix(upstream, "http://"))
		if err := json.Unmarshal([]byte(data), &server); err != nil {
			t.Fatal(err)
		}
		server["listen"] = []string{addresses[i]}
		server["automatic_https"] = map[string]any{"disable_certificates": true, "disable_redirects": true}
		server["tls_connection_policies"] = []any{map[string]any{}}
		servers[upstream] = server
	}
	apps["tls"] = map[string]any{"certificates": map[string]any{"load_files": []any{map[string]any{"certificate": certPath, "key": keyPath}}}}
	data, err = json.Marshal(caddy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "caddy.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(directory, "caddy.log"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "run", "--config", path)
	command.Env = append(os.Environ(), "XDG_DATA_HOME="+directory, "XDG_CONFIG_HOME="+directory)
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
		log.Close()
	})
	for _, address := range addresses {
		ready := false
		for i := 0; i < 100; i++ {
			connection, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
			if err == nil {
				connection.Close()
				ready = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !ready {
			data, _ := os.ReadFile(log.Name())
			t.Fatalf("Caddy did not start: %s", data)
		}
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return "https://" + addresses[0], "https://" + addresses[1], &tls.Config{RootCAs: roots, ServerName: testHost, MinVersion: tls.VersionTLS12}
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}
