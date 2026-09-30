package singbox

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// S3 F546: Proxy1 был в карте и снят снаружи, хук потерян. SyncProxies
// создаёт его заново — намеренно (ExpectCreate), без фантомов и E; живой
// Proxy0 не трогается.
func TestScenario_SyncProxies_RecreatesExternallyRemoved(t *testing.T) {
	withProxyComponent(t)
	ctx := context.Background()
	f := query.NewFakeNDMS(
		ndms.Interface{ID: "Proxy0", Type: "Proxy", State: "up"},
		ndms.Interface{ID: "Proxy1", Type: "Proxy", State: "up"},
	)
	pm := oracleProxyManager(f)
	if _, err := pm.queries.Interfaces.List(ctx); err != nil { // карта тёплая
		t.Fatal(err)
	}
	f.Remove("Proxy1")
	_ = f.DrainHooks() // ifdestroyed потерян
	f.ExpectCreate("Proxy1")

	err := pm.SyncProxies(ctx, []TunnelInfo{
		{Tag: "a", ListenPort: 1080, ProxyInterface: "Proxy0"},
		{Tag: "b", ListenPort: 1081, ProxyInterface: "Proxy1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Created, []string{"Proxy1"}) || !f.Has("Proxy1") {
		t.Fatalf("Proxy1 не создан заново: created=%v posts=%v", f.Created, f.Posts)
	}
	for _, p := range f.Posts {
		if strings.Contains(p, "Proxy0") {
			t.Fatalf("команда по живому Proxy0: %s", p)
		}
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d posts=%v", f.E, f.Phantoms, f.Posts)
	}
}
