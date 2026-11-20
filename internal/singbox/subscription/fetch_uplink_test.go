package subscription

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestBoundFetchSkipsSystemDNSAndEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "unique-provider-feed.invalid" {
			t.Errorf("HTTP host changed: %s", r.Host)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("nodes"))
	}))
	defer server.Close()
	var addresses []string
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		addresses = append(addresses, address)
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	body, ct, err := fetchWithBinding(context.Background(), "http://unique-provider-feed.invalid/list", nil, FetchOpts{}, uplink.Binding{Device: "wan0", IPv4: "192.0.2.2", DNSServers: []string{"192.0.2.53"}}, dial)
	if err != nil || string(body) != "nodes" || !strings.HasPrefix(ct, "text/plain") {
		t.Fatal(string(body), ct, err)
	}
	if len(addresses) != 1 || addresses[0] != "unique-provider-feed.invalid:80" {
		t.Fatal("environment proxy used", addresses)
	}
}

func TestBoundFetchRejectsInternalRedirectAndOversizedBody(t *testing.T) {
	for _, mode := range []string{"redirect", "body"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, "http://127.0.0.1:79/rci", http.StatusFound)
					return
				}
				w.Write([]byte("too many bytes"))
			}))
			defer server.Close()
			calls := 0
			dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
				calls++
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			_, _, err := fetchWithBinding(context.Background(), "http://provider.invalid/list?token=private-token", nil, FetchOpts{MaxBodyBytes: 4}, uplink.Binding{Device: "wan0", IPv4: "192.0.2.2"}, dial)
			if err == nil || calls != 1 {
				t.Fatal("unsafe download accepted", err, calls)
			}
			if strings.Contains(err.Error(), "private-token") {
				t.Fatal("secret URL leaked in error")
			}
		})
	}
}

func TestBoundFetchRedactsTransportErrors(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("provider unreachable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := fetchWithBinding(ctx, "https://provider.invalid/private-subscription-token?auth=secret", nil, FetchOpts{}, uplink.Binding{Device: "wan0", IPv4: "192.0.2.2"}, dial)
	if err == nil || !strings.Contains(err.Error(), "provider unreachable") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private-subscription-token") {
		t.Fatal(err)
	}
}
