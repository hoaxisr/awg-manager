package sbconfig

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestOwnedEndpointEditKeepsStrictDNSAndRejectsAuto(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var binding uplink.Binding
	for _, iface := range interfaces {
		if b, e := uplink.InspectBinding(iface.Name); e == nil {
			binding = b
			break
		}
	}
	if binding.Device == "" {
		t.Skip("no global-unicast interface for source binding")
	}
	p := uplink.Policy{Target: uplink.Target{Engine: "sing-box", Ref: "a"}, Enabled: true, Providers: []string{"isp"}}
	providers := []uplink.Provider{{ID: "isp", Device: binding.Device}}
	doc := map[string]any{"outbounds": []any{map[string]any{"tag": "a", "type": "vless", "server": "changed.example", "bind_interface": binding.Device}}}
	result, err := MaterializeOwned(context.Background(), doc, []uplink.Policy{p}, providers, func(context.Context, string) ([]string, error) { return []string{"9.9.9.9"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(result, "a", binding.Device, false); err != nil {
		t.Fatal(err)
	}
	ob := doc["outbounds"].([]any)[0].(map[string]any)
	delete(ob, "bind_interface")
	if _, err := MaterializeOwned(context.Background(), doc, []uplink.Policy{p}, providers, nil); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal(err)
	}
}
