package presets

import "testing"

func TestLoadBuiltins(t *testing.T) {
	ps, err := LoadBuiltins()
	if err != nil {
		t.Fatalf("LoadBuiltins: %v", err)
	}
	if len(ps) < 50 {
		t.Fatalf("expected the full catalog (>=50), got %d", len(ps))
	}
	for _, p := range ps {
		if p.ID == "" || p.Name == "" || p.IconSlug == "" {
			t.Errorf("preset %q has empty id/name/iconSlug", p.ID)
		}
		if p.Origin != OriginBuiltin {
			t.Errorf("preset %q origin = %q, want builtin", p.ID, p.Origin)
		}
	}
}

func TestDefaultsCatalogInvariants(t *testing.T) {
	ps, err := LoadBuiltins()
	if err != nil {
		t.Fatalf("LoadBuiltins: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if seen[p.ID] {
			t.Errorf("duplicate id %q", p.ID)
		}
		seen[p.ID] = true
		if sb := p.Engines.Singbox; sb != nil && sb.Action == "" {
			t.Errorf("preset %q singbox has empty action", p.ID)
		}
		if dns := p.Engines.DNS; dns != nil {
			if len(dns.Domains) == 0 && len(dns.Subnets) == 0 && dns.SubscriptionURL == "" {
				t.Errorf("preset %q dns engine is empty", p.ID)
			}
			if len(dns.Domains) > 500 {
				t.Errorf("preset %q dns domains %d exceed the 500 cap", p.ID, len(dns.Domains))
			}
		}
	}
	// Alias ids collapsed to canonical. (cloudflare-ips is intentionally NOT
	// here: U1c re-added it as a distinct DNS-only CIDR preset alongside
	// cloudflare — see M2.)
	for _, bad := range []string{"twitter", "chatgpt", "social"} {
		if seen[bad] {
			t.Errorf("alias id %q must be collapsed", bad)
		}
	}
	for _, need := range []string{"x", "openai", "cloudflare", "meta", "oculus", "russian-services", "cloudflare-ips"} {
		if !seen[need] {
			t.Errorf("expected id %q present", need)
		}
	}
	// russian-services is DNS-only (no .srs).
	for _, p := range ps {
		if p.ID == "russian-services" {
			if p.Engines.DNS == nil || p.Engines.Singbox != nil {
				t.Errorf("russian-services must be dns-only, got %+v", p.Engines)
			}
		}
	}
}
