//go:build linux

package singbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/sbconfig"
	"github.com/hoaxisr/awg-manager/internal/uplink/testsupport"
)

type nativeUplinkResolver struct{}

func (nativeUplinkResolver) Providers() []uplink.Provider {
	return []uplink.Provider{{ID: "isp1", Device: "wan1", Up: true}, {ID: "isp2", Device: "wan2", Up: true}}
}
func TestNativeUplinkVLESSFixture(t *testing.T) { testsupport.ServeVLESS(t) }

func TestNativeSingboxFallback(t *testing.T) {
	if os.Getenv("UPLINK_SINGBOX_BINARY") == "" {
		t.Skip("isolated native runtime fixture only")
	}
	self, _ := os.Readlink("/proc/self/ns/net")
	host, _ := os.Readlink("/proc/1/ns/net")
	if self == "" || self == host {
		t.Fatal("private namespace required")
	}
	testsupport.TLSFixture(t)
	first, second := testsupport.StartVLESS(t, 1), testsupport.StartVLESS(t, 2)
	op := NewOperator(OperatorDeps{Dir: t.TempDir(), Binary: os.Getenv("UPLINK_SINGBOX_BINARY"), IsNDMSProxyEnabled: func() bool { return false }})
	t.Cleanup(op.Process().Close)
	orch := orchestrator.NewWithAppliedPath(op.ConfigDir(), op.Process(), op.dir+"/applied.json")
	t.Cleanup(orch.Close)
	for _, meta := range orchestrator.KnownSlots() {
		if meta.Slot != orchestrator.SlotBase && meta.Slot != orchestrator.SlotTunnels && meta.Slot != orchestrator.SlotRouter {
			continue
		}
		if meta.Slot == orchestrator.SlotTunnels {
			meta.HasContent = op.HasUserTunnels
		}
		if err := orch.Register(meta); err != nil {
			t.Fatal(err)
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	op.SetOrch(orch)
	t.Cleanup(func() { op.Process().Stop() })
	binding, err := uplink.InspectBinding("wan1")
	if err != nil {
		t.Fatal(err)
	}
	binding.DNSServers = []string{"9.9.9.9:5300"}
	raw, err := sbconfig.Apply(map[string]any{
		"outbounds": []any{map[string]any{"type": "vless", "tag": "managed", "server": "provider-specific.invalid", "server_port": 8443, "uuid": "8f7d3b1a-2c4e-4f60-9a81-b5d7e6c30124"}},
		"inbounds":  []any{map[string]any{"type": "socks", "tag": "managed-in", "listen": "127.0.0.1", "listen_port": 1088}},
		"route":     map[string]any{"rules": []any{map[string]any{"inbound": []any{"managed-in"}, "outbound": "managed"}}},
	}, []sbconfig.Spec{{Tag: "managed", Binding: binding}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	cfg := NewConfig()
	cfg.raw = raw
	if err := op.ApplyConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := op.orch.ReloadNow(); err != nil {
		t.Fatal(err)
	}
	if err := op.Start(); err != nil {
		t.Fatal(err)
	}
	adapter := &UplinkAdapter{Operator: op, Active: func() bool { return true }, DNSForDevice: func(context.Context, string) ([]string, error) { return []string{"9.9.9.9:5300"}, nil }}
	store, err := uplink.OpenStore(op.dir + "/uplink.json")
	if err != nil {
		t.Fatal(err)
	}
	controller := uplink.New(store, nativeUplinkResolver{}, map[string]uplink.Adapter{"sing-box": adapter})
	op.SetManagedUplinkConfig(func(ctx context.Context, doc map[string]any) (map[string]any, error) {
		return sbconfig.MaterializeOwned(ctx, doc, store.MaterializationPolicies(), controller.Providers(), adapter.DNSForDevice)
	})
	_, err = controller.Put(ctx, uplink.Policy{ID: "native-sb", Target: uplink.Target{Engine: "sing-box", Ref: "managed"}, Providers: []string{"isp1", "isp2"}, Enabled: true, IntervalSeconds: 5, TimeoutSeconds: 5, FailureThreshold: 1, HoldDownSeconds: 10, Failback: "automatic", RecoveryThreshold: 2, RecoveryIntervalSeconds: 5})
	if err != nil {
		files, _ := os.ReadDir(op.ConfigDir())
		for _, f := range files {
			b, _ := os.ReadFile(op.ConfigDir() + "/" + f.Name())
			var d map[string]any
			json.Unmarshal(b, &d)
			t.Logf("%s route=%v", f.Name(), d["route"])
		}
		t.Fatal(err)
	}
	controller.Tick(ctx)
	state := controller.List()[0].State
	if state.Phase != "healthy" || state.Provider != "isp1" || first() == 0 {
		t.Fatalf("initial: %+v requests=%d", state, first())
	}
	unblock := testsupport.BlockVLESS(t, "wan1")
	testsupport.AssertDirectWAN(t, "wan1", 1)
	baselineBlocked := first()
	time.Sleep(6 * time.Second)
	controller.Tick(ctx)
	state = controller.List()[0].State
	if state.Phase != "healthy" || state.Provider != "isp2" || second() == 0 || first() != baselineBlocked {
		t.Fatalf("selective VLESS drop: %+v requests=%d/%d", state, first(), second())
	}
	testsupport.AssertDirectWAN(t, "wan1", 1)
	unblock()
	policy := controller.List()[0].Policy
	if err := controller.Switch(ctx, policy.ID, "isp1", policy.Revision); err != nil {
		t.Fatal(err)
	}
	t.Log("kernel selective VLESS drop triggered fallback while direct WAN1 HTTP remained reachable")
	if out, err := exec.Command("ip", "link", "set", "wan1", "down").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	// The preceding manual switch starts the policy's ten-second hold-down.
	time.Sleep(11 * time.Second)
	controller.Tick(ctx)
	state = controller.List()[0].State
	if state.Phase != "healthy" || state.Provider != "isp2" || second() == 0 {
		t.Fatalf("fallback: %+v requests=%d/%d", state, first(), second())
	}
	t.Logf("native sing-box WAN2 healthy: %+v", state)
	// Failed isolated primary probes must not interrupt the live WAN2 process.
	_, pidBefore := op.IsRunning()
	time.Sleep(11 * time.Second)
	controller.Tick(ctx)
	_, pidAfter := op.IsRunning()
	if pidBefore != pidAfter || controller.List()[0].State.Provider != "isp2" {
		t.Fatal("candidate probe changed the live path")
	}
	if out, err := exec.Command("ip", "link", "set", "wan1", "up").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if out, err := exec.Command("ip", "-6", "addr", "replace", "fd44:1::1/64", "dev", "wan1", "nodad").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if out, err := exec.Command("ip", "-6", "route", "replace", "default", "via", "fd44:1::2", "dev", "wan1").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if out, err := exec.Command("ip", "route", "replace", "default", "via", "10.44.1.2", "dev", "wan1").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	baseline := first()
	time.Sleep(6 * time.Second)
	controller.Tick(ctx)
	if controller.List()[0].State.Provider != "isp2" || first() <= baseline {
		t.Fatal("first candidate probe failed or bypassed recovery threshold", controller.List()[0])
	}
	time.Sleep(6 * time.Second)
	controller.Tick(ctx)
	state = controller.List()[0].State
	if state.Phase != "healthy" || state.Provider != "isp1" {
		t.Fatalf("automatic return: %+v", state)
	}
	leftovers, _ := os.ReadDir(os.TempDir())
	for _, f := range leftovers {
		if len(f.Name()) >= 22 && f.Name()[:22] == "awgm-uplink-candidate-" {
			t.Fatal("candidate resources leaked", f.Name())
		}
	}
	t.Logf("native sing-box isolated automatic return passed: %+v", state)
	beforePolicy := controller.List()[0].Policy
	op.SetTargetRenameCoordinator(func(ctx context.Context, old, next string, apply func() error) error {
		return controller.RenameTarget(ctx, uplink.Target{Engine: "sing-box", Ref: old}, uplink.Target{Engine: "sing-box", Ref: next}, apply)
	})
	if err := op.RenameTunnel(ctx, "managed", "renamed"); err != nil {
		t.Fatal(err)
	}
	afterPolicy := controller.List()[0].Policy
	if afterPolicy.ID != beforePolicy.ID || afterPolicy.Revision != beforePolicy.Revision+1 || afterPolicy.Target.Ref != "renamed" {
		t.Fatal(afterPolicy)
	}
	controller.Tick(ctx)
	if state := controller.List()[0].State; state.Phase != "healthy" {
		t.Fatal("renamed path not verified", state)
	}
	reopened, err := uplink.OpenStore(op.dir + "/uplink.json")
	if err != nil {
		t.Fatal(err)
	}
	restarted := uplink.New(reopened, nativeUplinkResolver{}, map[string]uplink.Adapter{"sing-box": adapter})
	restarted.Tick(ctx)
	if state := restarted.List()[0].State; state.Phase != "healthy" || state.Provider != "isp1" {
		t.Fatal("restart lost binding", state)
	}
	t.Log("native rename, stable policy ID, revision and daemon restart reconciliation passed")
	first6 := testsupport.StartVLESSAddress(t, 1, "2001:db8:113::9")
	second6 := testsupport.StartVLESSAddress(t, 2, "2001:db8:113::9")
	config, err := op.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range config.raw["outbounds"].([]any) {
		ob, _ := v.(map[string]any)
		if ob["tag"] == "renamed" {
			ob["server"] = "2001:db8:113::9"
		}
	}
	if err := op.ApplyConfig(ctx, config); err != nil {
		t.Fatal(err)
	}
	if err := op.RestartForUplink(ctx); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Probe(ctx, uplink.Target{Engine: "sing-box", Ref: "renamed"}, "wan1"); err != nil {
		t.Fatal(err)
	}
	p := restarted.List()[0].Policy
	if err := restarted.Switch(ctx, p.ID, "isp2", p.Revision); err != nil {
		t.Fatal(err)
	}
	if first6() == 0 || second6() == 0 {
		t.Fatal("IPv6 transport did not traverse both providers", first6(), second6())
	}
	t.Log("native IPv6 outer transport and HTTPS payload on both providers passed")
}
