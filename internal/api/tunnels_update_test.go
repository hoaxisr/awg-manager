package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// TestMergeInterfaceWhitelist_AppliesAWGParamsFromRequest covers issue #131:
// the full edit form sends new AWG obfuscation parameters (Jc, Jmin, S1-S4,
// H1-H4, I1-I5, Qlen) and the user expects them to land in storage. Earlier
// the whitelist always preserved AWG params from existing, silently
// discarding every UI edit; the regression manifested as "save" being a
// no-op for an Amnezia-Premium tunnel where the user wanted to clear or
// regenerate the I1 signature packet.
func TestMergeInterfaceWhitelist_AppliesAWGParamsFromRequest(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address:    "10.0.0.1",
			MTU:        1420,
			DNS:        "1.1.1.1",
			PrivateKey: "secret",
			AWGObfuscation: storage.AWGObfuscation{
				Qlen: 1000,
				Jc:   5, Jmin: 50, Jmax: 1000,
				S1: 100, S2: 200, S3: 300, S4: 400,
				H1: "h1val", H2: "h2val", H3: "h3val", H4: "h4val",
				I1: "i1val", I2: "i2val", I3: "i3val", I4: "i4val", I5: "i5val",
			},
		},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address: "10.0.0.1",
			MTU:     1420,
			DNS:     "1.1.1.1",
			AWGObfuscation: storage.AWGObfuscation{
				Qlen: 2000,
				Jc:   7, Jmin: 60, Jmax: 1100,
				S1: 110, S2: 210, S3: 310, S4: 410,
				H1: "new-h1", H2: "new-h2", H3: "new-h3", H4: "new-h4",
				I1: "new-i1", I2: "new-i2", I3: "new-i3", I4: "new-i4", I5: "new-i5",
			},
		},
	}
	mergeInterfaceWhitelist(req, existing)

	if req.Interface.Qlen != 2000 || req.Interface.Jc != 7 || req.Interface.Jmin != 60 ||
		req.Interface.Jmax != 1100 || req.Interface.S1 != 110 || req.Interface.S2 != 210 ||
		req.Interface.S3 != 310 || req.Interface.S4 != 410 {
		t.Fatalf("numeric AWG params not applied from req: %+v", req.Interface)
	}
	if req.Interface.H1 != "new-h1" || req.Interface.H2 != "new-h2" ||
		req.Interface.H3 != "new-h3" || req.Interface.H4 != "new-h4" {
		t.Fatalf("H1-H4 not applied from req: %+v", req.Interface)
	}
	if req.Interface.I1 != "new-i1" || req.Interface.I2 != "new-i2" ||
		req.Interface.I3 != "new-i3" || req.Interface.I4 != "new-i4" || req.Interface.I5 != "new-i5" {
		t.Fatalf("I1-I5 not applied from req: %+v", req.Interface)
	}
	// PrivateKey still preserves on empty (separate whitelist rule).
	if req.Interface.PrivateKey != "secret" {
		t.Fatalf("PrivateKey lost: got %q", req.Interface.PrivateKey)
	}
}

// TestMergeInterfaceWhitelist_AppliesAWG3ParamsFromRequest guards that the
// AWG 3.0 device params are part of the editable obfuscation block, so an edit
// applies them from the request. They ride along because the whitelist copies
// AWGObfuscation wholesale — this test locks that so a future field-by-field
// refactor can't silently drop them. Read (BuildTunnelResponse) returns the raw
// storage.Interface, so persisting them is enough to surface them to the UI.
func TestMergeInterfaceWhitelist_AppliesAWG3ParamsFromRequest(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address: "10.0.0.1", MTU: 1420, PrivateKey: "secret",
			AWGObfuscation: storage.AWGObfuscation{
				H1: "1", H2: "2", H3: "3", H4: "4",
				HeaderProtectionKey: "oldkey", RekeyAfterTime: "60",
			},
		},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address: "10.0.0.1", MTU: 1420,
			AWGObfuscation: storage.AWGObfuscation{
				H1: "1", H2: "2", H3: "3", H4: "4",
				HeaderProtectionKey:    "cGxhY2Vob2xkZXJrZXlwbGFjZWhvbGRlcmtleTEyMzQ=",
				ContentPaddingAddition: "16",
				RekeyAfterTime:         "120-150",
				RekeyTimeout:           "5",
				RejectAfterTime:        "180",
				KeepaliveTimeout:       "25",
				MaxHandshakeAttempts:   "5",
			},
		},
	}
	mergeInterfaceWhitelist(req, existing)

	got := req.Interface.AWGObfuscation
	if got.HeaderProtectionKey != "cGxhY2Vob2xkZXJrZXlwbGFjZWhvbGRlcmtleTEyMzQ=" ||
		got.ContentPaddingAddition != "16" || got.RekeyAfterTime != "120-150" ||
		got.RekeyTimeout != "5" || got.RejectAfterTime != "180" ||
		got.KeepaliveTimeout != "25" || got.MaxHandshakeAttempts != "5" {
		t.Fatalf("awg3 params not applied from req: %+v", got)
	}
}

