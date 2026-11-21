package firc

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"gopkg.in/yaml.v3"
)

func TestValidateTunnelFilters(t *testing.T) {
	t.Run("nil tunnel and empty filters pass", func(t *testing.T) {
		if err := ValidateTunnelFilters(nil); err != nil {
			t.Fatalf("unexpected error for nil tunnel: %v", err)
		}
		tun := &Tunnel{Filter: "   ", FilterExclude: "\t\n"}
		if err := ValidateTunnelFilters(tun); err != nil {
			t.Fatalf("unexpected error for blank filters: %v", err)
		}
		if tun.Filter != "" || tun.FilterExclude != "" {
			t.Fatalf("expected filters trimmed to empty, got %q / %q", tun.Filter, tun.FilterExclude)
		}
	})

	t.Run("valid include and exclude are trimmed and accepted", func(t *testing.T) {
		tun := &Tunnel{
			Filter:        "  (?i)(DE|NL)  ",
			FilterExclude: "  (?i)(RU|BRIDGE|LTE) ",
		}
		if err := ValidateTunnelFilters(tun); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tun.Filter != "(?i)(DE|NL)" {
			t.Fatalf("expected trimmed include filter, got %q", tun.Filter)
		}
		if tun.FilterExclude != "(?i)(RU|BRIDGE|LTE)" {
			t.Fatalf("expected trimmed exclude filter, got %q", tun.FilterExclude)
		}
	})

	t.Run("lookaround rejected as FilterError with hint", func(t *testing.T) {
		tun := &Tunnel{Filter: "^(?!RU).*$"}
		err := ValidateTunnelFilters(tun)
		if err == nil {
			t.Fatal("expected lookaround error")
		}
		var fe *subscription.FilterError
		if !errors.As(err, &fe) {
			t.Fatalf("expected *subscription.FilterError, got %T: %v", err, err)
		}
		if !strings.Contains(fe.Error(), "lookahead/lookbehind") {
			t.Fatalf("expected lookaround message, got %q", fe.Error())
		}
	})

	t.Run("invalid exclude regex rejected as FilterError", func(t *testing.T) {
		tun := &Tunnel{Filter: "(?i)DE", FilterExclude: "[unclosed"}
		err := ValidateTunnelFilters(tun)
		if err == nil {
			t.Fatal("expected invalid regex error")
		}
		var fe *subscription.FilterError
		if !errors.As(err, &fe) {
			t.Fatalf("expected *subscription.FilterError, got %T: %v", err, err)
		}
		if !strings.Contains(fe.Error(), "исключать") {
			t.Fatalf("expected exclude field mention, got %q", fe.Error())
		}
	})

	t.Run("exceeding MaxTunnelFilterBytes rejected as FilterError", func(t *testing.T) {
		tun := &Tunnel{FilterExclude: strings.Repeat("a", MaxTunnelFilterBytes+1)}
		err := ValidateTunnelFilters(tun)
		if err == nil {
			t.Fatal("expected byte-limit error")
		}
		var fe *subscription.FilterError
		if !errors.As(err, &fe) {
			t.Fatalf("expected *subscription.FilterError, got %T: %v", err, err)
		}
	})
}

func TestTunnelFilterJSONYAMLRoundTrip(t *testing.T) {
	orig := Tunnel{
		ID:            "t1",
		Device:        "tunvless0",
		Enable:        true,
		Uplink:        TunnelUplink{Kind: "auto"},
		Sources:       []TunnelSource{{ID: "s1", Kind: "subscription", URL: "https://example.com/sub"}},
		Filter:        "(?i)(DE|NL)",
		FilterExclude: "(?i)(RU|BRIDGE)",
	}

	ymlBytes, err := yaml.Marshal(orig)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if !strings.Contains(string(ymlBytes), "filterExclude:") {
		t.Fatalf("expected filterExclude in YAML, got:\n%s", string(ymlBytes))
	}
	var fromYAML Tunnel
	if err := yaml.Unmarshal(ymlBytes, &fromYAML); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if fromYAML.Filter != orig.Filter || fromYAML.FilterExclude != orig.FilterExclude {
		t.Fatalf("YAML round-trip mismatch: got %q / %q", fromYAML.Filter, fromYAML.FilterExclude)
	}

	stateJSON := `{"id":"t1","device":"tunvless0","status":"up","active":[],"nodes":[],"filteredNodes":[{"key":"k1","name":"RU-1","source":"VOX"}],"since":0}`
	var st TunnelState
	if err := json.Unmarshal([]byte(stateJSON), &st); err != nil {
		t.Fatalf("json.Unmarshal TunnelState: %v", err)
	}
	if len(st.FilteredNodes) != 1 || st.FilteredNodes[0].Key != "k1" || st.FilteredNodes[0].Name != "RU-1" || st.FilteredNodes[0].Source != "VOX" {
		t.Fatalf("unexpected FilteredNodes in TunnelState: %+v", st.FilteredNodes)
	}

	prevJSON := `{"nodes":[],"filteredNodes":[{"key":"k2","name":"LTE-2","source":"VOX"}],"matched":1,"total":2,"subscriptions":[]}`
	var pr PreviewResponse
	if err := json.Unmarshal([]byte(prevJSON), &pr); err != nil {
		t.Fatalf("json.Unmarshal PreviewResponse: %v", err)
	}
	if len(pr.FilteredNodes) != 1 || pr.FilteredNodes[0].Key != "k2" {
		t.Fatalf("unexpected FilteredNodes in PreviewResponse: %+v", pr.FilteredNodes)
	}
}
