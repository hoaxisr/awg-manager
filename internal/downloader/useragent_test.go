package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// uaProbe отдаёт сервер и канал с User-Agent каждого пришедшего запроса.
func uaProbe(t *testing.T) (*httptest.Server, chan string) {
	t.Helper()
	seen := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(ts.Close)
	return ts, seen
}

// Загрузчик общий: имя и версию панели он шлёт только там, где адрес наш.
func TestReadAll_ForwardsUserAgent(t *testing.T) {
	ts, seen := uaProbe(t)

	svc := NewService(Deps{})
	if _, _, err := svc.ReadAll(context.Background(), Request{
		Purpose:       "test-useragent",
		URL:           ts.URL,
		UserAgent:     "awgm/1.2.3 (mipsel-3.4)",
		MaxBodyBytes:  64,
		RouteOverride: &Route{Tag: "direct"},
	}); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got := <-seen; got != "awgm/1.2.3 (mipsel-3.4)" {
		t.Errorf("User-Agent = %q, want the one from the request", got)
	}
}

// Адрес вводит пользователь (подписки на списки доменов) — версия и арка
// панели туда уходить не должны: без UserAgent заголовка нет вовсе.
func TestReadAll_NoUserAgentByDefault(t *testing.T) {
	ts, seen := uaProbe(t)

	svc := NewService(Deps{})
	if _, _, err := svc.ReadAll(context.Background(), Request{
		Purpose:       "test-useragent",
		URL:           ts.URL,
		MaxBodyBytes:  64,
		RouteOverride: &Route{Tag: "direct"},
	}); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got := <-seen; got != "Go-http-client/1.1" {
		t.Errorf("User-Agent = %q, want Go's anonymous default", got)
	}
}

func TestDownloadFile_ForwardsUserAgent(t *testing.T) {
	ts, seen := uaProbe(t)

	svc := NewService(Deps{})
	if _, err := svc.DownloadFile(context.Background(), FileRequest{
		Request: Request{
			Purpose:       "test-useragent",
			URL:           ts.URL,
			UserAgent:     "awgm/1.2.3 (mipsel-3.4)",
			MaxBodyBytes:  64,
			RouteOverride: &Route{Tag: "direct"},
		},
		DestPath:     t.TempDir() + "/out.bin",
		MaxFileBytes: 64,
		Mode:         0o644,
	}); err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if got := <-seen; got != "awgm/1.2.3 (mipsel-3.4)" {
		t.Errorf("User-Agent = %q, want the one from the request", got)
	}
}

func TestDownloadFile_NoUserAgentByDefault(t *testing.T) {
	ts, seen := uaProbe(t)

	svc := NewService(Deps{})
	if _, err := svc.DownloadFile(context.Background(), FileRequest{
		Request: Request{
			Purpose:       "test-useragent",
			URL:           ts.URL,
			MaxBodyBytes:  64,
			RouteOverride: &Route{Tag: "direct"},
		},
		DestPath:     t.TempDir() + "/out.bin",
		MaxFileBytes: 64,
		Mode:         0o644,
	}); err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if got := <-seen; got != "Go-http-client/1.1" {
		t.Errorf("User-Agent = %q, want Go's anonymous default", got)
	}
}
