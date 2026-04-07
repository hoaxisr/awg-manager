package ops

import (
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// NewOperator creates the operator for kernel tunnel management.
// Always returns OS4 operator — OS5 now uses NativeWG exclusively.
func NewOperator(
	ndmsClient ndms.Client,
	wgClient wg.Client,
	backendImpl backend.Backend,
	firewallMgr firewall.Manager,
	log *logger.Logger,
) Operator {
	return NewOperatorOS4(ndmsClient, wgClient, backendImpl, firewallMgr, log)
}
