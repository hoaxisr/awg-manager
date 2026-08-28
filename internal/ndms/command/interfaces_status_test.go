package command

import (
	"context"
	"strings"
	"testing"
)

// Ответы сняты с живого роутера (KeeneticOS 5.01): NDMS отвечает HTTP 200 и
// кладёт отказ во вложенный status.
const (
	respCreateRejected = `{"interface":{"OpkgTun20":{"status":[{"status":"error","code":"6553609",` +
		`"ident":"Network::Interface::Base","message":"unable to find OpkgTun20 in \"Network::Interface::Base\"."}]}}}`
	respDeleteMissing = `{"interface":{"OpkgTun9":{"status":[{"status":"error","code":"6553611",` +
		`"ident":"Network::Interface::Repository","message":"unable to find interface \"OpkgTun9\"."}]}}}`
	respBusy = `{"interface":{"OpkgTun9":{"status":[{"status":"error","code":"1",` +
		`"ident":"Network::Interface::Repository","message":"interface is busy."}]}}}`
)

// Отказ в создании интерфейса обязан всплыть здесь, а не превратиться в
// «no "OpkgTun0" IP interface found» шагом позже (issue #768).
func TestCreateOpkgTun_SurfacesNestedError(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	poster.SetResponse(respCreateRejected)
	err := cmds.CreateOpkgTunWithSecurityLevel(context.Background(), "OpkgTun20", "d", "private")
	if err == nil {
		t.Fatal("ожидалась ошибка создания")
	}
	if !strings.Contains(err.Error(), "unable to find OpkgTun20") {
		t.Errorf("ошибка не доносит ответ роутера: %v", err)
	}
}

func TestInterfaceSetters_SurfaceNestedError(t *testing.T) {
	cases := []struct {
		name string
		call func(*InterfaceCommands) error
	}{
		{"SetAddress", func(c *InterfaceCommands) error {
			return c.SetAddress(context.Background(), "OpkgTun20", "10.0.0.1", "255.255.255.0")
		}},
		{"SetIPv6Address", func(c *InterfaceCommands) error {
			return c.SetIPv6Address(context.Background(), "OpkgTun20", "fd00::1")
		}},
		{"SetMTU", func(c *InterfaceCommands) error {
			return c.SetMTU(context.Background(), "OpkgTun20", 1280)
		}},
		{"SetDescription", func(c *InterfaceCommands) error {
			return c.SetDescription(context.Background(), "OpkgTun20", "d")
		}},
		{"SetSecurityLevel", func(c *InterfaceCommands) error {
			return c.SetSecurityLevel(context.Background(), "OpkgTun20", "private")
		}},
		{"InterfaceUp", func(c *InterfaceCommands) error {
			return c.InterfaceUp(context.Background(), "OpkgTun20")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmds, poster, _, _, _ := newTestInterfaceCommands(t)
			poster.SetResponse(respCreateRejected)
			if err := c.call(cmds); err == nil {
				t.Error("отказ роутера проглочен")
			}
		})
	}
}

// Снос идемпотентен по замыслу: реап и откат зовут его на интерфейсе, которого
// уже может не быть.
func TestDeleteOpkgTun_TolerantToMissing(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	poster.SetResponse(respDeleteMissing)
	if err := cmds.DeleteOpkgTun(context.Background(), "OpkgTun9"); err != nil {
		t.Errorf("отсутствующий интерфейс — не ошибка сноса: %v", err)
	}
}

// Прочие отказы сноса обязаны всплывать: сирота лучше видимая, чем тихая.
func TestDeleteOpkgTun_SurfacesRealError(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	poster.SetResponse(respBusy)
	if err := cmds.DeleteOpkgTun(context.Background(), "OpkgTun9"); err == nil {
		t.Error("реальный отказ сноса проглочен")
	}
}
