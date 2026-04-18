package ops

import (
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// NewOperator creates the operator for kernel tunnel management.
// Returns OS5 operator on Keenetic OS 5+ (uses OpkgTun two-layer arch),
// OS4 operator on Keenetic OS 4 (direct ip commands, NDMS via CQRS layer).
func NewOperator(
	queries *query.Queries,
	commands *command.Commands,
	wgClient wg.Client,
	backendImpl backend.Backend,
	firewallMgr firewall.Manager,
	log *logger.Logger,
) Operator {
	if osdetect.Is5() {
		return NewOperatorOS5(queries, commands, wgClient, backendImpl, firewallMgr, log)
	}
	return NewOperatorOS4(queries, commands, wgClient, backendImpl, firewallMgr, log)
}
