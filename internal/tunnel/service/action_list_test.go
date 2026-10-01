package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// nwgOnOracle — настоящий nwg-оператор над оракулом с работающим Wireguard0
// (рукопожатие есть — GetState отвечает Running). Батчи транспорта
// записываются: по ним тест видит, что шаги действительно прошли.
func nwgOnOracle(t *testing.T) (*nwg.OperatorNativeWG, *query.FakeNDMS, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		_, _ = w.Write([]byte(`[{}]`))
	}))
	t.Cleanup(srv.Close)
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	f.SetDetail("Wireguard0", json.RawMessage(`{"link":"up","summary":{"layer":{"conf":"running"}},
		"wireguard":{"status":"up","peer":[{"online":true,"last-handshake":5}]}}`))
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	sc := command.NewSaveCoordinator(f, nopPublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)
	cmds := command.NewCommands(command.Deps{Poster: f, Save: sc, Queries: q, IsOS5: func() bool { return true }})
	tr := transport.NewWithURL(srv.URL, transport.NewSemaphore(2))
	op := nwg.NewOperator(q, cmds, tr, nil)
	t.Cleanup(func() { op.Close(); tr.Close() })
	if _, err := q.Interfaces.List(context.Background()); err != nil { // карта тёплая, как в проде
		t.Fatal(err)
	}
	return op, f, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func anyContains(bodies []string, sub string) bool {
	for _, b := range bodies {
		if strings.Contains(b, sub) {
			return true
		}
	}
	return false
}

// F557: правка живого nativewg на kmod-прошивке — подтверждение правки и
// пересборка слота (SyncKmodSlot) по одному списку (было 2). Смена только
// параметров обфускации: в NDMS до слота не пишется ничего, и снимок для
// ResolveActiveWAN берётся из того же списка.
func TestApplyDiffNWG_WithKmodSlot_OneList(t *testing.T) {
	op, f, _ := nwgOnOracle(t)
	s := &ServiceImpl{state: NewMockStateManager(), nwgOperator: op}
	old := &storage.AWGTunnel{ID: "awg20", Backend: "nativewg",
		Interface: storage.AWGInterface{Address: "10.0.0.1/32", MTU: 1420,
			AWGObfuscation: storage.AWGObfuscation{Jc: 4, Jmin: 40, Jmax: 70}},
		Peer: storage.AWGPeer{PublicKey: "pk", Endpoint: "198.51.100.1:51820"}}
	upd := *old
	upd.Interface.AWGObfuscation.Jc = 5 // kmodShapingChanged → SyncKmodSlot

	lists := f.ListCalls()
	err := s.applyDiffNWG(context.Background(), old, &upd)
	// Слот на хосте теста не собирается (/proc/awg_proxy нет): ошибка шага
	// kmod — доказательство, что SyncKmodSlot прошёл своё подтверждение.
	if err == nil || !strings.Contains(err.Error(), "kmod add") {
		t.Fatalf("SyncKmodSlot не дошёл до слота: %v", err)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("правка + SyncKmodSlot: %d списков, want 1", got)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d phantoms=%d", f.E, f.Phantoms)
	}
}

// Правка с записью в NDMS до слота (DNS, пир): подтверждение — тот же один
// список, но ResolveActiveWAN в SyncKmodSlot читает снимок ПОСЛЕ своих записей
// (метка Invalidate) — второй список, читать-свои-записи. До F557 его роль
// играл список второго подтверждения; больше двух быть не должно.
func TestApplyDiffNWG_WritesThenKmodSlot_TwoLists(t *testing.T) {
	op, f, _ := nwgOnOracle(t)
	s := &ServiceImpl{state: NewMockStateManager(), nwgOperator: op}
	old := &storage.AWGTunnel{ID: "awg20", Backend: "nativewg",
		Interface: storage.AWGInterface{Address: "10.0.0.1/32", MTU: 1420, DNS: "1.1.1.1"},
		Peer:      storage.AWGPeer{PublicKey: "pk", Endpoint: "198.51.100.1:51820"}}
	upd := *old
	upd.Interface.DNS = "9.9.9.9"
	upd.Peer.Endpoint = "198.51.100.2:51820" // kmodShapingChanged → SyncKmodSlot

	lists := f.ListCalls()
	err := s.applyDiffNWG(context.Background(), old, &upd)
	if err == nil || !strings.Contains(err.Error(), "kmod add") {
		t.Fatalf("SyncKmodSlot не дошёл до слота: %v", err)
	}
	if got := f.ListCalls() - lists; got != 2 {
		t.Fatalf("правка с записями + SyncKmodSlot: %d списков, want 2", got)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d phantoms=%d", f.E, f.Phantoms)
	}
}

