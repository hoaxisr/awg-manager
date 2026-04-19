package query

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

const runningConfigTTL = 60 * time.Minute

type RunningConfigStore struct {
	*cache.ListStore[[]string]
	getter Getter
}

func NewRunningConfigStore(g Getter, log Logger) *RunningConfigStore {
	return NewRunningConfigStoreWithTTL(g, log, runningConfigTTL)
}

func NewRunningConfigStoreWithTTL(g Getter, log Logger, ttl time.Duration) *RunningConfigStore {
	s := &RunningConfigStore{getter: g}
	s.ListStore = cache.NewListStore(ttl, log, "running-config", s.fetch)
	return s
}

// Lines returns the cached /show/running-config message lines. Thin
// alias over the promoted ListStore.List — callers use "lines" in the
// running-config domain rather than the generic "list".
func (s *RunningConfigStore) Lines(ctx context.Context) ([]string, error) {
	return s.ListStore.List(ctx)
}

type rcResp struct {
	Message []string `json:"message"`
}

func (s *RunningConfigStore) fetch(ctx context.Context) ([]string, error) {
	raw, err := s.getter.GetRaw(ctx, "/show/running-config")
	if err != nil {
		return nil, fmt.Errorf("fetch running-config: %w", err)
	}
	var resp rcResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse running-config: %w", err)
	}
	return resp.Message, nil
}
