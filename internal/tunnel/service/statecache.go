package service

import (
	"context"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

const (
	// stateCacheTTL ограничивает stale-окно для read-путей: внешние
	// изменения состояния видны с лагом не больше TTL; собственные
	// мутации и действия оркестратора инвалидируют запись немедленно.
	stateCacheTTL = 2 * time.Second
	// stateFetchTimeout ограничивает fetch, отвязанный от ctx вызывающего.
	stateFetchTimeout = 5 * time.Second
)

// rawState returns the backend-raw StateInfo for stored through the 2s
// TTL + singleflight cache. Read paths only (GetState/Get/List/
// RunningTunnels) — lifecycle code and the orchestrator keep hitting the
// live managers: state decisions are never made off the cache.
func (s *ServiceImpl) rawState(ctx context.Context, stored *storage.AWGTunnel) tunnel.StateInfo {
	if s.stateCache == nil {
		// Tests building a bare ServiceImpl skip the cache.
		info, _ := s.fetchRawStateByID(ctx, stored.ID)
		return info
	}
	info, _ := s.stateCache.Get(ctx, stored.ID)
	return info
}

// fetchRawStateByID is the KeyedStore fetch: it re-reads stored (cheap,
// in-memory) and detaches from the caller's context — a cancelled UI
// request must not poison the coalesced result for waiting pollers.
func (s *ServiceImpl) fetchRawStateByID(ctx context.Context, tunnelID string) (tunnel.StateInfo, error) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stateFetchTimeout)
	defer cancel()
	stored, err := s.store.Get(tunnelID)
	if err != nil || stored == nil {
		return tunnel.StateInfo{State: tunnel.StateUnknown}, nil
	}
	// WDTT Raw registry rows are metadata-only; they must not hit the kernel
	// state matrix (wdttraw-* IDs map to OpkgTun0 via extractTunnelNum).
	if stored.Backend == "wdtt-raw" {
		return wdttRawKernelState(stored), nil
	}
	if s.nwgState != nil && s.isNativeWG(stored) {
		return s.nwgState.GetState(fctx, stored), nil
	}
	return s.state.GetState(fctx, stored.ID), nil
}

func (s *ServiceImpl) invalidateState(tunnelID string) {
	if s.stateCache != nil {
		s.stateCache.Invalidate(tunnelID)
	}
}

// startStateInvalidator drops cached state on orchestrator-driven
// transitions (recovery, boot) that don't pass through Service methods —
// the SSE-triggered UI refetch must not read a pre-transition snapshot.
// Dropped bus events degrade gracefully: staleness is bounded by the TTL.
func (s *ServiceImpl) startStateInvalidator(bus *events.Bus) {
	if s.stateCache == nil || bus == nil {
		return
	}
	s.invalidatorOnce.Do(func() {
		_, ch, _ := bus.Subscribe()
		go func() {
			for ev := range ch {
				switch ev.Type {
				case "tunnel:state", "tunnel:deleted":
					switch e := ev.Data.(type) {
					case events.TunnelStateEvent:
						if e.ID != "" {
							s.stateCache.Invalidate(e.ID)
						} else {
							s.stateCache.InvalidateAll()
						}
					case events.TunnelDeletedEvent:
						if e.ID != "" {
							s.stateCache.Invalidate(e.ID)
						} else {
							s.stateCache.InvalidateAll()
						}
					default:
						s.stateCache.InvalidateAll()
					}
				}
			}
		}()
	})
}
