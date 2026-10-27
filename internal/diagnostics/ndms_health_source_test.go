package diagnostics

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// «NDMS отвечает» обязан предупреждать, если версию дал файл: значит, при
// старте молчали и ndmc, и RCI. Версия от ndmc — основной путь (F742), Pass.
func TestNDMSHealth_WarnsOnlyWhenVersionFromFile(t *testing.T) {
	for src, want := range map[string]string{"components.xml": StatusWarn, "ndmc": StatusPass} {
		store := ndmsquery.NewSystemInfoStore(nil, nil)
		store.Adopt(ndms.Version{Release: "5.01.C.3.0-1", Title: "5.1.3"}, src)

		r := &Runner{deps: Deps{NDMSQueries: &ndmsquery.Queries{SystemInfo: store}}}
		got := r.testNDMSHealth(context.Background())

		if got.Status != want {
			t.Errorf("%s: статус = %q, хотели %q; detail=%q", src, got.Status, want, got.Detail)
		}
	}
}
