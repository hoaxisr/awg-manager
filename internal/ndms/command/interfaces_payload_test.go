package command

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInterfaceCommandsPayloads_CoreOps(t *testing.T) {
	cmds, poster, _, _, hn := newTestInterfaceCommands(t)

	if err := cmds.CreateOpkgTun(context.Background(), "OpkgTun10", "Test tunnel"); err != nil {
		t.Fatalf("CreateOpkgTun: %v", err)
	}
	requireJSONEqual(t, poster.Payloads()[0], `{
		"interface": {
			"OpkgTun10": {
				"description": "Test tunnel",
				"security-level": {"public": true}
			}
		}
	}`)

	if err := cmds.DeleteOpkgTun(context.Background(), "OpkgTun10"); err != nil {
		t.Fatalf("DeleteOpkgTun: %v", err)
	}
	requireJSONEqual(t, poster.Payloads()[1], `{
		"interface": {"OpkgTun10": {"no": true}}
	}`)

	if err := cmds.SetIPGlobal(context.Background(), "OpkgTun10"); err != nil {
		t.Fatalf("SetIPGlobal: %v", err)
	}
	requireJSONEqual(t, poster.Payloads()[2], `{
		"interface": {"OpkgTun10": {"ip": {"global": {"auto": true}}}}
	}`)

	if err := cmds.SetMTU(context.Background(), "OpkgTun10", 1420); err != nil {
		t.Fatalf("SetMTU: %v", err)
	}
	requireJSONEqual(t, poster.Payloads()[3], `{
		"interface": {"OpkgTun10": {"ip": {"mtu": 1420, "tcp": {"adjust-mss": {"pmtu": true}}}}}
	}`)

	if err := cmds.SetDescription(context.Background(), "OpkgTun10", "new desc"); err != nil {
		t.Fatalf("SetDescription: %v", err)
	}
	requireJSONEqual(t, poster.Payloads()[4], `{
		"interface": {"OpkgTun10": {"description": "new desc"}}
	}`)

	if err := cmds.InterfaceUp(context.Background(), "OpkgTun10"); err != nil {
		t.Fatalf("InterfaceUp: %v", err)
	}
	if err := cmds.InterfaceDown(context.Background(), "OpkgTun10"); err != nil {
		t.Fatalf("InterfaceDown: %v", err)
	}
	requireJSONEqual(t, poster.Payloads()[5], `{"interface":{"OpkgTun10":{"up":true}}}`)
	requireJSONEqual(t, poster.Payloads()[6], `{"interface":{"OpkgTun10":{"up":false}}}`)
	if len(hn.calls) != 2 || hn.calls[0] != (hookCall{"OpkgTun10", "running"}) || hn.calls[1] != (hookCall{"OpkgTun10", "disabled"}) {
		t.Fatalf("ExpectHook calls = %#v", hn.calls)
	}
}

func TestInterfaceCommandsPayloads_AddressAndIPv6(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)

	if err := cmds.SetAddress(context.Background(), "OpkgTun10", "10.0.0.2", "255.255.255.255"); err != nil {
		t.Fatalf("SetAddress: %v", err)
	}
	if len(poster.Payloads()) < 2 {
		t.Fatalf("SetAddress payload count = %d, want >=2", len(poster.Payloads()))
	}
	requireJSONEqual(t, poster.Payloads()[0], `{"interface":{"OpkgTun10":{"ip":{"address":{"no":true}}}}}`)
	requireJSONEqual(t, poster.Payloads()[1], `{"interface":{"OpkgTun10":{"ip":{"address":{"address":"10.0.0.2","mask":"255.255.255.255"}}}}}`)

	if err := cmds.SetIPv6Address(context.Background(), "OpkgTun10", "fd00::1"); err != nil {
		t.Fatalf("SetIPv6Address: %v", err)
	}
	requireJSONEqual(t, poster.Payloads()[2], `{
		"interface":{"OpkgTun10":{"ipv6":{"address":[{"no":true},{"block":"fd00::1/128"}]}}}
	}`)

	if err := cmds.ClearIPv6Address(context.Background(), "OpkgTun10"); err != nil {
		t.Fatalf("ClearIPv6Address: %v", err)
	}
	requireJSONEqual(t, poster.Payloads()[3], `{"interface":{"OpkgTun10":{"ipv6":{"address":{"no":true}}}}}`)
}

func TestInterfaceCommandsPayloads_DNS(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)

	if err := cmds.SetDNS(context.Background(), "OpkgTun10", []string{"1.1.1.1", "8.8.8.8"}); err != nil {
		t.Fatalf("SetDNS: %v", err)
	}
	requireJSONEqual(t, poster.Payloads()[0], `{"ip":{"name-server":{"address":"1.1.1.1","interface":"OpkgTun10"}}}`)
	requireJSONEqual(t, poster.Payloads()[1], `{"ip":{"name-server":{"address":"8.8.8.8","interface":"OpkgTun10"}}}`)

	poster.SetError(errors.New("boom"))
	err := cmds.SetDNS(context.Background(), "OpkgTun10", []string{"9.9.9.9"})
	if err == nil || !strings.Contains(err.Error(), "set dns") {
		t.Fatalf("SetDNS failure err = %v, want contains 'set dns'", err)
	}

	poster.SetError(errors.New("ignored"))
	if err := cmds.ClearDNS(context.Background(), "OpkgTun10", []string{"1.1.1.1", "8.8.8.8"}); err != nil {
		t.Fatalf("ClearDNS: %v", err)
	}
}

// Снятие `ip global` идёт parse-формой: JSON-обёртки у обратной команды нет,
// форма снята со стенда 5.01 2026-09-06 («global priority cleared»).
func TestClearIPGlobal_ParseForm(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)

	if err := cmds.ClearIPGlobal(context.Background(), "OpkgTun10"); err != nil {
		t.Fatalf("ClearIPGlobal: %v", err)
	}
	if len(poster.Payloads()) != 1 {
		t.Fatalf("payloads: want 1, got %d", len(poster.Payloads()))
	}
	requireJSONEqual(t, poster.Payloads()[0], `{"parse": "interface OpkgTun10 no ip global"}`)
}
