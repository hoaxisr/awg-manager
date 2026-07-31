package nwg

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// stubResolveGap обнуляет паузу между попытками резолва — failing-resolve
// тесты не должны спать по 2×300ms.
func stubResolveGap(t *testing.T) {
	t.Helper()
	orig := resolveRetryGap
	resolveRetryGap = 0
	t.Cleanup(func() { resolveRetryGap = orig })
}

// orderPin возвращает run-callback для stubWGTool, проверяющий, что к
// моменту wg set RCI-батч уже ушёл: NDMS, применяя peer-команду, сбрасывает
// kernel-endpoint — wg set ДО батча был бы затёрт заглушкой.
func orderPin(t *testing.T, cs *captureServer) func(context.Context, string, ...string) error {
	t.Helper()
	return func(context.Context, string, ...string) error {
		if len(cs.bodies) == 0 {
			t.Error("wg set must run AFTER the RCI batch")
		}
		return nil
	}
}

// SyncPeer с v6-endpoint'ом: в RCI уходит заглушка (NDMS отвергает v6 в
// peer-командах), реальный endpoint доезжает до ядра wg set'ом и реестр
// стража обновляется — для работающего туннеля.
func TestSyncPeer_IPv6EndpointUsesPlaceholderAndKernelSet(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	op.guardRegister("awg20", guardEntry{iface: "nwg5", pubkey: "OLDKEY", endpoint: "[2a02::1]:1", name: "Wireguard5"})
	log := &eventLog{}
	calls := stubWGTool(t, log, "/opt/bin/wg", orderPin(t, cs))

	stored := &storage.AWGTunnel{
		ID:       "awg20",
		NWGIndex: 5,
		Peer: storage.AWGPeer{
			PublicKey: "NEWKEY",
			Endpoint:  "[2a02:6b8::feed:ff]:51820",
		},
	}
	if err := op.SyncPeer(context.Background(), stored, "OLDKEY"); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}

	body := strings.Join(cs.bodies, "\n")
	if strings.Contains(body, "2a02:6b8") {
		t.Fatalf("v6 endpoint must not reach RCI:\n%s", body)
	}
	if !strings.Contains(body, ndmsEndpointPlaceholder) {
		t.Fatalf("placeholder endpoint missing in RCI batch:\n%s", body)
	}
	if len(*calls) != 1 {
		t.Fatalf("wg set calls = %d, want 1: %v", len(*calls), *calls)
	}
	got := strings.Join((*calls)[0], " ")
	if got != "/opt/bin/wg set nwg5 peer NEWKEY endpoint [2a02:6b8::feed:ff]:51820" {
		t.Fatalf("unexpected wg set: %q", got)
	}

	// Реестр стража обновлён новым ключом и endpoint'ом.
	op.guardMu.Lock()
	entry := op.guard["awg20"]
	op.guardMu.Unlock()
	if entry.pubkey != "NEWKEY" || entry.endpoint != "[2a02:6b8::feed:ff]:51820" {
		t.Fatalf("guard entry not updated: %+v", entry)
	}
}

// Hostname с AAAA-only резолвом на работающем туннеле: в RCI — заглушка
// (NDMS такое имя не резолвит), в ядро — свежерезолвнутый v6, реестр
// стража обновлён (ключ, endpoint и spec для перерезолва DDNS).
func TestSyncPeer_HostnameResolvesV6UsesPlaceholderAndKernelSet(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	op.resolveFn = func(string) (string, int, error) { return "2a02:6b8::feed:ff", 51820, nil }
	op.guardRegister("awg20", guardEntry{iface: "nwg5", pubkey: "OLDKEY", endpoint: "[2a02::1]:1", spec: "old.example.com:1", name: "Wireguard5"})
	log := &eventLog{}
	calls := stubWGTool(t, log, "/opt/bin/wg", orderPin(t, cs))

	stored := &storage.AWGTunnel{
		ID:       "awg20",
		NWGIndex: 5,
		Peer: storage.AWGPeer{
			PublicKey: "NEWKEY",
			Endpoint:  "vpn.example.com:51820",
		},
	}
	if err := op.SyncPeer(context.Background(), stored, "OLDKEY"); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}

	body := strings.Join(cs.bodies, "\n")
	if !strings.Contains(body, ndmsEndpointPlaceholder) {
		t.Fatalf("placeholder endpoint missing in RCI batch:\n%s", body)
	}
	if len(*calls) != 1 {
		t.Fatalf("wg set calls = %d, want 1: %v", len(*calls), *calls)
	}
	got := strings.Join((*calls)[0], " ")
	if got != "/opt/bin/wg set nwg5 peer NEWKEY endpoint [2a02:6b8::feed:ff]:51820" {
		t.Fatalf("unexpected wg set: %q", got)
	}
	entry, ok := op.guardGet("awg20")
	if !ok || entry.pubkey != "NEWKEY" || entry.endpoint != "[2a02:6b8::feed:ff]:51820" || entry.spec != "vpn.example.com:51820" {
		t.Fatalf("guard entry not updated: %+v", entry)
	}
}

