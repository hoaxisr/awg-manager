package singbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClashClient_GetProxies(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxies" {
			t.Errorf("path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"proxies":{"Germany":{"name":"Germany","type":"vless","history":[{"delay":42}]}}}`))
	}))
	defer ts.Close()

	c := NewClashClient(strings.TrimPrefix(ts.URL, "http://"))
	p, err := c.GetProxies()
	if err != nil {
		t.Fatal(err)
	}
	if p["Germany"].Type != "vless" {
		t.Errorf("type: %+v", p["Germany"])
	}
	if len(p["Germany"].History) != 1 || p["Germany"].History[0].Delay != 42 {
		t.Errorf("history: %+v", p["Germany"].History)
	}
}

func TestClashClient_DelayTest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/proxies/") {
			t.Errorf("path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]int{"delay": 87})
	}))
	defer ts.Close()

	c := NewClashClient(strings.TrimPrefix(ts.URL, "http://"))
	delay, err := c.TestDelay(context.Background(), "Germany", "https://www.gstatic.com/generate_204", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if delay != 87 {
		t.Errorf("delay: %d", delay)
	}
}

func TestClashClient_HasOutbound(t *testing.T) {
	body := `{"proxies":{"direct":{"name":"direct","type":"direct"},"us-vless":{"name":"us-vless","type":"vless"}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxies" {
			_, _ = io.WriteString(w, body)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	c := NewClashClient(addr)

	if !c.HasOutbound("us-vless") {
		t.Errorf("HasOutbound(us-vless) = false, want true")
	}
	if !c.HasOutbound("direct") {
		t.Errorf("HasOutbound(direct) = false, want true")
	}
	if c.HasOutbound("nonexistent") {
		t.Errorf("HasOutbound(nonexistent) = true, want false")
	}
}

func TestClashClient_HasOutbound_ClashDown(t *testing.T) {
	// Point to unused port so HTTP GET fails fast.
	c := NewClashClient("127.0.0.1:1")
	if c.HasOutbound("any-tag") {
		t.Errorf("expected false when Clash is unreachable")
	}
}

// F39: отмена контекста обязана обрывать САМ запрос пробы. Раньше ctx доезжал
// только до backoff-селекта между попытками, а голый http.Get висел до
// таймаута — ручка не отпускала клиента.
//
// Мутация для пина: вернуть `c.http.Get(u)` вместо NewRequestWithContext+Do —
// компилируется, тест краснеет по таймауту.
func TestClashClient_DelayTestHonorsContext(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // сервер не отвечает, пока тест не разрешит
	}))
	defer ts.Close()
	defer close(release)

	c := NewClashClient(strings.TrimPrefix(ts.URL, "http://"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменён ДО вызова

	done := make(chan error, 1)
	go func() {
		_, err := c.TestDelay(ctx, "Germany", "https://www.gstatic.com/generate_204", 3*time.Second)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("проба вернулась без ошибки на отменённом контексте")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("проба не оборвалась по отмене контекста")
	}
}
