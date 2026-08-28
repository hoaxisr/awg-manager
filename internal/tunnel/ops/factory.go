package ops

import (
	"context"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// Backend manages the lifecycle of a tunnel network interface.
// Implemented by *backend.KernelBackend; an interface for test doubles.
type Backend interface {
	Start(ctx context.Context, ifaceName string) error
	Stop(ctx context.Context, ifaceName string) error
	WaitReady(ctx context.Context, ifaceName string, timeout time.Duration) error
}

// NewOperator creates the operator for kernel tunnel management.
// Returns OS5 operator on Keenetic OS 5+ (uses OpkgTun two-layer arch),
// OS4 operator on Keenetic OS 4 (direct ip commands, NDMS via CQRS layer).
func NewOperator(
	queries *query.Queries,
	commands *command.Commands,
	wgClient wg.Client,
	backendImpl Backend,
	firewallMgr firewall.Manager,
) Operator {
	if osdetect.Is5() {
		return NewOperatorOS5(queries, commands, wgClient, backendImpl, firewallMgr)
	}
	return NewOperatorOS4(queries, commands, wgClient, backendImpl, firewallMgr)
}
