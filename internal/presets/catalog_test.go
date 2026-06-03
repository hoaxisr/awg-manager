package presets

import "testing"

func builtinsFixture() []Preset {
	return []Preset{
		{ID: "youtube", Name: "YouTube", IconSlug: "youtube", Origin: OriginBuiltin},
		{ID: "hbo", Name: "HBO", IconSlug: "hbo", Origin: OriginBuiltin},
	}
}

func TestMergeEmptyOverlay(t *testing.T) {
	out := Merge(builtinsFixture(), &Overlay{})
	if len(out) != 2 {
		t.Fatalf("want 2, got %d", len(out))
	}
	for _, p := range out {
		if p.Origin != OriginBuiltin {
			t.Errorf("%q origin = %q", p.ID, p.Origin)
		}
	}
}

func TestMergeDisable(t *testing.T) {
	out := Merge(builtinsFixture(), &Overlay{DisabledBuiltins: []string{"youtube"}})
	if len(out) != 1 || out[0].ID != "hbo" {
		t.Fatalf("disable failed: %+v", out)
	}
}

func TestMergeOverridePreservesOrderForcesUser(t *testing.T) {
	ov := &Overlay{Presets: []Preset{{ID: "youtube", Name: "YT custom", IconSlug: "youtube", Origin: OriginBuiltin}}}
	out := Merge(builtinsFixture(), ov)
	if len(out) != 2 || out[0].ID != "youtube" || out[0].Name != "YT custom" || out[0].Origin != OriginUser {
		t.Fatalf("override failed: %+v", out)
	}
}

func TestMergeAppendCustomForcesUser(t *testing.T) {
	ov := &Overlay{Presets: []Preset{{ID: "mine", Name: "Mine", IconSlug: "lucide-globe"}}}
	out := Merge(builtinsFixture(), ov)
	if len(out) != 3 || out[2].ID != "mine" || out[2].Origin != OriginUser {
		t.Fatalf("append failed: %+v", out)
	}
}

func TestCatalogListMergesBuiltinsWithEmptyOverlay(t *testing.T) {
	c := NewCatalog(NewStore(t.TempDir()))
	out, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) < 3 {
		t.Fatalf("want >=3 (builtins), got %d", len(out))
	}
}
