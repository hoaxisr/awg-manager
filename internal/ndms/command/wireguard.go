package command

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type WireguardCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
}

func NewWireguardCommands(p Poster, s *SaveCoordinator, q *query.Queries) *WireguardCommands {
	return &WireguardCommands{poster: p, save: s, queries: q}
}

// SetASCParams sets the AmneziaWG ASC obfuscation parameters. The params
// json.RawMessage must be a JSON object with string values for
// jc/jmin/jmax/s1/s2 and hex strings for h1/h2/h3/h4 (OS ≥ 5.1 adds
// s3/s4/i1-i5). Caller is responsible for firmware-appropriate field set.
func (c *WireguardCommands) SetASCParams(ctx context.Context, name string, params json.RawMessage) error {
	var asc map[string]any
	if err := json.Unmarshal(params, &asc); err != nil {
		return fmt.Errorf("set asc params %s: parse: %w", name, err)
	}
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"wireguard": map[string]any{"asc": asc},
			},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "set asc params "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll)
}
