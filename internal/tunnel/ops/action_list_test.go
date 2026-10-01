package ops

import (
	"context"
	"fmt"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// F557: бюджеты полных списков OS5-оператора в ctx одного действия
// (WithActionList): бут N туннелей и правка MTU+DNS+адреса — один список
// (было N и 3).
func TestActionList_OS5Budgets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
		steps   func(ctx context.Context, o *OperatorOS5Impl, cs []tunnel.Config) []error
	}{
		{"boot Reconcile N=5", true, func(ctx context.Context, o *OperatorOS5Impl, cs []tunnel.Config) (errs []error) {
			for _, c := range cs {
				errs = append(errs, o.Reconcile(ctx, c))
			}
			return
		}},
		{"boot ColdStart N=5", false, func(ctx context.Context, o *OperatorOS5Impl, cs []tunnel.Config) (errs []error) {
			for _, c := range cs {
				errs = append(errs, o.ColdStart(ctx, c))
			}
			return
		}},
		{"edit MTU+DNS+address", true, func(ctx context.Context, o *OperatorOS5Impl, cs []tunnel.Config) []error {
			return []error{
				o.SetMTU(ctx, cs[0].ID, 1400),
				o.SyncDNS(ctx, cs[0].ID, []string{"9.9.9.9"}),
				o.SyncAddress(ctx, cs[0].ID, "10.9.7.3", 26, ""),
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ndmsquery.NewFakeNDMS()
			var cs []tunnel.Config
			for i := 10; i < 15; i++ {
				c := lifecycleCfg(t)
				c.ID, c.Name = fmt.Sprintf("awg%d", i), fmt.Sprintf("T%d", i)
				f.Add(ndms.Interface{ID: tunnel.NewNames(c.ID).NDMSName, Type: "OpkgTun", Description: c.Name})
				cs = append(cs, c)
			}
			o, _, _ := newOS5Oracle(t, f, &MockBackend{running: tc.running, pid: 1})
			if _, err := o.queries.Interfaces.List(context.Background()); err != nil { // карта тёплая
				t.Fatal(err)
			}
			lists := f.ListCalls()
			for _, err := range tc.steps(ndmsquery.WithActionList(context.Background()), o, cs) {
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := f.ListCalls() - lists; got != 1 {
				t.Fatalf("%d списков, want 1", got)
			}
			clean(t, f)
		})
	}
}
