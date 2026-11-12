package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// flakyResolverGetter роняет первый POST (вопрос резолвера об имени ядра
// вслед за списком), дальше отвечает как обычный FakeGetter.
type flakyResolverGetter struct {
	*ndmsquery.FakeGetter
	failed bool
}

func (g *flakyResolverGetter) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	if !g.failed {
		g.failed = true
		return nil, errors.New("rci timeout")
	}
	return g.FakeGetter.Post(ctx, payload)
}

// F785: резолвер имён не ответил на загрузке. Без повтора списка PPPoE остался
// бы вне модели WAN до первого хука, а стабильно поднятый канал хуков не шлёт —
// авто-выбор ушёл бы на резерв.
func TestPopulateWANModel_RetriesWhenResolverFailed(t *testing.T) {
	fg := ndmsquery.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{
		"PPPoE0": {"id":"PPPoE0","type":"PPPoE","state":"up","summary":{"layer":{"ipv4":"running"}},"security-level":"public"}
	}`)
	fg.SetPostSystemName("PPPoE0", `"ppp1"`)
	queries := ndmsquery.NewQueries(ndmsquery.Deps{
		Getter: &flakyResolverGetter{FakeGetter: fg},
		Logger: ndmsquery.NopLogger(),
		IsOS5:  func() bool { return true },
	})
	model := wan.NewModel()

	populateWANModel(context.Background(), queries, model, logging.NewScopedLogger(nil, logging.GroupSystem, logging.SubBoot))

	if !model.IsUp("ppp1") {
		t.Fatalf("PPPoE0 (ppp1) не попал в модель WAN: %+v", model.ForUI())
	}
}
