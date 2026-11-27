package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
)

// Смена ядра на AWGM Route без рантайма отказывает до запуска кандидата —
// иначе пользователь видел «fircd binary missing» из глубины переключения.
func TestSwitchToFirc_RuntimeMissingRejected(t *testing.T) {
	tmp := t.TempDir()
	op := firc.NewOperator(firc.Options{
		ConfigDir:   filepath.Join(tmp, "firc"),
		FircdBinary: filepath.Join(tmp, "missing", "fircd"),
		TunvlessBin: filepath.Join(tmp, "missing", "tunvless"),
		InboundPort: 1099,
	})
	mux := http.NewServeMux()
	NewFircHandler(op).RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	cases := []struct{ name, method, path, body string }{
		{"settings routingEngine", http.MethodPut, "/api/firc/settings", `{"routingEngine":"firc"}`},
		{"settings selected", http.MethodPut, "/api/firc/settings", `{"selected":true}`},
		{"engine switch", http.MethodPost, "/api/firc/engine/switch", `{"engine":"firc"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader(c.body)))
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "FIRC_RUNTIME_NOT_INSTALLED") {
				t.Fatalf("want 409 FIRC_RUNTIME_NOT_INSTALLED, got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