// F557: замена конфига работающего nativewg (RequireIface, Stop, синхронизация,
// Start) — один список (было 3).
func TestReplaceConfigNWG_Running_OneList(t *testing.T) {
	op, f, bodies := nwgOnOracle(t)
	s, _ := serviceWithStore(t)
	s.nwgOperator = op
	if err := s.store.Create(&storage.AWGTunnel{ID: "awg20", Name: "nwg", Backend: "nativewg",
		Interface: storage.AWGInterface{Address: "10.0.0.1/32", MTU: 1420, DNS: "1.1.1.1"},
		Peer:      storage.AWGPeer{PublicKey: "old", Endpoint: "198.51.100.1:51820"}}); err != nil {
		t.Fatal(err)
	}

	lists := f.ListCalls()
	if err := s.ReplaceConfig(context.Background(), "awg20", sampleConf, "", ReplaceOptions{}); err != nil {
		t.Fatal(err)
	}
	// Stop работающего туннеля — `interface Wireguard0 up:false` батчем.
	if !anyContains(bodies(), `"up":false`) {
		t.Fatalf("Stop не прошёл (туннель не признан работающим): %v", bodies())
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("ReplaceConfig работающего: %d списков, want 1", got)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d phantoms=%d", f.E, f.Phantoms)
	}
}

// confirmingKernelOp — MockOperator, чьи SetMTU/SyncDNS/SyncAddress, как у
// OS5-оператора, подтверждают запись OpkgTun по ctx (requireOpkgTun).
type confirmingKernelOp struct {
	MockOperator
	t        *testing.T
	q        *query.Queries
	confirms int
}

func (c *confirmingKernelOp) confirm(ctx context.Context) error {
	c.confirms++
	if _, _, ok, err := c.q.Interfaces.Confirm(ctx, "OpkgTun10"); err != nil || !ok {
		c.t.Errorf("Confirm OpkgTun10: ok=%v err=%v", ok, err)
	}
	return nil
}

func (c *confirmingKernelOp) SetMTU(ctx context.Context, _ string, _ int) error {
	return c.confirm(ctx)
}
func (c *confirmingKernelOp) SyncDNS(ctx context.Context, _ string, _ []string) error {
	return c.confirm(ctx)
}
func (c *confirmingKernelOp) SyncAddress(ctx context.Context, _, _ string, _ int, _ string) error {
	return c.confirm(ctx)
}

// F557: OS5 applyDiffKernel — MTU, DNS и адрес в одной правке по одному
// списку (было 3).
func TestApplyDiffKernel_MTUDNSAddress_OneList(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun", Description: "k"})
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	op := &confirmingKernelOp{t: t, q: q}
	s := &ServiceImpl{state: NewMockStateManager(), legacyOperator: op}
	old := &storage.AWGTunnel{ID: "awg10", Backend: "kernel",
		Interface: storage.AWGInterface{Address: "10.0.0.1/32", MTU: 1420, DNS: "1.1.1.1"}}
	upd := *old
	upd.Interface.Address = "10.0.0.2/32"
	upd.Interface.MTU = 1400
	upd.Interface.DNS = "9.9.9.9"

	lists := f.ListCalls()
	if err := s.applyDiffKernel(context.Background(), old, &upd); err != nil {
		t.Fatal(err)
	}
	if op.confirms != 3 {
		t.Fatalf("подтверждений %d, want 3", op.confirms)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("MTU+DNS+адрес: %d списков, want 1", got)
	}
}
