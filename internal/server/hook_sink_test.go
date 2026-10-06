package server

import (
	"net/http"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	ndmsevents "github.com/hoaxisr/awg-manager/internal/ndms/events"
)

// spyHookDispatcher запоминает события, поставленные в очередь.
type spyHookDispatcher struct{ got []ndmsevents.Event }

func (s *spyHookDispatcher) Enqueue(e ndmsevents.Event) { s.got = append(s.got, e) }

// registerRoutes публикует готовый HookHandler в HookSink: после этого
// событие spool проходит полный путь обработчика (здесь — его диспетчер,
// подключённый SetDispatcher), а не только диспетчер приёмника. Без Publish
// хуки навсегда остались бы «только кэш».
func TestRegisterRoutes_PublishesHookHandler(t *testing.T) {
	s, _ := newGuardServer(t)
	disp := &spyHookDispatcher{}
	s.ndmsDispatcher = disp                // диспетчер обработчика
	s.hookSink = api.NewHookSink(nil, nil) // до Publish событие не дошло бы никуда

	s.registerRoutes(http.NewServeMux())
	s.hookSink.Handle(ndmsevents.Event{Type: ndmsevents.EventIfCreated, ID: "OpkgTun99"})

	if len(disp.got) != 1 || disp.got[0].ID != "OpkgTun99" {
		t.Fatalf("событие не прошло через опубликованный HookHandler: %#v", disp.got)
	}
}
