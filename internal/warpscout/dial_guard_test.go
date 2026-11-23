package warpscout

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type guardRoundTripper func(*http.Request) (*http.Response, error)

func (f guardRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type guardTestAddr string

func (a guardTestAddr) Network() string { return "tcp" }
func (a guardTestAddr) String() string  { return string(a) }

type guardTestConn struct {
	net.Conn
	remoteAddr net.Addr
	closed     bool
}

func (c *guardTestConn) RemoteAddr() net.Addr { return c.remoteAddr }
func (c *guardTestConn) Close() error         { c.closed = true; return nil }
func (c *guardTestConn) LocalAddr() net.Addr  { return guardTestAddr("127.0.0.1:12345") }
func (c *guardTestConn) Read([]byte) (int, error) {
	return 0, errors.New("eof")
}
func (c *guardTestConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *guardTestConn) SetDeadline(time.Time) error      { return nil }
func (c *guardTestConn) SetReadDeadline(time.Time) error  { return nil }
func (c *guardTestConn) SetWriteDeadline(time.Time) error { return nil }

func TestGuardedDialContext_BlocksLiteralAndResolvedDisallowedIPs(t *testing.T) {
	ctx := context.Background()

	disallowedLiterals := []string{
		"169.254.169.254:443",
		"[fe80::1]:443",
		"[fe80::1%eth0]:443",
		"[::ffff:169.254.169.254]:443",
		"0.0.0.0:443",
		"[::]:443",
		"224.0.0.1:443",
		"[ff02::1]:443",
	}
	for _, target := range disallowedLiterals {
		dialCalls := 0
		dial := NewGuardedDialContextWithLookup(func(context.Context, string, string) (net.Conn, error) {
			dialCalls++
			return &guardTestConn{remoteAddr: guardTestAddr(target)}, nil
		}, func(context.Context, string) ([]net.IPAddr, error) {
			t.Fatalf("literal %q should not invoke DNS lookup", target)
			return nil, nil
		})
		conn, err := dial(ctx, "tcp", target)
		if !errors.Is(err, ErrDisallowedAddress) || dialCalls != 0 || conn != nil {
			t.Fatalf("literal %q: got conn=%v err=%v dialCalls=%d, want ErrDisallowedAddress with 0 dials", target, conn, err, dialCalls)
		}
	}

	disallowedResolved := map[string][]net.IPAddr{
		"meta.test":      {{IP: net.ParseIP("169.254.169.254")}},
		"ll6.test":       {{IP: net.ParseIP("fe80::1"), Zone: "eth0"}},
		"unspec.test":    {{IP: net.ParseIP("0.0.0.0")}},
		"multicast.test": {{IP: net.ParseIP("224.0.0.1")}},
		"mixed.test":     {{IP: net.ParseIP("1.2.3.4")}, {IP: net.ParseIP("169.254.169.254")}},
	}
	for host, addrs := range disallowedResolved {
		dialCalls := 0
		dial := NewGuardedDialContextWithLookup(func(context.Context, string, string) (net.Conn, error) {
			dialCalls++
			return &guardTestConn{remoteAddr: guardTestAddr("1.2.3.4:443")}, nil
		}, func(_ context.Context, gotHost string) ([]net.IPAddr, error) {
			if gotHost != host {
				t.Fatalf("unexpected lookup host %q, want %q", gotHost, host)
			}
			return addrs, nil
		})
		conn, err := dial(ctx, "tcp", net.JoinHostPort(host, "443"))
		if !errors.Is(err, ErrDisallowedAddress) || dialCalls != 0 || conn != nil {
			t.Fatalf("resolved %q: got conn=%v err=%v dialCalls=%d, want ErrDisallowedAddress with 0 dials", host, conn, err, dialCalls)
		}
	}
}

func TestGuardedDialContext_PinsResolvedIPv4FirstAndVerifiesRemoteAddr(t *testing.T) {
	ctx := context.Background()

	var dialedAddrs []string
	var dialedNets []string
	dial := NewGuardedDialContextWithLookup(func(_ context.Context, network, addr string) (net.Conn, error) {
		dialedNets = append(dialedNets, network)
		dialedAddrs = append(dialedAddrs, addr)
		if addr == "127.0.0.1:443" {
			return nil, errors.New("v4 unreachable")
		}
		return &guardTestConn{remoteAddr: guardTestAddr(addr)}, nil
	}, func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "dual.example.test" {
			t.Fatalf("unexpected host %q", host)
		}
		return []net.IPAddr{
			{IP: net.ParseIP("2606:4700::1")},
			{IP: net.ParseIP("127.0.0.1")},
		}, nil
	})

	conn, err := dial(ctx, "tcp", "dual.example.test:443")
	if err != nil {
		t.Fatalf("expected IPv6 fallback dial to succeed, got %v", err)
	}
	_ = conn.Close()
	if len(dialedAddrs) != 2 || dialedAddrs[0] != "127.0.0.1:443" || dialedNets[0] != "tcp4" || dialedAddrs[1] != "[2606:4700::1]:443" || dialedNets[1] != "tcp6" {
		t.Fatalf("unexpected dial sequence: nets=%v addrs=%v", dialedNets, dialedAddrs)
	}

	// Verify RemoteAddr post-dial check closes conn if peer resolves to disallowed address.
	badConn := &guardTestConn{remoteAddr: guardTestAddr("169.254.169.254:443")}
	badRemoteDial := NewGuardedDialContextWithLookup(func(context.Context, string, string) (net.Conn, error) {
		return badConn, nil
	}, func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	})
	conn, err = badRemoteDial(ctx, "tcp", "good-dns.test:443")
	if !errors.Is(err, ErrDisallowedAddress) || !badConn.closed || conn != nil {
		t.Fatalf("expected RemoteAddr guard to close conn and return ErrDisallowedAddress, conn=%v closed=%v err=%v", conn, badConn.closed, err)
	}
}

