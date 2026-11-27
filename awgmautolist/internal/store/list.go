// Keyset-paginated list queries for the candidates and history tables.
//
// The cursors are opaque base64 JSON keys anchored to the sort columns, not
// shifting offsets: a cursor names the last row of the previous page, and the
// next page starts strictly after it. Appends between page requests therefore
// never re-issue rows the caller already saw (BACKLOG section 5).
package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

const (
	// DefaultListLimit is the page size when the request omits limit.
	DefaultListLimit = 128
	// MaxListLimit caps one page; larger requests are clamped, not rejected.
	MaxListLimit = 128
)

// Page is one list response: the requested window plus the counters the
// client needs for the «Показать ещё» button and the totals row. Items is
// always non-nil so the wire shape stays stable on an empty page.
type Page[T any] struct {
	Items      []T    `json:"items"`
	Total      int    `json:"total"`
	NextCursor string `json:"nextCursor"`
	HasMore    bool   `json:"hasMore"`
}

const (
	cursorKindCandidates = "c"
	cursorKindHistory    = "h"
)

type candidateCursor struct {
	V  int       `json:"v"`
	K  string    `json:"k"`
	FA time.Time `json:"fa"`
	ID string    `json:"id"`
}

type historyCursor struct {
	V  int       `json:"v"`
	K  string    `json:"k"`
	At time.Time `json:"at"`
	O  int       `json:"o"`
}

func encodeCursor(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(raw string, out any) error {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("invalid cursor: %w", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid cursor: %w", err)
	}
	switch c := out.(type) {
	case *candidateCursor:
		if c.V != 1 || c.K != cursorKindCandidates {
			return errors.New("invalid cursor: not a candidates cursor")
		}
	case *historyCursor:
		if c.V != 1 || c.K != cursorKindHistory {
			return errors.New("invalid cursor: not a history cursor")
		}
	}
	return nil
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return DefaultListLimit
	}
	if limit > MaxListLimit {
		return MaxListLimit
	}
	return limit
}

// candidateInState applies the state filter: "" matches everything, "active"
// is waiting+checking, otherwise the state must match exactly.
func candidateInState(candidate model.Candidate, state string) bool {
	switch state {
	case "":
		return true
	case "active":
		return candidate.State == model.CandidateWaiting || candidate.State == model.CandidateChecking
	default:
		return string(candidate.State) == state
	}
}

func candidateMatchesQuery(candidate model.Candidate, needle string) bool {
	return strings.Contains(strings.ToLower(candidate.Target.Domain), needle) ||
		strings.Contains(strings.ToLower(candidate.Target.Host), needle) ||
		strings.Contains(strings.ToLower(candidate.Target.IP), needle) ||
		strings.Contains(strings.ToLower(candidate.ClientIP), needle) ||
		strings.Contains(strings.ToLower(candidate.ClientMAC), needle)
}

func historyMatchesQuery(entry model.HistoryEntry, needle string) bool {
	return strings.Contains(strings.ToLower(entry.Domain), needle) ||
		strings.Contains(strings.ToLower(entry.Host), needle) ||
		strings.Contains(strings.ToLower(entry.IP), needle) ||
		strings.Contains(strings.ToLower(entry.ClientIP), needle) ||
		strings.Contains(strings.ToLower(entry.ClientMAC), needle)
}

// candidateKeyAfter reports whether candidate sorts strictly after the cursor
// key in the (FirstAt ASC, ID ASC) order.
func candidateKeyAfter(candidate model.Candidate, cursor candidateCursor) bool {
	if candidate.FirstAt.Equal(cursor.FA) {
		return candidate.ID > cursor.ID
	}
	return candidate.FirstAt.After(cursor.FA)
}

// historyKeyAfter reports whether the entry at the given append ordinal sorts
// strictly after the cursor key in the (At DESC, ordinal DESC) order.
func historyKeyAfter(entry model.HistoryEntry, ordinal int, cursor historyCursor) bool {
	if entry.At.Equal(cursor.At) {
		return ordinal < cursor.O
	}
	return entry.At.Before(cursor.At)
}

