package firc

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTunnelSourceIntervalJSONYAMLRoundTrip(t *testing.T) {
	t.Run("manual interval zero is preserved in JSON and YAML", func(t *testing.T) {
		src := TunnelSource{
			ID:       "a1b2c3d4",
			Kind:     "subscription",
			Name:     "ManualSub",
			URL:      "https://example.com/sub",
			Interval: 0,
		}

		rawJSON, err := json.Marshal(src)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		if !strings.Contains(string(rawJSON), `"interval":0`) {
			t.Fatalf("expected explicit \"interval\":0 in JSON, got: %s", string(rawJSON))
		}
		var fromJSON TunnelSource
		if err := json.Unmarshal(rawJSON, &fromJSON); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}
		if fromJSON.Interval != 0 {
			t.Fatalf("expected JSON round-trip interval 0, got %d", fromJSON.Interval)
		}

		rawYAML, err := yaml.Marshal(src)
		if err != nil {
			t.Fatalf("yaml.Marshal: %v", err)
		}
		if !strings.Contains(string(rawYAML), "interval: 0s") {
			t.Fatalf("expected explicit interval: 0s in YAML, got:\n%s", string(rawYAML))
		}
		var fromYAML TunnelSource
		if err := yaml.Unmarshal(rawYAML, &fromYAML); err != nil {
			t.Fatalf("yaml.Unmarshal: %v", err)
		}
		if fromYAML.Interval != 0 {
			t.Fatalf("expected YAML round-trip interval 0, got %d", fromYAML.Interval)
		}
	})

	t.Run("positive interval round-trips in JSON and YAML", func(t *testing.T) {
		src := TunnelSource{
			ID:       "11223344",
			Kind:     "subscription",
			Name:     "HourlySub",
			URL:      "https://example.com/sub",
			Interval: 3600,
		}

		rawJSON, err := json.Marshal(src)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		if !strings.Contains(string(rawJSON), `"interval":3600`) {
			t.Fatalf("expected \"interval\":3600 in JSON, got: %s", string(rawJSON))
		}
		var fromJSON TunnelSource
		if err := json.Unmarshal(rawJSON, &fromJSON); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}
		if fromJSON.Interval != 3600 {
			t.Fatalf("expected 3600, got %d", fromJSON.Interval)
		}

		rawYAML, err := yaml.Marshal(src)
		if err != nil {
			t.Fatalf("yaml.Marshal: %v", err)
		}
		if !strings.Contains(string(rawYAML), "interval: 3600s") {
			t.Fatalf("expected interval: 3600s in YAML, got:\n%s", string(rawYAML))
		}
		var fromYAML TunnelSource
		if err := yaml.Unmarshal(rawYAML, &fromYAML); err != nil {
			t.Fatalf("yaml.Unmarshal: %v", err)
		}
		if fromYAML.Interval != 3600 {
			t.Fatalf("expected 3600, got %d", fromYAML.Interval)
		}
	})

	t.Run("omitted or null subscription interval defaults to 21600", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			in   string
		}{
			{"json omitted", `{"id":"s1","kind":"subscription","name":"Sub","url":"https://example.com/sub"}`},
			{"json null", `{"id":"s1","kind":"subscription","name":"Sub","url":"https://example.com/sub","interval":null}`},
			{"json inferred subscription", `{"id":"s1","url":"https://example.com/sub"}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var s TunnelSource
				if err := json.Unmarshal([]byte(tc.in), &s); err != nil {
					t.Fatalf("json.Unmarshal: %v", err)
				}
				if s.Interval != DefaultSubscriptionInterval {
					t.Fatalf("expected default %d, got %d", DefaultSubscriptionInterval, s.Interval)
				}
			})
		}

		for _, tc := range []struct {
			name string
			in   string
		}{
			{"yaml nested omitted", "id: s1\nsubscription:\n  name: Sub\n  url: https://example.com/sub\n"},
			{"yaml nested null", "id: s1\nsubscription:\n  name: Sub\n  url: https://example.com/sub\n  interval: null\n"},
			{"yaml nested empty", "id: s1\nsubscription:\n  name: Sub\n  url: https://example.com/sub\n  interval: \"\"\n"},
			{"yaml flat omitted", "id: s1\nkind: subscription\nurl: https://example.com/sub\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var s TunnelSource
				if err := yaml.Unmarshal([]byte(tc.in), &s); err != nil {
					t.Fatalf("yaml.Unmarshal: %v", err)
				}
				if s.Interval != DefaultSubscriptionInterval {
					t.Fatalf("expected default %d, got %d", DefaultSubscriptionInterval, s.Interval)
				}
			})
		}

		for _, tc := range []struct {
			name string
			in   string
		}{
			{"yaml nested 0s", "id: s1\nsubscription:\n  name: Sub\n  url: https://example.com/sub\n  interval: 0s\n"},
			{"yaml nested int 0", "id: s1\nsubscription:\n  name: Sub\n  url: https://example.com/sub\n  interval: 0\n"},
			{"yaml flat 0s", "id: s1\nkind: subscription\nurl: https://example.com/sub\ninterval: 0s\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var s TunnelSource
				if err := yaml.Unmarshal([]byte(tc.in), &s); err != nil {
					t.Fatalf("yaml.Unmarshal: %v", err)
				}
				if s.Interval != 0 {
					t.Fatalf("expected explicit manual interval 0, got %d", s.Interval)
				}
			})
		}
	})

	t.Run("negative and out-of-range intervals are normalized on JSON and YAML marshal", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			in       int
			wantJSON string
			wantYAML string
		}{
			{"negative defaults to 21600", -5, `"interval":21600`, "interval: 21600s"},
			{"zero stays manual", 0, `"interval":0`, "interval: 0s"},
			{"short positive clamps to 600", 120, `"interval":600`, "interval: 600s"},
			{"above max clamps to 604800", 700000, `"interval":604800`, "interval: 604800s"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				src := TunnelSource{
					ID:       "1234abcd",
					Kind:     "subscription",
					Name:     "Sub",
					URL:      "https://example.com/sub",
					Interval: tc.in,
				}
				rawJSON, err := json.Marshal(src)
				if err != nil {
					t.Fatalf("json.Marshal: %v", err)
				}
				if !strings.Contains(string(rawJSON), tc.wantJSON) {
					t.Fatalf("expected %s in JSON, got: %s", tc.wantJSON, string(rawJSON))
				}
				rawYAML, err := yaml.Marshal(src)
				if err != nil {
					t.Fatalf("yaml.Marshal: %v", err)
				}
				if !strings.Contains(string(rawYAML), tc.wantYAML) {
					t.Fatalf("expected %s in YAML, got:\n%s", tc.wantYAML, string(rawYAML))
				}
			})
		}
	})

	t.Run("link source omits interval in JSON and YAML", func(t *testing.T) {
		src := TunnelSource{
			ID:   "deadbeef",
			Kind: "link",
			Link: "vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls",
		}
		rawJSON, err := json.Marshal(src)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		if strings.Contains(string(rawJSON), "interval") {
			t.Fatalf("link JSON must not include interval: %s", string(rawJSON))
		}
		rawYAML, err := yaml.Marshal(src)
		if err != nil {
			t.Fatalf("yaml.Marshal: %v", err)
		}
		if strings.Contains(string(rawYAML), "interval") {
			t.Fatalf("link YAML must not include interval:\n%s", string(rawYAML))
		}

		// Reusing a populated TunnelSource variable resets stale fields.
		reused := TunnelSource{Kind: "subscription", URL: "https://old.example/sub", Interval: 21600}
		if err := json.Unmarshal(rawJSON, &reused); err != nil {
			t.Fatalf("json.Unmarshal link into reused struct: %v", err)
		}
		if reused.Kind != "link" || reused.URL != "" || reused.Interval != 0 {
			t.Fatalf("stale fields leaked on JSON unmarshal: %+v", reused)
		}
		reused = TunnelSource{Kind: "link", Link: "vless://stale", Interval: 999}
		if err := yaml.Unmarshal([]byte("id: s1\nsubscription:\n  name: Sub\n  url: https://example.com/sub\n  interval: 0s\n"), &reused); err != nil {
			t.Fatalf("yaml.Unmarshal sub into reused struct: %v", err)
		}
		if reused.Kind != "subscription" || reused.Link != "" || reused.Interval != 0 {
			t.Fatalf("stale fields leaked on YAML unmarshal: %+v", reused)
		}
	})
}

func TestNormalizeSubscriptionIntervalAndSources(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{-100, DefaultSubscriptionInterval},
		{-1, DefaultSubscriptionInterval},
		{0, 0},
		{1, MinSubscriptionInterval},
		{599, MinSubscriptionInterval},
		{600, 600},
		{3600, 3600},
		{21600, 21600},
		{604800, 604800},
		{700000, MaxSubscriptionInterval},
	}
	for _, tc := range cases {
		if got := NormalizeSubscriptionInterval(tc.in); got != tc.want {
			t.Errorf("NormalizeSubscriptionInterval(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}

	// 4 nested clash://install-config?url=... layers survive NormalizeSubscriptionURLForFirc's
	// 3 unwrap passes, forcing NormalizeTunnelSources into the !isHTTPSubscriptionURL fallback
	// (sub_normalizer.go:498-514) where ParseInlineSourcesForFirc unwraps the 4th layer.
	deepWrapped := "https://example.com/deep-wrapped"
	for range 4 {
		deepWrapped = "clash://install-config?url=" + url.QueryEscape(deepWrapped)
	}
	if afterFirst, _, err := NormalizeSubscriptionURLForFirc(deepWrapped, ""); err != nil || isHTTPSubscriptionURL(afterFirst) {
		t.Fatalf("precondition: expected wrapper to survive first NormalizeSubscriptionURLForFirc pass, got %q err=%v", afterFirst, err)
	}

	tun := Tunnel{
		ID:     "t1",
		Device: "tunvless0",
		Enable: true,
		Sources: []TunnelSource{
			{
				ID:       "aaaaaaaa",
				Kind:     "subscription",
				Name:     "DirectManual",
				URL:      "https://example.com/direct",
				Interval: 0,
			},
			{
				ID:       "bbbbbbbb",
				Kind:     "subscription",
				Name:     "WrapperManual",
				URL:      "happ://add/https://example.com/wrapped",
				Interval: 0,
				Headers:  map[string]string{"User-Agent": "Happ/3.6.1"},
			},
			{
				ID:       "b2b2b2b2",
				Kind:     "subscription",
				Name:     "NestedWrapperManual",
				URL:      deepWrapped,
				Interval: 0,
				Headers:  map[string]string{"X-Test": "nested"},
			},
			{
				ID:       "cccccccc",
				Kind:     "subscription",
				Name:     "TooShort",
				URL:      "https://example.com/short",
				Interval: 120,
			},
			{
				ID:       "dddddddd",
				Kind:     "subscription",
				Name:     "Negative",
				URL:      "https://example.com/neg",
				Interval: -10,
			},
			{
				ID:   "eeeeeeee",
				Kind: "link",
				Name: "SubInLinkBox",
				Link: "https://example.com/pasted-as-link",
			},
			{
				ID:       "e1e1e1e1",
				Kind:     "link",
				Name:     "SubInLinkBoxShort",
				Link:     "https://example.com/pasted-short",
				Interval: 120,
			},
			{
				ID:       "e2e2e2e2",
				Kind:     "link",
				Name:     "SubInLinkBoxOverMax",
				Link:     "https://example.com/pasted-overmax",
				Interval: 700000,
			},
		},
	}
	if err := NormalizeTunnelSources(&tun, ""); err != nil {
		t.Fatalf("NormalizeTunnelSources: %v", err)
	}
	if len(tun.Sources) != 8 {
		t.Fatalf("expected 8 sources, got %d: %+v", len(tun.Sources), tun.Sources)
	}
	if tun.Sources[0].Interval != 0 {
		t.Fatalf("direct manual subscription interval=%d, want 0", tun.Sources[0].Interval)
	}
	if tun.Sources[1].Interval != 0 || tun.Sources[1].Headers["User-Agent"] != "Happ/3.6.1" {
		t.Fatalf("wrapped manual subscription lost interval or headers: %+v", tun.Sources[1])
	}
	if tun.Sources[2].Kind != "subscription" || tun.Sources[2].URL != "https://example.com/deep-wrapped" ||
		tun.Sources[2].Interval != 0 || tun.Sources[2].Headers["X-Test"] != "nested" {
		t.Fatalf("nested wrapped manual subscription lost interval or headers: %+v", tun.Sources[2])
	}
	if tun.Sources[3].Interval != MinSubscriptionInterval {
		t.Fatalf("too-short interval=%d, want %d", tun.Sources[3].Interval, MinSubscriptionInterval)
	}
	if tun.Sources[4].Interval != DefaultSubscriptionInterval {
		t.Fatalf("negative interval=%d, want %d", tun.Sources[4].Interval, DefaultSubscriptionInterval)
	}
	if tun.Sources[5].Kind != "subscription" || tun.Sources[5].Interval != DefaultSubscriptionInterval {
		t.Fatalf("subscription pasted into link box should keep default interval %d, got %+v", DefaultSubscriptionInterval, tun.Sources[5])
	}
	if tun.Sources[6].Kind != "subscription" || tun.Sources[6].Interval != MinSubscriptionInterval {
		t.Fatalf("link-pasted subscription with short interval should clamp to %d, got %+v", MinSubscriptionInterval, tun.Sources[6])
	}
	if tun.Sources[7].Kind != "subscription" || tun.Sources[7].Interval != MaxSubscriptionInterval {
		t.Fatalf("link-pasted subscription with over-max interval should clamp to %d, got %+v", MaxSubscriptionInterval, tun.Sources[7])
	}
}

func TestOperatorSaveLoadManualSubscriptionInterval(t *testing.T) {
	op := NewOperator(Options{ConfigDir: filepath.Join(t.TempDir(), "firc")})
	if err := os.MkdirAll(op.configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := []Tunnel{{
		ID:     "t-manual",
		Device: "tunvless0",
		Enable: true,
		Uplink: TunnelUplink{Kind: "auto"},
		Sources: []TunnelSource{{
			ID:       "12345678",
			Kind:     "subscription",
			Name:     "ManualSub",
			URL:      "https://example.com/sub",
			Interval: 0,
		}},
	}}
	if err := op.SaveTunnelsFile(want); err != nil {
		t.Fatalf("SaveTunnelsFile: %v", err)
	}
	raw, err := os.ReadFile(op.TunnelsPath())
	if err != nil {
		t.Fatalf("read tunnels.yaml: %v", err)
	}
	if !strings.Contains(string(raw), "interval: 0s") {
		t.Fatalf("tunnels.yaml missing interval: 0s:\n%s", string(raw))
	}
	got, err := op.LoadTunnelsFile()
	if err != nil {
		t.Fatalf("LoadTunnelsFile: %v", err)
	}
	if len(got) != 1 || len(got[0].Sources) != 1 || got[0].Sources[0].Interval != 0 {
		t.Fatalf("expected interval 0 after reload, got %+v", got)
	}
}