// Смена endpoint'а работающего v6-туннеля на v4-литерал: реальный адрес
// уходит в RCI (им управляет NDMS), страж снимается — иначе он воскресил бы
// удалённого пира со старым ключом.
func TestSyncPeer_V6ToV4LiteralUnregistersGuard(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	op.guardRegister("awg20", guardEntry{iface: "nwg5", pubkey: "OLDKEY", endpoint: "[2a02::1]:51820", spec: "[2a02::1]:51820", name: "Wireguard5"})
	log := &eventLog{}
	calls := stubWGTool(t, log, "/opt/bin/wg", nil)

	stored := &storage.AWGTunnel{
		ID:       "awg20",
		NWGIndex: 5,
		Peer:     storage.AWGPeer{PublicKey: "NEWKEY", Endpoint: "1.2.3.4:51820"},
	}
	if err := op.SyncPeer(context.Background(), stored, "OLDKEY"); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}

	if !strings.Contains(strings.Join(cs.bodies, "\n"), "1.2.3.4:51820") {
		t.Fatalf("real v4 endpoint must reach RCI:\n%s", strings.Join(cs.bodies, "\n"))
	}
	if len(*calls) != 0 {
		t.Fatalf("wg set must not run for v4 endpoint: %v", *calls)
	}
	if op.guardHas("awg20") {
		t.Fatal("guard must be unregistered after switch to v4")
	}
}

// Резолв hostname'а упал, а параметры пира изменились: устаревший реестр
// стража снимается (wg set по старому ключу воскресил бы удалённого пира).
// Hostname уходит в RCI как раньше.
func TestSyncPeer_ResolveFailedChangedPeerDropsStaleGuard(t *testing.T) {
	stubResolveGap(t)
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	op.resolveFn = func(string) (string, int, error) { return "", 0, context.DeadlineExceeded }
	op.guardRegister("awg20", guardEntry{iface: "nwg5", pubkey: "OLDKEY", endpoint: "[2a02::1]:51820", spec: "old.example.com:51820", name: "Wireguard5"})
	log := &eventLog{}
	calls := stubWGTool(t, log, "/opt/bin/wg", nil)

	stored := &storage.AWGTunnel{
		ID:       "awg20",
		NWGIndex: 5,
		Peer:     storage.AWGPeer{PublicKey: "NEWKEY", Endpoint: "vpn.example.com:51820"},
	}
	if err := op.SyncPeer(context.Background(), stored, "OLDKEY"); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}

	if !strings.Contains(strings.Join(cs.bodies, "\n"), "vpn.example.com:51820") {
		t.Fatalf("hostname must reach RCI unchanged on resolve failure:\n%s", strings.Join(cs.bodies, "\n"))
	}
	if len(*calls) != 0 {
		t.Fatalf("wg set must not run on resolve failure: %v", *calls)
	}
	if op.guardHas("awg20") {
		t.Fatal("stale guard entry must be unregistered when peer changed and resolve failed")
	}
}

// Резолв упал, но пир НЕ менялся (правка keepalive и т.п.): реестр стража
// не трогаем — транзиентный сбой DNS не должен снимать защиту endpoint'а.
// Резолв в SyncPeer — свежий, БЕЗ кэш-фолбэка: кэшированный IP прежнего
// endpoint'а не должен «подтверждать» v4 и снимать стража.
func TestSyncPeer_ResolveFailedUnchangedPeerKeepsGuard(t *testing.T) {
	stubResolveGap(t)
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	op.resolveFn = func(string) (string, int, error) { return "", 0, context.DeadlineExceeded }
	op.guardRegister("awg20", guardEntry{iface: "nwg5", pubkey: "KEY", endpoint: "[2a02::1]:51820", spec: "vpn.example.com:51820", name: "Wireguard5"})
	log := &eventLog{}
	_ = stubWGTool(t, log, "/opt/bin/wg", nil)

	stored := &storage.AWGTunnel{
		ID:       "awg20",
		NWGIndex: 5,
		// Отравленный кэш: v4-адрес ПРЕЖНЕГО endpoint'а. Фолбэк на него
		// «подтвердил» бы v4 и снял стража — свежий резолв его игнорирует.
		ResolvedEndpointIP: "1.2.3.4",
		Peer:               storage.AWGPeer{PublicKey: "KEY", Endpoint: "vpn.example.com:51820", PersistentKeepalive: "25"},
	}
	if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}
	if !op.guardHas("awg20") {
		t.Fatal("guard must survive transient resolve failure when peer unchanged")
	}
}

