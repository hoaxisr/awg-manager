package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

func TestStateRestartAndRollingBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store := New(path)
	store.now = func() time.Time { return now }
	target, _ := model.NewTarget("example.com", "185.59.211.40", model.NetworkTCP, 443)
	candidate := model.Candidate{ID: target.CandidateKey(), Target: target, State: model.CandidateAdded, FirstAt: now}
	if err := store.Update(true, func(state *State) {
		state.Candidates[candidate.ID] = candidate
		state.History = append(state.History, model.HistoryEntry{At: now, Domain: "example.com", Action: "added", Source: "auto", Result: "ok"})
		state.Additions = append(state.Additions, now, now.Add(-2*time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	restarted := New(path)
	restarted.now = func() time.Time { return now }
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	state := restarted.Snapshot(now)
	if len(state.Candidates) != 1 || len(state.History) != 1 || len(state.Additions) != 1 {
		t.Fatalf("unexpected restarted state: %+v", state)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestCheckingUpdateDoesNotWriteState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := New(path)
	if err := store.Update(false, func(state *State) {
		state.Candidates["checking"] = model.Candidate{ID: "checking", State: model.CandidateChecking}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("checking state reached flash: %v", err)
	}
}

func TestLegacyHistoryImportPreservesActions(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "awgmautolist-history.json")
	legacy := `[{"domain":"example.com","action":"dismissed","source":"user","at":"2026-11-01T12:00:00Z"}]`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(dir, "state.json"))
	if err := store.ImportLegacyHistory(legacyPath); err != nil {
		t.Fatal(err)
	}
	history := store.Snapshot(time.Now()).History
	if len(history) != 1 || history[0].Action != "dismissed" || history[0].Domain != "example.com" {
		t.Fatalf("history=%+v", history)
	}
	if err := store.ImportLegacyHistory(legacyPath); err != nil {
		t.Fatal(err)
	}
	if len(store.Snapshot(time.Now()).History) != 1 {
		t.Fatal("legacy import duplicated history")
	}
}

func TestCheckingCandidateIsNeverPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := New(path)
	now := time.Now()
	store.now = func() time.Time { return now }
	_ = store.Update(false, func(state *State) {
		state.Candidates["checking"] = model.Candidate{ID: "checking", State: model.CandidateChecking, FirstAt: now}
	})
	if err := store.Update(true, func(state *State) {
		state.History = append(state.History, model.HistoryEntry{At: now, Action: "unrelated"})
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk diskState
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Candidates) != 0 {
		t.Fatalf("checking candidate reached flash: %+v", disk.Candidates)
	}
}

func TestAdditionsArePrunedAtOneHour(t *testing.T) {
	store := New("")
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if err := store.Update(false, func(state *State) {
		state.Additions = append(state.Additions, now.Add(-2*time.Hour), now.Add(-30*time.Minute), now)
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Snapshot(now).Additions); got != 2 {
		t.Fatalf("additions after first prune=%d", got)
	}
	now = now.Add(2 * time.Hour)
	if err := store.Update(false, func(state *State) {}); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Snapshot(now).Additions); got != 0 {
		t.Fatalf("old additions survived pruning: %d", got)
	}
}

// The candidate and history tables are unbounded (BACKLOG section 5): no
// silent eviction, an "added" card survives list growth and restarts.
func TestHistoryAndCandidatesAreUnbounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := New(path)
	now := time.Now()
	const total = 600 // more than the old 512 cap
	if err := store.Update(true, func(state *State) {
		for i := 0; i < total; i++ {
			state.History = append(state.History, model.HistoryEntry{At: now.Add(time.Duration(i) * time.Second)})
			state.Candidates[fmt.Sprintf("id-%03d", i)] = model.Candidate{
				ID:      fmt.Sprintf("id-%03d", i),
				State:   model.CandidateAdded,
				FirstAt: now.Add(time.Duration(i) * time.Second),
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	state := store.Snapshot(time.Now())
	if len(state.History) != total || len(state.Candidates) != total {
		t.Fatalf("history=%d candidates=%d, want %d each", len(state.History), len(state.Candidates), total)
	}
	restarted := New(path)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	reloaded := restarted.Snapshot(time.Now())
	if len(reloaded.History) != total || len(reloaded.Candidates) != total {
		t.Fatalf("reloaded history=%d candidates=%d", len(reloaded.History), len(reloaded.Candidates))
	}
	for i := 0; i < total; i++ {
		if reloaded.Candidates[fmt.Sprintf("id-%03d", i)].State != model.CandidateAdded {
			t.Fatalf("candidate id-%03d lost its added state", i)
		}
	}
}
