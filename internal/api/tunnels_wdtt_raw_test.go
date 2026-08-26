package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// TestGet_WdttRawCard пинит контракт карточки зеркальной записи: GET одного
// туннеля обязан идти по raw-ветке и отдавать состояние САМОЙ записи —
// наложения живых полей из старого движка больше нет, запись и есть источник.
func TestGet_WdttRawCard(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	// Ни одно поле фикстуры не совпадает с дефолтом ветки: Enabled=false дал
	// бы "stopped" сам собой, пустой RawKernelIface — подстановку id, а
	// заданный ConnectivityCheck отличает чтение записи от дефолта "http".
	if err := store.Save(&storage.AWGTunnel{
		ID:                "wdttraw-de",
		Name:              "Германия",
		Backend:           backendWdttRaw,
		WdttClientID:      "de",
		Enabled:           true,
		RawKernelIface:    "opkgtun18",
		RawNdmsIface:      "OpkgTun18",
		ConnectivityCheck: &storage.ConnectivityCheckConfig{Method: "ping"},
	}); err != nil {
		t.Fatal(err)
	}

	h := &TunnelsHandler{store: store}
	w := httptest.NewRecorder()
	h.Get(w, httptest.NewRequest(http.MethodGet, "/api/tunnels/get?id=wdttraw-de", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp struct {
		Data struct {
			Backend           string `json:"backend"`
			State             string `json:"state"`
			Enabled           bool   `json:"enabled"`
			InterfaceName     string `json:"interfaceName"`
			NDMSName          string `json:"ndmsName"`
			ConnectivityCheck struct {
				Method string `json:"method"`
			} `json:"connectivityCheck"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Backend != backendWdttRaw {
		t.Fatalf("backend = %q, want %q", resp.Data.Backend, backendWdttRaw)
	}
	if resp.Data.State != "running" || !resp.Data.Enabled {
		t.Fatalf("state = %q, enabled = %v", resp.Data.State, resp.Data.Enabled)
	}
	if resp.Data.InterfaceName != "opkgtun18" || resp.Data.NDMSName != "OpkgTun18" {
		t.Fatalf("ifaces = %q / %q", resp.Data.InterfaceName, resp.Data.NDMSName)
	}
	if resp.Data.ConnectivityCheck.Method != "ping" {
		t.Fatalf("connectivityCheck.method = %q, want ping", resp.Data.ConnectivityCheck.Method)
	}
}

// TestWdttRawConnectivityCheck_Default — запись без проверки связности
// получает тот же дефолт, что кладёт в новую запись зеркало прокси-рантайма.
func TestWdttRawConnectivityCheck_Default(t *testing.T) {
	got := wdttRawConnectivityCheck(&storage.AWGTunnel{ID: "wdttraw-de"})
	if got == nil || got.Method != "http" {
		t.Fatalf("default = %+v, want method http", got)
	}
}
