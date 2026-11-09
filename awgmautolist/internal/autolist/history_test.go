package autolist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func snapshotHistory(t *testing.T, eng *Engine) []HistoryEntry {
	t.Helper()
	history, ok := eng.Snapshot()["history"].([]HistoryEntry)
	if !ok {
		t.Fatalf("snapshot history has unexpected type %T", eng.Snapshot()["history"])
	}
	return history
}

func historyByDomain(t *testing.T, entries []HistoryEntry) map[string]HistoryEntry {
	t.Helper()
	out := make(map[string]HistoryEntry, len(entries))
	for _, entry := range entries {
		if _, exists := out[entry.Domain]; exists {
			t.Fatalf("duplicate history entry for %s: %+v", entry.Domain, entries)
		}
		out[entry.Domain] = entry
	}
	return out
}

func TestHistoryPersistenceNewestFirstAndFileMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "awgmautolist.json")
	eng := NewEngine("http://127.0.0.1:1", "")
	now := time.Date(2026, time.November, 7, 12, 0, 0, 0, time.UTC)
	eng.now = func() time.Time { return now }
	if err := eng.LoadConfig(configPath); err != nil {
		t.Fatal(err)
	}

	eng.table.Note(Host{Name: "www.example.com"}, WhyReset, "dial", now)
	eng.Dismiss("example.com")
	now = now.Add(time.Minute)
	eng.table.Note(Host{Name: "slow.other.example"}, WhyFreeze, "stall", now)
	eng.Dismiss("other.example")

	history := snapshotHistory(t, eng)
	if len(history) != 2 || history[0].Domain != "other.example" || history[1].Domain != "example.com" {
		t.Fatalf("history is not newest first: %+v", history)
	}
	manual := history[1]
	if manual.Action != "dismissed" || manual.Source != "user" || manual.Host != "www.example.com" || manual.Why != "reset" || !manual.At.Equal(now.Add(-time.Minute)) {
		t.Fatalf("manual dismissal = %+v", manual)
	}

	historyPath := filepath.Join(dir, "awgmautolist-history.json")
	info, err := os.Stat(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("history mode = %04o, want 0600", info.Mode().Perm())
	}
	b, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored []HistoryEntry
	if err := json.Unmarshal(b, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].Domain != "example.com" || stored[1].Domain != "other.example" {
		t.Fatalf("on-disk history is not chronological: %+v", stored)
	}

	restarted := NewEngine("http://127.0.0.1:1", "")
	if err := restarted.LoadConfig(configPath); err != nil {
		t.Fatal(err)
	}
	if got := snapshotHistory(t, restarted); !reflect.DeepEqual(got, history) {
		t.Fatalf("restarted history = %+v, want %+v", got, history)
	}
}

func TestHistoryInMemoryBoundAndNewestFirst(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	for i := 1; i <= historyLimit+1; i++ {
		eng.Dismiss(fmt.Sprintf("d%d.example", i))
	}
	history := snapshotHistory(t, eng)
	if len(history) != historyLimit {
		t.Fatalf("history length = %d, want %d", len(history), historyLimit)
	}
	if history[0].Domain != fmt.Sprintf("d%d.example", historyLimit+1) || history[len(history)-1].Domain != "d2.example" {
		t.Fatalf("bounded newest-first history starts with %s and ends with %s", history[0].Domain, history[len(history)-1].Domain)
	}
}

func TestHistoryMalformedFileDoesNotDestroyConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configBytes := []byte("{\"autoAdd\":true,\"targetOutbound\":\"vpn\"}\n")
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPathFromConfig(configPath), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine("http://127.0.0.1:1", "")
	if err := eng.LoadConfig(configPath); err != nil {
		t.Fatalf("history must not reject a valid config: %v", err)
	}
	if cfg := eng.Config(); cfg.AutoAdd || cfg.TargetOutbound != "vpn" {
		t.Fatalf("config was not loaded: %+v", cfg)
	}
	if history := snapshotHistory(t, eng); len(history) != 0 {
		t.Fatalf("malformed history produced entries: %+v", history)
	}
	if got, err := os.ReadFile(configPath); err != nil || !reflect.DeepEqual(got, configBytes) {
		t.Fatalf("config changed after malformed history: %q, %v", got, err)
	}
}

