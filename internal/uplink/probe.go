package uplink

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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

// ProbePathHTTPS checks one service through the supplied path. The caller
// supplies a strictly bound dialer or a dialer for a dedicated proxy. The
// check does not follow redirects or substitute another destination.
func ProbePathHTTPS(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), target string, expectedStatus int) error {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("probe requires an HTTPS URL without credentials or fragment")
	}
	if dial == nil || expectedStatus < 200 || expectedStatus >= 600 {
		return errors.New("probe requires a path dialer and expected HTTP status")
	}
	client := newProbeClient(dial, nil)
	defer client.CloseIdleConnections()
	err = probeStatus(ctx, client, u.String(), expectedStatus)
	if err != nil {
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			return requestErr.Err
		}
		return err
	}
	return nil
}

func newProbeClient(dial func(context.Context, string, string) (net.Conn, error), tlsConfig *tls.Config) *http.Client {
	transport := &http.Transport{
		DialContext: dial, DisableKeepAlives: true, TLSHandshakeTimeout: 3 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: 8192, TLSClientConfig: tlsConfig,
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func probeStatus(ctx context.Context, client *http.Client, target string, expectedStatus int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != expectedStatus {
		return fmt.Errorf("probe status %d, expected %d", resp.StatusCode, expectedStatus)
	}
	return nil
}

func probeHTTP(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), targets []string, tlsConfig *tls.Config) error {
	client := newProbeClient(dial, tlsConfig)
	defer client.CloseIdleConnections()
	var failures []error
	for _, target := range targets {
		// Give the second independent target a chance before the shared deadline.
		timeout := 2 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			timeout = time.Until(deadline) / 2
		}
		attempt, cancel := context.WithTimeout(ctx, timeout)
		err := probeStatus(attempt, client, target, http.StatusNoContent)
		cancel()
		if err == nil {
			return nil
		}
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
