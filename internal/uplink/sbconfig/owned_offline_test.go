package sbconfig

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestOfflineStrictTargetDoesNotBlockOtherSlotWrites(t *testing.T) {
	doc, err := Apply(map[string]any{"outbounds": []any{map[string]any{"tag": "offline", "type": "vless", "server": "192.0.2.1"}}}, []Spec{{Tag: "offline", Binding: uplink.Binding{Device: "absent-wan", IPv4: "192.0.2.2"}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := MaterializeOwned(context.Background(), doc, []uplink.Policy{{Target: uplink.Target{Engine: "sing-box", Ref: "offline"}, Enabled: true, Providers: []string{"isp"}}}, []uplink.Provider{{ID: "isp", Device: "absent-wan"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(result, "offline", "absent-wan", false); err != nil {
		t.Fatal(err)
	}
}
