package query

import (
	"context"
	"errors"
	"testing"
	"time"
)

const fqdnPath = "/show/rc/object-group/fqdn"

const sampleFQDNJSON = `{
	"group1": {
		"include": [{"address": "example.com"}, {"address": "example.org"}],
		"exclude": [{"address": "bad.example.com"}]
	},
	"group2": {
		"include": [{"address": "other.com"}]
	}
}`

func TestObjectGroupStore_List_ParsesAndCaches(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(fqdnPath, sampleFQDNJSON)
	s := NewObjectGroupStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: want 2, got %d", len(got))
	}
	byName := map[string]int{}
	for i, g := range got {
		byName[g.Name] = i
	}
	g1 := got[byName["group1"]]
	if len(g1.Includes) != 2 || g1.Includes[0] != "example.com" {
		t.Errorf("group1 includes: %#v", g1.Includes)
	}
	if len(g1.Excludes) != 1 || g1.Excludes[0] != "bad.example.com" {
		t.Errorf("group1 excludes: %#v", g1.Excludes)
	}
	_, _ = s.List(context.Background())
	if fg.Calls(fqdnPath) != 1 {
		t.Errorf("calls: %d", fg.Calls(fqdnPath))
	}
}

func TestObjectGroupStore_List_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(fqdnPath, sampleFQDNJSON)
	s := NewObjectGroupStoreWithTTL(fg, NopLogger(), 20*time.Millisecond)
	_, _ = s.List(context.Background())
	time.Sleep(30 * time.Millisecond)
	fg.SetError(fqdnPath, errors.New("boom"))
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("stale-ok: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len: %d", len(got))
	}
}

func TestObjectGroupStore_List_EmptyArray(t *testing.T) {
	// NDMS returns `[]` instead of `{}` when no FQDN groups exist.
	fg := newFakeGetter()
	fg.SetJSON(fqdnPath, `[]`)
	s := NewObjectGroupStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("empty array: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len: want 0, got %d", len(got))
	}
}

func TestObjectGroupStore_InvalidateAllForcesRefetch(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(fqdnPath, sampleFQDNJSON)
	s := NewObjectGroupStore(fg, NopLogger())
	_, _ = s.List(context.Background())
	s.InvalidateAll()
	_, _ = s.List(context.Background())
	if fg.Calls(fqdnPath) != 2 {
		t.Errorf("calls: %d", fg.Calls(fqdnPath))
	}
}