// CountCandidates returns the number of candidates in the requested state.
func (s *Store) CountCandidates(state string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, candidate := range s.state.Candidates {
		if candidateInState(candidate, state) {
			total++
		}
	}
	return total
}

// ListCandidates returns one page of candidates filtered by state and q,
// sorted by (FirstAt, ID), plus the total number of rows after the filters.
// The query runs under the store lock and materializes only the filtered set,
// never a full snapshot of the state.
func (s *Store) ListCandidates(state, q, cursor string, limit int) (Page[model.Candidate], error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit = clampLimit(limit)
	var after *candidateCursor
	if cursor != "" {
		var parsed candidateCursor
		if err := decodeCursor(cursor, &parsed); err != nil {
			return Page[model.Candidate]{Items: []model.Candidate{}}, err
		}
		after = &parsed
	}
	needle := strings.ToLower(q)
	matched := make([]model.Candidate, 0, len(s.state.Candidates))
	for _, candidate := range s.state.Candidates {
		if !candidateInState(candidate, state) {
			continue
		}
		if needle != "" && !candidateMatchesQuery(candidate, needle) {
			continue
		}
		matched = append(matched, candidate)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].FirstAt.Equal(matched[j].FirstAt) {
			return matched[i].ID < matched[j].ID
		}
		return matched[i].FirstAt.Before(matched[j].FirstAt)
	})

	total := len(matched)
	start := 0
	if after != nil {
		start = sort.Search(len(matched), func(i int) bool {
			return candidateKeyAfter(matched[i], *after)
		})
	}
	end := start + limit
	if end > len(matched) {
		end = len(matched)
	}
	items := matched[start:end]
	if items == nil {
		items = []model.Candidate{}
	}
	page := Page[model.Candidate]{Items: items, Total: total, HasMore: end < len(matched)}
	if page.HasMore && end > start {
		last := items[len(items)-1]
		page.NextCursor = encodeCursor(candidateCursor{V: 1, K: cursorKindCandidates, FA: last.FirstAt, ID: last.ID})
	}
	return page, nil
}

// ListHistory returns one page of history entries filtered by q, newest first
// with the append ordinal as the deterministic tie-breaker for equal At,
// plus the total number of rows after the q filter. The history log is
// append-only, so the ordinal stored in a cursor stays valid until the log is
// cleared; a cursor from before a clear is rejected as stale.
func (s *Store) ListHistory(q, cursor string, limit int) (Page[model.HistoryEntry], error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit = clampLimit(limit)
	var after *historyCursor
	if cursor != "" {
		var parsed historyCursor
		if err := decodeCursor(cursor, &parsed); err != nil {
			return Page[model.HistoryEntry]{Items: []model.HistoryEntry{}}, err
		}
		after = &parsed
	}
	if after != nil && (after.O < 0 || after.O >= len(s.state.History)) {
		return Page[model.HistoryEntry]{Items: []model.HistoryEntry{}}, errors.New("stale cursor: history log no longer contains that page")
	}
	if after != nil && !s.state.History[after.O].At.Equal(after.At) {
		return Page[model.HistoryEntry]{Items: []model.HistoryEntry{}}, errors.New("stale cursor: history log changed at that position")
	}

	needle := strings.ToLower(q)
	items := make([]model.HistoryEntry, 0, limit)
	total := 0
	passed := 0
	lastOrdinal := -1
	for i := len(s.state.History) - 1; i >= 0; i-- {
		entry := s.state.History[i]
		if needle != "" && !historyMatchesQuery(entry, needle) {
			continue
		}
		total++
		if after != nil && !historyKeyAfter(entry, i, *after) {
			continue
		}
		passed++
		if len(items) < limit {
			items = append(items, entry)
			lastOrdinal = i
		}
	}
	page := Page[model.HistoryEntry]{Items: items, Total: total, HasMore: passed > len(items)}
	if page.HasMore && lastOrdinal >= 0 {
		page.NextCursor = encodeCursor(historyCursor{V: 1, K: cursorKindHistory, At: s.state.History[lastOrdinal].At, O: lastOrdinal})
	}
	return page, nil
}
