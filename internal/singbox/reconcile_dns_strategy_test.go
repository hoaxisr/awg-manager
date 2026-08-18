package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// reconcileBaseDNSStrategy — симметричное примирение dns.strategy базового
// слота с владением routing-слотов (20-router.json / 21-fakeip.json).
func TestReconcileBaseDNSStrategy(t *testing.T) {
	type slot struct {
		name string
		body string
	}
	cases := []struct {
		name      string
		base      string
		slots     []slot
		wantBase  any // ожидаемое значение dns.strategy; nil — ключа быть не должно
		wantNoDNS bool
	}{
		{
			name:  "владеет 20-router.json → strategy стрижётся из base",
			base:  `{"dns":{"strategy":"prefer_ipv4","servers":[{"tag":"dns-bootstrap","type":"udp","server":"1.1.1.1"}]}}`,
			slots: []slot{{"20-router.json", `{"dns":{"strategy":"ipv4_only"}}`}},
		},
		{
			name:  "владеет 21-fakeip.json → strategy стрижётся из base",
			base:  `{"dns":{"strategy":"prefer_ipv4","servers":[{"tag":"dns-bootstrap","type":"udp","server":"1.1.1.1"}]}}`,
			slots: []slot{{"21-fakeip.json", `{"dns":{"strategy":"ipv4_only"}}`}},
		},
		{
			name:     "никто не владеет, в base dns-блок без strategy → восстановлен prefer_ipv4",
			base:     `{"dns":{"servers":[{"tag":"dns-bootstrap","type":"udp","server":"1.1.1.1"}]}}`,
			wantBase: "prefer_ipv4",
		},
		{
			name:     "никто не владеет, strategy на месте → нетронута",
			base:     `{"dns":{"strategy":"prefer_ipv4"}}`,
			wantBase: "prefer_ipv4",
		},
		{
			name:     "никто не владеет, кастомная strategy → нетронута",
			base:     `{"dns":{"strategy":"ipv6_only"}}`,
			wantBase: "ipv6_only",
		},
		{
			name:      "dns-блока нет → no-op",
			base:      `{"route":{"default_domain_resolver":"dns-bootstrap"}}`,
			wantNoDNS: true,
		},
		{
			name:     "routing-слот с пустой strategy → не владелец, base сохраняет свою",
			base:     `{"dns":{"strategy":"prefer_ipv4"}}`,
			slots:    []slot{{"20-router.json", `{"dns":{"strategy":""}}`}},
			wantBase: "prefer_ipv4",
		},
		{
			name:     "routing-слот в disabled/ → не владелец",
			base:     `{"dns":{"strategy":"prefer_ipv4"}}`,
			slots:    []slot{{filepath.Join("disabled", "21-fakeip.json"), `{"dns":{"strategy":"ipv4_only"}}`}},
			wantBase: "prefer_ipv4",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			basePath := filepath.Join(dir, "00-base.json")
			if err := os.WriteFile(basePath, []byte(c.base), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, s := range c.slots {
				p := filepath.Join(dir, s.name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(s.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			reconcileBaseDNSStrategy(dir)

			m := readJSONMap(t, basePath)
			dns, _ := m["dns"].(map[string]any)
			if c.wantNoDNS {
				if dns != nil {
					t.Fatalf("dns-блок не должен материализоваться, got %v", dns)
				}
				return
			}
			if dns == nil {
				t.Fatalf("dns-блок пропал: %v", m)
			}
			got, has := dns["strategy"]
			if c.wantBase == nil {
				if has {
					t.Fatalf("strategy должна быть вычищена из base, got %v", got)
				}
				return
			}
			if got != c.wantBase {
				t.Fatalf("dns.strategy = %v, want %v", got, c.wantBase)
			}
		})
	}
}

// removeDNSFinalFromBase после Task 6 занимается ТОЛЬКО dns.final: strategy он
// не трогает ни при каком владении.
func TestRemoveDNSFinalFromBase_NeverTouchesStrategy(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "00-base.json")
	if err := os.WriteFile(basePath,
		[]byte(`{"dns":{"final":"dns-bootstrap","strategy":"prefer_ipv4"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"),
		[]byte(`{"dns":{"strategy":"ipv4_only"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	removeDNSFinalFromBase(basePath)

	dns, _ := readJSONMap(t, basePath)["dns"].(map[string]any)
	if _, has := dns["final"]; has {
		t.Fatalf("dns.final обязан стричься безусловно: %v", dns)
	}
	if dns["strategy"] != "prefer_ipv4" {
		t.Fatalf("strategy — забота reconcile-dns-strategy, а не remove-dns-final: %v", dns)
	}
}

// Рантайм-вариант шага: метод Operator пишет исцелённый base (orch == nil —
// прямая запись, как у ApplyLogLevel).
func TestOperatorReconcileBaseDNSStrategy_StripsWhenRoutingSlotOwns(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{Dir: dir})
	basePath := filepath.Join(op.ConfigDir(), "00-base.json")

	if err := os.WriteFile(filepath.Join(op.ConfigDir(), "20-router.json"),
		[]byte(`{"dns":{"strategy":"ipv4_only"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if dns, _ := readJSONMap(t, basePath)["dns"].(map[string]any); dns["strategy"] != "prefer_ipv4" {
		t.Fatalf("предусловие: свежий base обязан нести prefer_ipv4, got %v", dns)
	}

	if err := op.ReconcileBaseDNSStrategy(); err != nil {
		t.Fatalf("ReconcileBaseDNSStrategy: %v", err)
	}

	dns, _ := readJSONMap(t, basePath)["dns"].(map[string]any)
	if _, has := dns["strategy"]; has {
		t.Fatalf("strategy обязана уйти из base, когда ею владеет routing-слот: %v", dns)
	}
}

func TestOperatorReconcileBaseDNSStrategy_RestoresWhenNobodyOwns(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{Dir: dir})
	basePath := filepath.Join(op.ConfigDir(), "00-base.json")

	m := readJSONMap(t, basePath)
	dns, _ := m["dns"].(map[string]any)
	delete(dns, "strategy")
	if err := writeJSONFile(basePath, m); err != nil {
		t.Fatal(err)
	}

	if err := op.ReconcileBaseDNSStrategy(); err != nil {
		t.Fatalf("ReconcileBaseDNSStrategy: %v", err)
	}

	dns, _ = readJSONMap(t, basePath)["dns"].(map[string]any)
	if dns["strategy"] != "prefer_ipv4" {
		t.Fatalf("без владельца base обязан вернуть prefer_ipv4, got %v", dns)
	}
}

// --- перенесено из operator_test.go (кейсы #445, ни один не потерян):
// removeDNSFinalFromBase больше не трогает strategy, поэтому оба кейса
// прогоняются в паре с новым шагом reconcile-dns-strategy.

func TestReconcileDNSStrategy_StripsStrategyWhenRouterOwnsIt(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "00-base.json")
	if err := os.WriteFile(basePath,
		[]byte(`{"dns":{"final":"dns-bootstrap","strategy":"prefer_ipv4","servers":[{"tag":"dns-bootstrap","type":"udp","server":"1.1.1.1"}]}}`),
		0644); err != nil {
		t.Fatal(err)
	}
	// Router slot sets a non-empty strategy → base strategy strip is enabled.
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"),
		[]byte(`{"dns":{"final":"dns-direct","strategy":"ipv4_only","servers":[{"tag":"dns-direct","type":"udp","server":"8.8.8.8"}]}}`),
		0644); err != nil {
		t.Fatal(err)
	}

	removeDNSFinalFromBase(basePath)
	reconcileBaseDNSStrategy(dir)

	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	dns, _ := m["dns"].(map[string]any)
	if _, has := dns["final"]; has {
		t.Errorf("dns.final should be stripped")
	}
	if _, has := dns["strategy"]; has {
		t.Errorf("dns.strategy should be stripped when router owns it, got %v", dns["strategy"])
	}
}

func TestReconcileDNSStrategy_RouterStrategyEmpty_KeepsBaseStrategy(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "00-base.json")
	if err := os.WriteFile(basePath,
		[]byte(`{"dns":{"final":"dns-bootstrap","strategy":"prefer_ipv4"}}`),
		0644); err != nil {
		t.Fatal(err)
	}
	// Router slot exists but strategy is empty → base keeps its strategy.
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"),
		[]byte(`{"dns":{"final":"dns-direct","strategy":"","servers":[{"tag":"dns-direct","type":"udp","server":"8.8.8.8"}]}}`),
		0644); err != nil {
		t.Fatal(err)
	}

	removeDNSFinalFromBase(basePath)
	reconcileBaseDNSStrategy(dir)

	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	dns, _ := m["dns"].(map[string]any)
	if _, has := dns["final"]; has {
		t.Errorf("dns.final should still be stripped")
	}
	if dns["strategy"] != "prefer_ipv4" {
		t.Errorf("dns.strategy must survive when router strategy empty, got %v", dns["strategy"])
	}
}
