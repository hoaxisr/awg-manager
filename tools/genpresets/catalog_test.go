package main

import "testing"

func TestAdditionsCoverMetaAndOculus(t *testing.T) {
	byID := map[string]addition{}
	for _, a := range additions {
		byID[a.id] = a
	}
	for _, id := range []string{"meta", "oculus", "soundcloud", "slack", "blizzard", "threads"} {
		a, ok := byID[id]
		if !ok {
			t.Fatalf("addition %q missing", id)
		}
		if a.name == "" || a.iconSlug == "" || a.category == "" || a.srsURL == "" || a.action == "" {
			t.Errorf("addition %q incomplete: %+v", id, a)
		}
	}
}
