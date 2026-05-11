// internal/singbox/integration_test.go
package singbox

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestIntegration_ParseAddValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Parse 3 different links
	links := []string{
		"vless://uuid-1@de.tld:443?security=reality&type=grpc&pbk=pbk1&sni=google.com&fp=chrome#Germany",
		"hy2://pw@fi.tld:8443?sni=fi.tld#Finland",
		"naive+https://u:p@jp.tld:443#Japan",
	}
	cfg := NewConfig()
	for _, link := range links {
		p, err := vlink.ParseLink(link)
		if err != nil {
			t.Fatalf("parse %s: %v", link, err)
		}
		if err := cfg.AddTunnel(p.Tag, p.Protocol, p.Server, int(p.Port), p.Outbound); err != nil {
			t.Fatal(err)
		}
	}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	tunnels := cfg.Tunnels()
	if len(tunnels) != 3 {
		t.Fatalf("tunnel count: %d", len(tunnels))
	}

	// Ports should be 1080, 1081, 1082
	ports := []int{tunnels[0].ListenPort, tunnels[1].ListenPort, tunnels[2].ListenPort}
	for i, want := range []int{1080, 1081, 1082} {
		if ports[i] != want {
			t.Errorf("port[%d]=%d want %d", i, ports[i], want)
		}
	}

	// Remove middle, then add another — port 1081 should be reused
	if err := cfg.RemoveTunnel("Finland"); err != nil {
		t.Fatal(err)
	}
	p, _ := vlink.ParseLink("vless://u@nl.tld:443#Netherlands")
	cfg.AddTunnel(p.Tag, p.Protocol, p.Server, int(p.Port), p.Outbound)
	var nl TunnelInfo
	for _, ti := range cfg.Tunnels() {
		if ti.Tag == "Netherlands" {
			nl = ti
		}
	}
	if nl.ListenPort != 1081 {
		t.Errorf("port reuse: got %d, want 1081", nl.ListenPort)
	}
	// ProxyInterface must be derived from listen_port, not iteration index
	if nl.ProxyInterface != "Proxy1" {
		t.Errorf("Netherlands ProxyInterface=%q, want Proxy1 (port 1081 = slot 1)", nl.ProxyInterface)
	}
	var japan TunnelInfo
	for _, ti := range cfg.Tunnels() {
		if ti.Tag == "Japan" {
			japan = ti
		}
	}
	if japan.ProxyInterface != "Proxy2" {
		t.Errorf("Japan ProxyInterface=%q, want Proxy2 (must remain stable after Finland removal)", japan.ProxyInterface)
	}

	// Validate with mock exec (real sing-box not available in CI)
	v := &Validator{
		binary: "sing-box",
		exec: func(bin string, args ...string) ([]byte, error) {
			// Check that the last arg is the absolute path to our config
			if len(args) != 3 {
				t.Errorf("args len: %v", args)
			}
			if args[2] != path {
				t.Errorf("path arg: %s, want %s", args[2], path)
			}
			_, err := os.Stat(path)
			return nil, err
		},
	}
	if err := v.Validate(path); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Shared helpers for staging integration tests
// ---------------------------------------------------------------------------

// integrationProc is a minimal ProcessController that records lifecycle calls.
type integrationProc struct {
	mu      sync.Mutex
	running bool
	starts  int
	reloads int
}

func (p *integrationProc) IsRunning() (bool, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return true, 1
	}
	return false, 0
}

func (p *integrationProc) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts++
	p.running = true
	return nil
}

func (p *integrationProc) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = false
	return nil
}

func (p *integrationProc) Reload() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reloads++
	return nil
}

func (p *integrationProc) startedOrReloaded() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.starts + p.reloads
}

// integrationSingbox is the minimal SingboxController needed by router.Deps.
type integrationSingbox struct {
	dir string
}

func (s *integrationSingbox) Reload() error                              { return nil }
func (s *integrationSingbox) IsRunning() (bool, int)                    { return false, 0 }
func (s *integrationSingbox) Start() error                              { return nil }
func (s *integrationSingbox) ValidateConfigDir(_ context.Context) error { return nil }
func (s *integrationSingbox) ConfigDir() string                         { return s.dir }
func (s *integrationSingbox) Binary() string                            { return "" }

// integrationBus captures Publish calls for assertions.
type integrationBus struct {
	mu     sync.Mutex
	events []string
}

func (b *integrationBus) Publish(event string, _ any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
}

// integrationEnv wires all pieces together.
type integrationEnv struct {
	svc       *router.ServiceImpl
	orch      *orchestrator.Orchestrator
	proc      *integrationProc
	bus       *integrationBus
	configDir string
}

