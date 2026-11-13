// Package store persists candidates, history and the rolling addition budget.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

const HistoryLimit = 512
const CandidateLimit = 512

type State struct {
	Candidates map[string]model.Candidate `json:"-"`
	History    []model.HistoryEntry       `json:"-"`
	Additions  []time.Time                `json:"-"`
}

type diskState struct {
	Candidates []model.Candidate    `json:"candidates"`
	History    []model.HistoryEntry `json:"history"`
	Additions  []time.Time          `json:"additions"`
}

type Store struct {
	mu    sync.Mutex
	path  string
	state State
	now   func() time.Time
}

func New(path string) *Store {
	return &Store{path: path, now: time.Now, state: State{Candidates: map[string]model.Candidate{}, History: []model.HistoryEntry{}, Additions: []time.Time{}}}
}

func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var disk diskState
	if err := json.Unmarshal(data, &disk); err != nil {
		return err
	}
	state := State{Candidates: map[string]model.Candidate{}, History: disk.History, Additions: disk.Additions}
	for _, candidate := range disk.Candidates {
		if candidate.ID != "" && candidate.State != model.CandidateChecking {
			state.Candidates[candidate.ID] = candidate
		}
	}
	if state.History == nil {
		state.History = []model.HistoryEntry{}
	}
	if state.Additions == nil {
		state.Additions = []time.Time{}
	}
	s.state = state
	s.trimLocked()
	return nil
}

// ImportLegacyHistory reads the old JSON array without modifying its file.
func (s *Store) ImportLegacyHistory(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.History) != 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var legacy []model.HistoryEntry
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}
	for _, entry := range legacy {
		if entry.Reason == "" {
			entry.Reason = entry.Why
		}
		s.state.History = append(s.state.History, entry)
	}
	s.trimLocked()
	return nil
}

// Update applies one in-memory transition. persist=false is for checking and
// other high-frequency stages; final decisions use persist=true. A disk error
// does not undo the visible in-memory action and is returned to the caller.
func (s *Store) Update(persist bool, mutate func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mutate(&s.state)
	s.trimLocked()
	if !persist || s.path == "" {
		return nil
	}
	return s.writeLocked()
}

func (s *Store) Snapshot(now time.Time) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(now)
}

func (s *Store) snapshotLocked(now time.Time) State {
	out := State{Candidates: map[string]model.Candidate{}, History: []model.HistoryEntry{}, Additions: []time.Time{}}
	for key, candidate := range s.state.Candidates {
		out.Candidates[key] = candidate
	}
	out.History = append(out.History, s.state.History...)
	cutoff := now.Add(-time.Hour)
	for _, at := range s.state.Additions {
		if at.After(cutoff) {
			out.Additions = append(out.Additions, at)
		}
	}
	return out
}

func (s *Store) CandidatesByFirstSeen(now time.Time) []model.Candidate {
	state := s.Snapshot(now)
	out := make([]model.Candidate, 0, len(state.Candidates))
	for _, candidate := range state.Candidates {
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FirstAt.Equal(out[j].FirstAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].FirstAt.Before(out[j].FirstAt)
	})
	return out
}

func (s *Store) trimLocked() {
	cutoff := s.now().Add(-time.Hour)
	additions := make([]time.Time, 0, len(s.state.Additions))
	for _, at := range s.state.Additions {
		if at.After(cutoff) {
			additions = append(additions, at)
		}
	}
	s.state.Additions = additions
	if len(s.state.History) > HistoryLimit {
		s.state.History = append([]model.HistoryEntry(nil), s.state.History[len(s.state.History)-HistoryLimit:]...)
	}
	if len(s.state.Candidates) > CandidateLimit {
		ids := make([]string, 0, len(s.state.Candidates))
		for id := range s.state.Candidates {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			return s.state.Candidates[ids[i]].FirstAt.Before(s.state.Candidates[ids[j]].FirstAt)
		})
		for len(s.state.Candidates) > CandidateLimit {
			delete(s.state.Candidates, ids[0])
			ids = ids[1:]
		}
	}
}

func (s *Store) writeLocked() error {
	disk := diskState{History: append([]model.HistoryEntry(nil), s.state.History...), Additions: append([]time.Time(nil), s.state.Additions...)}
	for _, candidate := range s.state.Candidates {
		if candidate.State == model.CandidateChecking {
			continue // an unfinished decision must not survive a crash/restart
		}
		disk.Candidates = append(disk.Candidates, candidate)
	}
	sort.Slice(disk.Candidates, func(i, j int) bool { return disk.Candidates[i].ID < disk.Candidates[j].ID })
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".awgmautolist-state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}
