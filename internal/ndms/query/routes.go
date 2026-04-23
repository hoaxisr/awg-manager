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
	*cache.ListStore[[]ndms.Route]
	getter Getter
}

func NewRouteStore(g Getter, log Logger) *RouteStore {
	return NewRouteStoreWithTTL(g, log, routeTTL)
}

func NewRouteStoreWithTTL(g Getter, log Logger, ttl time.Duration) *RouteStore {
	s := &RouteStore{getter: g}
	s.ListStore = cache.NewListStore(ttl, log, "routes", s.fetch)
	return s
}

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
