package state

import (
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// StateInputs holds the inputs for the v2 state matrix.
// Uses NDMS intent (conf layer) instead of the unreliable state: field.
type StateInputs struct {
	HasNDMS        bool
	OpkgTunExists  bool
	Intent         ndms.InterfaceIntent
	LinkUp         bool
	ProcessRunning bool
	HasPeer        bool
}

// StateMatrixV2 determines tunnel state using NDMS conf layer (intent).
type StateMatrixV2 struct{}

// DetermineState applies the v2 state matrix to inputs.
//
// OS4 decision tree (no NDMS):
//
//	process + link up + peer → Running
//	process (link down or no peer) → Starting
//	no process                     → Stopped
//
// OS5 decision tree (NDMS):
//
//	!OpkgTun              → NotCreated
//	Intent=UP, process, link up → Running
//	Intent=UP, process, no link → Starting
//	Intent=UP, no process       → NeedsStart
//	Intent=DOWN, process        → NeedsStop
//	Intent=DOWN, no process     → Disabled
func (StateMatrixV2) DetermineState(input StateInputs) tunnel.State {
	if !input.HasNDMS {
		// OS4 / lightweight: determine state from process + link + peer
		if input.ProcessRunning {
			if input.LinkUp && input.HasPeer {
				return tunnel.StateRunning
			}
			return tunnel.StateStarting
		}
		return tunnel.StateStopped
	}

	// OS5: full NDMS-based state detection
	if !input.OpkgTunExists {
		return tunnel.StateNotCreated
	}

	if input.Intent == ndms.IntentUp {
		if input.ProcessRunning && input.LinkUp {
			return tunnel.StateRunning
		}
		if input.ProcessRunning {
			return tunnel.StateStarting
		}
		return tunnel.StateNeedsStart
	}

	// Intent == IntentDown
	if input.ProcessRunning {
		return tunnel.StateNeedsStop
	}
	return tunnel.StateDisabled
}
