//go:build linux

package firc

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Read-only device inventory. Never writes daemon config, starts processes or
// prints proxy credentials/subscription URLs.
func TestRouterUplinkReadiness(t *testing.T) {
	if os.Getenv("UPLINK_ROUTER_READINESS") != "1" {
		t.Skip("explicit router read-only inventory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := NewClient("/tmp/firc/firc.sock")
	health, err := c.Ping(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("installed FIRC WAN DNS capability=%d", health.UplinkDNSVersion)
	list, err := c.GetTunnels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tun := range list {
		poolInbound := false
		for _, in := range tun.Inbounds {
			if in.Port != 0 && in.Node == TunnelNodePool && (in.Listen == "" || in.Listen == "lo" || in.Listen == "127.0.0.1" || in.Listen == "0.0.0.0") {
				poolInbound = true
			}
		}
		b, _ := json.Marshal(struct {
			ID          string `json:"id"`
			Enabled     bool   `json:"enabled"`
			RequiresDNS bool   `json:"requiresWanDNS"`
			PoolInbound bool   `json:"dedicatedPoolInbound"`
		}{tun.ID, tun.Enable, requiresUplinkDNS(tun), poolInbound})
		t.Log(string(b))
	}
}
