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
	getter  Getter
	log     Logger
	lines   *cache.TTL[struct{}, []string]
	linesSF *cache.SingleFlight[struct{}, []string]
}

func NewRunningConfigStore(g Getter, log Logger) *RunningConfigStore {
	return NewRunningConfigStoreWithTTL(g, log, runningConfigTTL)
}

func NewRunningConfigStoreWithTTL(g Getter, log Logger, ttl time.Duration) *RunningConfigStore {
	if log == nil {
		log = NopLogger()
	}
	return &RunningConfigStore{
		getter:  g, log: log,
		lines:   cache.NewTTL[struct{}, []string](ttl),
		linesSF: cache.NewSingleFlight[struct{}, []string](),
	}
}

func (s *RunningConfigStore) Lines(ctx context.Context) ([]string, error) {
	if v, ok := s.lines.Get(struct{}{}); ok {
		return v, nil
	}
	return s.linesSF.Do(struct{}{}, func() ([]string, error) {
		v, err := s.fetch(ctx)
		if err != nil {
			if stale, ok := s.lines.Peek(struct{}{}); ok {
				s.log.Warnf("running-config fetch failed, serving stale cache: %v", err)
				return stale, nil
			}
			return nil, err
		}
		s.lines.Set(struct{}{}, v)
		return v, nil
	})
}

func (s *RunningConfigStore) InvalidateAll() { s.lines.InvalidateAll() }

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
