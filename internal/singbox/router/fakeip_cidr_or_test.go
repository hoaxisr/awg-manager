package router

import (
	"reflect"
	"testing"
)

// Нормализованная форма «пресет ИЛИ свои адреса» (normalizeAddressOrRule)
// строго безопаснее плоской: каждая ветка совпадает сама по себе, поэтому
// пакет по любому из CIDR гарантированно уходит в прокси и не может
// вернуться в tun через route.final. Гейт мержимости здесь не нужен.
func TestDesiredTunCIDRs_NormalizedAddressOrRule(t *testing.T) {
	mixed := RuleSet{Tag: "mixed", Type: "inline", Rules: []map[string]any{
		{"domain_suffix": []any{"example.com"}},
		{"ip_cidr": []any{"198.51.100.0/24"}},
	}}

	tests := []struct {
		name     string
		rules    []Rule
		ruleSets []RuleSet
		wantV4   []string
	}{
		{
			// Ровно случай #699: до нормализации это правило не давало ни одного
			// маршрута (набор не mergeable), после — даёт обе стороны.
			name: "or{НЕ-mergeable набор | свой ip_cidr} → маршруты с обеих сторон",
			rules: []Rule{normalizeAddressOrRule(Rule{
				Action: "route", Outbound: "proxy",
				RuleSet: []string{"mixed"}, IPCIDR: []string{"203.0.113.0/24"},
			})},
			ruleSets: []RuleSet{mixed},
			wantV4:   []string{"203.0.113.0/24", "198.51.100.0/24"},
		},
		{
			name: "or{набор | свой домен} → CIDR только из набора",
			rules: []Rule{normalizeAddressOrRule(Rule{
				Action: "route", Outbound: "proxy",
				RuleSet: []string{"mixed"}, DomainSuffix: []string{"a.com"},
			})},
			ruleSets: []RuleSet{mixed},
			wantV4:   []string{"198.51.100.0/24"},
		},
		{
			// Сужающий матчер поднимает всю конструкцию в logical(and): пакет по
			// IP может не пройти порт → маршрут был бы петлёй.
			name: "and[порт, or{...}] → маршрутов нет",
			rules: []Rule{normalizeAddressOrRule(Rule{
				Action: "route", Outbound: "proxy", Port: []int{443},
				RuleSet: []string{"mixed"}, IPCIDR: []string{"203.0.113.0/24"},
			})},
			ruleSets: []RuleSet{mixed},
			wantV4:   nil,
		},
		{
			name: "or{...} с direct-outbound → маршрутов нет",
			rules: []Rule{normalizeAddressOrRule(Rule{
				Action: "route", Outbound: "direct",
				RuleSet: []string{"mixed"}, IPCIDR: []string{"203.0.113.0/24"},
			})},
			ruleSets: []RuleSet{mixed},
			wantV4:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &RouterConfig{Route: Route{Rules: tt.rules, RuleSet: tt.ruleSets}}
			gotV4, _ := desiredTunCIDRs(cfg)
			if !reflect.DeepEqual(gotV4, tt.wantV4) {
				t.Errorf("v4 = %v, want %v", gotV4, tt.wantV4)
			}
		})
	}
}