// TestMergeInterfaceWhitelist_ClearsAWGParamsFromRequest covers the
// "просто удалить i1" case from issue #131: the user explicitly empties
// signature packet fields in the edit form. The frontend sends i1=""
// (omitted via i1: undefined in buildUpdatePayload); the backend must
// honour that and persist the cleared value, not silently restore the
// previous one from existing.
func TestMergeInterfaceWhitelist_ClearsAWGParamsFromRequest(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address: "10.0.0.1", MTU: 1420,
			AWGObfuscation: storage.AWGObfuscation{
				I1: "<r 2><b 0x8580...>", I2: "old-i2", I3: "old-i3",
			},
		},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address: "10.0.0.1", MTU: 1420,
			// All I1-I5 omitted — JSON unmarshal leaves them empty.
		},
	}
	mergeInterfaceWhitelist(req, existing)

	if req.Interface.I1 != "" || req.Interface.I2 != "" || req.Interface.I3 != "" {
		t.Fatalf("I1-I3 should be cleared, got %+v", req.Interface)
	}
}

// TestMergeInterfaceWhitelist_PartialNoAddress preserves the entire
// Interface when Address is empty (routing-page partial update).
func TestMergeInterfaceWhitelist_PartialNoAddress(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "1.1.1.1", AWGObfuscation: storage.AWGObfuscation{Qlen: 1000}},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{}, // empty — partial update
	}
	mergeInterfaceWhitelist(req, existing)

	if req.Interface.Address != "10.0.0.1" || req.Interface.MTU != 1420 || req.Interface.Qlen != 1000 {
		t.Fatalf("Interface not fully preserved: %+v", req.Interface)
	}
}

// TestMergeInterfaceWhitelist_NewPrivateKey allows replacing the
// PrivateKey when frontend explicitly sends a non-empty one (re-import
// or .conf replace flow).
func TestMergeInterfaceWhitelist_NewPrivateKey(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, PrivateKey: "old"},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, PrivateKey: "new"},
	}
	mergeInterfaceWhitelist(req, existing)

	if req.Interface.PrivateKey != "new" {
		t.Fatalf("PrivateKey not replaced: got %q", req.Interface.PrivateKey)
	}
}

// TestMergeInterfaceWhitelist_DNSCleared accepts an explicit empty DNS
// (user wants to remove DNS servers from the .conf).
func TestMergeInterfaceWhitelist_DNSCleared(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "1.1.1.1"},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: ""},
	}
	mergeInterfaceWhitelist(req, existing)

	if req.Interface.DNS != "" {
		t.Fatalf("DNS not cleared: got %q", req.Interface.DNS)
	}
}