func TestRegisterWithOptions_DisallowedResolvedRelaySkipsFallbacksAndDefaultSinkholeFallsBack(t *testing.T) {
	ctx := context.Background()
	st, err := NewStore(t.TempDir(), plainCipher{})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Custom RelayURL failing with ErrDisallowedAddress -> aborts immediately with ErrInvalidInput without fallbacks.
	bypassCalled := false
	fbCalled := false
	sRelay := New(Deps{
		Store: st,
		HTTP: func(string) (*http.Client, error) {
			return &http.Client{Transport: failRoundTripper{err: fmt.Errorf("%w: evil.test -> 169.254.169.254", ErrDisallowedAddress)}}, nil
		},
		HTTPDirectBypass: func(string) (*http.Client, error) {
			bypassCalled = true
			return nil, errors.New("should not run")
		},
		HTTPFallbacks: func(context.Context) []FallbackClient {
			fbCalled = true
			return nil
		},
	})
	t.Cleanup(sRelay.Stop)

	_, regErr := sRelay.RegisterWithOptions(ctx, RegisterOptions{RelayURL: "https://evil.test"})
	if !errors.Is(regErr, ErrInvalidInput) || !errors.Is(regErr, ErrDisallowedAddress) {
		t.Fatalf("expected ErrInvalidInput + ErrDisallowedAddress, got %v", regErr)
	}
	if bypassCalled || fbCalled {
		t.Fatalf("custom disallowed relay must not invoke bypass=%v or fallbacks=%v", bypassCalled, fbCalled)
	}

	// 2. Default Cloudflare API hitting ISP DNS sinkhole (ErrDisallowedAddress) falls back cleanly.
	fbUsed := false
	sDefault := New(Deps{
		Store: st,
		HTTP: func(string) (*http.Client, error) {
			return &http.Client{Transport: failRoundTripper{err: fmt.Errorf("%w: api.devices.cloudflare.com -> 0.0.0.0", ErrDisallowedAddress)}}, nil
		},
		HTTPFallbacks: func(context.Context) []FallbackClient {
			return []FallbackClient{{
				Client: &http.Client{Transport: guardRoundTripper(func(r *http.Request) (*http.Response, error) {
					fbUsed = true
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(rec, `{"id":"fb-id","token":"fb-tok","warp_enabled":true,"account":{"account_type":"free"},"config":{"client_id":"AQID","peers":[{"public_key":%q}],"interface":{"addresses":{"v4":"172.16.0.4"}}}}`, WarpPeerPublicKey)
					return rec.Result(), nil
				})},
			}}
		},
	})
	t.Cleanup(sDefault.Stop)

	acct, defErr := sDefault.RegisterWithOptions(ctx, RegisterOptions{})
	if defErr != nil || !fbUsed || acct == nil || acct.ID != "fb-id" {
		t.Fatalf("default sinkhole should fall back cleanly: err=%v fbUsed=%v acct=%+v", defErr, fbUsed, acct)
	}

	// 3. SanitizeProxyError keeps both ErrInvalidInput and ErrDisallowedAddress sentinels while masking credentials.
	rawErr := fmt.Errorf("%w: %w: connect socks5://alice:topsecret@evil.test:1080", ErrInvalidInput, ErrDisallowedAddress)
	sanitized := SanitizeProxyError(rawErr, "socks5://alice:topsecret@evil.test:1080")
	if strings.Contains(sanitized.Error(), "topsecret") {
		t.Fatalf("password leaked in sanitized error: %v", sanitized)
	}
	if !errors.Is(sanitized, ErrInvalidInput) || !errors.Is(sanitized, ErrDisallowedAddress) {
		t.Fatalf("sentinels lost after sanitization: %v", sanitized)
	}
}
