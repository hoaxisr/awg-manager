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
