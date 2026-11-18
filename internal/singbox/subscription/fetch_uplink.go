package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func validateBoundFetchURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid subscription URL")
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("subscription URL points to an internal address")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
		return fmt.Errorf("subscription URL points to an internal address")
	}
	return nil
}

// FetchWithBinding intentionally skips ValidatePublicURL's system DNS lookup.
// Both redirects and actual dial addresses are guarded without leaving the WAN.
func FetchWithBinding(ctx context.Context, raw string, headers []Header, opts FetchOpts, binding uplink.Binding) ([]byte, string, error) {
	return fetchWithBinding(ctx, raw, headers, opts, binding, binding.DialContext)
}

func fetchWithBinding(ctx context.Context, raw string, headers []Header, opts FetchOpts, binding uplink.Binding, dial func(context.Context, string, string) (net.Conn, error)) ([]byte, string, error) {
	if err := binding.Validate(); err != nil {
		return nil, "", err
	}
	if err := validateBoundFetchURL(raw); err != nil {
		return nil, "", err
	}
	req := buildRequest(raw, headers, opts)
	transport := &http.Transport{DialContext: dial, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: req.Timeout, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > httpclient.MaxRedirectHops {
			return fmt.Errorf("too many subscription redirects")
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && r.URL.Scheme != "https" {
			return fmt.Errorf("subscription redirect downgrades HTTPS")
		}
		return validateBoundFetchURL(r.URL.String())
	}}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", err
	}
	httpReq.Header = req.Headers.Clone()
	httpReq.Header.Set("User-Agent", req.UserAgent)
	resp, err := client.Do(httpReq)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return nil, "", fmt.Errorf("bound subscription download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, req.MaxBodyBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("bound subscription body read failed")
	}
	if int64(len(body)) > req.MaxBodyBytes {
		return nil, "", fmt.Errorf("fetch: body exceeds limit")
	}
	return body, resp.Header.Get("Content-Type"), nil
}
