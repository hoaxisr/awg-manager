package payloads

import (
	"context"
	"encoding/json"
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

// F546: команды по существующему интерфейсу берут query.Confirmed; форма RCI
// прежняя. Эталон — точный JSON на каждую функцию: аргументы различимы, так
// что перепутанное имя или порядок аргументов роняют тест.
func TestCmd_Confirmed_Payloads(t *testing.T) {
	q := query.NewQueries(query.Deps{
		Getter: query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"}),
		Logger: query.NopLogger(),
	})
	c, _, ok, err := q.Interfaces.Confirm(context.Background(), "Wireguard0")
	if err != nil || !ok {
		t.Fatalf("Confirm = (%v, %v)", ok, err)
	}
	peer := PeerConfig{PublicKey: "pk", Endpoint: "1.2.3.4:51820", AllowedIPv4: []AllowedIP{{Address: "0.0.0.0", Mask: "0"}}, KeepaliveInterval: 25, PresharedKey: "psk"}
	cases := []struct {
		name string
		got  any
		want string
	}{
		{"Description", CmdInterfaceDescription(c, "d"), `{"interface":{"description":"d","name":"Wireguard0"}}`},
		{"SecurityLevel", CmdInterfaceSecurityLevel(c, "public"), `{"interface":{"name":"Wireguard0","security-level":{"public":true}}}`},
		{"Up", CmdInterfaceUp(c, true), `{"interface":{"name":"Wireguard0","up":true}}`},
		{"Down", CmdInterfaceUp(c, false), `{"interface":{"name":"Wireguard0","up":false}}`},
		{"IPAddress", CmdInterfaceIPAddress(c, "10.0.0.2", "255.255.255.0"), `{"interface":{"ip":{"address":{"address":"10.0.0.2","mask":"255.255.255.0"}},"name":"Wireguard0"}}`},
		{"MTU", CmdInterfaceMTU(c, 1420), `{"interface":{"ip":{"mtu":1420},"name":"Wireguard0"}}`},
		{"AdjustMSS", CmdInterfaceAdjustMSS(c, true), `{"interface":{"ip":{"adjust-mss":true},"name":"Wireguard0"}}`},
		{"AdjustMSSOff", CmdInterfaceAdjustMSS(c, false), `{"interface":{"ip":{"adjust-mss":false},"name":"Wireguard0"}}`},
		{"IPGlobal", CmdInterfaceIPGlobal(c, true), `{"interface":{"ip":{"global":{"auto":true}},"name":"Wireguard0"}}`},
		{"IPGlobalManual", CmdInterfaceIPGlobal(c, false), `{"interface":{"ip":{"global":{}},"name":"Wireguard0"}}`},
		{"DNS", CmdInterfaceDNS(c, []string{"1.1.1.1"}), `{"interface":{"ip":{"name-server":[{"name-server":"1.1.1.1"}]},"name":"Wireguard0"}}`},
		{"IPv6Address", CmdInterfaceIPv6Address(c, "fd00::2"), `{"interface":{"ipv6":{"address":[{"block":"fd00::2/128"}]},"name":"Wireguard0"}}`},
		{"PrivateKey", CmdWireguardPrivateKey(c, "key"), `{"interface":{"name":"Wireguard0","wireguard":{"private-key":"key"}}}`},
		{"Peer", CmdWireguardPeer(c, peer), `{"interface":{"name":"Wireguard0","wireguard":{"peer":{"allow-ips":[{"address":"0.0.0.0","mask":"0"}],"endpoint":{"address":"1.2.3.4:51820"},"keepalive-interval":{"interval":25},"key":"pk","preshared-key":"psk"}}}}`},
		{"PeerEndpoint", CmdWireguardPeerEndpoint(c, "pk", "1.2.3.4:51820"), `{"interface":{"name":"Wireguard0","wireguard":{"peer":{"endpoint":{"address":"1.2.3.4:51820"},"key":"pk"}}}}`},
		{"PeerConnect", CmdWireguardPeerConnect(c, "pk", "PPPoE0"), `{"interface":{"name":"Wireguard0","wireguard":{"peer":{"connect":{"via":"PPPoE0"},"key":"pk"}}}}`},
		{"PeerDisconnect", CmdWireguardPeerDisconnect(c, "pk"), `{"interface":{"name":"Wireguard0","wireguard":{"peer":{"connect":{"no":true},"key":"pk"}}}}`},
		{"PeerNo", CmdWireguardPeerNo(c, "pk"), `{"interface":{"name":"Wireguard0","wireguard":{"peer":{"key":"pk","no":true}}}}`},
	}
	for _, tc := range cases {
		if got := mustJSON(t, tc.got); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

// Создание адресует ещё не существующее имя — остаётся строкой.
func TestCmdInterfaceCreate_String(t *testing.T) {
	if got := mustJSON(t, CmdInterfaceCreate("Wireguard9")); got != `{"interface":{"name":"Wireguard9"}}` {
		t.Fatalf("got %s", got)
	}
}
