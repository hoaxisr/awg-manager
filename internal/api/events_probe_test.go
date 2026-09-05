package api

import (
	"sort"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// busProbe — подписчик шины для пинов «мутация опубликовала инвалидацию».
// Publish кладёт событие в буферизованный канал синхронно, поэтому после
// возврата handler'а всё уже в канале; invalidated() вычерпывает его без ожидания.
type busProbe struct {
	b  *events.Bus
	ch <-chan events.Event
}

func newBusProbe(t *testing.T) *busProbe {
	t.Helper()
	b := events.NewBus()
	_, ch, unsub := b.Subscribe()
	t.Cleanup(unsub)
	return &busProbe{b: b, ch: ch}
}

func (p *busProbe) bus() *events.Bus { return p.b }

// invalidated — отсортированный список "resource/reason" событий resource:invalidated,
// накопленных с прошлого вызова; прочие типы событий пропускаются.
func (p *busProbe) invalidated() []string {
	out := []string{}
	for {
		select {
		case e, ok := <-p.ch:
			if !ok {
				sort.Strings(out)
				return out
			}
			if e.Type != events.EventResourceInvalidated {
				continue
			}
			if d, ok := e.Data.(events.ResourceInvalidatedEvent); ok {
				out = append(out, string(d.Resource)+"/"+d.Reason)
			}
		default:
			sort.Strings(out)
			return out
		}
	}
}

// events — data всех событий типа kind, накопленных с прошлого вычерпывания
// (только map[string]string; прочие пропускаются).
func (p *busProbe) events(kind string) []map[string]string {
	var out []map[string]string
	for {
		select {
		case e := <-p.ch:
			if e.Type != kind {
				continue
			}
			if d, ok := e.Data.(map[string]string); ok {
				out = append(out, d)
			}
		default:
			return out
		}
	}
}

// waitInvalidated ждёт первое resource:invalidated не дольше d — для публикаций
// из горутины (Restart); возвращает его и всё, что уже накопилось.
func (p *busProbe) waitInvalidated(t *testing.T, d time.Duration) []string {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case e := <-p.ch:
			if e.Type != events.EventResourceInvalidated {
				continue
			}
			if d, ok := e.Data.(events.ResourceInvalidatedEvent); ok {
				return append([]string{string(d.Resource) + "/" + d.Reason}, p.invalidated()...)
			}
		case <-deadline:
			t.Fatalf("инвалидация не пришла за %v", d)
			return nil
		}
	}
}
