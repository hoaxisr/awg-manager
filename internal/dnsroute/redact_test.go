package dnsroute

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingDownloader struct{ err error }

func (d failingDownloader) ReadAll(context.Context, SubscriptionDownloadRequest) ([]byte, SubscriptionDownloadMeta, error) {
	return nil, SubscriptionDownloadMeta{}, d.err
}

// TestRefreshSubscriptions_KeepsTheTokenOut — загрузчик net/http цитирует
// адрес целиком, в том числе адрес хопа редиректа. Этот текст ложился в
// lastError (его показывают REST и MCP get_dns_route, хотя само поле url
// там уже очищено от query) и строкой url=… в журнал.
func TestRefreshSubscriptions_KeepsTheTokenOut(t *testing.T) {
	// A public IP literal: the early guard resolves a name, and a test must
	// not depend on DNS.
	const listURL = "https://1.1.1.1/l/Tok3nAbc?key=K3y"
	svc := newTestService(t)
	svc.SetDownloader(failingDownloader{err: errors.New(`Get "https://cdn.example.net/r/Hop5ecret": dial tcp: i/o timeout`)})

	data := svc.store.GetCached()
	data.Lists = append(data.Lists, DomainList{ID: "l1", Name: "geo", Subscriptions: []Subscription{{URL: listURL}}})
	if err := svc.store.Save(data); err != nil {
		t.Fatal(err)
	}
	_ = svc.refreshSubscriptions(context.Background(), "l1")

	got := svc.store.GetCached().Lists[0].Subscriptions[0]
	if got.LastError == "" {
		t.Fatal("a failed fetch must still be recorded")
	}
	if !strings.Contains(got.LastError, "cdn.example.net") {
		t.Fatalf("the host tells the reader which hop failed: %q", got.LastError)
	}
	// The journal line is scrubbed by logging.Service.AppLog, the one
	// sink every writer goes through (see its test); here only the
	// stored text is checked.
	for _, secret := range []string{"Tok3nAbc", "K3y", "Hop5ecret"} {
		if strings.Contains(got.LastError, secret) {
			t.Fatalf("lastError: %q survived in %q", secret, got.LastError)
		}
	}
}

// TestStore_ScrubsLastErrorSavedBeforeTheFix — see the subscription
// store's test of the same name: an error saved before the fix keeps its
// token on disk and in REST until the next fetch succeeds.
func TestStore_ScrubsLastErrorSavedBeforeTheFix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dns-routes.json")
	legacy := `{"lists":[{"id":"l1","name":"geo","subscriptions":[{"url":"https://1.1.1.1/l/Tok3nAbc","lastError":"Get \"https://cdn.example.net/r/Hop5ecret?key=K3y\": i/o timeout"}]}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	got := data.Lists[0].Subscriptions[0].LastError
	onDisk, _ := os.ReadFile(path)
	for _, secret := range []string{"Hop5ecret", "K3y"} {
		if strings.Contains(got, secret) || strings.Contains(string(onDisk), secret) {
			t.Fatalf("%q survived: lastError %q, file %s", secret, got, onDisk)
		}
	}
	if !strings.Contains(got, "cdn.example.net") {
		t.Fatalf("the failure must stay recorded, only scrubbed: %q", got)
	}
}

// TestValidateSubscriptions_KeepsTheTokenOut — добавление списка проверяет
// подписку загрузкой и возвращает ошибку в REST и в инструменты MCP,
// создающие DNS-маршрут. Рядом с уже очищенным refresh этот путь отдавал
// и настроенный адрес, и адрес хопа редиректа целиком.
func TestValidateSubscriptions_KeepsTheTokenOut(t *testing.T) {
	cause := errors.New(`Get "https://cdn.example.net/r/Hop5ecret": i/o timeout`)
	svc := newTestService(t)
	svc.SetDownloader(failingDownloader{err: cause})

	err := svc.validateSubscriptions(context.Background(), []Subscription{{URL: "https://1.1.1.1/l/Tok3nAbc?key=K3y"}})
	if err == nil {
		t.Fatal("a failing subscription must be refused")
	}
	for _, secret := range []string{"Tok3nAbc", "K3y", "Hop5ecret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("%q survived in %q", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "1.1.1.1") || !strings.Contains(err.Error(), "cdn.example.net") {
		t.Fatalf("the hosts tell the user which address failed: %q", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("the cause must stay reachable for errors.Is")
	}
}
