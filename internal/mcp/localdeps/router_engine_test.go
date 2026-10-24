package localdeps

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakeSlots is the 90-user.json slot of the orchestrator.
type fakeSlots struct {
	effective []byte
	draft     []byte
	enabled   bool
	checkRes  singboxorch.ValidationResult
	checkErr  error
	applyRes  singboxorch.ValidationResult
	applyErr  error
	enableErr error
	enabledTo []bool
}

func (f *fakeSlots) Snapshot() []singboxorch.SlotState {
	return []singboxorch.SlotState{{Slot: singboxorch.SlotUser, Enabled: f.enabled}}
}
func (f *fakeSlots) LoadEffective(singboxorch.Slot) ([]byte, error) {
	if f.draft != nil {
		return f.draft, nil
	}
	return f.effective, nil
}
func (f *fakeSlots) HasDraft(singboxorch.Slot) bool { return f.draft != nil }
func (f *fakeSlots) SaveDraft(_ singboxorch.Slot, b []byte) error {
	f.draft = b
	return nil
}
func (f *fakeSlots) CheckMerged(singboxorch.Slot, []byte) (singboxorch.ValidationResult, error) {
	return f.checkRes, f.checkErr
}
func (f *fakeSlots) ApplyDraft(singboxorch.Slot) (singboxorch.ValidationResult, error) {
	return f.applyRes, f.applyErr
}
func (f *fakeSlots) DiscardDraft(singboxorch.Slot) error { f.draft = nil; return nil }
func (f *fakeSlots) SetEnabled(_ singboxorch.Slot, on bool) error {
	f.enabledTo = append(f.enabledTo, on)
	return f.enableErr
}

// TestLocal_UpdateSingboxRouterSettingsKeepsTheRest — UpdateSettings берёт
// структуру целиком: поля, которых не касались, обязаны дойти такими, какие
// они сохранены.
func TestLocal_UpdateSingboxRouterSettingsKeepsTheRest(t *testing.T) {
	fr := routerHarness()
	fr.settings = storage.SingboxRouterSettings{Enabled: true, RoutingMode: "tproxy", PolicyName: "Policy1", UDPTimeout: "5m", BypassGeoIPTags: []string{"ru"}, FakeIPPool6: "fd00::/8"}
	l := New(Config{Router: fr})

	if _, err := l.UpdateSingboxRouterSettings(context.Background(), mcpsrv.SingboxRouterSettingsPatch{Sniffer: ptr(true), BypassPorts: ptr(" 5060 ")}); err != nil {
		t.Fatal(err)
	}
	s := fr.settings
	if !s.SnifferEnabled || s.BypassExtraPorts != "5060" || s.PolicyName != "Policy1" || s.UDPTimeout != "5m" || s.RoutingMode != "tproxy" || len(s.BypassGeoIPTags) != 1 || s.FakeIPPool6 != "fd00::/8" {
		t.Fatalf("settings = %+v", s)
	}
	if _, err := l.UpdateSingboxRouterSettings(context.Background(), mcpsrv.SingboxRouterSettingsPatch{
		UDPTimeout: ptr(" 10m "), BypassSubnets: ptr(" 10.0.0.0/8 "), PolicyName: ptr(" Policy2 "), DeviceMode: ptr(" all "),
	}); err != nil {
		t.Fatal(err)
	}
	s = fr.settings
	if s.UDPTimeout != "10m" || s.BypassExtraSubnets != "10.0.0.0/8" || s.PolicyName != "Policy2" || s.DeviceMode != "all" || !s.SnifferEnabled || s.BypassExtraPorts != "5060" {
		t.Fatalf("every field of the patch lands, trimmed, and the rest stays: %+v", s)
	}
}

