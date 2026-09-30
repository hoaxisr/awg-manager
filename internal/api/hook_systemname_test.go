package api

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/events"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// listingWANModel — WAN-модель, которая на SetUp перечитывает список WAN из
// кэша интерфейсов, как настоящая на незнакомом имени (repopulate → ListWAN).
type listingWANModel struct {
	store *ndmsquery.InterfaceStore
	names []string
}

func (m *listingWANModel) SetUp(string, bool) bool {
	wans, _ := m.store.ListWAN(context.Background())
	for _, w := range wans {
		m.names = append(m.names, w.Name)
	}
	return true
}

// I3 (F570): горячо подключённый модем — резолвер вслед за списком имени не
// дал, хук несёт system_name. Имя обязано попасть в кэш ДО SetUp в том же
// Handle: диспетчер кладёт его асинхронно, и первый WAN up терялся бы.
func TestHookHandler_Handle_SystemNameBeforeWANModel(t *testing.T) {
	fg := ndmsquery.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{
		"UsbQmi0": {"id":"UsbQmi0","type":"UsbQmi","state":"up","security-level":"public"}
	}`)
	fg.SetPostSystemName("UsbQmi0", `""`) // устройство ещё не привязано
	store := ndmsquery.NewInterfaceStore(fg, ndmsquery.NopLogger())
	if got := store.ResolveSystemName(context.Background(), "UsbQmi0"); got != "" {
		t.Fatalf("до хука имя %q, want пусто", got)
	}

	h := newTestHookHandler(nil) // без диспетчера: только синхронный путь
	wm := &listingWANModel{store: store}
	h.SetWANModel(wm)
	h.SetSystemNames(store)

	handleForm(t, h, "type=iflayerchanged&id=UsbQmi0&system_name=usb0&layer=ipv4&level=running")

	if len(wm.names) != 1 || wm.names[0] != "usb0" {
		t.Fatalf("WAN-модель в том же Handle видит %v, want [usb0]", wm.names)
	}
}

// orderLog — общий журнал порядка вызовов карты имён и диспетчера.
type orderLog struct{ calls []string }

func (o *orderLog) OnSystemName(id, _ string) { o.calls = append(o.calls, "name:"+id) }
func (o *orderLog) Enqueue(e events.Event)    { o.calls = append(o.calls, "enqueue:"+e.ID) }

// Имя кладётся ДО постановки в очередь: воркер диспетчера, применив
// ifdestroyed того же события (Forget) раньше синхронного вызова, оставил бы
// в карте имя снятого id.
func TestHookHandler_Handle_SystemNameBeforeEnqueue(t *testing.T) {
	o := &orderLog{}
	h := newTestHookHandler(o)
	h.SetSystemNames(o)

	handleForm(t, h, "type=ifdestroyed&id=UsbQmi0&system_name=usb0")

	if len(o.calls) != 2 || o.calls[0] != "name:UsbQmi0" || o.calls[1] != "enqueue:UsbQmi0" {
		t.Fatalf("порядок %v, want [name:UsbQmi0 enqueue:UsbQmi0]", o.calls)
	}
}
