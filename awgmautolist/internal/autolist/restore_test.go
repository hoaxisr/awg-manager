package autolist

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRestoreDomainClearsHiddenStateAndOnlyHiddenHistory(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	configPath := filepath.Join(t.TempDir(), "awgmautolist.json")
	if err := eng.LoadConfig(configPath); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	eng.table.Note(Host{Name: "www.example.com"}, WhyReset, "packet", now)
	eng.Dismiss("example.com")
	if err := eng.ExcludeDomains([]string{"example.com", "other.example"}); err != nil {
		t.Fatal(err)
	}
	eng.recordHistory([]HistoryEntry{
		{Domain: "example.com", Action: "added", Source: "user", At: now},
		{Domain: "example.com", Action: "removed", Source: "reachable", At: now},
	})

	if err := eng.RestoreDomain("EXAMPLE.COM."); err != nil {
		t.Fatal(err)
	}
	cfg := eng.Config()
	if len(cfg.ExcludeDomains) != 1 || cfg.ExcludeDomains[0] != "other.example" {
		t.Fatalf("exclusions after restore = %v", cfg.ExcludeDomains)
	}
	history := snapshotHistory(t, eng)
	if len(history) != 1 {
		t.Fatalf("history after restore = %+v", history)
	}
	byDomain := map[string][]HistoryEntry{}
	for _, entry := range history {
		byDomain[entry.Domain] = append(byDomain[entry.Domain], entry)
	}
	if len(byDomain["example.com"]) != 0 {
		t.Fatalf("removed statuses/hidden history were not cleared: %+v", byDomain["example.com"])
	}
	if len(byDomain["other.example"]) != 1 || byDomain["other.example"][0].Action != "excluded" {
		t.Fatalf("other domain history changed: %+v", byDomain["other.example"])
	}
	if !eng.table.Note(Host{Name: "www.example.com"}, WhyReset, "packet", now) {
		t.Fatal("restored domain is still dismissed")
	}
	before := snapshotHistory(t, eng)
	if err := eng.RestoreDomain("example.com"); err != nil {
		t.Fatal(err)
	}
	if got := snapshotHistory(t, eng); !reflect.DeepEqual(got, before) {
		t.Fatalf("idempotent restore changed history: %+v", got)
	}

	restarted := NewEngine("http://127.0.0.1:1", "")
	if err := restarted.LoadConfig(configPath); err != nil {
		t.Fatal(err)
	}
	got := snapshotHistory(t, restarted)
	if len(got) != len(before) {
		t.Fatalf("restored history did not survive restart: %+v", got)
	}
	for i := range got {
		if got[i].Domain != before[i].Domain || got[i].Host != before[i].Host ||
			got[i].Action != before[i].Action || got[i].Why != before[i].Why ||
			got[i].Source != before[i].Source || !got[i].At.Equal(before[i].At) {
			t.Fatalf("restored history entry %d = %+v, want %+v", i, got[i], before[i])
		}
	}
	if got := restarted.Config().ExcludeDomains; len(got) != 1 || got[0] != "other.example" {
		t.Fatalf("restored exclusions did not survive restart: %v", got)
	}
}
