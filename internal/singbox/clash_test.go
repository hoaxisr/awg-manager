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

func clashServer(t *testing.T, h http.HandlerFunc) *ClashClient {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return NewClashClient(strings.TrimPrefix(ts.URL, "http://"))
}

func TestClashClient_IsHealthy(t *testing.T) {
	ok := clashServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			t.Errorf("path: %s", r.URL.Path)
		}
		w.WriteHeader(200)
	})
	if !ok.IsHealthy() {
		t.Error("200 на /version → healthy")
	}
	bad := clashServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	if bad.IsHealthy() {
		t.Error("500 → not healthy")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	addr := strings.TrimPrefix(ts.URL, "http://")
	ts.Close()
	down := NewClashClient(addr)
	if down.IsHealthy() {
		t.Error("недоступный порт → not healthy")
	}
}

func TestClashClient_SetSelector(t *testing.T) {
	var gotMethod, gotPath, gotBody, gotCT string
	c := clashServer(t, func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath: пин экранирования тега — URL.Path у net/http всегда декодирован.
		gotMethod, gotPath, gotCT = r.Method, r.URL.EscapedPath(), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(204)
	})
	if err := c.SetSelector("sel 1", "member-2"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" || gotPath != "/proxies/sel%201" || gotBody != `{"name":"member-2"}` || gotCT != "application/json" {
		t.Fatalf("PUT %s %s %q ct=%q", gotMethod, gotPath, gotBody, gotCT)
	}
	bad := clashServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	if err := bad.SetSelector("sel-1", "m"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("500 → ошибка с кодом, got %v", err)
	}
}

func TestClashClient_SelectorActive(t *testing.T) {
	c := clashServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/proxies/sel-1" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"name":"sel-1","type":"Selector","now":"member-2"}`))
	})
	now, err := c.SelectorActive("sel-1")
	if err != nil || now != "member-2" {
		t.Fatalf("now=%q err=%v", now, err)
	}
	absent := clashServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	if now, err := absent.SelectorActive("sel-9"); err != nil || now != "" {
		t.Fatalf("404 → (\"\", nil), got %q %v", now, err)
	}
	bad := clashServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) })
	if _, err := bad.SelectorActive("sel-1"); err == nil {
		t.Fatal("503 → ошибка")
	}
}
