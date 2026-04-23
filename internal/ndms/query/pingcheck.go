package query

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

const (
	pingCheckProfileTTL = 60 * time.Minute
	pingCheckStatusTTL  = 30 * time.Second
)

// pingCheckEntryWire mirrors one element of /show/ping-check/.ping-check.
type pingCheckEntryWire struct {
	Profile        string   `json:"profile"`
	Host           []string `json:"host"`
	Mode           string   `json:"mode"`
	UpdateInterval int      `json:"update-interval"`
	MaxFails       int      `json:"max-fails"`
	MinSuccess     int      `json:"min-success"`
	Timeout        int      `json:"timeout"`
	Port           int      `json:"port"`
	Interface      map[string]struct {
		SuccessCount int    `json:"successcount"`
		FailCount    int    `json:"failcount"`
		Status       string `json:"status"`
	} `json:"interface"`
}

type pingCheckListWire struct {
	PingCheck []pingCheckEntryWire `json:"pingcheck"`
}

// fetchPingCheck is shared by both stores — they cache different views
// of the same response.
func fetchPingCheck(ctx context.Context, getter Getter) ([]pingCheckEntryWire, error) {
	var raw pingCheckListWire
	if err := getter.Get(ctx, "/show/ping-check/", &raw); err != nil {
		return nil, fmt.Errorf("fetch ping-check: %w", err)
	}
	return raw.PingCheck, nil
}

// PingCheckProfileStore exposes the profile list (stable config).
type PingCheckProfileStore struct {
	*cache.ListStore[[]ndms.PingCheckProfile]
	getter Getter
}

func NewPingCheckProfileStore(g Getter, log Logger) *PingCheckProfileStore {
	return NewPingCheckProfileStoreWithTTL(g, log, pingCheckProfileTTL)
}

func NewPingCheckProfileStoreWithTTL(g Getter, log Logger, ttl time.Duration) *PingCheckProfileStore {
	s := &PingCheckProfileStore{getter: g}
	s.ListStore = cache.NewListStore(ttl, log, "ping-check profiles", s.fetch)
	return s
}

func (s *PingCheckProfileStore) fetch(ctx context.Context) ([]ndms.PingCheckProfile, error) {
	raw, err := fetchPingCheck(ctx, s.getter)
	if err != nil {
		return nil, err
	}
	out := make([]ndms.PingCheckProfile, 0, len(raw))
	for _, e := range raw {
		out = append(out, ndms.PingCheckProfile{
			Profile:        e.Profile,
			Host:           append([]string(nil), e.Host...),
			Mode:           e.Mode,
			UpdateInterval: e.UpdateInterval,
			MaxFails:       e.MaxFails,
			MinSuccess:     e.MinSuccess,
			Timeout:        e.Timeout,
			Port:           e.Port,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Profile < out[j].Profile })
	return out, nil
}

// PingCheckStatusStore exposes runtime counters, one row per
// (profile, interface) pair.
type PingCheckStatusStore struct {
	*cache.ListStore[[]ndms.PingCheckStatus]
	getter Getter
}

func NewPingCheckStatusStore(g Getter, log Logger) *PingCheckStatusStore {
	return NewPingCheckStatusStoreWithTTL(g, log, pingCheckStatusTTL)
}

func NewPingCheckStatusStoreWithTTL(g Getter, log Logger, ttl time.Duration) *PingCheckStatusStore {
	s := &PingCheckStatusStore{getter: g}
	s.ListStore = cache.NewListStore(ttl, log, "ping-check status", s.fetch)
	return s
}

func (s *PingCheckStatusStore) fetch(ctx context.Context) ([]ndms.PingCheckStatus, error) {
	raw, err := fetchPingCheck(ctx, s.getter)
	if err != nil {
		return nil, err
	}
	out := []ndms.PingCheckStatus{}
	for _, e := range raw {
		for iface, st := range e.Interface {
			out = append(out, ndms.PingCheckStatus{
				Profile:      e.Profile,
				Interface:    iface,
				Status:       st.Status,
				SuccessCount: st.SuccessCount,
				FailCount:    st.FailCount,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Profile != out[j].Profile {
			return out[i].Profile < out[j].Profile
		}
		return out[i].Interface < out[j].Interface
	})
	return out, nil
}
