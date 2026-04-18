package query

import (
	"context"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

const routeTTL = 30 * time.Minute

type RouteStore struct {
	getter Getter
	log    Logger
	list   *cache.TTL[struct{}, []ndms.Route]
	listSF *cache.SingleFlight[struct{}, []ndms.Route]
}

func NewRouteStore(g Getter, log Logger) *RouteStore {
	return NewRouteStoreWithTTL(g, log, routeTTL)
}

func NewRouteStoreWithTTL(g Getter, log Logger, ttl time.Duration) *RouteStore {
	if log == nil {
		log = NopLogger()
	}
	return &RouteStore{
		getter: g, log: log,
		list:   cache.NewTTL[struct{}, []ndms.Route](ttl),
		listSF: cache.NewSingleFlight[struct{}, []ndms.Route](),
	}
}

func (s *RouteStore) List(ctx context.Context) ([]ndms.Route, error) {
	if v, ok := s.list.Get(struct{}{}); ok {
		return v, nil
	}
	return s.listSF.Do(struct{}{}, func() ([]ndms.Route, error) {
		v, err := s.fetch(ctx)
		if err != nil {
			if stale, ok := s.list.Peek(struct{}{}); ok {
				s.log.Warnf("routes fetch failed, serving stale cache: %v", err)
				return stale, nil
			}
			return nil, err
		}
		s.list.Set(struct{}{}, v)
		return v, nil
	})
}

func (s *RouteStore) InvalidateAll() { s.list.InvalidateAll() }

// GetDefaultGatewayInterface returns the NDMS interface name carrying the
// IPv4 default route (0.0.0.0/0 or "default"). Returns ErrNoDefaultRoute
// when no default route is active.
func (s *RouteStore) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	routes, err := s.List(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range routes {
		if r.Destination == "0.0.0.0/0" || r.Destination == "default" {
			return r.Interface, nil
		}
	}
	return "", ErrNoDefaultRoute
}

func (s *RouteStore) fetch(ctx context.Context) ([]ndms.Route, error) {
	var wire []ndms.Route
	if err := s.getter.Get(ctx, "/show/ip/route", &wire); err != nil {
		return nil, fmt.Errorf("fetch routes: %w", err)
	}
	return wire, nil
}