// TestMergePeerWhitelist_PreservesAllowedIPsOnPartial — when PublicKey
// is empty, the entire Peer preserves from existing.
func TestMergePeerWhitelist_PreservesAllowedIPsOnPartial(t *testing.T) {
	existing := &storage.AWGTunnel{
		Peer: storage.AWGPeer{
			PublicKey:           "pubkey",
			PresharedKey:        "psk",
			Endpoint:            "1.2.3.4:51820",
			AllowedIPs:          []string{"0.0.0.0/0", "::/0"},
			PersistentKeepalive: "25",
		},
	}
	req := &storage.AWGTunnel{
		Peer: storage.AWGPeer{}, // empty — partial update
	}
	mergePeerWhitelist(req, existing)

	if req.Peer.PublicKey != "pubkey" || req.Peer.PresharedKey != "psk" ||
		req.Peer.Endpoint != "1.2.3.4:51820" || req.Peer.PersistentKeepalive != "25" ||
		len(req.Peer.AllowedIPs) != 2 {
		t.Fatalf("Peer not fully preserved: %+v", req.Peer)
	}
}

// TestMergePeerWhitelist_AppliesAllFiveFields — when PublicKey is
// non-empty, all five whitelist fields apply from req.
func TestMergePeerWhitelist_AppliesAllFiveFields(t *testing.T) {
	existing := &storage.AWGTunnel{
		Peer: storage.AWGPeer{
			PublicKey:           "oldkey",
			PresharedKey:        "oldpsk",
			Endpoint:            "1.1.1.1:51820",
			AllowedIPs:          []string{"10.0.0.0/8"},
			PersistentKeepalive: "25",
		},
	}
	req := &storage.AWGTunnel{
		Peer: storage.AWGPeer{
			PublicKey:           "newkey",
			PresharedKey:        "newpsk",
			Endpoint:            "2.2.2.2:51820",
			AllowedIPs:          []string{"0.0.0.0/0"},
			PersistentKeepalive: "60",
		},
	}
	mergePeerWhitelist(req, existing)

	if req.Peer.PublicKey != "newkey" || req.Peer.PresharedKey != "newpsk" ||
		req.Peer.Endpoint != "2.2.2.2:51820" || req.Peer.PersistentKeepalive != "60" ||
		len(req.Peer.AllowedIPs) != 1 || req.Peer.AllowedIPs[0] != "0.0.0.0/0" {
		t.Fatalf("Peer fields not applied: %+v", req.Peer)
	}
}

// TestMergePeerWhitelist_PSKCleared lets the user remove the preshared
// key by explicitly sending empty PSK with non-empty PublicKey.
func TestMergePeerWhitelist_PSKCleared(t *testing.T) {
	existing := &storage.AWGTunnel{
		Peer: storage.AWGPeer{PublicKey: "k", PresharedKey: "psk"},
	}
	req := &storage.AWGTunnel{
		Peer: storage.AWGPeer{PublicKey: "k", PresharedKey: ""},
	}
	mergePeerWhitelist(req, existing)

	if req.Peer.PresharedKey != "" {
		t.Fatalf("PSK not cleared: got %q", req.Peer.PresharedKey)
	}
}

// stubTunnelSvc — минимальный TunnelService для тестов Update-хэндлера.
// Get возвращает ошибку: BuildTunnelResponse тогда отдаёт UPDATE_FAILED,
// но это ПОСЛЕ store.Save — ассерты идут по стору, код ответа не важен.
type stubTunnelSvc struct {
	updateFn func(ctx context.Context, oldStored, newStored *storage.AWGTunnel) error
	deleteFn func(ctx context.Context, tunnelID string) error
	stopFn   func(ctx context.Context, tunnelID string) error
	stateFn  func(tunnelID string) tunnel.StateInfo

	replaceCalls int

	// createdCfg — конфиг, с которым хендлер позвал Create: по нему видно,
	// что уехало в NDMS.
	createdCfg *tunnel.Config
	// createdRecord — запись, которую хендлер отдал сервису: по ней видно,
	// что успело проставиться до передачи владения.
	createdRecord *storage.AWGTunnel
	createErr     error
}

