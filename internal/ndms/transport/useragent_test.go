package transport

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/appver"
)

// RCI-запросы обязаны представляться: без заголовка Go шлёт
// "Go-http-client/1.1", и в журнале роутера наш запрос неотличим от чужого.
func TestClient_SendsAppUserAgent(t *testing.T) {
	seen := make(chan string, 3)
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{}`)
	}))

	var dst struct{}
	if err := c.Get(context.Background(), "/show/version", &dst); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := c.Post(context.Background(), map[string]any{"show": map[string]any{}}); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if err := c.GetStream(context.Background(), "/show/log", func(io.Reader) error { return nil }); err != nil {
		t.Fatalf("GetStream: %v", err)
	}

	for i := 0; i < 3; i++ {
		if got := <-seen; got != appver.UA() {
			t.Errorf("request %d User-Agent = %q, want %q", i, got, appver.UA())
		}
	}
}
