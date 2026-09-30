package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/events"
	ndmsevents "github.com/hoaxisr/awg-manager/internal/ndms/events"
)

// registerRoutes публикует готовый HookHandler в HookSink: после этого
// событие spool проходит полный путь (здесь — обновление списка туннелей),
// а не только кэш. Без Publish хуки навсегда остались бы «только кэш».
func TestRegisterRoutes_PublishesHookHandler(t *testing.T) {
	s, _ := newGuardServer(t)
	s.hookSink = api.NewHookSink(nil)
	_, ch, unsub := s.bus.Subscribe()
	defer unsub()

	s.registerRoutes(http.NewServeMux())
	s.hookSink.Handle(ndmsevents.Event{Type: ndmsevents.EventIfCreated, ID: "OpkgTun99"})

	timeout := time.After(time.Second)
	for {
		select {
		case ev := <-ch:
			if inv, ok := ev.Data.(events.ResourceInvalidatedEvent); ok && inv.Resource == events.ResourceTunnels {
				return
			}
		case <-timeout:
			t.Fatal("за 1 с нет инвалидации ResourceTunnels: HookHandler не опубликован")
		}
	}
}