func TestHistorySkipsTunnelAdditions(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.November, 7, 13, 0, 0, 0, time.UTC)
	eng.now = func() time.Time { return now }
	eng.table.Note(Host{Name: "www.a.example"}, WhyReset, "dial", now)
	eng.table.Note(Host{Name: "www.b.example"}, WhyFreeze, "stall", now)

	results := eng.AddToTunnel([]string{"a.example", "a.example", "b.example"})
	for _, domain := range []string{"a.example", "b.example"} {
		if err := results[domain]; err != nil {
			t.Fatalf("add %s: %v", domain, err)
		}
	}
	history := snapshotHistory(t, eng)
	if len(history) != 0 {
		t.Fatalf("добавление в фактический rule-set не должно создавать историю: %+v", history)
	}
	eng.AddToTunnel([]string{"a.example", "b.example"})
	if got := snapshotHistory(t, eng); !reflect.DeepEqual(got, history) {
		t.Fatalf("idempotent additions changed history: %+v", got)
	}
}

func TestHistorySkipsAutomaticAdd(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	completed := time.Date(2026, time.November, 7, 14, 0, 0, 0, time.UTC)
	eng.now = func() time.Time { return completed }
	eng.table.Note(Host{Name: "www.auto.example"}, WhyNoConnect, "packet", completed)
	eng.enqueue(probeTask{host: "www.auto.example", domain: "auto.example", source: "packet"})
	task, ok := eng.nextProbe()
	if !ok {
		t.Fatal("automatic probe not issued")
	}
	eng.completeProbe(task, ProbeResult{
		Verdict: VerdictBlocked, Why: WhyNoConnect, TunnelProbed: true, TunnelVerdict: VerdictReachable,
	}, completed)

	if history := snapshotHistory(t, eng); len(history) != 0 {
		t.Fatalf("automatic add создал лишнюю историю: %+v", history)
	}
	if got := awgm.addedRules(); len(got) != 1 || got[0] != "auto.example" {
		t.Fatalf("automatic add не изменил фактический rule-set: %v", got)
	}
}

func TestReachableDoesNotDismissOrWriteHistory(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	completed := time.Date(2026, time.November, 7, 15, 0, 0, 0, time.UTC)
	eng.table.Note(Host{Name: "www.reachable.example"}, WhyFreeze, "stall", completed)
	eng.completeProbe(probeTask{host: "www.reachable.example", domain: "reachable.example"},
		ProbeResult{Verdict: VerdictReachable}, completed)
	if history := snapshotHistory(t, eng); len(history) != 0 {
		t.Fatalf("reachable создал историю: %+v", history)
	}
	if !eng.table.Note(Host{Name: "www.reachable.example"}, WhyFreeze, "stall", completed.Add(time.Second)) {
		t.Fatal("reachable автоматически скрыл следующее наблюдение")
	}
}

