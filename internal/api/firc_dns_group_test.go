package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
)

// Группа «только DNS» никуда не направляет: ни целью автолиста, ни целью кандидата ей не быть.
func TestFircAutolist_DNSOnlyGroupIsNotATarget(t *testing.T) {
	groups := `{"groups":[
		{"id":"vpn","name":"VPN","interface":"nwg0","enable":true,"resolve":{"tunnel":true}},
		{"id":"doh","name":"DoH","interface":"dns","enable":true,"resolve":{"server":"https://9.9.9.9/dns-query","fallback":true}}]}`
	var (
		mu       sync.Mutex
		settings []string
	)
	mock := http.NewServeMux()
	reply := func(path, body string) {
		mock.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				var req struct {
					Settings map[string]any `json:"settings"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				group, _ := req.Settings["app.autolist.group"].(string)
				mu.Lock()
				settings = append(settings, group)
				mu.Unlock()
			}
			_, _ = w.Write([]byte(body))
		})
	}
	reply("/api/v1/system/netfilter", `{"ok":true}`)
	reply("/api/v1/system/settings", `{"settings":{}}`)
	reply("/api/v1/system/autolist", `{}`)
	current := groups
	mock.HandleFunc("/api/v1/groups", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write([]byte(current))
	})
	srv := httptest.NewServer(mock)
	defer srv.Close()

	op := firc.NewOperator(firc.Options{ConfigDir: t.TempDir()})
	op.SetClient(firc.NewHTTPClient(srv.URL, srv.Client()))
	mux := http.NewServeMux()
	NewFircHandler(op).RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	rec := do(http.MethodPut, "/api/firc/autolist/config", `{"enabled":true}`)
	if rec.Code != http.StatusOK || len(settings) != 1 || settings[0] != "vpn" {
		t.Fatalf("default target: %d %v %s", rec.Code, settings, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"groupId":"vpn"`) {
		t.Fatalf("status shows another target: %s", rec.Body)
	}
	if rec := do(http.MethodPut, "/api/firc/autolist/config", `{"enabled":true,"groupId":"doh"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "GROUP_DNS_ONLY") {
		t.Fatalf("explicit dns target: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodPut, "/api/firc/autolist/config", `{"enabled":false,"groupId":"doh"}`); rec.Code != http.StatusOK {
		t.Fatalf("switching off must not depend on the target: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodPost, "/api/firc/autolist/candidate", `{"domain":"a.example","action":"add","groupId":"doh","confirmed":true}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "GROUP_DNS_ONLY") {
		t.Fatalf("candidate: %d %s", rec.Code, rec.Body)
	}

	mu.Lock()
	current = `{"groups":[{"id":"doh","name":"DoH","interface":"dns","enable":true,"resolve":{"server":"https://9.9.9.9/dns-query","fallback":true}}]}`
	mu.Unlock()
	if rec := do(http.MethodPut, "/api/firc/autolist/config", `{"enabled":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("only dns groups left: %d %s", rec.Code, rec.Body)
	}
}

// Нарушение правил resolve — ошибка запроса, а не сервера, по обоим маршрутам сохранения.
func TestFircPutGroups_InvalidGroupIs400(t *testing.T) {
	op := firc.NewOperator(firc.Options{ConfigDir: t.TempDir()})
	mux := http.NewServeMux()
	NewFircHandler(op).RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	body := `{"groups":[{"id":"doh","name":"DoH","interface":"dns","enable":true,"resolve":{"server":""}}]}`
	for _, path := range []string{"/api/firc/groups", "/api/firc/v1/groups"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, path, strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "a dns group needs resolve.server") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}
