package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

func seedCandidates(t *testing.T, s *Store, n int) []model.Candidate {
	t.Helper()
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	seeded := make([]model.Candidate, 0, n)
	err := s.Update(false, func(state *State) {
		for i := 0; i < n; i++ {
			candidate := model.Candidate{
				ID:        fmt.Sprintf("id-%04d", i),
				State:     model.CandidateWaiting,
				FirstAt:   base.Add(time.Duration(i) * time.Second),
				Target:    model.Target{Domain: fmt.Sprintf("host-%04d.example.com", i), Host: fmt.Sprintf("www.host-%04d.example.com", i), IP: fmt.Sprintf("10.0.%d.%d", i/256, i%256)},
				ClientIP:  fmt.Sprintf("192.168.1.%d", i%256),
				ClientMAC: fmt.Sprintf("4c:9f:f1:b9:%02x:%02x", i/256, i%256),
			}
			state.Candidates[candidate.ID] = candidate
			seeded = append(seeded, candidate)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return seeded
}

func seedHistory(t *testing.T, s *Store, n int) []model.HistoryEntry {
	t.Helper()
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	seeded := make([]model.HistoryEntry, 0, n)
	err := s.Update(false, func(state *State) {
		for i := 0; i < n; i++ {
			entry := model.HistoryEntry{
				At:        base.Add(time.Duration(i) * time.Second),
				Domain:    fmt.Sprintf("hist-%04d.example.com", i),
				Host:      fmt.Sprintf("www.hist-%04d.example.com", i),
				IP:        fmt.Sprintf("10.1.%d.%d", i/256, i%256),
				ClientIP:  fmt.Sprintf("192.168.2.%d", i%256),
				ClientMAC: fmt.Sprintf("4c:9f:f1:ba:%02x:%02x", i/256, i%256),
				Action:    "added",
			}
			state.History = append(state.History, entry)
			seeded = append(seeded, entry)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return seeded
}

func candidateIDs(page Page[model.Candidate]) []string {
	ids := make([]string, 0, len(page.Items))
	for _, candidate := range page.Items {
		ids = append(ids, candidate.ID)
	}
	return ids
}

func TestListCandidatesPagesOf128(t *testing.T) {
	store := New("")
	const total = 300
	seeded := seedCandidates(t, store, total)

	page1, err := store.ListCandidates("", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 128 || page1.Total != total || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("page1=%+v", page1)
	}
	page2, err := store.ListCandidates("", "", page1.NextCursor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 128 || page2.Total != total || !page2.HasMore {
		t.Fatalf("page2=%+v", page2)
	}
	page3, err := store.ListCandidates("", "", page2.NextCursor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page3.Items) != total-256 || page3.Total != total || page3.HasMore || page3.NextCursor != "" {
		t.Fatalf("page3=%+v", page3)
	}

	// Pages concatenate into exactly the seeded (FirstAt ASC, ID ASC) order.
	seen := append(append(candidateIDs(page1), candidateIDs(page2)...), candidateIDs(page3)...)
	if len(seen) != total {
		t.Fatalf("seen=%d", len(seen))
	}
	for i, id := range seen {
		if id != seeded[i].ID {
			t.Fatalf("position %d: got %s want %s", i, id, seeded[i].ID)
		}
	}
}

func TestListCandidatesLimitIsClamped(t *testing.T) {
	store := New("")
	seedCandidates(t, store, 300)
	for _, limit := range []int{0, -5, 1 << 20} {
		page, err := store.ListCandidates("", "", "", limit)
		if err != nil {
			t.Fatal(err)
		}
		if want := min(300, MaxListLimit); len(page.Items) != want {
			t.Fatalf("limit=%d page=%d, want %d", limit, len(page.Items), want)
		}
	}
	page, err := store.ListCandidates("", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 10 || !page.HasMore {
		t.Fatalf("explicit small limit ignored: %+v", page)
	}
}

func TestListCandidatesStateFilter(t *testing.T) {
	store := New("")
	seedCandidates(t, store, 10)
	err := store.Update(false, func(state *State) {
		state.Candidates["id-0000"] = withState(state.Candidates["id-0000"], model.CandidateAdded)
		state.Candidates["id-0001"] = withState(state.Candidates["id-0001"], model.CandidateChecking)
		state.Candidates["id-0002"] = withState(state.Candidates["id-0002"], model.CandidateRejected)
	})
	if err != nil {
		t.Fatal(err)
	}
	for state, wantIDs := range map[string][]string{
		"":         {"id-0000", "id-0001", "id-0002", "id-0003", "id-0004", "id-0005", "id-0006", "id-0007", "id-0008", "id-0009"},
		"active":   {"id-0001", "id-0003", "id-0004", "id-0005", "id-0006", "id-0007", "id-0008", "id-0009"},
		"waiting":  {"id-0003", "id-0004", "id-0005", "id-0006", "id-0007", "id-0008", "id-0009"},
		"added":    {"id-0000"},
		"checking": {"id-0001"},
		"rejected": {"id-0002"},
	} {
		page, err := store.ListCandidates(state, "", "", 128)
		if err != nil {
			t.Fatal(err)
		}
		got := candidateIDs(page)
		if page.Total != len(wantIDs) || strings.Join(got, ",") != strings.Join(wantIDs, ",") {
			t.Fatalf("state=%q got=%v want=%v total=%d", state, got, wantIDs, page.Total)
		}
	}
}

func withState(candidate model.Candidate, state model.CandidateState) model.Candidate {
	candidate.State = state
	return candidate
}

func TestListCandidatesSearchBeyondFirstPage(t *testing.T) {
	store := New("")
	seedCandidates(t, store, 400)
	// Only candidates ranked after the first 128 carry the needle in Host.
	err := store.Update(false, func(state *State) {
		for i := 250; i < 400; i++ {
			candidate := state.Candidates[fmt.Sprintf("id-%04d", i)]
			candidate.Target.Host = fmt.Sprintf("needle-%04d.example.com", i)
			state.Candidates[candidate.ID] = candidate
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	page1, err := store.ListCandidates("", "NEEDLE", "", 128)
	if err != nil {
		t.Fatal(err)
	}
	if page1.Total != 150 || len(page1.Items) != 128 || !page1.HasMore {
		t.Fatalf("page1 total=%d items=%d", page1.Total, len(page1.Items))
	}
	for _, candidate := range page1.Items {
		if !strings.Contains(strings.ToLower(candidate.Target.Host), "needle") {
			t.Fatalf("q matched a non-needle row: %+v", candidate.Target)
		}
	}
	page2, err := store.ListCandidates("", "needle", page1.NextCursor, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 22 || page2.HasMore || page2.Total != 150 {
		t.Fatalf("page2=%+v", page2)
	}
}

func TestListCandidatesSearchMatchesFlatFields(t *testing.T) {
	store := New("")
	seedCandidates(t, store, 20)
	for _, tc := range []struct {
		q    string
		want int
	}{
		{"HOST-0007.example.com", 1},
		{"10.0.0.9", 1},
		{"192.168.1.10", 1},
		{"4C:9F:F1:B9:00:11", 1},
	} {
		page, err := store.ListCandidates("", tc.q, "", 128)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != tc.want || len(page.Items) != tc.want {
			t.Fatalf("q=%q total=%d items=%d, want %d", tc.q, page.Total, len(page.Items), tc.want)
		}
	}
}

// New candidates appended between page requests must not re-issue rows the
// caller already saw: the keyset cursor pins the position, appends land after it.
func TestListCandidatesAppendBetweenPages(t *testing.T) {
	store := New("")
	seeded := seedCandidates(t, store, 300)
	page1, err := store.ListCandidates("", "", "", 128)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(false, func(state *State) {
		base := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)
		for i := 0; i < 5; i++ {
			id := fmt.Sprintf("id-late-%d", i)
			state.Candidates[id] = model.Candidate{ID: id, State: model.CandidateWaiting, FirstAt: base.Add(time.Duration(i) * time.Second)}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	page2, err := store.ListCandidates("", "", page1.NextCursor, 128)
	if err != nil {
		t.Fatal(err)
	}
	if page2.Total != 305 || len(page2.Items) != 128 {
		t.Fatalf("page2 total=%d items=%d", page2.Total, len(page2.Items))
	}
	seen := map[string]bool{}
	for _, id := range append(candidateIDs(page1), candidateIDs(page2)...) {
		if seen[id] {
			t.Fatalf("duplicate row across pages: %s", id)
		}
		seen[id] = true
	}
	for i := 128; i < 256; i++ {
		if !seen[seeded[i].ID] {
			t.Fatalf("seeded row %s missing after an interleave append", seeded[i].ID)
		}
	}
}

func TestListCandidatesRejectsBadCursor(t *testing.T) {
	store := New("")
	seedCandidates(t, store, 10)
	if _, err := store.ListCandidates("", "", "not-base64!!!", 128); err == nil {
		t.Fatal("garbage cursor must be rejected")
	}
	// A history cursor has the right shape but the wrong kind discriminator.
	seedHistory(t, store, 10)
	historyPage, err := store.ListHistory("", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListCandidates("", "", historyPage.NextCursor, 128); err == nil {
		t.Fatal("cross-kind cursor must be rejected")
	}
}

func TestListHistoryNewestFirstWithTieBreaker(t *testing.T) {
	store := New("")
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	err := store.Update(false, func(state *State) {
		for i := 0; i < 10; i++ {
			// Pairs share At; the later append must come first.
			state.History = append(state.History, model.HistoryEntry{At: base.Add(time.Duration(i/2) * time.Second), Action: fmt.Sprintf("a-%d", i)})
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListHistory("", "", 128)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 10 {
		t.Fatalf("total=%d", page.Total)
	}
	want := []string{"a-9", "a-8", "a-7", "a-6", "a-5", "a-4", "a-3", "a-2", "a-1", "a-0"}
	for i, entry := range page.Items {
		if entry.Action != want[i] {
			t.Fatalf("position %d: got %s want %s", i, entry.Action, want[i])
		}
	}
}

func TestListHistoryPagesAndSearch(t *testing.T) {
	store := New("")
	const total = 300
	seeded := seedHistory(t, store, total)
	page1, err := store.ListHistory("", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 128 || page1.Total != total || !page1.HasMore {
		t.Fatalf("page1=%+v", page1)
	}
	if !page1.Items[0].At.Equal(seeded[total-1].At) {
		t.Fatalf("newest first broken: %+v", page1.Items[0])
	}
	page2, err := store.ListHistory("", page1.NextCursor, 128)
	if err != nil {
		t.Fatal(err)
	}
	page3, err := store.ListHistory("", page2.NextCursor, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 128 || len(page3.Items) != total-256 || page3.HasMore {
		t.Fatalf("page2=%d page3=%d", len(page2.Items), len(page3.Items))
	}
	seen := map[time.Time]bool{}
	for _, page := range []Page[model.HistoryEntry]{page1, page2, page3} {
		for _, entry := range page.Items {
			if seen[entry.At] {
				t.Fatalf("duplicate entry at %s", entry.At)
			}
			seen[entry.At] = true
		}
	}

	needle, err := store.ListHistory("HIST-0299.example.com", "", 128)
	if err != nil {
		t.Fatal(err)
	}
	if needle.Total != 1 || needle.Items[0].Domain != "hist-0299.example.com" {
		t.Fatalf("needle=%+v", needle)
	}
	// Case-insensitive substring on the flat client fields.
	byMAC, err := store.ListHistory("4C:9F:F1:BA:01:2B", "", 128)
	if err != nil {
		t.Fatal(err)
	}
	if byMAC.Total != 1 || byMAC.Items[0].ClientMAC != "4c:9f:f1:ba:01:2b" {
		t.Fatalf("byMAC=%+v", byMAC)
	}
}

// Appends between history pages never duplicate issued rows: the newest
// entries land before the cursor and wait for the next first page.
func TestListHistoryAppendBetweenPages(t *testing.T) {
	store := New("")
	const total = 300
	seedHistory(t, store, total)
	page1, err := store.ListHistory("", "", 128)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(false, func(state *State) {
		base := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)
		for i := 0; i < 5; i++ {
			state.History = append(state.History, model.HistoryEntry{At: base.Add(time.Duration(i) * time.Second), Action: "late"})
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	page2, err := store.ListHistory("", page1.NextCursor, 128)
	if err != nil {
		t.Fatal(err)
	}
	if page2.Total != total+5 {
		t.Fatalf("total=%d want %d", page2.Total, total+5)
	}
	seen := map[string]bool{}
	for _, page := range []Page[model.HistoryEntry]{page1, page2} {
		for _, entry := range page.Items {
			key := entry.At.Format(time.RFC3339Nano) + "|" + entry.Domain + "|" + entry.Action
			if seen[key] {
				t.Fatalf("duplicate row across pages: %s", key)
			}
			seen[key] = true
		}
	}
	if len(seen) != 256 {
		t.Fatalf("unique rows=%d want 256", len(seen))
	}
}

// A cursor taken before a clear no longer maps into the log and is rejected,
// so the client restarts pagination from a fresh first page.
func TestListHistoryStaleCursorAfterClear(t *testing.T) {
	store := New("")
	seedHistory(t, store, 50)
	page1, err := store.ListHistory("", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(false, func(state *State) { state.History = []model.HistoryEntry{} }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListHistory("", page1.NextCursor, 10); err == nil {
		t.Fatal("stale cursor after clear must be rejected")
	}
	// The fresh first page works and reflects the cleared log.
	fresh, err := store.ListHistory("", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Total != 0 || len(fresh.Items) != 0 || fresh.HasMore {
		t.Fatalf("fresh page=%+v", fresh)
	}
}

func TestListHistoryRejectsBadCursor(t *testing.T) {
	store := New("")
	seedHistory(t, store, 10)
	seedCandidates(t, store, 10)
	if _, err := store.ListHistory("", "garbage!!!", 128); err == nil {
		t.Fatal("garbage cursor must be rejected")
	}
	page, err := store.ListCandidates("", "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListHistory("", page.NextCursor, 128); err == nil {
		t.Fatal("cross-kind cursor must be rejected")
	}
}