func (s *stubTunnelSvc) List(context.Context) ([]service.TunnelWithStatus, error) { return nil, nil }
func (s *stubTunnelSvc) Get(context.Context, string) (*service.TunnelWithStatus, error) {
	return nil, fmt.Errorf("stub")
}
func (s *stubTunnelSvc) Create(_ context.Context, stored *storage.AWGTunnel) error {
	cfg := orchestrator.StoredToConfig(stored)
	s.createdCfg = &cfg
	rec := *stored
	s.createdRecord = &rec
	if s.createErr != nil {
		return s.createErr
	}
	return nil
}
func (s *stubTunnelSvc) Update(ctx context.Context, oldStored, newStored *storage.AWGTunnel) error {
	if s.updateFn != nil {
		return s.updateFn(ctx, oldStored, newStored)
	}
	return nil
}
func (s *stubTunnelSvc) Delete(ctx context.Context, tunnelID string) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, tunnelID)
	}
	return nil
}
func (s *stubTunnelSvc) Start(context.Context, string) error { return nil }
func (s *stubTunnelSvc) Stop(ctx context.Context, tunnelID string) error {
	if s.stopFn != nil {
		return s.stopFn(ctx, tunnelID)
	}
	return nil
}
func (s *stubTunnelSvc) Restart(context.Context, string) error                  { return nil }
func (s *stubTunnelSvc) CheckAddressConflicts(context.Context, string) []string { return nil }
func (s *stubTunnelSvc) GetState(_ context.Context, tunnelID string) tunnel.StateInfo {
	if s.stateFn != nil {
		return s.stateFn(tunnelID)
	}
	return tunnel.StateInfo{}
}
func (s *stubTunnelSvc) SetEnabled(context.Context, string, bool) error      { return nil }
func (s *stubTunnelSvc) SetDefaultRoute(context.Context, string, bool) error { return nil }
func (s *stubTunnelSvc) Import(context.Context, string, string, string) (*service.TunnelWithStatus, error) {
	return nil, fmt.Errorf("stub")
}
func (s *stubTunnelSvc) ReplaceConfig(context.Context, string, string, string) error {
	s.replaceCalls++
	return nil
}
func (s *stubTunnelSvc) WANModel() *wan.Model                     { return nil }
func (s *stubTunnelSvc) GetResolvedISP(string) string             { return "" }
func (s *stubTunnelSvc) SetSelfCreateGate(tunnel.SelfCreateGater) {}

func newTunnelsUpdateHarness(t *testing.T, stub *stubTunnelSvc) (*TunnelsHandler, *storage.AWGTunnelStore) {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	// nil AppLogger безопасен — см. комментарий в settings_test.go.
	return NewTunnelsHandler(stub, store, nil), store
}