// Живой переход v4→v6 (реестр пуст, устройство есть): wg set проходит,
// туннель берётся под стражу — иначе NDMS затрёт kernel-endpoint заглушкой
// при первом переприменении конфига, а чинить будет некому.
func TestSyncPeer_LiveV4ToV6TransitionRegistersGuard(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	log := &eventLog{}
	calls := stubWGTool(t, log, "/opt/bin/wg", orderPin(t, cs))

	stored := &storage.AWGTunnel{
		ID:       "awg21",
		NWGIndex: 6,
		Peer:     storage.AWGPeer{PublicKey: "K", Endpoint: "[2a02::1]:51820"},
	}
	if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}
	if !strings.Contains(strings.Join(cs.bodies, "\n"), ndmsEndpointPlaceholder) {
		t.Fatal("placeholder endpoint missing in RCI batch")
	}
	if len(*calls) != 1 {
		t.Fatalf("wg set calls = %d, want 1: %v", len(*calls), *calls)
	}
	entry, ok := op.guardGet("awg21")
	if !ok || entry.pubkey != "K" || entry.endpoint != "[2a02::1]:51820" || entry.spec != "[2a02::1]:51820" || entry.iface != "nwg6" {
		t.Fatalf("tunnel must be guarded after live v4->v6 transition: %+v", entry)
	}
}

// Туннель не запускался (kernel-устройства нет — wg set падает): под стражу
// не берём, endpoint доедет при старте.
func TestSyncPeer_IPv6DeviceMissingStaysUnguarded(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	log := &eventLog{}
	noDevice := func(context.Context, string, ...string) error {
		return context.DeadlineExceeded // «Unable to modify interface: No such device»
	}
	calls := stubWGTool(t, log, "/opt/bin/wg", noDevice)

	stored := &storage.AWGTunnel{
		ID:       "awg21",
		NWGIndex: 6,
		Peer:     storage.AWGPeer{PublicKey: "K", Endpoint: "[2a02::1]:51820"},
	}
	if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("wg set attempts = %d, want 1: %v", len(*calls), *calls)
	}
	if op.guardHas("awg21") {
		t.Fatal("tunnel without kernel device must not be guarded")
	}
}

// Частичный отказ: RCI-батч уже заменил пира, wg set упал. Реестр стража
// ОБЯЗАН перейти на новый ключ/endpoint — устаревшая запись с OLDKEY
// воскресила бы удалённого пира первым же sweep'ом; свежую доведёт страж.
func TestSyncPeer_WGSetFailureStillUpdatesGuardRegistry(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	op.guardRegister("awg20", guardEntry{iface: "nwg5", pubkey: "OLDKEY", endpoint: "[2a02::1]:1", spec: "[2a02::1]:1", name: "Wireguard5"})
	log := &eventLog{}
	failing := func(context.Context, string, ...string) error { return context.DeadlineExceeded }
	_ = stubWGTool(t, log, "/opt/bin/wg", failing)

	stored := &storage.AWGTunnel{
		ID:       "awg20",
		NWGIndex: 5,
		Peer:     storage.AWGPeer{PublicKey: "NEWKEY", Endpoint: "[2a02:6b8::feed:ff]:51820"},
	}
	if err := op.SyncPeer(context.Background(), stored, "OLDKEY"); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}
	entry, ok := op.guardGet("awg20")
	if !ok || entry.pubkey != "NEWKEY" || entry.endpoint != "[2a02:6b8::feed:ff]:51820" {
		t.Fatalf("registry must carry the NEW peer even when wg set failed: %+v", entry)
	}
}

// v6-литерал без порта — НЕ «подтверждённый v4»: при неизменном пире страж
// остаётся (v4Confirmed по такой форме снял бы его с ложным «endpoint
// теперь v4»).
func TestSyncPeer_V6LiteralWithoutPortIsNotV4(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	resolves := 0
	op.resolveFn = func(string) (string, int, error) { resolves++; return "", 0, context.DeadlineExceeded }
	op.guardRegister("awg20", guardEntry{iface: "nwg5", pubkey: "K", endpoint: "[2a02::1]:51820", spec: "[2a02::1]", name: "Wireguard5"})
	log := &eventLog{}
	_ = stubWGTool(t, log, "/opt/bin/wg", nil)

	stored := &storage.AWGTunnel{
		ID:       "awg20",
		NWGIndex: 5,
		Peer:     storage.AWGPeer{PublicKey: "K", Endpoint: "[2a02::1]"},
	}
	if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}
	if resolves != 0 {
		t.Fatalf("IP literal must not be resolved, got %d resolves", resolves)
	}
	if !op.guardHas("awg20") {
		t.Fatal("v6 literal without port must not be treated as confirmed v4")
	}
}

// Пустой endpoint (листен-only пир) не гоняет резолвер — сохранение
// настроек не должно платить секунды за DNS-ретраи.
func TestSyncPeer_EmptyEndpointSkipsResolve(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	resolves := 0
	op.resolveFn = func(string) (string, int, error) { resolves++; return "", 0, context.DeadlineExceeded }
	log := &eventLog{}
	calls := stubWGTool(t, log, "/opt/bin/wg", nil)

	stored := &storage.AWGTunnel{
		ID:       "awg21",
		NWGIndex: 6,
		Peer:     storage.AWGPeer{PublicKey: "K", Endpoint: ""},
	}
	if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}
	if resolves != 0 || len(*calls) != 0 {
		t.Fatalf("empty endpoint: resolves=%d wg=%v, want 0/none", resolves, *calls)
	}
}
