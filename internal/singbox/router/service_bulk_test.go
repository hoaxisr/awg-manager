package router

import (
	"errors"
	"strings"
	"testing"
)

func knownAllBut(unknown ...string) func(string) bool {
	blocked := make(map[string]bool, len(unknown))
	for _, t := range unknown {
		blocked[t] = true
	}
	return func(tag string) bool { return !blocked[tag] }
}

// --- bulkSetRuleOutbound ---

func TestBulkSetRuleOutbound_Valid(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{
		{Domain: []string{"a.com"}, Action: "route", Outbound: "old"},
		{Domain: []string{"b.com"}, Action: "route", Outbound: "old"},
		{Domain: []string{"c.com"}, Action: "route", Outbound: "untouched"},
	}
	if err := bulkSetRuleOutbound(cfg, []int{0, 1}, "new", knownAllBut()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Route.Rules[0].Outbound != "new" || cfg.Route.Rules[1].Outbound != "new" {
		t.Fatalf("outbound not updated: %+v", cfg.Route.Rules)
	}
	if cfg.Route.Rules[2].Outbound != "untouched" {
		t.Fatalf("unselected rule was touched: %+v", cfg.Route.Rules[2])
	}
}

func TestBulkSetRuleOutbound_EmptyIndices(t *testing.T) {
	cfg := NewEmptyConfig()
	err := bulkSetRuleOutbound(cfg, []int{}, "direct", knownAllBut())
	if err == nil || !strings.Contains(err.Error(), "empty indices") {
		t.Fatalf("expected empty indices error, got %v", err)
	}
}

func TestBulkSetRuleOutbound_DuplicateIndex(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{
		{Domain: []string{"a.com"}, Action: "route", Outbound: "old"},
	}
	err := bulkSetRuleOutbound(cfg, []int{0, 0}, "direct", knownAllBut())
	if !errors.Is(err, ErrBulkInvalidSelection) || !strings.Contains(err.Error(), "0") {
		t.Fatalf("expected ErrBulkInvalidSelection duplicate index error, got %v", err)
	}
}

func TestBulkSetRuleOutbound_IndexOutOfRange(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{
		{Domain: []string{"a.com"}, Action: "route", Outbound: "old"},
	}
	err := bulkSetRuleOutbound(cfg, []int{5}, "direct", knownAllBut())
	if !errors.Is(err, ErrRuleIndexOutOfRange) {
		t.Fatalf("expected ErrRuleIndexOutOfRange, got %v", err)
	}
}

func TestBulkSetRuleOutbound_NonRouteAction(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{
		{Domain: []string{"a.com"}, Action: "route", Outbound: "old"},
		{Domain: []string{"b.com"}, Action: "reject"},
		{Action: "sniff"},
	}
	if err := bulkSetRuleOutbound(cfg, []int{1}, "direct", knownAllBut()); !errors.Is(err, ErrBulkInvalidSelection) || !strings.Contains(err.Error(), "1") {
		t.Fatalf("expected ErrBulkInvalidSelection naming index 1 for reject rule, got %v", err)
	}
	if err := bulkSetRuleOutbound(cfg, []int{2}, "direct", knownAllBut()); !errors.Is(err, ErrBulkInvalidSelection) || !strings.Contains(err.Error(), "2") {
		t.Fatalf("expected ErrBulkInvalidSelection naming index 2 for sniff rule, got %v", err)
	}
}

// Системный префикс режима лежит в режимном слоте, а bulk правит общий: правило
// «ip_is_private → выход» в общем слоте — пользовательское (миграция намеренно
// оставляет такие на месте), и массовая смена outbound'а его касается.
func TestBulkSetRuleOutbound_UserPrivateRule(t *testing.T) {
	cfg := NewEmptyConfig()
	private := true
	cfg.Route.Rules = []Rule{
		{Domain: []string{"a.com"}, Action: "route", Outbound: "old"},
		{Action: "route", IPIsPrivate: &private, Outbound: "old"},
	}
	if err := bulkSetRuleOutbound(cfg, []int{1}, "direct", knownAllBut()); err != nil {
		t.Fatalf("bulkSetRuleOutbound: %v", err)
	}
	if cfg.Route.Rules[1].Outbound != "direct" {
		t.Fatalf("Outbound = %q, want direct: %+v", cfg.Route.Rules[1].Outbound, cfg.Route.Rules[1])
	}
}

func TestBulkSetRuleOutbound_UnknownOutbound(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{
		{Domain: []string{"a.com"}, Action: "route", Outbound: "old"},
	}
	err := bulkSetRuleOutbound(cfg, []int{0}, "ghost", knownAllBut("ghost"))
	if !errors.Is(err, ErrBulkInvalidSelection) || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected ErrBulkInvalidSelection unknown outbound error, got %v", err)
	}
}

