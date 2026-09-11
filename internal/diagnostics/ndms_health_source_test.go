package diagnostics

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// «NDMS отвечает» обязан предупреждать, если версию добыл запасной канал:
// наполненный store больше не означает, что HTTP-морда RCI жива. Без этой
// проверки диагностика давала бы Pass ровно в тот момент, когда обязана
// предупреждать.
func TestNDMSHealth_WarnsWhenVersionAdopted(t *testing.T) {
	store := ndmsquery.NewSystemInfoStore(nil, nil)
	store.Adopt(ndms.Version{Release: "5.01.C.3.0-1", Title: "5.1.3"}, "ndmc")

	r := &Runner{deps: Deps{NDMSQueries: &ndmsquery.Queries{SystemInfo: store}}}
	got := r.testNDMSHealth(context.Background())

	if got.Status != StatusWarn {
		t.Errorf("статус = %q, хотели %q (RCI молчал); detail=%q", got.Status, StatusWarn, got.Detail)
	}
}
