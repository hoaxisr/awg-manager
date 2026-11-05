package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// batchPoster отвечает на пакет по элементам: reply(i) — ответ на i-й элемент
// всего потока; nil — успех.
type batchPoster struct {
	sizes []int
	sent  int
	reply func(i int) string
	err   error
	short bool
}

func (p *batchPoster) Post(_ context.Context, payload any) (json.RawMessage, error) {
	batch, ok := payload.([]any)
	if !ok { // сохранение
		return json.RawMessage(`{}`), nil
	}
	p.sizes = append(p.sizes, len(batch))
	if p.err != nil {
		return nil, p.err
	}
	replies := make([]json.RawMessage, len(batch))
	for i := range replies {
		body := `{}`
		if p.reply != nil {
			if r := p.reply(p.sent); r != "" {
				body = r
			}
		}
		p.sent++
		replies[i] = json.RawMessage(body)
	}
	if p.short {
		replies = replies[1:]
	}
	return json.Marshal(replies)
}

func batchFixture(t *testing.T, p *batchPoster, n int) (*RouteCommands, []StaticRouteSpec) {
	t.Helper()
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	confirmed, err := q.Interfaces.ConfirmEach(context.Background(), []string{"OpkgTun10"})
	if err != nil {
		t.Fatal(err)
	}
	c := NewRouteCommands(p, NewSaveCoordinator(p, nil, time.Hour, time.Hour, 0, nil), q)
	specs := make([]StaticRouteSpec, n)
	for i := range specs {
		specs[i] = StaticRouteSpec{Interface: confirmed["OpkgTun10"], Host: fmt.Sprintf("10.0.%d.%d", i/250, i%250)}
	}
	return c, specs
}

const routeErrReply = `{"ip":{"route":{"status":[{"status":"error","message":"no such interface: X."}]}}}`

func TestAddStaticRoutes_SplitsIntoBatches(t *testing.T) {
	p := &batchPoster{}
	c, specs := batchFixture(t, p, routeBatchSize+1)
	for i, err := range c.AddStaticRoutes(context.Background(), specs) {
		if err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
	}
	if len(p.sizes) != 2 || p.sizes[0] != routeBatchSize || p.sizes[1] != 1 {
		t.Fatalf("размеры пакетов %v", p.sizes)
	}
}

// Отказ привязан к своему маршруту и в чужом пакете: иначе откат снял бы не то.
func TestAddStaticRoutes_ErrorStaysAtItsIndex(t *testing.T) {
	bad := routeBatchSize + 3
	p := &batchPoster{reply: func(i int) string {
		if i == bad {
			return routeErrReply
		}
		return ""
	}}
	c, specs := batchFixture(t, p, routeBatchSize+5)
	for i, err := range c.AddStaticRoutes(context.Background(), specs) {
		if (err != nil) != (i == bad) {
			t.Fatalf("route %d: err=%v, отказ ждали только на %d", i, err, bad)
		}
	}
}

func TestRemoveStaticRoutes_ToleratesNoSuchInterface(t *testing.T) {
	p := &batchPoster{reply: func(int) string { return routeErrReply }}
	c, specs := batchFixture(t, p, 2)
	for i, err := range c.RemoveStaticRoutes(context.Background(), specs) {
		if err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
	}
	for i, err := range c.AddStaticRoutes(context.Background(), specs) {
		if err == nil {
			t.Fatalf("route %d: установка ту же ошибку терпеть не должна", i)
		}
	}
}

// Что применилось при обрыве или ответе не той длины, неизвестно: отказ на весь
// пакет, успехом не притворяемся, и в занятый ndm больше ничего не шлём.
func TestAddStaticRoutes_UnknownOutcomeStopsBatches(t *testing.T) {
	for name, p := range map[string]*batchPoster{
		"transport": {err: errors.New("timeout")},
		"short":     {short: true},
	} {
		t.Run(name, func(t *testing.T) {
			c, specs := batchFixture(t, p, routeBatchSize+1)
			errs := c.AddStaticRoutes(context.Background(), specs)
			for i := 0; i < routeBatchSize; i++ {
				if !errors.Is(errs[i], ErrRouteOutcomeUnknown) {
					t.Fatalf("route %d: %v", i, errs[i])
				}
			}
			if !errors.Is(errs[routeBatchSize], ErrRouteNotSent) {
				t.Fatalf("хвост: %v", errs[routeBatchSize])
			}
			if len(p.sizes) != 1 {
				t.Fatalf("пакетов после обрыва: %v", p.sizes)
			}
		})
	}
}

// Сохранение одно на пачку: пакет дольше дебаунса.
func TestAddStaticRoutes_OneSaveRequestPerCall(t *testing.T) {
	p := &batchPoster{}
	c, specs := batchFixture(t, p, routeBatchSize*3)
	c.AddStaticRoutes(context.Background(), specs)
	c.save.mu.Lock()
	n := c.save.pendingCount
	c.save.mu.Unlock()
	if n != 1 {
		t.Fatalf("запросов сохранения %d, want 1", n)
	}
}

func TestAddStaticRoutes_BadSpecDoesNotStopOthers(t *testing.T) {
	p := &batchPoster{}
	c, specs := batchFixture(t, p, 3)
	specs[1].Interface = query.Confirmed{}
	errs := c.AddStaticRoutes(context.Background(), specs)
	if errs[0] != nil || errs[1] == nil || errs[2] != nil {
		t.Fatalf("errs=%v", errs)
	}
	if len(p.sizes) != 1 || p.sizes[0] != 2 {
		t.Fatalf("размеры пакетов %v", p.sizes)
	}
}

func TestStaticRoutes_EmptyPostsNothing(t *testing.T) {
	p := &batchPoster{}
	c, _ := batchFixture(t, p, 0)
	if errs := c.AddStaticRoutes(context.Background(), nil); len(errs) != 0 || len(p.sizes) != 0 {
		t.Fatalf("errs=%v sizes=%v", errs, p.sizes)
	}
}
