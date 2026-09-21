package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/downloader"
)

// Страж хопов (F413) обязан доехать от dnsroute до загрузчика через адаптер:
// поле, потерянное при копировании запроса, молча открыло бы редирект на
// внутренний адрес.
func TestDNSRouteDownloaderAdapter_ForwardsRedirectGuard(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("example.com\n"))
	}))
	defer final.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirect.Close()

	a := &dnsRouteDownloaderAdapter{svc: downloader.NewService(downloader.Deps{})}
	_, _, err := a.ReadAll(context.Background(), dnsroute.SubscriptionDownloadRequest{
		URL:           redirect.URL,
		MaxBodyBytes:  128,
		AllowedStatus: []int{http.StatusOK},
		RedirectGuard: func(string) error { return errors.New("страж хопа отклонил") },
	})
	if err == nil || !strings.Contains(err.Error(), "страж хопа отклонил") {
		t.Fatalf("страж хопов не доехал до загрузчика: %v", err)
	}
}
