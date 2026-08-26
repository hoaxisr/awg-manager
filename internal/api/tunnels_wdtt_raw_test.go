package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// rawUpdateStore — store с одной зеркальной записью для PATCH-ветки Update.
func rawUpdateStore(t *testing.T) *storage.AWGTunnelStore {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Save(&storage.AWGTunnel{
		ID:                "wdttraw-de",
		Name:              "Германия",
		Backend:           backendWdttRaw,
		WdttClientID:      "de",
		RawKernelIface:    "opkgtun18",
		ConnectivityCheck: &storage.ConnectivityCheckConfig{Method: "http"},
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func updateRaw(t *testing.T, store *storage.AWGTunnelStore, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := &TunnelsHandler{store: store}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tunnels/update?id=wdttraw-de", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.Update(w, req)
	return w
}

// Требование 7: имя зеркальной записи — производная конфига инстанса, зеркало
// перезапишет его на ближайшем объявлении. PATCH обязан отказать, а не
// подтвердить переименование, которое молча откатится.
func TestUpdate_WdttRawRenameRejected(t *testing.T) {
	store := rawUpdateStore(t)
	w := updateRaw(t, store, `{"name":"Нидерланды","connectivityCheck":{"method":"ping"}}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != "WDTT_RAW_NAME_READONLY" {
		t.Fatalf("code = %q, want WDTT_RAW_NAME_READONLY", resp.Code)
	}

	stored, err := store.Get("wdttraw-de")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "Германия" {
		t.Fatalf("имя в сторе = %q, ожидали неизменное «Германия»", stored.Name)
	}
	// Отказ fail-closed: запись не сохранялась вовсе, а не «имя откатили».
	if stored.ConnectivityCheck == nil || stored.ConnectivityCheck.Method != "http" {
		t.Fatalf("connectivityCheck = %+v, ожидали нетронутый http", stored.ConnectivityCheck)
	}
}

// Тот же PATCH, что шлёт форма редактирования: имя пришло неизменным. Отказ
// по одному лишь факту непустого name запер бы правку связности целиком.
func TestUpdate_WdttRawSameNameSavesConnectivityCheck(t *testing.T) {
	store := rawUpdateStore(t)
	w := updateRaw(t, store, `{"name":"Германия","connectivityCheck":{"method":"ping"}}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	stored, err := store.Get("wdttraw-de")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConnectivityCheck == nil || stored.ConnectivityCheck.Method != "ping" {
		t.Fatalf("connectivityCheck = %+v, ожидали method ping", stored.ConnectivityCheck)
	}
	if stored.Name != "Германия" {
		t.Fatalf("имя в сторе = %q", stored.Name)
	}
}
