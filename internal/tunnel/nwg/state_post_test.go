package nwg

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// newStateTestOperator — оператор, читающий интерфейс через шлюз query.
// Wireguard5 есть в списке NDMS; ответ точечного чтения задаёт тест через
// SetPostInterface/SetPostInterfaceError.
func newStateTestOperator(t *testing.T) (*OperatorNativeWG, *query.FakeGetter) {
	t.Helper()
	g := query.NewFakeGetter()
	g.SetJSON("/show/interface/", `{"Wireguard5":{"id":"Wireguard5"}}`)
	o := &OperatorNativeWG{
		queries:      query.NewQueries(query.Deps{Getter: g, Logger: query.NopLogger(), IsOS5: func() bool { return true }}),
		appLog:       logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps),
		supportsASC:  func() bool { return true },
		hasProxySlot: func(int) bool { return false },
	}
	t.Cleanup(o.Close)
	return o, g
}

func TestGetState_ViaPost_Running(t *testing.T) {
	op, g := newStateTestOperator(t)
	g.SetPostInterface("Wireguard5", `{"show":{"interface":{
		"id":"Wireguard5","link":"up",
		"summary":{"layer":{"conf":"running"}},
		"wireguard":{"status":"up","peer":[{"online":true,"last-handshake":12,"rxbytes":100,"txbytes":200,"via":"PPPoE0"}]}
	}}}`)

	info := op.GetState(context.Background(), &storage.AWGTunnel{NWGIndex: 5})

	if info.State != tunnel.StateRunning {
		t.Fatalf("State = %v, want %v", info.State, tunnel.StateRunning)
	}
	if !info.InterfaceUp || info.RxBytes != 100 || info.TxBytes != 200 || !info.HasHandshake {
		t.Fatalf("unexpected StateInfo: %+v", info)
	}
	// Чтение — POST-форма с именем в теле (GET этот счётчик не трогает).
	if n := g.PostInterfaceCalls("Wireguard5"); n != 1 {
		t.Fatalf("POST show interface Wireguard5: %d, want 1", n)
	}
}

func TestGetState_StatusErrorWithoutID_NotCreated(t *testing.T) {
	// NDMS replies HTTP 200 + status-error object (no "id") for a missing
	// interface — same semantics the old GET path had with {}.
	op, g := newStateTestOperator(t)
	g.SetPostInterface("Wireguard5", `{"show":{"interface":{
		"status":[{"status":"error","code":"6553619","message":"unable to find"}]
	}}}`)

	info := op.GetState(context.Background(), &storage.AWGTunnel{NWGIndex: 5})
	if info.State != tunnel.StateNotCreated {
		t.Fatalf("State = %v, want %v", info.State, tunnel.StateNotCreated)
	}
}

func TestGetState_TransportError_NotCreated(t *testing.T) {
	op, g := newStateTestOperator(t)
	g.SetPostInterfaceError("Wireguard5", errors.New("boom"))

	info := op.GetState(context.Background(), &storage.AWGTunnel{NWGIndex: 5})
	if info.State != tunnel.StateNotCreated {
		t.Fatalf("State = %v, want %v", info.State, tunnel.StateNotCreated)
	}
}

// Интерфейса нет в NDMS: состояние — «не создан», и ни одного точечного
// чтения по отсутствующему имени (каждое — E «unable to find» в журнале, F546).
func TestGetState_AbsentInterface_NoRCI(t *testing.T) {
	f := query.NewFakeNDMS() // Wireguard0 нет
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	o := &OperatorNativeWG{queries: q, appLog: logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps)}
	st := o.GetState(context.Background(), nwgStored(storage.AWGInterface{}))
	if st.State != tunnel.StateNotCreated || f.E != 0 {
		t.Fatalf("state=%v E=%d", st.State, f.E)
	}
}

func TestResolveActiveWAN_NoVia_ReturnsEmpty(t *testing.T) {
	op, g := newStateTestOperator(t)
	g.SetPostInterface("Wireguard5", `{"show":{"interface":{
		"id":"Wireguard5","link":"up",
		"wireguard":{"status":"up","peer":[{"online":true}]}
	}}}`)

	if got := op.ResolveActiveWAN(context.Background(), &storage.AWGTunnel{NWGIndex: 5}); got != "" {
		t.Fatalf("ResolveActiveWAN = %q, want empty", got)
	}
	if n := g.PostInterfaceCalls("Wireguard5"); n != 1 {
		t.Fatalf("POST show interface Wireguard5: %d, want 1", n)
	}
}
