package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/downloader"
)

func TestAmneziaCPLoginUsesDownloadClient(t *testing.T) {
	var seen *http.Request
	h := NewAmneziaCPHandler(nil)
	h.downloadClientOverride = func(_ context.Context) (*downloader.Lease, *http.Client, downloader.RouteInfo, error) {
		client := &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				seen = req.Clone(req.Context())
				headers := http.Header{}
				headers.Add("Set-Cookie", "v_sid=session-123; Path=/; HttpOnly")
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     headers,
					Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
					Request:    req,
				}, nil
			}),
		}
		route := downloader.RouteInfo{Tag: "awg-a", Kind: "awg", Label: "AWG A"}
		return &downloader.Lease{Client: client, Route: route}, client, route, nil
	}

	r := httptest.NewRequest(http.MethodPost, "/api/amnezia-premium/login", strings.NewReader(`{"vpnKey":"vpn://premium","remember":true}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Login(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "session-123") {
		t.Fatalf("response does not contain sid: %s", w.Body.String())
	}
	if seen == nil {
		t.Fatal("request was not sent")
	}
	if seen.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", seen.Method)
	}
	if seen.URL.String() != amneziaCPOrigin+"/api/login" {
		t.Fatalf("url = %q", seen.URL.String())
	}
	bodyBytes, _ := io.ReadAll(seen.Body)
	if !strings.Contains(string(bodyBytes), `"vpnKey":"vpn://premium"`) {
		t.Fatalf("body = %s", string(bodyBytes))
	}
	if seen.Header.Get("Origin") != amneziaCPOrigin {
		t.Fatalf("Origin header = %q", seen.Header.Get("Origin"))
	}
}

func TestAmneziaCPLoginPreservesCPErrorStatus(t *testing.T) {
	h := NewAmneziaCPHandler(nil)
	h.downloadClientOverride = func(_ context.Context) (*downloader.Lease, *http.Client, downloader.RouteInfo, error) {
		client := &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusUnprocessableEntity,
					Status:     "422 Unprocessable Entity",
					Header:     http.Header{},
					Body:       io.NopCloser(strings.NewReader(`{"message":"bad key"}`)),
					Request:    req,
				}, nil
			}),
		}
		route := downloader.RouteInfo{Tag: "direct", Kind: "direct", Label: "Direct (WAN)"}
		return &downloader.Lease{Client: client, Route: route}, client, route, nil
	}

	r := httptest.NewRequest(http.MethodPost, "/api/amnezia-premium/login", strings.NewReader(`{"vpnKey":"vpn://bad"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Login(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "bad key") {
		t.Fatalf("response does not contain CP error: %s", w.Body.String())
	}
}

func TestAmneziaCPLoginRouteError(t *testing.T) {
	h := NewAmneziaCPHandler(nil)
	h.downloadClientOverride = func(_ context.Context) (*downloader.Lease, *http.Client, downloader.RouteInfo, error) {
		return nil, nil, downloader.RouteInfo{}, fmt.Errorf("route unavailable")
	}

	r := httptest.NewRequest(http.MethodPost, "/api/amnezia-premium/login", strings.NewReader(`{"vpnKey":"vpn://bad"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Login(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "AMNEZIA_CP_ROUTE_ERROR") {
		t.Fatalf("response does not contain route error code: %s", w.Body.String())
	}
}

func TestAmneziaCPDownloadClientDirectRouteGetsCPTransportSettings(t *testing.T) {
	h := NewAmneziaCPHandler(nil)
	h.SetDownloader(downloader.NewService(downloader.Deps{}))

	lease, client, route, err := h.downloadClient(context.Background())
	if err != nil {
		t.Fatalf("downloadClient returned error: %v", err)
	}
	if lease == nil {
		t.Fatal("lease is nil")
	}
	defer lease.Close()

	if route.Tag != "direct" || route.Kind != "direct" {
		t.Fatalf("route = %+v, want direct", route)
	}
	if client.Timeout != 45*time.Second {
		t.Fatalf("timeout = %s, want 45s", client.Timeout)
	}

	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if tr == nil {
		t.Fatal("transport is nil")
	}
	if !tr.DisableKeepAlives {
		t.Fatal("DisableKeepAlives = false, want true")
	}
	if tr.TLSHandshakeTimeout != 15*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %s, want 15s", tr.TLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != 25*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %s, want 25s", tr.ResponseHeaderTimeout)
	}
	if tr.ExpectContinueTimeout != time.Second {
		t.Fatalf("ExpectContinueTimeout = %s, want 1s", tr.ExpectContinueTimeout)
	}
	if tr.IdleConnTimeout != 45*time.Second {
		t.Fatalf("IdleConnTimeout = %s, want 45s", tr.IdleConnTimeout)
	}
	if tr.MaxIdleConnsPerHost != 8 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 8", tr.MaxIdleConnsPerHost)
	}
	if tr.Proxy == nil {
		t.Fatal("Proxy is nil, want ProxyFromEnvironment")
	}
}

func TestConfigureAmneziaCPTransportPreservesExistingProxy(t *testing.T) {
	wantProxyURL, err := url.Parse("http://127.0.0.1:8080")
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}
	tr := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return wantProxyURL, nil
		},
	}

	configureAmneziaCPTransport(tr)

	gotURL, err := tr.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}})
	if err != nil {
		t.Fatalf("proxy func returned error: %v", err)
	}
	if gotURL.String() != wantProxyURL.String() {
		t.Fatalf("proxy url = %s, want %s", gotURL, wantProxyURL)
	}
	if !tr.DisableKeepAlives {
		t.Fatal("DisableKeepAlives = false, want true")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