func TestTunnelUpdate_PreservesStartedAt(t *testing.T) {
	h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	if err := store.Save(&storage.AWGTunnel{
		ID: "awg10", Name: "t1", Enabled: true,
		StartedAt: "2026-08-18T10:00:00Z", ActiveWAN: "ISP",
		Interface: storage.AWGInterface{Address: "10.0.0.2/32"},
		Peer:      storage.AWGPeer{Endpoint: "1.2.3.4:51820"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Частичное тело — как шлёт фронт (Partial<AWGTunnel>): без StartedAt.
	req := httptest.NewRequest(http.MethodPost, "/tunnels/update?id=awg10",
		strings.NewReader(`{"name":"t1"}`))
	h.Update(httptest.NewRecorder(), req)

	saved, err := store.Get("awg10")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if saved.StartedAt != "2026-08-18T10:00:00Z" {
		t.Fatalf("StartedAt затёрт: %q", saved.StartedAt)
	}
}

// Красный до фикса: пока svc.Update «ходит в RCI», оркестратор записал
// новые runtime-поля; хэндлер сохраняет затем снапшот existing и затирает их.
func TestTunnelUpdate_KeepsConcurrentRuntimeWrites(t *testing.T) {
	stub := &stubTunnelSvc{}
	h, store := newTunnelsUpdateHarness(t, stub)
	if err := store.Save(&storage.AWGTunnel{
		ID: "awg10", Name: "t1", Enabled: true,
		StartedAt: "2026-08-18T10:00:00Z", ActiveWAN: "ISP",
		Interface: storage.AWGInterface{Address: "10.0.0.2/32"},
		Peer:      storage.AWGPeer{Endpoint: "1.2.3.4:51820"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stub.updateFn = func(ctx context.Context, oldStored, newStored *storage.AWGTunnel) error {
		fresh, err := store.Get("awg10")
		if err != nil {
			return err
		}
		fresh.ActiveWAN = "Wireguard2"           // WAN-failover в окне
		fresh.StartedAt = "2026-08-18T11:00:00Z" // рестарт pingcheck'ом
		fresh.Enabled = false                    // suspend оркестратором
		return store.Save(fresh)
	}

	req := httptest.NewRequest(http.MethodPost, "/tunnels/update?id=awg10",
		strings.NewReader(`{"name":"t1"}`))
	h.Update(httptest.NewRecorder(), req)

	saved, err := store.Get("awg10")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if saved.ActiveWAN != "Wireguard2" || saved.StartedAt != "2026-08-18T11:00:00Z" || saved.Enabled {
		t.Fatalf("runtime-поля затёрты снапшотом existing: ActiveWAN=%q StartedAt=%q Enabled=%v",
			saved.ActiveWAN, saved.StartedAt, saved.Enabled)
	}
}

// Issue #795: занятый пер-туннельный замок — ретраибельный конфликт, а не
// провал удаления. 500 фронт показывает как фатальную «Ошибка удаления
// (500)», хотя повтор через несколько секунд проходит. start/stop/restart
// в control.go уже отдают 409 — delete должен вести себя так же.
func TestTunnelDelete_OperationInProgressIs409(t *testing.T) {
	stub := &stubTunnelSvc{deleteFn: func(context.Context, string) error {
		return fmt.Errorf("%w (awg11)", tunnel.ErrOperationInProgress)
	}}
	h, store := newTunnelsUpdateHarness(t, stub)
	if err := store.Save(&storage.AWGTunnel{ID: "awg11", Name: "NL_CHIS"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	h.Delete(rec, httptest.NewRequest(http.MethodPost, "/tunnels/delete?id=awg11", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("код ответа = %d, ожидался 409; тело: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "OPERATION_IN_PROGRESS") {
		t.Fatalf("нет кода OPERATION_IN_PROGRESS в теле: %s", rec.Body.String())
	}
	// Форма 409 для «туннель используется» другая — фронт различает их по
	// полю error, поэтому этот ответ не должен на неё походить.
	if strings.Contains(rec.Body.String(), "tunnel_referenced") {
		t.Fatalf("ответ спутан с tunnel_referenced: %s", rec.Body.String())
	}
}

// Issue #795, шаг 5 из репорта: «нажал заменить конфиг → та же ошибка».
// ReplaceConf останавливает running-туннель перед заменой, и занятый замок
// прилетал сюда как 500 — фронт показывал фатальную ошибку там, где повтор
// через несколько секунд проходит.
func TestTunnelReplaceConf_OperationInProgressIs409(t *testing.T) {
	stub := &stubTunnelSvc{
		stateFn: func(string) tunnel.StateInfo {
			return tunnel.StateInfo{State: tunnel.StateRunning}
		},
		stopFn: func(context.Context, string) error {
			return fmt.Errorf("%w (awg11)", tunnel.ErrOperationInProgress)
		},
	}
	h, store := newTunnelsUpdateHarness(t, stub)
	if err := store.Save(&storage.AWGTunnel{ID: "awg11", Name: "NL_CHIS"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ReplaceConf(rec, httptest.NewRequest(http.MethodPost, "/tunnels/replace?id=awg11",
		strings.NewReader(`{"content":"[Interface]\nAddress = 10.0.0.2/32\n"}`)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("код ответа = %d, ожидался 409; тело: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "OPERATION_IN_PROGRESS") {
		t.Fatalf("нет кода OPERATION_IN_PROGRESS в теле: %s", rec.Body.String())
	}
	// Без этого ассерта тест зелёный и когда хэндлер после ErrorWithStatus
	// не выходит: первый WriteHeader выигрывает, код остаётся 409, а конфиг
	// работающего туннеля молча заменяется под чужой операцией.
	if stub.replaceCalls != 0 {
		t.Fatalf("конфиг заменён вопреки занятому замку: вызовов ReplaceConfig = %d", stub.replaceCalls)
	}
}
