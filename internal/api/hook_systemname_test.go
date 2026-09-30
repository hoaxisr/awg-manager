package api

import (
	"context"
	"testing"

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