func TestBulkSetRuleOutbound_AtomicOnPartialFailure(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{
		{Domain: []string{"a.com"}, Action: "route", Outbound: "old"},
		{Domain: []string{"b.com"}, Action: "route", Outbound: "old"},
	}
	err := bulkSetRuleOutbound(cfg, []int{0, 99}, "new", knownAllBut())
	if !errors.Is(err, ErrRuleIndexOutOfRange) {
		t.Fatalf("expected ErrRuleIndexOutOfRange, got %v", err)
	}
	if cfg.Route.Rules[0].Outbound != "old" || cfg.Route.Rules[1].Outbound != "old" {
		t.Fatalf("config mutated despite invalid element: %+v", cfg.Route.Rules)
	}
}

// --- bulkSetRuleSetDetour ---

func TestBulkSetRuleSetDetour_Valid(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{
		{Tag: "geosite-a", Type: "remote", DownloadDetour: "old"},
		{Tag: "geosite-b", Type: "remote", DownloadDetour: "old"},
		{Tag: "geosite-c", Type: "remote", DownloadDetour: "untouched"},
	}
	if err := bulkSetRuleSetDetour(cfg, []string{"geosite-a", "geosite-b"}, "new", knownAllBut()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Route.RuleSet[0].DownloadDetour != "new" || cfg.Route.RuleSet[1].DownloadDetour != "new" {
		t.Fatalf("detour not updated: %+v", cfg.Route.RuleSet)
	}
	if cfg.Route.RuleSet[2].DownloadDetour != "untouched" {
		t.Fatalf("unselected rule set was touched: %+v", cfg.Route.RuleSet[2])
	}
}

func TestBulkSetRuleSetDetour_ClearsWithEmptyDetour(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{
		{Tag: "geosite-a", Type: "remote", DownloadDetour: "old"},
	}
	// known() must not be consulted for an empty detour — it always returns
	// false here to prove that.
	if err := bulkSetRuleSetDetour(cfg, []string{"geosite-a"}, "", knownAllBut("")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Route.RuleSet[0].DownloadDetour != "" {
		t.Fatalf("detour not cleared: %+v", cfg.Route.RuleSet[0])
	}
}

func TestBulkSetRuleSetDetour_EmptyTags(t *testing.T) {
	cfg := NewEmptyConfig()
	err := bulkSetRuleSetDetour(cfg, []string{}, "direct", knownAllBut())
	if err == nil || !strings.Contains(err.Error(), "empty tags") {
		t.Fatalf("expected empty tags error, got %v", err)
	}
}

func TestBulkSetRuleSetDetour_DuplicateTag(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{Tag: "geosite-a", Type: "remote"}}
	err := bulkSetRuleSetDetour(cfg, []string{"geosite-a", "geosite-a"}, "direct", knownAllBut())
	if !errors.Is(err, ErrBulkInvalidSelection) || !strings.Contains(err.Error(), "geosite-a") {
		t.Fatalf("expected ErrBulkInvalidSelection duplicate tag error, got %v", err)
	}
}

func TestBulkSetRuleSetDetour_TagNotFound(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{Tag: "geosite-a", Type: "remote"}}
	err := bulkSetRuleSetDetour(cfg, []string{"missing"}, "direct", knownAllBut())
	if !errors.Is(err, ErrRuleSetNotFound) {
		t.Fatalf("expected ErrRuleSetNotFound, got %v", err)
	}
}

func TestBulkSetRuleSetDetour_NonRemoteType(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{
		{Tag: "local-a", Type: "local"},
		{Tag: "inline-a", Type: "inline", Rules: []map[string]any{{"domain": "x"}}},
	}
	if err := bulkSetRuleSetDetour(cfg, []string{"local-a"}, "direct", knownAllBut()); !errors.Is(err, ErrBulkInvalidSelection) || !strings.Contains(err.Error(), "local-a") {
		t.Fatalf("expected ErrBulkInvalidSelection naming tag local-a, got %v", err)
	}
	if err := bulkSetRuleSetDetour(cfg, []string{"inline-a"}, "direct", knownAllBut()); !errors.Is(err, ErrBulkInvalidSelection) || !strings.Contains(err.Error(), "inline-a") {
		t.Fatalf("expected ErrBulkInvalidSelection naming tag inline-a, got %v", err)
	}
}

func TestBulkSetRuleSetDetour_UnknownDetourTag(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{Tag: "geosite-a", Type: "remote"}}
	err := bulkSetRuleSetDetour(cfg, []string{"geosite-a"}, "ghost", knownAllBut("ghost"))
	if !errors.Is(err, ErrBulkInvalidSelection) || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected ErrBulkInvalidSelection unknown outbound error, got %v", err)
	}
}

func TestBulkSetRuleSetDetour_AtomicOnPartialFailure(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{
		{Tag: "geosite-a", Type: "remote", DownloadDetour: "old"},
		{Tag: "geosite-b", Type: "remote", DownloadDetour: "old"},
	}
	err := bulkSetRuleSetDetour(cfg, []string{"geosite-a", "missing"}, "new", knownAllBut())
	if !errors.Is(err, ErrRuleSetNotFound) {
		t.Fatalf("expected ErrRuleSetNotFound, got %v", err)
	}
	if cfg.Route.RuleSet[0].DownloadDetour != "old" || cfg.Route.RuleSet[1].DownloadDetour != "old" {
		t.Fatalf("config mutated despite invalid element: %+v", cfg.Route.RuleSet)
	}
}
