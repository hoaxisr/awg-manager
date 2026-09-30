package payloads

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// F546: команды по существующему интерфейсу берут query.Confirmed. Payload
// Confirmed-версии совпадает со строковой (…Legacy) для того же имени —
// форма RCI не изменилась. Аргументы различимы, чтобы перестановка ловилась.
func TestCmd_Confirmed_SamePayloadAsLegacy(t *testing.T) {
	q := query.NewQueries(query.Deps{
		Getter: query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"}),
		Logger: query.NopLogger(),
	})
	c, _, ok, err := q.Interfaces.Confirm(context.Background(), "Wireguard0")
	if err != nil || !ok {
		t.Fatalf("Confirm = (%v, %v)", ok, err)
	}
	const n = "Wireguard0"
	peer := PeerConfig{PublicKey: "pk", Endpoint: "1.2.3.4:51820", AllowedIPv4: []AllowedIP{{Address: "0.0.0.0", Mask: "0"}}, KeepaliveInterval: 25, PresharedKey: "psk"}
	cases := []struct {
		name      string
		confirmed any
		legacy    any
	}{
		{"Delete", CmdInterfaceDelete(c), CmdInterfaceDeleteLegacy(n)},
		{"Description", CmdInterfaceDescription(c, "d"), CmdInterfaceDescriptionLegacy(n, "d")},
		{"SecurityLevel", CmdInterfaceSecurityLevel(c, "public"), CmdInterfaceSecurityLevelLegacy(n, "public")},
		{"Up", CmdInterfaceUp(c, true), CmdInterfaceUpLegacy(n, true)},
		{"IPAddress", CmdInterfaceIPAddress(c, "10.0.0.2", "255.255.255.0"), CmdInterfaceIPAddressLegacy(n, "10.0.0.2", "255.255.255.0")},
		{"MTU", CmdInterfaceMTU(c, 1420), CmdInterfaceMTULegacy(n, 1420)},
		{"AdjustMSS", CmdInterfaceAdjustMSS(c, true), CmdInterfaceAdjustMSSLegacy(n, true)},
		{"IPGlobal", CmdInterfaceIPGlobal(c, true), CmdInterfaceIPGlobalLegacy(n, true)},
		{"DNS", CmdInterfaceDNS(c, []string{"1.1.1.1"}), CmdInterfaceDNSLegacy(n, []string{"1.1.1.1"})},
		{"IPv6Address", CmdInterfaceIPv6Address(c, "fd00::2"), CmdInterfaceIPv6AddressLegacy(n, "fd00::2")},
		{"PrivateKey", CmdWireguardPrivateKey(c, "key"), CmdWireguardPrivateKeyLegacy(n, "key")},
		{"Peer", CmdWireguardPeer(c, peer), CmdWireguardPeerLegacy(n, peer)},
		{"PeerEndpoint", CmdWireguardPeerEndpoint(c, "pk", "1.2.3.4:51820"), CmdWireguardPeerEndpointLegacy(n, "pk", "1.2.3.4:51820")},
		{"PeerConnect", CmdWireguardPeerConnect(c, "pk", "PPPoE0"), CmdWireguardPeerConnectLegacy(n, "pk", "PPPoE0")},
		{"PeerDisconnect", CmdWireguardPeerDisconnect(c, "pk"), CmdWireguardPeerDisconnectLegacy(n, "pk")},
		{"PeerNo", CmdWireguardPeerNo(c, "pk"), CmdWireguardPeerNoLegacy(n, "pk")},
	}
	for _, tc := range cases {
		got, want := mustJSON(t, tc.confirmed), mustJSON(t, tc.legacy)
		if !strings.Contains(got, `"name":"Wireguard0"`) {
			t.Errorf("%s: нет имени в %s", tc.name, got)
		}
		if got != want {
			t.Errorf("%s: Confirmed %s != Legacy %s", tc.name, got, want)
		}
	}
}

// Создание адресует ещё не существующее имя — остаётся строкой.
func TestCmdInterfaceCreate_String(t *testing.T) {
	if got := mustJSON(t, CmdInterfaceCreate("Wireguard9")); got != `{"interface":{"name":"Wireguard9"}}` {
		t.Fatalf("got %s", got)
	}
}
