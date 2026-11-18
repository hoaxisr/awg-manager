//go:build linux

// Package testsupport provides isolated native transport fixtures.
package testsupport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TLSFixture(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated uplink test"}, DNSNames: []string{"cp.cloudflare.com", "www.gstatic.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	path := "/opt/etc/firc/test-ca.pem"
	if err = os.WriteFile(path, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", path)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile("/opt/etc/firc/test-key.pem", keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func VLESSFixture(t *testing.T, ip string, cert tls.Certificate) *atomic.Int64 {
	t.Helper()
	// A separate direct service must remain reachable during selective drops.
	witness, err := net.Listen("tcp", net.JoinHostPort(ip, "18080"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	t.Cleanup(func() { server.Close() })
	go server.Serve(witness)
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, "8443"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	count := new(atomic.Int64)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				header := make([]byte, 18)
				if _, err := io.ReadFull(conn, header); err != nil {
					return
				}
				uuid := []byte{0x8f, 0x7d, 0x3b, 0x1a, 0x2c, 0x4e, 0x4f, 0x60, 0x9a, 0x81, 0xb5, 0xd7, 0xe6, 0xc3, 0x01, 0x24}
				if header[0] != 0 || !bytes.Equal(header[1:17], uuid) {
					return
				}
				if _, err := io.CopyN(io.Discard, conn, int64(header[17])); err != nil {
					return
				}
				request := make([]byte, 4)
				if _, err := io.ReadFull(conn, request); err != nil {
					return
				}
				port := int(request[1])<<8 | int(request[2])
				if request[0] != 1 || (port != 443 && port != 80) {
					return
				}
				size := 0
				switch request[3] {
				case 1:
					size = 4
				case 3:
					size = 16
				case 2:
					var n [1]byte
					if _, err := io.ReadFull(conn, n[:]); err != nil {
						return
					}
					size = int(n[0])
				default:
					return
				}
				destination := make([]byte, size)
				if _, err := io.ReadFull(conn, destination); err != nil {
					return
				}
				if _, err := conn.Write([]byte{0, 0}); err != nil {
					return
				}
				// FIRC's internal node readiness probe uses plain HTTP/80. It
				// is accepted here but never counted as the controller's TLS proof.
				if port == 80 {
					req, err := http.ReadRequest(bufio.NewReader(conn))
					if err != nil {
						return
					}
					req.Body.Close()
					fmt.Fprint(conn, "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
					return
				}
				secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				req, err := http.ReadRequest(bufio.NewReader(secure))
				if err != nil {
					return
				}
				defer req.Body.Close()
				if (req.Host != "cp.cloudflare.com" && req.Host != "www.gstatic.com") || req.URL.Path != "/generate_204" {
					return
				}
				count.Add(1)
				if path := os.Getenv("FIRC_UPLINK_FIXTURE_LOG"); path != "" {
					f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
					if err != nil {
						return
					}
					fmt.Fprintln(f, "TLS probe")
					f.Close()
				}
				fmt.Fprint(secure, "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
			}()
		}
	}()
	return count
}

func ServeVLESS(t *testing.T) {
	if os.Getenv("FIRC_UPLINK_FIXTURE_IP") == "" {
		t.Skip("native fixture subprocess only")
	}
	cert, err := tls.LoadX509KeyPair("/opt/etc/firc/test-ca.pem", "/opt/etc/firc/test-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	VLESSFixture(t, os.Getenv("FIRC_UPLINK_FIXTURE_IP"), cert)
	if err := os.WriteFile(os.Getenv("FIRC_UPLINK_FIXTURE_LOG")+".ready", []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	select {}
}

func StartVLESS(t *testing.T, number int) func() int {
	return StartVLESSAddress(t, number, fmt.Sprintf("203.0.113.%d", number))
}
func StartVLESSAddress(t *testing.T, number int, ip string) func() int {
	t.Helper()
	path := fmt.Sprintf("/opt/isp%d.%s.requests", number, strings.ReplaceAll(ip, ":", "_"))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ip", "netns", "exec", fmt.Sprintf("isp%d", number), executable, "-test.run", "^TestNativeUplinkVLESSFixture$", "-test.timeout", "150s")
	cmd.Env = append(os.Environ(), "FIRC_UPLINK_FIXTURE_IP="+ip, "FIRC_UPLINK_FIXTURE_LOG="+path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err := os.Stat(path + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("VLESS fixture did not start", ip)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return func() int { b, _ := os.ReadFile(path); return strings.Count(string(b), "TLS probe\n") }
}

// AssertDirectWAN proves that the selected WAN carries ordinary HTTP even when
// the tunnel protocol is blocked. It never uses a proxy or another interface.
func AssertDirectWAN(t *testing.T, device string, number int) {
	t.Helper()
	binding, err := uplink.InspectBinding(device)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: binding.DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://203.0.113.%d:18080/generate_204", number), nil)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("ordinary HTTP failed on "+device, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal("unexpected direct HTTP status", response.StatusCode)
	}
}

// BlockVLESS drops only outer VLESS traffic in the current private namespace.
// Both address families are filtered; DNS and ordinary HTTP remain untouched.
func BlockVLESS(t *testing.T, device string) func() {
	return BlockTransport(t, device, "tcp", "8443")
}

// BlockTransport isolates a transport failure from WAN reachability. Filters
// affect only the given protocol/port in the private native fixture namespace.
func BlockTransport(t *testing.T, device, network, port string) func() {
	t.Helper()
	if network != "tcp" && network != "udp" {
		t.Fatal("unsupported selective-drop transport")
	}
	self, _ := os.Readlink("/proc/self/ns/net")
	host, _ := os.Readlink("/proc/1/ns/net")
	root := os.Getenv("FIRC_UPLINK_WORKSPACE_ROOT")
	if self == "" || self == host || root == "" {
		t.Fatal("selective drop requires the private native fixture")
	}
	run := func(args ...string) {
		t.Helper()
		tools := root + "/tmp/uplink-native-tools"
		command := append([]string{"--library-path", tools + "/lib:" + tools + "/usr/lib", tools + "/sbin/tc"}, args...)
		if output, err := exec.Command("/lib/ld-musl-x86_64.so.1", command...).CombinedOutput(); err != nil {
			t.Fatalf("tc %v: %v: %s", args, err, output)
		}
	}
	run("qdisc", "add", "dev", device, "clsact")
	removed := false
	cleanup := func() {
		if !removed {
			removed = true
			run("qdisc", "del", "dev", device, "clsact")
		}
	}
	t.Cleanup(cleanup)
	for _, protocol := range []string{"ip", "ipv6"} {
		run("filter", "add", "dev", device, "egress", "protocol", protocol, "flower", "ip_proto", network, "dst_port", port, "action", "drop")
	}
	return cleanup
}
