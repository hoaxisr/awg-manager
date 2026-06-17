package command

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// NATCommands wraps segment NAT (`ip nat`/`no ip nat`) and Static NAT
// (`ip static`/`no ip static`) RCI mutations. Payloads are ported verbatim
// from managed/rci.go (rciSetNAT / rciSetStaticNAT) — see Task PE-B.
type NATCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
}

func NewNATCommands(p Poster, s *SaveCoordinator, q *query.Queries) *NATCommands {
	return &NATCommands{poster: p, save: s, queries: q}
}

// SetSegmentNAT enables dynamic NAT (masquerade) for a segment.
func (c *NATCommands) SetSegmentNAT(ctx context.Context, seg string) error {
	return c.mutate(ctx, map[string]any{"ip": map[string]any{"nat": map[string]any{"interface": seg}}}, "ip nat "+seg)
}

// RemoveSegmentNAT disables dynamic NAT for a segment.
func (c *NATCommands) RemoveSegmentNAT(ctx context.Context, seg string) error {
	return c.mutate(ctx, map[string]any{"ip": map[string]any{"nat": []map[string]any{{"no": true, "interface": seg}}}}, "no ip nat "+seg)
}

// SetStaticNAT adds Static NAT (SNAT-only) from a segment to a WAN interface.
func (c *NATCommands) SetStaticNAT(ctx context.Context, seg, wan string) error {
	return c.mutate(ctx, map[string]any{"ip": map[string]any{"static": map[string]any{"interface": seg, "to-interface": wan}}}, "ip static "+seg+" "+wan)
}

// RemoveStaticNAT removes Static NAT from a segment to a WAN interface.
func (c *NATCommands) RemoveStaticNAT(ctx context.Context, seg, wan string) error {
	return c.mutate(ctx, map[string]any{"ip": map[string]any{"static": []map[string]any{{"no": true, "interface": seg, "to-interface": wan}}}}, "no ip static "+seg+" "+wan)
}

// mutate posts the payload, schedules a save, and invalidates RunningConfig
// (the cache affected by NAT/static-NAT changes).
func (c *NATCommands) mutate(ctx context.Context, payload any, op string) error {
	return postMutation(ctx, c.poster, c.save, payload, op,
		c.queries.RunningConfig.InvalidateAll)
}
