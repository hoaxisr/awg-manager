package localdeps

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func ptr[T any](v T) *T { return &v }

// TestLocal_UpdateTunnelSettingsSendsWholeBlocks — EditTunnel берёт блок
// интерфейса и пира целиком, если его прислали; поэтому блок собирается из
// записи, а ключи уходят пустыми — «оставить как есть», и из стора они не
// выходят.
func TestLocal_UpdateTunnelSettingsSendsWholeBlocks(t *testing.T) {
	l, tunnels, _, _ := newLocal(t)
	_ = tunnels
	if err := l.c.TunnelStore.Update("tn-1", func(t *storage.AWGTunnel) error {
		t.Interface = storage.AWGInterface{PrivateKey: "PRIV", Address: "10.8.0.2/32", MTU: 1420, DNS: "1.1.1.1",
			AWGObfuscation: storage.AWGObfuscation{Jc: 4, Jmin: 40, Jmax: 70, H1: "1"}}
		t.Peer.PublicKey, t.Peer.PresharedKey, t.Peer.PersistentKeepalive = "PUB", "PSK", "25"
		t.Peer.Endpoint, t.Peer.AllowedIPs = "vpn.example.net:51820", []string{"0.0.0.0/0"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var got storage.AWGTunnel
	l.c.TunnelEdit = func(_ context.Context, id string, req storage.AWGTunnel) (api.EditedTunnel, error) {
		got = req
		return api.EditedTunnel{Warnings: []string{"10.8.0.2/32 conflicts with nwg3"}}, nil
	}
	l.c.Targets = catalogHarness()

	_, warnings, err := l.UpdateTunnelSettings(context.Background(), mcpsrv.TunnelSettingsInput{
		TunnelID: "tn-1", MTU: ptr(1380), PersistentKeepalive: ptr("15"), ISPInterface: ptr("wan:ppp0"),
		AWG: &mcpsrv.AWGParamsInput{Jc: ptr(6), H1: ptr(" 5-9 ")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Interface.Address != "10.8.0.2/32" || got.Interface.MTU != 1380 || got.Interface.DNS != "1.1.1.1" {
		t.Fatalf("the interface block must be whole, with the change: %+v", got.Interface)
	}
	if o := got.Interface.AWGObfuscation; o.Jc != 6 || o.Jmin != 40 || o.Jmax != 70 || o.H1 != "5-9" {
		t.Fatalf("obfuscation = %+v", o)
	}
	if got.Peer.PublicKey != "PUB" || got.Peer.PersistentKeepalive != "15" || got.Peer.Endpoint != "vpn.example.net:51820" || len(got.Peer.AllowedIPs) != 1 {
		t.Fatalf("the peer block must be whole, with the change: %+v", got.Peer)
	}
	if got.Interface.PrivateKey != "" || got.Peer.PresharedKey != "" {
		t.Fatal("keys must be left empty: EditTunnel keeps the stored ones")
	}
	if got.ISPInterface != "ppp0" || got.ISPInterfaceLabel != "ISP" {
		t.Fatalf("a wan: id goes as the kernel name, with the catalog's label: %q %q", got.ISPInterface, got.ISPInterfaceLabel)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v", warnings)
	}

	// Only the connectivity check: no interface or peer block at all.
	if _, _, err := l.UpdateTunnelSettings(context.Background(), mcpsrv.TunnelSettingsInput{TunnelID: "tn-1", ConnectivityCheck: ptr("ping"), PingTarget: "8.8.8.8"}); err != nil {
		t.Fatal(err)
	}
	if got.Interface.Address != "" || got.Peer.PublicKey != "" || got.ConnectivityCheck == nil || got.ConnectivityCheck.PingTarget != "8.8.8.8" {
		t.Fatalf("an untouched block must not be sent: %+v", got)
	}
}

func TestLocal_UpdateTunnelSettingsRefusals(t *testing.T) {
	l, _, _, _ := newLocal(t)
	called := false
	l.c.TunnelEdit = func(context.Context, string, storage.AWGTunnel) (api.EditedTunnel, error) {
		called = true
		return api.EditedTunnel{}, &api.TunnelEditError{Status: http.StatusForbidden, Code: "TUNNEL_LOCKED", Message: "туннель защищён от изменений"}
	}
	l.c.Targets = catalogHarness()
	ctx := context.Background()

	if _, _, err := l.UpdateTunnelSettings(ctx, mcpsrv.TunnelSettingsInput{TunnelID: "tn-1", MTU: ptr(1400)}); err == nil || !strings.Contains(err.Error(), "защищён") {
		t.Fatalf("the card's refusal must come back in its own words: %v", err)
	}
	called = false
	if _, _, err := l.UpdateTunnelSettings(ctx, mcpsrv.TunnelSettingsInput{TunnelID: "tn-raw", MTU: ptr(1400)}); err == nil || called {
		t.Fatalf("a wdtt-raw tunnel takes no MTU, and EditTunnel would drop it silently: %v", err)
	}
	for _, v := range []string{"tunnel:tn-1", "tunnel:system:Proxy0", "tunnel:wan:ppp0"} {
		if _, _, err := l.UpdateTunnelSettings(ctx, mcpsrv.TunnelSettingsInput{TunnelID: "tn-1", ISPInterface: ptr(v)}); err == nil || called {
			t.Fatalf("%s: a chain goes through another managed tunnel only: %v", v, err)
		}
	}
	if isp, label, err := l.ispInterface(ctx, "tn-2", "tunnel:tn-1"); err != nil || isp != "tunnel:tn-1" || label != "AMS" {
		t.Fatalf("another managed tunnel is a chain target: %q %q %v", isp, label, err)
	}
	if _, _, err := l.UpdateTunnelSettings(ctx, mcpsrv.TunnelSettingsInput{TunnelID: "tn-1", ISPInterface: ptr("wan:ghost")}); err == nil || called {
		t.Fatalf("an unknown WAN must be refused before the edit: %v", err)
	}
	if _, _, err := l.UpdateTunnelSettings(ctx, mcpsrv.TunnelSettingsInput{TunnelID: "ghost", MTU: ptr(1400)}); err == nil || !strings.Contains(err.Error(), "list_tunnels") {
		t.Fatalf("err = %v", err)
	}
}