func TestHistoryExcludeOnlyAfterConfigSave(t *testing.T) {
	t.Run("successful mass exclusion", func(t *testing.T) {
		eng := NewEngine("http://127.0.0.1:1", "")
		if err := eng.LoadConfig(filepath.Join(t.TempDir(), "config.json")); err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, time.November, 7, 16, 0, 0, 0, time.UTC)
		eng.now = func() time.Time { return now }
		eng.table.Note(Host{Name: "www.example.com"}, WhyReset, "dial", now)
		eng.table.Note(Host{Name: "other.example"}, WhyNoAnswer, "dial", now)

		if err := eng.ExcludeDomains([]string{"WWW.Example.COM.", "www.example.com", "Other.Example"}); err != nil {
			t.Fatal(err)
		}
		history := snapshotHistory(t, eng)
		if len(history) != 2 {
			t.Fatalf("normalized mass exclusion history = %+v", history)
		}
		byDomain := historyByDomain(t, history)
		subdomain := byDomain["www.example.com"]
		if subdomain.Action != "excluded" || subdomain.Source != "user" || subdomain.Host != "www.example.com" || subdomain.Why != "reset" || !subdomain.At.Equal(now) {
			t.Fatalf("subdomain exclusion = %+v", subdomain)
		}
		other := byDomain["other.example"]
		if other.Action != "excluded" || other.Source != "user" || other.Host != "other.example" || other.Why != "no-answer" {
			t.Fatalf("second exclusion = %+v", other)
		}
		if rows := eng.table.List(); len(rows) != 0 {
			t.Fatalf("excluded candidates remain: %+v", rows)
		}
	})

	t.Run("failed config save records and forgets nothing", func(t *testing.T) {
		eng := NewEngine("http://127.0.0.1:1", "")
		path := filepath.Join(t.TempDir(), "missing", "config.json")
		if err := eng.LoadConfig(path); err != nil {
			t.Fatal(err)
		}
		eng.table.Note(Host{Name: "www.failure.example"}, WhyFreeze, "stall", time.Now())
		before := eng.Config()
		if err := eng.ExcludeDomains([]string{"failure.example"}); err == nil {
			t.Fatal("exclusion unexpectedly saved config")
		}
		if history := snapshotHistory(t, eng); len(history) != 0 {
			t.Fatalf("failed exclusion was recorded: %+v", history)
		}
		if rows := eng.table.List(); len(rows) != 1 {
			t.Fatalf("failed exclusion forgot candidate: %+v", rows)
		}
		if after := eng.Config(); !reflect.DeepEqual(after, before) {
			t.Fatalf("failed save changed in-memory config: before=%+v after=%+v", before, after)
		}
	})
}

func TestAddDoesNotDependOnHistory(t *testing.T) {
	awgm := newFakeAWGM(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte("{\"targetInterface\":\"nwg0\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory at the destination makes temp+rename fail after the router
	// operation has succeeded, without relying on permission checks as root.
	if err := os.Mkdir(historyPathFromConfig(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.LoadConfig(configPath); err != nil {
		t.Fatal(err)
	}
	if err := eng.AddToTunnel([]string{"durable.example"})["durable.example"]; err != nil {
		t.Fatalf("history failure rolled back add result: %v", err)
	}
	if got := awgm.addedRules(); len(got) != 1 || got[0] != "durable.example" {
		t.Fatalf("history failure changed router result: %v", got)
	}
	if history := snapshotHistory(t, eng); len(history) != 0 {
		t.Fatalf("добавление не должно использовать историю: %+v", history)
	}
}

func TestStatusHistoryJSONContract(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	now := time.Date(2026, time.November, 7, 18, 0, 0, 123456789, time.UTC)
	eng.now = func() time.Time { return now }

	marshalStatus := func() map[string]json.RawMessage {
		t.Helper()
		b, err := json.Marshal(eng.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		var status map[string]json.RawMessage
		if err := json.Unmarshal(b, &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	if raw := marshalStatus()["history"]; string(raw) != "[]" {
		t.Fatalf("empty history JSON = %s, want []", raw)
	}

	eng.table.Note(Host{Name: "www.example.com"}, WhyReset, "dial", now)
	eng.Dismiss("example.com")
	eng.Dismiss("bare.example")
	var payload struct {
		History []map[string]json.RawMessage `json:"history"`
	}
	b, err := json.Marshal(eng.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.History) != 2 {
		t.Fatalf("status history = %+v", payload.History)
	}
	if len(payload.History[0]) != 4 || payload.History[0]["host"] != nil || payload.History[0]["why"] != nil {
		t.Fatalf("optional fields were not omitted: %+v", payload.History[0])
	}
	entry := payload.History[1]
	if len(entry) != 6 {
		t.Fatalf("full history entry has unexpected keys: %+v", entry)
	}
	want := map[string]string{
		"domain": "example.com", "host": "www.example.com", "action": "dismissed",
		"why": "reset", "source": "user",
	}
	for key, expected := range want {
		var got string
		if err := json.Unmarshal(entry[key], &got); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got != expected {
			t.Fatalf("%s = %q, want %q", key, got, expected)
		}
	}
	var at string
	if err := json.Unmarshal(entry["at"], &at); err != nil {
		t.Fatal(err)
	}
	parsed, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatalf("at is not RFC3339: %q: %v", at, err)
	}
	if !parsed.Equal(now) {
		t.Fatalf("at = %s, want %s", parsed, now)
	}
}
