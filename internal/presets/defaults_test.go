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
			if len(dns.Domains) == 0 && len(dns.Subnets) == 0 && dns.SubscriptionURL == "" && len(p.Covers) == 0 {
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
	// singbox-only presets (except rkn) get DNS from hoaxisr/rulesets/raw where lists exist.
	for _, id := range []string{"unavailable-in-russia", "google-play"} {
		var found *Preset
		for i := range ps {
			if ps[i].ID == id {
				found = &ps[i]
				break
			}
		}
		if found == nil {
			t.Errorf("expected id %q present", id)
			continue
		}
		if found.Engines.DNS == nil {
			t.Errorf("preset %q: expected dns engine from hoaxisr/raw", id)
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

func TestDefaultsCatalogCovers(t *testing.T) {
	ps, err := LoadBuiltins()
	if err != nil {
		t.Fatalf("LoadBuiltins: %v", err)
	}
	ids := map[string]bool{}
	for _, p := range ps {
		ids[p.ID] = true
	}
	for _, p := range ps {
		for _, child := range p.Covers {
			if !ids[child] {
				t.Errorf("preset %q covers unknown id %q", p.ID, child)
			}
			if child == p.ID {
				t.Errorf("preset %q must not cover itself", p.ID)
			}
		}
	}
	wantParents := map[string]int{
		"category-ai":    6,
		"category-games": 11,
		"category-media": 12,
		"meta":           4,
		"dev-tools":      3,
		"google":         2,
	}
	for id, n := range wantParents {
		var found *Preset
		for i := range ps {
			if ps[i].ID == id {
				found = &ps[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("expected preset %q", id)
		}
		if len(found.Covers) != n {
			t.Errorf("preset %q covers: got %d want %d (%v)", id, len(found.Covers), n, found.Covers)
		}
	}
}

func TestDefaultsCatalogCoversNoCycles(t *testing.T) {
	ps, err := LoadBuiltins()
	if err != nil {
		t.Fatalf("LoadBuiltins: %v", err)
	}
	byID := map[string]Preset{}
	for _, p := range ps {
		byID[p.ID] = p
	}
	var visit func(id string, stack map[string]bool) bool
	visit = func(id string, stack map[string]bool) bool {
		if stack[id] {
			return true
		}
		p, ok := byID[id]
		if !ok {
			return false
		}
		stack[id] = true
		for _, child := range p.Covers {
			if visit(child, stack) {
				return true
			}
		}
		delete(stack, id)
		return false
	}
	for _, p := range ps {
		if visit(p.ID, map[string]bool{}) {
			t.Errorf("preset %q is part of a covers cycle", p.ID)
		}
	}
}

func TestDefaultsCatalogCoversNoDuplicateDns(t *testing.T) {
	ps, err := LoadBuiltins()
	if err != nil {
		t.Fatalf("LoadBuiltins: %v", err)
	}
	byID := map[string]Preset{}
	for _, p := range ps {
		byID[p.ID] = p
	}
	for _, p := range ps {
		if len(p.Covers) == 0 || p.Engines.DNS == nil {
			continue
		}
		covered := map[string]bool{}
		for _, childID := range p.Covers {
			child, ok := byID[childID]
			if !ok {
				continue
			}
			if child.Engines.DNS == nil {
				continue
			}
			for _, d := range child.Engines.DNS.Domains {
				covered[d] = true
			}
			for _, s := range child.Engines.DNS.Subnets {
				covered[s] = true
			}
		}
		for _, d := range p.Engines.DNS.Domains {
			if covered[d] {
				t.Errorf("preset %q dns domain %q is duplicated from a covered preset", p.ID, d)
			}
		}
		for _, s := range p.Engines.DNS.Subnets {
			if covered[s] {
				t.Errorf("preset %q dns subnet %q is duplicated from a covered preset", p.ID, s)
			}
		}
	}
}
