package nwg

import (
	"context"
	"fmt"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// F557: бюджеты полных списков по шагам настоящего оператора в ctx одного
// действия (query.WithActionList) — так их зовут оркестратор и service.
// Подтверждения действия — один список; сверх него только снимки «прочитать
// свои записи» (Invalidate после команды) в GetState/ResolveActiveWAN.
func TestActionList_NWGBudgets(t *testing.T) {
	mk := func(i int) *storage.AWGTunnel {
		st := nwgStored(awgObfuscatedIface())
		st.ID = fmt.Sprintf("awg%d", i)
		st.NWGIndex = i
		st.PingCheck = &storage.TunnelPingCheck{Enabled: true}
		return st
	}
	pc := ndms.PingCheckConfig{Host: "8.8.8.8", Mode: "icmp"}
	for _, tc := range []struct {
		name  string
		asc   bool
		lists int
		steps func(ctx context.Context, o *OperatorNativeWG) []error
	}{
		// Рестарт демона на kmod-прошивке (decideReconnect): было 5.
		{"reconnect kmod N=5", false, 1, func(ctx context.Context, o *OperatorNativeWG) (errs []error) {
			for i := range 5 {
				errs = append(errs, o.RestoreKmodTunnel(ctx, mk(i)))
				_ = o.ResolveActiveWAN(ctx, mk(i))
			}
			return
		}},
		// Бут на kmod-прошивке (Reconcile + ping-check): было 14. Остаток —
		// GetState следующего туннеля после записи ping-check предыдущего:
		// метка Invalidate одна на стор, снимок читает список (N-1).
		{"boot kmod N=5", false, 5, func(ctx context.Context, o *OperatorNativeWG) (errs []error) {
			for i := range 5 {
				_ = o.GetState(ctx, mk(i))
				errs = append(errs, o.RestoreKmodTunnel(ctx, mk(i)))
				_ = o.ResolveActiveWAN(ctx, mk(i))
				errs = append(errs, o.ConfigurePingCheck(ctx, mk(i), pc))
			}
			return
		}},
		// Бут/старт на ASC: было 15. Остаток — ResolveActiveWAN после записей
		// Start каждого туннеля (N).
		{"boot ASC N=5", true, 6, func(ctx context.Context, o *OperatorNativeWG) (errs []error) {
			for i := range 5 {
				errs = append(errs, o.Start(ctx, mk(i)))
				_ = o.ResolveActiveWAN(ctx, mk(i))
				errs = append(errs, o.ConfigurePingCheck(ctx, mk(i), pc))
			}
			return
		}},
		// Пользовательский Stop: было 2.
		{"stop", false, 1, func(ctx context.Context, o *OperatorNativeWG) []error {
			return []error{o.RemovePingCheck(ctx, mk(0)), o.Stop(ctx, mk(0))}
		}},
		// Замена конфига работающего: было 3.
		{"replace running", true, 1, func(ctx context.Context, o *OperatorNativeWG) []error {
			_ = o.GetState(ctx, mk(0))
			_, err := o.RequireIface(ctx, mk(0))
			return []error{err, o.Stop(ctx, mk(0)), o.Start(ctx, mk(0))}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, _, f, _ := newLifecycleOperator(t, tc.asc, tc.asc)
			for i := 1; i < 5; i++ {
				f.Add(ndms.Interface{ID: fmt.Sprintf("Wireguard%d", i), Type: "Wireguard"})
			}
			if _, err := o.queries.Interfaces.List(context.Background()); err != nil { // карта тёплая
				t.Fatal(err)
			}
			lists := f.ListCalls()
			for _, err := range tc.steps(query.WithActionList(context.Background()), o) {
				if err != nil {
					t.Fatal(err) // шаг не дошёл до своих команд — бюджет был бы холостым
				}
			}
			if got := f.ListCalls() - lists; got != tc.lists {
				t.Fatalf("%d списков, want %d", got, tc.lists)
			}
			if f.E != 0 || f.Phantoms != 0 {
				t.Fatalf("E=%d phantoms=%d", f.E, f.Phantoms)
			}
		})
	}
}
