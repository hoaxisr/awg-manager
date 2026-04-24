// internal/deviceproxy/ndms_adapter.go
package deviceproxy

import (
	"context"
	"fmt"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// NDMSAdapter satisfies NDMSInterfaceQuery by delegating to the
// existing ndms/query.Interfaces store.
type NDMSAdapter struct {
	q *ndmsquery.Queries
}

func NewNDMSAdapter(q *ndmsquery.Queries) *NDMSAdapter { return &NDMSAdapter{q: q} }

func (a *NDMSAdapter) GetInterfaceAddress(ctx context.Context, ndmsID string) (string, error) {
	iface, err := a.q.Interfaces.Get(ctx, ndmsID)
	if err != nil {
		return "", err
	}
	if iface == nil || iface.Address == "" {
		return "", fmt.Errorf("interface %q has no IPv4 address", ndmsID)
	}
	return iface.Address, nil
}

// ListBridges returns all Bridge interfaces with their current IPv4
// address. Used by Service.ListenChoices to populate the inbound
// interface dropdown and derive the LAN IP.
func (a *NDMSAdapter) ListBridges(ctx context.Context) ([]BridgeChoice, error) {
	all, err := a.q.Interfaces.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []BridgeChoice{}
	for _, iface := range all {
		if iface.Type != "Bridge" {
			continue
		}
		label := iface.Description
		if label == "" {
			label = iface.ID
		}
		out = append(out, BridgeChoice{ID: iface.ID, Label: label, IP: iface.Address})
	}
	return out, nil
}
