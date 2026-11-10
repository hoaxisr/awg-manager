package autolistdetect

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestProxyConfigWriteForwarding — прокси пересылает body конверта на
// POST /config сателлита (а не превращает его в GET-чтение).
func TestProxyConfigWriteForwarding(t *testing.T) {
	var gotMethod, gotBody string
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Write([]byte(`{"ok":true}`))
	}))
	defer sat.Close()

	s := NewService()
	statusURLForTest = func() string {
		return strings.TrimPrefix(sat.URL, "http://")
	}
	defer func() { statusURLForTest = nil }()

	proxy := NewProxy(s)
	req := httptest.NewRequest(http.MethodPost, "/api/autolist/config",
		bytes.NewReader([]byte(`{"path":"/config","body":{"autoAdd":false,"targetOutbound":"stabilization"}}`)))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if gotMethod != http.MethodPost {
		t.Fatalf("сателлит видел %s, want POST (GET = баг чтения вместо записи)", gotMethod)
	}
	if gotBody != `{"autoAdd":false,"targetOutbound":"stabilization"}` {
		t.Fatalf("сателлит видел тело %q", gotBody)
	}
}

// TestProxyConfigReadViaGET — конверт без body превращается в GET (чтение),
// пустой POST не затирает конфиг.
func TestProxyConfigReadViaGET(t *testing.T) {
	var gotMethod string
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Write([]byte(`{"autoAdd":true}`))
	}))
	defer sat.Close()

	s := NewService()
	statusURLForTest = func() string { return strings.TrimPrefix(sat.URL, "http://") }
	defer func() { statusURLForTest = nil }()

	proxy := NewProxy(s)
	req := httptest.NewRequest(http.MethodPost, "/api/autolist/config",
		bytes.NewReader([]byte(`{"path":"/config"}`)))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("прокси вернул %d: %s", rec.Code, rec.Body.String())
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("сателлит видел %s, want GET (чтение)", gotMethod)
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			AutoAdd bool `json:"autoAdd"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("ответ не JSON: %v", err)
	}
	if !envelope.Success || !envelope.Data.AutoAdd {
		t.Fatalf("конфиг должен быть в data без потери autoAdd: %s", rec.Body.String())
	}
}

func TestProxyAddUsesCommandTimeout(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.Write([]byte(`{"results":{}}`))
	}))
	defer sat.Close()

	s := NewService()
	// С таким коротким базовым timeout обычный путь упал бы сразу; /add
	// обязан использовать отдельный commandHTTPTimeout.
	s.client = &http.Client{Timeout: time.Millisecond}
	statusURLForTest = func() string { return strings.TrimPrefix(sat.URL, "http://") }
	defer func() { statusURLForTest = nil }()

	req := httptest.NewRequest(http.MethodPost, "/api/autolist/add",
		bytes.NewReader([]byte(`{"path":"/add","body":{"domains":["pocket-id.org"]}}`)))
	rec := httptest.NewRecorder()
	NewProxy(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/add не должен наследовать короткий status timeout: %d %s", rec.Code, rec.Body.String())
	}
}
