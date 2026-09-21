package subscription

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

func TestFetch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("dmxlc3M6Ly9hYjQ0Lg==")) // base64
	}))
	defer srv.Close()

	body, ct, err := Fetch(srv.URL, nil, FetchOpts{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("ct=%q", ct)
	}
	if string(body) != "dmxlc3M6Ly9hYjQ0Lg==" {
		t.Errorf("body=%q", body)
	}
}

func TestFetch_5xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, _, err := Fetch(srv.URL, nil, FetchOpts{Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 5xx error, got %v", err)
	}
}

func TestFetch_AppliesCustomHeaders(t *testing.T) {
	var seenUA, seenCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUA = r.Header.Get("User-Agent")
		seenCustom = r.Header.Get("X-Device-OS")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	headers := []Header{
		{Name: "User-Agent", Value: "Happ/4.6.0"},
		{Name: "X-Device-OS", Value: "iOS"},
	}
	_, _, err := Fetch(srv.URL, headers, FetchOpts{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if seenUA != "Happ/4.6.0" {
		t.Errorf("UA=%q", seenUA)
	}
	if seenCustom != "iOS" {
		t.Errorf("custom=%q", seenCustom)
	}
}

func TestFetch_SkipsForbiddenHeaders(t *testing.T) {
	var seenHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost = r.Host
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	headers := []Header{
		{Name: "Host", Value: "evil.com"},
	}
	_, _, err := Fetch(srv.URL, headers, FetchOpts{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// seenHost should be the URL's host, not "evil.com".
	if seenHost == "evil.com" {
		t.Errorf("Host header was overridden to evil.com — should be skipped")
	}
}

func TestFetch_BodyLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Send 6 MiB
		large := make([]byte, 6*1024*1024)
		w.Write(large)
	}))
	defer srv.Close()

	body, _, err := Fetch(srv.URL, nil, FetchOpts{Timeout: 5 * time.Second, MaxBodyBytes: 5 * 1024 * 1024})
	if err == nil {
		t.Errorf("expected body-limit error, body len=%d", len(body))
	}
}

func TestFetch_FollowsRedirect(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok-final"))
	}))
	defer final.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirect.Close()

	body, _, err := Fetch(redirect.URL, nil, FetchOpts{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("fetch redirect err: %v", err)
	}
	if string(body) != "ok-final" {
		t.Fatalf("body=%q, want ok-final", body)
	}
}

func TestFetch_RedirectLimitFive(t *testing.T) {
	// 6 redirects in chain should hit explicit redirect guard (>5).
	var servers [7]*httptest.Server
	for i := 0; i < 6; i++ {
		next := i + 1
		idx := i
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, servers[next].URL, http.StatusFound)
		}))
		_ = idx
	}
	servers[6] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("never"))
	}))
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()

	_, _, err := Fetch(servers[0].URL, nil, FetchOpts{Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "слишком много перенаправлений") {
		t.Fatalf("expected too many redirects error, got %v", err)
	}
}

// Граница F308: загрузка идёт стражным клиентом — loopback закрыт на dial.
// TestMain снимает страж ради httptest; здесь он возвращается на время теста.
func TestFetch_BlocksInternalAddress(t *testing.T) {
	restoreGuard()
	defer func() { restoreGuard = httpclient.AllowInternalDialForTest() }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("запрос доехал до внутреннего адреса")
	}))
	defer srv.Close()

	_, _, err := Fetch(srv.URL, nil, FetchOpts{Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "внутренний адрес") {
		t.Fatalf("expected internal-address rejection, got %v", err)
	}
}

// Ранний отказ на входном адресе: схема проверяется до транспорта, строка
// внятная, а не «unsupported protocol scheme» из net/http.
func TestFetch_RejectsNonHTTPScheme(t *testing.T) {
	_, _, err := Fetch("ftp://203.0.113.34/sub", nil, FetchOpts{Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Fatalf("expected scheme rejection, got %v", err)
	}
}