func TestLocal_SwitchSingboxRouterModeRunsInTheBackground(t *testing.T) {
	fr := routerHarness()
	fr.switched = make(chan string, 1)
	l := New(Config{Router: fr})
	if err := l.SwitchSingboxRouterMode(context.Background(), "fakeip-tun"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-fr.switched:
		if got != "fakeip-tun" {
			t.Fatalf("mode = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the switch was not started")
	}
}

// TestLocal_SingboxUserConfig — черновик сохраняется и проверяется;
// применение с провалом проверки ничего не включает; удачное применение
// включает слот после apply, как редактор.
func TestLocal_SingboxUserConfig(t *testing.T) {
	fs := &fakeSlots{effective: []byte(`{"outbounds":[]}`), enabled: true}
	l := New(Config{UserConfig: fs})
	ctx := context.Background()

	got, err := l.GetSingboxUserConfig(ctx)
	if err != nil || got.Content != `{"outbounds":[]}` || got.HasDraft || !got.Enabled {
		t.Fatalf("got %+v, %v", got, err)
	}
	fs.checkRes = singboxorch.ValidationResult{Errors: []singboxorch.ValidationError{{Kind: "unknown-outbound", Message: "unknown outbound x"}, {Severity: singboxorch.SeverityWarning, Message: "dns final"}}}
	check, err := l.SaveSingboxUserConfig(ctx, []byte(`{"route":{"final":"x"}}`))
	if err != nil || check.Ok || len(check.Errors) != 1 || len(check.Warnings) != 1 || string(fs.draft) != `{"route":{"final":"x"}}` {
		t.Fatalf("check = %+v, %v", check, err)
	}

	fs.applyRes = fs.checkRes
	check, err = l.ApplySingboxUserConfig(ctx)
	if err != nil || check.Ok || len(fs.enabledTo) != 0 {
		t.Fatalf("a refused apply must enable nothing: %+v, %v, %v", check, err, fs.enabledTo)
	}
	fs.applyRes, fs.applyErr = singboxorch.ValidationResult{}, errors.New("sing-box check: FATAL")
	if check, err = l.ApplySingboxUserConfig(ctx); err != nil || check.Ok || !strings.Contains(check.Errors[0], "FATAL") {
		t.Fatalf("a sing-box refusal is a failed check: %+v, %v", check, err)
	}
	fs.applyErr = nil
	if check, err = l.ApplySingboxUserConfig(ctx); err != nil || !check.Ok || len(fs.enabledTo) != 1 || !fs.enabledTo[0] || len(check.Warnings) != 0 {
		t.Fatalf("a good apply enables the slot: %+v, %v, %v", check, err, fs.enabledTo)
	}
	// A rule mixing a rule_set with its own addresses is flagged on save and
	// on apply, as the editor's check and apply do (#699).
	mixed := []byte(`{"route":{"rules":[{"rule_set":["geosite-x"],"ip_cidr":["10.0.0.0/8"],"outbound":"vpn"}]}}`)
	fs.checkRes = singboxorch.ValidationResult{}
	if check, err = l.SaveSingboxUserConfig(ctx, mixed); err != nil || len(check.Warnings) != 1 || !strings.Contains(check.Warnings[0], "route.rules[0]") {
		t.Fatalf("save must flag the rule: %+v, %v", check, err)
	}
	if check, err = l.ApplySingboxUserConfig(ctx); err != nil || len(check.Warnings) != 1 {
		t.Fatalf("apply must flag the rule of the draft it applied: %+v, %v", check, err)
	}
	fs.enableErr = errors.New("slot busy")
	if _, err = l.ApplySingboxUserConfig(ctx); err == nil || !strings.Contains(err.Error(), "could not be enabled") {
		t.Fatalf("applied but not enabled must say so: %v", err)
	}
	fs.enableErr, fs.applyErr = nil, singboxorch.ErrNoDraft
	if _, err = l.ApplySingboxUserConfig(ctx); err == nil || !strings.Contains(err.Error(), "no draft") {
		t.Fatalf("a draft gone before the commit is no draft, not a failed check: %v", err)
	}
	fs.applyErr, fs.checkErr = nil, errors.New("merge: bad json")
	if check, err = l.SaveSingboxUserConfig(ctx, []byte(`{}`)); err != nil || check.Ok || len(check.Errors) != 1 || !strings.Contains(check.Errors[0], "bad json") {
		t.Fatalf("a check that could not run is a failed check: %+v, %v", check, err)
	}
	fs.checkErr = nil
	if err := l.DiscardSingboxUserConfig(ctx); err != nil || fs.draft != nil {
		t.Fatal("discard must drop the draft")
	}
	if _, err := l.ApplySingboxUserConfig(ctx); err == nil {
		t.Fatal("no draft, nothing to apply")
	}

	fs.effective = []byte(strings.Repeat("x", maxUserConfigOut+10))
	if got, _ := l.GetSingboxUserConfig(ctx); !got.Truncated || len(got.Content) != maxUserConfigOut {
		t.Fatalf("a long slot must be cut and say so: %d %v", len(got.Content), got.Truncated)
	}
}
