package nwg

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// newStateTestOperator — оператор, читающий интерфейс из снимка списка.
// Wireguard5 есть в списке NDMS; поля записи сверх базовых (summary,
// wireguard) задаёт тест через FakeNDMS.SetDetail.
func newStateTestOperator(t *testing.T) (*OperatorNativeWG, *query.FakeNDMS) {
	t.Helper()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard5", Type: "Wireguard"})
	o := &OperatorNativeWG{
		queries:      query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }}),
		appLog:       logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps),
		supportsASC:  func() bool { return true },
		hasProxySlot: func(int) bool { return false },
	}
	t.Cleanup(o.Close)
	return o, f
}

func TestGetState_FromSnapshot_Running(t *testing.T) {
	op, f := newStateTestOperator(t)
	f.SetDetail("Wireguard5", json.RawMessage(`{"link":"up",
		"summary":{"layer":{"conf":"running"}},
		"wireguard":{"status":"up","peer":[{"online":true,"last-handshake":12,"rxbytes":100,"txbytes":200,"via":"PPPoE0"}]}}`))

	info := op.GetState(context.Background(), &storage.AWGTunnel{NWGIndex: 5})

	if info.State != tunnel.StateRunning {
		t.Fatalf("State = %v, want %v", info.State, tunnel.StateRunning)
	}
	if !info.InterfaceUp || info.RxBytes != 100 || info.TxBytes != 200 || !info.HasHandshake {
		t.Fatalf("unexpected StateInfo: %+v", info)
	}
	// Один список (bootstrap), ни одного POST по имени.
	if f.ListCalls() != 1 || len(f.Posts) != 0 {
		t.Fatalf("ListCalls=%d Posts=%v, want 1/none", f.ListCalls(), f.Posts)
	}
}

// Записи нет в списке — «не создан», запроса по имени нет.
func TestGetState_NotInSnapshot_NotCreated(t *testing.T) {
	op, f := newStateTestOperator(t)
	f.Remove("Wireguard5")

	info := op.GetState(context.Background(), &storage.AWGTunnel{NWGIndex: 5})
	if info.State != tunnel.StateNotCreated || f.E != 0 || len(f.Posts) != 0 {
		t.Fatalf("State = %v E=%d Posts=%v, want %v/0/none", info.State, f.E, f.Posts, tunnel.StateNotCreated)
	}
}

// Список не прочитан — «не создан»; чтения по имени взамен нет (решение 4).
func TestGetState_ListError_NotCreated(t *testing.T) {
	op, f := newStateTestOperator(t)
	f.FailList(errors.New("boom"))

	info := op.GetState(context.Background(), &storage.AWGTunnel{NWGIndex: 5})
	if info.State != tunnel.StateNotCreated || len(f.Posts) != 0 {
		t.Fatalf("State = %v Posts=%v, want %v/none", info.State, f.Posts, tunnel.StateNotCreated)
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
	op, f := newStateTestOperator(t)
	f.SetDetail("Wireguard5", json.RawMessage(`{"link":"up",
		"wireguard":{"status":"up","peer":[{"online":true}]}}`))

	if got := op.ResolveActiveWAN(context.Background(), &storage.AWGTunnel{NWGIndex: 5}); got != "" {
		t.Fatalf("ResolveActiveWAN = %q, want empty", got)
	}
	// Снимок не старше 2 с (bootstrap), без POST.
	if f.ListCalls() != 1 || len(f.Posts) != 0 {
		t.Fatalf("ListCalls=%d Posts=%v, want 1/none", f.ListCalls(), f.Posts)
	}
}
