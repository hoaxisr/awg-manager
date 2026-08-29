package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// listSvcStub serves a fixed tunnel list; every other TunnelService method is
// unused by List and left inert.
type listSvcStub struct {
	tunnels []service.TunnelWithStatus
}

func (s *listSvcStub) List(context.Context) ([]service.TunnelWithStatus, error) {
	return s.tunnels, nil
}
func (s *listSvcStub) Get(context.Context, string) (*service.TunnelWithStatus, error) {
	return nil, nil
}
func (s *listSvcStub) Create(context.Context, *storage.AWGTunnel) error {
	return nil
}
func (s *listSvcStub) Update(context.Context, *storage.AWGTunnel, *storage.AWGTunnel) error {
	return nil
}
func (s *listSvcStub) Delete(context.Context, string) error                   { return nil }
func (s *listSvcStub) Start(context.Context, string) error                    { return nil }
func (s *listSvcStub) Stop(context.Context, string) error                     { return nil }
func (s *listSvcStub) Restart(context.Context, string) error                  { return nil }
func (s *listSvcStub) CheckAddressConflicts(context.Context, string) []string { return nil }
func (s *listSvcStub) GetState(context.Context, string) tunnel.StateInfo {
	return tunnel.StateInfo{}
}
func (s *listSvcStub) SetEnabled(context.Context, string, bool) error      { return nil }
func (s *listSvcStub) SetDefaultRoute(context.Context, string, bool) error { return nil }
func (s *listSvcStub) Import(context.Context, string, string, string) (*service.TunnelWithStatus, error) {
	return nil, nil
}
func (s *listSvcStub) ReplaceConfig(context.Context, string, string, string) error { return nil }
func (s *listSvcStub) WANModel() *wan.Model                                        { return nil }
func (s *listSvcStub) GetResolvedISP(string) string                                { return "" }
func (s *listSvcStub) SetSelfCreateGate(tunnel.SelfCreateGater)                    {}

// TestList_ExposesWdttClientID pins the list response contract: the WDTT link
// is carried by wdttClientId, so clients need not guess it from the endpoint.
func TestList_ExposesWdttClientID(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID:           "awg-wd",
		Name:         "WD",
		WdttClientID: "client-a",
		Interface:    storage.AWGInterface{Address: "10.0.0.2/32"},
		Peer:         storage.AWGPeer{Endpoint: "127.0.0.1:9005"},
	}); err != nil {
		t.Fatal(err)
	}

	h := &TunnelsHandler{
		store: store,
		svc:   &listSvcStub{tunnels: []service.TunnelWithStatus{{ID: "awg-wd", Name: "WD"}}},
	}

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/api/tunnels/list", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp struct {
		Data []struct {
			ID           string `json:"id"`
			WdttClientID string `json:"wdttClientId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].WdttClientID != "client-a" {
		t.Fatalf("wdttClientId = %q, want %q", resp.Data[0].WdttClientID, "client-a")
	}
}

// TestList_IncludesWdttRawMirror держит блокер задачи 17: зеркальная запись
// прокси-выхода обязана попадать в общий список туннелей — прежний обходной
// путь (отдельный сборщик из живого wdtt.Service) снесён вместе с движком, и
// без записи в списке карточка выхода исчезала целиком.
//
// backend проверяется отдельно от backendType: первый список считает сам
// (дефолт — "kernel", то есть значение отличает работу от бездействия), второй
// берёт из состояния, которое считает tunnel/service.
func TestList_IncludesWdttRawMirror(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID:             "wdttraw-de",
		Name:           "Германия",
		Backend:        backendWdttRaw,
		WdttClientID:   "de",
		Enabled:        true,
		RawKernelIface: "opkgtun18",
		RawNdmsIface:   "OpkgTun18",
		Interface:      storage.AWGInterface{MTU: 1300},
		Peer:           storage.AWGPeer{Endpoint: "1.2.3.4:56000"},
	}); err != nil {
		t.Fatal(err)
	}

	h := &TunnelsHandler{
		store: store,
		svc: &listSvcStub{tunnels: []service.TunnelWithStatus{{
			ID: "wdttraw-de", Name: "Германия", Enabled: true,
			State:         tunnel.StateRunning,
			StateInfo:     tunnel.StateInfo{State: tunnel.StateRunning, BackendType: backendWdttRaw},
			InterfaceName: "opkgtun18", NDMSName: "OpkgTun18",
		}}},
	}

	items, err := h.listItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	got := items[0]
	if got.Backend != backendWdttRaw {
		t.Fatalf("backend = %q, want %q", got.Backend, backendWdttRaw)
	}
	if got.BackendType != backendWdttRaw {
		t.Fatalf("backendType = %q, want %q", got.BackendType, backendWdttRaw)
	}
	// "raw" — не версия протокола, а признак «версии нет»: бейдж AWG на
	// карточке зеркала не рисуется. Дефолт ветки — "wg", он бы нарисовался.
	if got.AWGVersion != "raw" {
		t.Fatalf("awgVersion = %q, want %q", got.AWGVersion, "raw")
	}
	if got.Status != "running" {
		t.Fatalf("status = %q, want running", got.Status)
	}
	if got.NDMSName != "OpkgTun18" || got.InterfaceName != "opkgtun18" {
		t.Fatalf("iface names = %q / %q", got.InterfaceName, got.NDMSName)
	}
}
