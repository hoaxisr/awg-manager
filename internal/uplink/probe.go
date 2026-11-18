package uplink

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/proxy"
)

// ProbeSOCKS checks useful traffic through a tunnel's dedicated local inbound.
// DNS for the test hostname is delegated to SOCKS; there is no direct fallback.
// A fresh transport avoids reusing connections from the previous provider.
func ProbeSOCKS(ctx context.Context, port uint16, user, password string) error {
	return probeSOCKS(ctx, port, user, password, []string{"https://cp.cloudflare.com/generate_204", "https://www.gstatic.com/generate_204"}, nil)
}

func probeSOCKS(ctx context.Context, port uint16, user, password string, targets []string, tlsConfig *tls.Config) error {
	if port == 0 {
		return errors.New("no dedicated SOCKS inbound")
	}
	var auth *proxy.Auth
	if user != "" || password != "" {
		auth = &proxy.Auth{User: user, Password: password}
	}
	d, err := proxy.SOCKS5("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), auth, &net.Dialer{Timeout: time.Second * 3})
	if err != nil {
		return err
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return errors.New("SOCKS dialer has no cancellation support")
	}
	return probeHTTP(ctx, cd.DialContext, targets, tlsConfig)
}

// ProbeBinding verifies HTTPS payload through a kernel tunnel. Both DNS and
// payload sockets are bound to its device, with no system resolver fallback.
func ProbeBinding(ctx context.Context, binding Binding) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	return probeHTTP(ctx, binding.DialContext, []string{"https://cp.cloudflare.com/generate_204", "https://www.gstatic.com/generate_204"}, nil)
}

func probeHTTP(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), targets []string, tlsConfig *tls.Config) error {
	t := &http.Transport{DialContext: dial, DisableKeepAlives: true, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: 8192}
	t.TLSClientConfig = tlsConfig
	defer t.CloseIdleConnections()
	client := &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var failures []error
	for _, target := range targets {
		// Give the second independent target a chance before the shared deadline.
		timeout := 2 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			timeout = time.Until(deadline) / 2
		}
		attempt, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(attempt, http.MethodGet, target, nil)
		if err == nil {
			var resp *http.Response
			resp, err = client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode != http.StatusNoContent {
					err = fmt.Errorf("probe status %d", resp.StatusCode)
				}
			}
		}
		cancel()
		if err == nil {
			return nil
		}
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
