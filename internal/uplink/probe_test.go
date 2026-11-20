package uplink

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The SOCKS server forwards only to a local fixture. A successful request to
// example.com therefore proves the prober used SOCKS and delegated hostname
// resolution, without making an external request.
func localSOCKS(t *testing.T, backend string) (uint16, <-chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	hosts := make(chan string, 10)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				var hello [2]byte
				if _, err := io.ReadFull(c, hello[:]); err != nil {
					return
				}
				methods := make([]byte, int(hello[1]))
				if _, err := io.ReadFull(c, methods); err != nil {
					return
				}
				if _, err := c.Write([]byte{5, 0}); err != nil {
					return
				}
				var req [4]byte
				if _, err := io.ReadFull(c, req[:]); err != nil {
					return
				}
				var size int
				switch req[3] {
				case 1:
					size = 4
				case 4:
					size = 16
				case 3:
					var n [1]byte
					if _, err := io.ReadFull(c, n[:]); err != nil {
						return
					}
					size = int(n[0])
				default:
					return
				}
				address := make([]byte, size)
				if _, err := io.ReadFull(c, address); err != nil {
					return
				}
				var port [2]byte
				if _, err := io.ReadFull(c, port[:]); err != nil {
					return
				}
				hosts <- string(address)
				if backend == "" {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				up, err := net.Dial("tcp", backend)
				if err != nil {
					return
				}
				defer up.Close()
				if _, err := c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				go func() { io.Copy(up, c); up.Close() }()
				io.Copy(c, up)
			}()
		}
	}()
	return uint16(l.Addr().(*net.TCPAddr).Port), hosts
}

func TestProbeTraversesSOCKSAndRejectsRedirect(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/first" {
			w.Header().Set("Location", "https://example.com/login")
			w.WriteHeader(302)
			return
		}
		w.WriteHeader(204)
	}))
	defer s.Close()
	port, hosts := localSOCKS(t, s.Listener.Addr().String())
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := probeSOCKS(ctx, port, "", "", []string{"https://example.com/first", "https://example.com/second"}, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("redirect followed or second target not attempted", calls.Load())
	}
	for range 2 {
		select {
		case host := <-hosts:
			if host != "example.com" {
				t.Fatal(host)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestSOCKSFailureCannotFallBackDirect(t *testing.T) {
	port, hosts := localSOCKS(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := probeSOCKS(ctx, port, "", "", []string{"https://never-resolve.invalid/first", "https://another.invalid/second"}, nil); err == nil {
		t.Fatal("failed SOCKS path reported healthy")
	}
	for range 2 {
		select {
		case <-hosts:
		case <-ctx.Done():
			t.Fatal("hostname was not sent to SOCKS")
		}
	}
}

func TestProbeHonorsCancellation(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, e := l.Accept()
		if e == nil {
			defer c.Close()
			var b [8]byte
			io.ReadFull(c, b[:])
		}
	}()
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], uint16(l.Addr().(*net.TCPAddr).Port))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ProbeSOCKS(ctx, binary.BigEndian.Uint16(b[:]), "", ""); err == nil {
		t.Fatal("cancelled probe succeeded")
	}
}
