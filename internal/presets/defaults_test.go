package presets

import "testing"

func TestLoadBuiltins(t *testing.T) {
	ps, err := LoadBuiltins()
	if err != nil {
		t.Fatalf("LoadBuiltins: %v", err)
	}
	if len(ps) < 3 {
		t.Fatalf("want >=3 builtins, got %d", len(ps))
	}
	byID := map[string]Preset{}
	for _, p := range ps {
		if p.ID == "" || p.Name == "" || p.IconSlug == "" {
			t.Errorf("preset %q has empty id/name/iconSlug", p.ID)
		}
		if p.Origin != OriginBuiltin {
			t.Errorf("preset %q origin = %q, want builtin", p.ID, p.Origin)
		}
		byID[p.ID] = p
	}
	if yt, ok := byID["youtube"]; !ok || yt.Engines.DNS == nil || yt.Engines.Singbox == nil || yt.Engines.HydraRoute == nil {
		t.Errorf("youtube must carry all three engines")
	}
	if ads, ok := byID["category-ads"]; !ok || ads.Engines.Singbox == nil || ads.Engines.DNS != nil {
		t.Errorf("category-ads must be singbox-only")
	}
	if ru, ok := byID["russian-services"]; !ok || ru.Engines.DNS == nil || ru.Engines.Singbox != nil {
		t.Errorf("russian-services must be dns-only")
	}
}
