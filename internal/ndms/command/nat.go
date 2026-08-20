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
//
// Снятие терпит «unknown interface»: сегмент или WAN-выход мог исчезнуть раньше
// правила (проверено на роутере — NDMS отвечает ошибкой на обе стороны пары).
func (c *NATCommands) RemoveStaticNAT(ctx context.Context, seg, wan string) error {
	return c.mutateTolerant(ctx, map[string]any{"ip": map[string]any{"static": []map[string]any{{"no": true, "interface": seg, "to-interface": wan}}}}, "no ip static "+seg+" "+wan, isUnknownInterface)
}

// mutate posts the payload, schedules a save, and invalidates the caches
// affected by NAT/static-NAT changes: RunningConfig плюс сами NAT-сторы (TTL
// 30 с) — иначе source-preserve читал бы собственную мутацию как дрейф и
// применял бы её повторно.
func (c *NATCommands) mutate(ctx context.Context, payload any, op string) error {
	return c.mutateTolerant(ctx, payload, op, nil)
}

// mutateTolerant — mutate, признающий часть отказов безобидными (см. tolerate.go).
func (c *NATCommands) mutateTolerant(ctx context.Context, payload any, op string, tolerate func(string) bool) error {
	return postMutationCheckedTolerant(ctx, c.poster, c.save, payload, op, tolerate,
		c.queries.RunningConfig.InvalidateAll,
		c.queries.NAT.InvalidateAll,
		c.queries.StaticNAT.InvalidateAll)
}
