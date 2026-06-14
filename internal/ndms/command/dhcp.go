package command

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// DHCPCommands manages DHCP pool DNS delivery (fakeip-tun mode: hand clients the
// sing-box tun DNS address). Uses the RCI "parse" form for `ip dhcp pool`, which
// has no structured RCI endpoint. Verified live: `ip dhcp pool <p> dns-server <ip>`.
type DHCPCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
}

func NewDHCPCommands(p Poster, s *SaveCoordinator, q *query.Queries) *DHCPCommands {
	return &DHCPCommands{poster: p, save: s, queries: q}
}

// SetPoolDNS sets the DHCP-advertised DNS servers for a pool (ordered: primary first).
func (c *DHCPCommands) SetPoolDNS(ctx context.Context, pool string, servers []string) error {
	if pool == "" || len(servers) == 0 {
		return fmt.Errorf("dhcp set pool dns: pool and at least one server required")
	}
	cmd := fmt.Sprintf("ip dhcp pool %s dns-server %s", pool, strings.Join(servers, " "))
	return postMutation(ctx, c.poster, c.save, map[string]any{"parse": cmd}, "dhcp set pool dns "+pool,
		c.queries.RunningConfig.InvalidateAll)
}

// ClearPoolDNS removes the custom DNS so the pool falls back to default.
func (c *DHCPCommands) ClearPoolDNS(ctx context.Context, pool string) error {
	if pool == "" {
		return fmt.Errorf("dhcp clear pool dns: pool required")
	}
	cmd := fmt.Sprintf("ip dhcp pool %s no dns-server", pool)
	return postMutation(ctx, c.poster, c.save, map[string]any{"parse": cmd}, "dhcp clear pool dns "+pool,
		c.queries.RunningConfig.InvalidateAll)
}
