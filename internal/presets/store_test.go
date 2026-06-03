package presets

import (
	"reflect"
	"testing"
)

func TestStoreLoadMissingReturnsEmpty(t *testing.T) {
	s := NewStore(t.TempDir())
	o, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(o.Presets) != 0 || len(o.DisabledBuiltins) != 0 {
		t.Fatalf("want empty overlay, got %+v", o)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	in := &Overlay{
		DisabledBuiltins: []string{"hbo"},
		Presets:          []Preset{{ID: "mine", Name: "Mine", IconSlug: "lucide-globe", Category: "social"}},
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(in.DisabledBuiltins, out.DisabledBuiltins) {
		t.Fatalf("disabled mismatch: %+v", out.DisabledBuiltins)
	}
	if len(out.Presets) != 1 || out.Presets[0].ID != "mine" {
		t.Fatalf("presets mismatch: %+v", out.Presets)
	}
}