// newIntegrationEnv sets up a router ServiceImpl backed by a real orchestrator
// and a fake ProcessController, all rooted in t.TempDir(). SlotBase and
// SlotRouter are registered; SlotBase gets a seeded 00-base.json declaring a
// "direct" outbound so cross-slot validation accepts references to it.
func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()
	dir := t.TempDir()

	proc := &integrationProc{running: true} // already running so ApplyStaging triggers Reload not Start
	orch := orchestrator.New(dir, proc)

	if err := orch.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotBase,
		Filename: "00-base.json",
		AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register SlotBase: %v", err)
	}
	if err := orch.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotRouter,
		Filename: "20-router.json",
	}); err != nil {
		t.Fatalf("register SlotRouter: %v", err)
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("orch.Bootstrap: %v", err)
	}

	// Seed 00-base.json with a "direct" outbound so cross-slot validation
	// accepts route rules that reference it.
	if err := orch.Save(orchestrator.SlotBase,
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`)); err != nil {
		t.Fatalf("seed base: %v", err)
	}

	// Enable SlotRouter so ApplyDraft promotes pending → active (not disabled/).
	if err := orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
		t.Fatalf("enable SlotRouter: %v", err)
	}

	settingsDir := t.TempDir()
	settings := storage.NewSettingsStore(settingsDir)

	bus := &integrationBus{}

	svc := router.NewService(router.Deps{
		Settings: settings,
		Singbox:  &integrationSingbox{dir: dir},
		Orch:     orch,
		Bus:      bus,
	})

	return &integrationEnv{
		svc:       svc,
		orch:      orch,
		proc:      proc,
		bus:       bus,
		configDir: dir,
	}
}

// ---------------------------------------------------------------------------
// TestStaging_FullCycle
// ---------------------------------------------------------------------------

func TestStaging_FullCycle(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx := context.Background()

	// 1. Stage a rule referencing "direct" (sing-box builtin — always valid).
	// AddRule calls withConfig → persistConfig → SaveDraft, so pending/ is
	// written without touching the active path.
	rule := router.Rule{
		Action:       "route",
		Outbound:     "direct",
		DomainSuffix: []string{"example.com"},
	}
	if err := env.svc.AddRule(ctx, rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	// 2. Pending file must exist; active must not.
	pending := filepath.Join(env.configDir, "pending", "20-router.json")
	if _, err := os.Stat(pending); err != nil {
		t.Fatalf("pending file missing after AddRule: %v", err)
	}
	active := filepath.Join(env.configDir, "20-router.json")
	if _, err := os.Stat(active); !os.IsNotExist(err) {
		t.Fatalf("active file should not exist after staging only: %v", err)
	}

	// 3. Load via service — must return staged content (LoadEffective prefers pending/).
	rules, err := env.svc.ListRules(ctx)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 || rules[0].Outbound != "direct" {
		t.Errorf("staged rules unexpected: %+v", rules)
	}

	// 4. Apply staging.
	res, err := env.svc.ApplyStaging(ctx)
	if err != nil {
		t.Fatalf("ApplyStaging error: %v", err)
	}
	if !res.Ok() {
		t.Fatalf("ApplyStaging validation failed: %s", res.Error())
	}

	// 5. Pending gone, active present.
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Errorf("pending file should be gone after apply: %v", err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("active file missing after apply: %v", err)
	}

	// 6. Active file must contain the rule.
	appliedCfg, err := router.LoadConfig(active)
	if err != nil {
		t.Fatalf("load active config: %v", err)
	}
	if len(appliedCfg.Route.Rules) != 1 || appliedCfg.Route.Rules[0].Outbound != "direct" {
		t.Errorf("active config rule unexpected: %+v", appliedCfg.Route.Rules)
	}

	// 7. Wait past debounce window; the orchestrator must have called
	// Reload (proc was already running, so it SIGHUP's rather than starts).
	time.Sleep(400 * time.Millisecond)
	if env.proc.startedOrReloaded() == 0 {
		t.Error("expected at least one Reload/Start from orchestrator debounce after apply, got none")
	}
}

// ---------------------------------------------------------------------------
// TestStaging_ValidationFailRetains
// ---------------------------------------------------------------------------

func TestStaging_ValidationFailRetains(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx := context.Background()

	// 1. Stage a rule referencing a non-existent outbound. validateRule does not
	// check the outbound tag string, so AddRule accepts it; the cross-slot
	// check only runs at ApplyStaging time.
	if err := env.svc.AddRule(ctx, router.Rule{
		Action:       "route",
		Outbound:     "ghost-outbound",
		DomainSuffix: []string{"example.com"},
	}); err != nil {
		t.Fatalf("AddRule (ghost): %v", err)
	}

	// 2. Apply — transport-level succeeds, validation must fail.
	res, err := env.svc.ApplyStaging(ctx)
	if err != nil {
		t.Fatalf("ApplyStaging returned unexpected error: %v", err)
	}
	if res.Ok() {
		t.Fatal("ApplyStaging should report validation failure for unknown outbound")
	}

	// 3. Must have at least one unknown-outbound error for "ghost-outbound".
	found := false
	for _, e := range res.Errors {
		if e.Kind == "unknown-outbound" && e.Tag == "ghost-outbound" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected unknown-outbound error for ghost-outbound; got: %v", res.Errors)
	}

	// 4. Pending must still exist (draft preserved for further editing).
	pending := filepath.Join(env.configDir, "pending", "20-router.json")
	if _, err := os.Stat(pending); err != nil {
		t.Fatalf("pending file should still exist after validation failure: %v", err)
	}

	// 5. Active must NOT exist (no commit happened).
	active := filepath.Join(env.configDir, "20-router.json")
	if _, err := os.Stat(active); !os.IsNotExist(err) {
		t.Errorf("active file must not exist after validation failure: stat: %v", err)
	}

	// 6. Fix: overwrite the staged rule with a valid outbound.
	// UpdateRule writes a new draft over the failed one.
	if err := env.svc.UpdateRule(ctx, 0, router.Rule{
		Action:       "route",
		Outbound:     "direct",
		DomainSuffix: []string{"example.com"},
	}); err != nil {
		t.Fatalf("UpdateRule (fix): %v", err)
	}

	// 7. Apply again — must succeed.
	res2, err := env.svc.ApplyStaging(ctx)
	if err != nil {
		t.Fatalf("ApplyStaging (fixed) error: %v", err)
	}
	if !res2.Ok() {
		t.Fatalf("ApplyStaging (fixed) should succeed; got: %s", res2.Error())
	}

	// 8. Pending gone after successful apply.
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Errorf("pending file should be gone after successful apply: %v", err)
	}
}
