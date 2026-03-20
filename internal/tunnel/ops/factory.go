package ops

import (
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// NewOperator creates the appropriate operator for the current OS.
func NewOperator(
	ndmsClient ndms.Client,
	wgClient wg.Client,
	backendImpl backend.Backend,
	firewallMgr firewall.Manager,
	log *logger.Logger,
) Operator {
	if osdetect.Is5() {
		return NewOperatorOS5(ndmsClient, wgClient, backendImpl, firewallMgr, log)
	}
	return NewOperatorOS4(ndmsClient, wgClient, backendImpl, firewallMgr, log)
}
