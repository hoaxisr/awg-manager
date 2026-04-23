package updater

import (
	"reflect"
	"testing"
)

func TestSlice_OnlyNewerThanFrom(t *testing.T) {
	entries := map[string]Entry{
		"2.8.0":  {Version: "2.8.0", Date: "2026-04-17"},
		"2.7.11": {Version: "2.7.11", Date: "2026-04-16"},
		"2.7.10": {Version: "2.7.10", Date: "2026-04-15"},
	}
	got := Slice(entries, "2.7.10", "2.8.0")
	want := []Entry{
		{Version: "2.8.0", Date: "2026-04-17"},
		{Version: "2.7.11", Date: "2026-04-16"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestSlice_IncludesTo(t *testing.T) {
	entries := map[string]Entry{
		"1.0.0": {Version: "1.0.0"},
		"1.0.1": {Version: "1.0.1"},
	}
	got := Slice(entries, "1.0.0", "1.0.1")
	if len(got) != 1 || got[0].Version != "1.0.1" {
		t.Errorf("to-bound must be included, from must be excluded; got %+v", got)
	}
}

func TestSlice_FromGreaterOrEqualReturnsEmpty(t *testing.T) {
	entries := map[string]Entry{"1.0.0": {Version: "1.0.0"}}
	if got := Slice(entries, "1.0.0", "1.0.0"); len(got) != 0 {
		t.Errorf("from==to must return empty, got %+v", got)
	}
	if got := Slice(entries, "2.0.0", "1.0.0"); len(got) != 0 {
		t.Errorf("from>to must return empty, got %+v", got)
	}
}

func TestSlice_MissingToIgnored(t *testing.T) {
	entries := map[string]Entry{
		"1.0.0": {Version: "1.0.0"},
		"2.0.0": {Version: "2.0.0"},
	}
	got := Slice(entries, "1.0.0", "9.9.9")
	if len(got) != 1 || got[0].Version != "2.0.0" {
		t.Errorf("missing 'to' should still slice by version comparison: %+v", got)
	}
}
