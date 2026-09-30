package diagnostics

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// diagTunnels — TunnelServiceForDiag с заданным списком туннелей.
type diagTunnels []service.TunnelWithStatus

func (d diagTunnels) List(context.Context) ([]service.TunnelWithStatus, error) { return d, nil }
func (diagTunnels) Start(context.Context, string) error                        { return nil }
func (diagTunnels) Stop(context.Context, string) error                         { return nil }
func (diagTunnels) WANModel() *wan.Model                                       { return nil }
func (diagTunnels) GetResolvedISP(string) string                               { return "" }

// F546: состояние NDMS в отчёте — из ОДНОГО снимка списка на отчёт, по имени
// NDMS не спрашивают: туннель, снятый снаружи без доставленного хука, не
// даёт E.
func TestCollectTunnels_OneSnapshotNoPointReads(t *testing.T) {
	ctx := context.Background()
	f := ndmsquery.NewFakeNDMS(
		ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun", State: "up"},
		ndms.Interface{ID: "OpkgTun11", Type: "OpkgTun", State: "up"})
	q := ndmsquery.NewQueries(ndmsquery.Deps{Getter: f, Logger: ndmsquery.NopLogger()})
	if _, err := q.Interfaces.List(ctx); err != nil { // карта тёплая, как в проде
		t.Fatal(err)
	}
	f.Remove("OpkgTun11") // хук не доставлен
	q.Interfaces.Invalidate("OpkgTun10")
	lists := f.ListCalls()

	r := &Runner{deps: Deps{
		TunnelService: diagTunnels{{ID: "awg10", Name: "a"}, {ID: "awg11", Name: "b"}},
		NDMSQueries:   q,
		TunnelStore:   storage.NewAWGTunnelStore(t.TempDir()),
	}}
	infos := r.collectTunnels(ctx)

	if len(infos) != 2 {
		t.Fatalf("туннелей в отчёте %d, want 2", len(infos))
	}
	if infos[0].Interface.NDMSState == "" {
		t.Fatal("состояние OpkgTun10 не попало в отчёт")
	}
	if f.E != 0 || len(f.Posts) != 0 {
		t.Fatalf("E=%d Posts=%v, want 0/none", f.E, f.Posts)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("списков на отчёт %d, want 1", got)
	}
}
