package state

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// ManagerImpl is the implementation of the state Manager.
// It is the SINGLE SOURCE OF TRUTH for tunnel state.
type ManagerImpl struct {
	ndms     ndms.Client
	wg       wg.Client
	backend  backend.Backend
	matrixV2 StateMatrixV2
	// deviceExists checks if a network device exists. Defaults to sysfs check.
	// Override in tests where /sys/class/net is not available.
	deviceExists func(ifaceName string) bool
}

// New creates a new StateManager.
func New(ndmsClient ndms.Client, wgClient wg.Client, backendImpl backend.Backend) *ManagerImpl {
	m := &ManagerImpl{
		ndms:     ndmsClient,
		wg:       wgClient,
		backend:  backendImpl,
		matrixV2: StateMatrixV2{},
	}
	m.deviceExists = m.sysfsDeviceExists
	return m
}

// GetState returns the comprehensive state of a tunnel.
// This is the SINGLE SOURCE OF TRUTH - all state checks go through here.
func (m *ManagerImpl) GetState(ctx context.Context, tunnelID string) tunnel.StateInfo {
	info := tunnel.StateInfo{}
	names := tunnel.NewNames(tunnelID)
	hasNDMS := names.NDMSName != ""

	// 1-2. NDMS queries (OS5 only — OS4 has no NDMS)
	var intent ndms.InterfaceIntent
	var linkUp bool
	var showInterfaceFailed bool
	if !hasNDMS {
		// OS4 / lightweight: check link status via sysfs operstate (fast, no NDMS)
		linkUp = m.sysfsLinkUp(names.IfaceName)
	} else if hasNDMS {
		info.OpkgTunExists = m.ndms.OpkgTunExists(ctx, names.NDMSName)

		if info.OpkgTunExists {
			if raw, err := m.ndms.ShowInterface(ctx, names.NDMSName); err == nil {
				if ifInfo, err := ndms.ParseInterfaceInfo(raw); err == nil {
					intent = ifInfo.Intent()
					linkUp = ifInfo.LinkUp()
				} else {
					showInterfaceFailed = true
				}
			} else {
				showInterfaceFailed = true
			}
			// Populate InterfaceUp for backwards compatibility (API consumers, diagnostics)
			info.InterfaceUp = linkUp
		}
	}

	// 3. Check process/backend status
	info.ProcessRunning, info.ProcessPID = m.backend.IsRunning(ctx, names.IfaceName)

	// 4. Fix zero-value intent fallback: when ShowInterface fails (NDMS busy),
	// intent defaults to IntentDown (0). If the process IS alive, this would
	// produce NeedsStop/Disabled — wrong state that blocks manual Stop.
	// Assume IntentUp when we can't read NDMS but the process is clearly running.
	if hasNDMS && showInterfaceFailed && info.ProcessRunning {
		intent = ndms.IntentUp
	}

	// 5. Check WireGuard state (peer, handshake, traffic)
	// Only if interface exists (process running or device present)
	if info.ProcessRunning || m.deviceExists(names.IfaceName) {
		wgInfo, err := m.wg.Show(ctx, names.IfaceName)
		if err == nil && wgInfo != nil {
			info.HasPeer = wgInfo.HasPeer
			info.HasHandshake = !wgInfo.LastHandshake.IsZero()
			info.LastHandshake = wgInfo.LastHandshake
			info.RxBytes = wgInfo.RxBytes
			info.TxBytes = wgInfo.TxBytes
		}
	}

	// 6. Determine final state using the v2 state matrix
	info.State = m.matrixV2.DetermineState(StateInputs{
		HasNDMS:        hasNDMS,
		OpkgTunExists:  info.OpkgTunExists,
		Intent:         intent,
		LinkUp:         linkUp,
		ProcessRunning: info.ProcessRunning,
		HasPeer:        info.HasPeer,
	})

	// 7. Add backend type
	info.BackendType = m.backend.Type().String()

	// 8. Add diagnostic details
	info.Details = m.buildDetails(info)

	return info
}

// GetStateLightweight returns tunnel state without NDMS queries.
// Uses only process status and WireGuard peer/traffic data.
// Produces Running/Starting/Stopped — caller enriches with storage knowledge.
func (m *ManagerImpl) GetStateLightweight(ctx context.Context, tunnelID string) tunnel.StateInfo {
	info := tunnel.StateInfo{}
	names := tunnel.NewNames(tunnelID)

	// 1. Check process/backend status (PID file — no NDMS)
	info.ProcessRunning, info.ProcessPID = m.backend.IsRunning(ctx, names.IfaceName)

	// 2. Check WireGuard state (only if interface exists)
	if info.ProcessRunning || m.deviceExists(names.IfaceName) {
		wgInfo, err := m.wg.Show(ctx, names.IfaceName)
		if err == nil && wgInfo != nil {
			info.HasPeer = wgInfo.HasPeer
			info.HasHandshake = !wgInfo.LastHandshake.IsZero()
			info.LastHandshake = wgInfo.LastHandshake
			info.RxBytes = wgInfo.RxBytes
			info.TxBytes = wgInfo.TxBytes
		}
	}

	// 2.5. Check link status via sysfs operstate (fast, no NDMS query).
	// Detects kernel KillLink (ip link set down) where sysfs exists but link is down.
	linkUp := m.sysfsLinkUp(names.IfaceName)

	// 3. Determine state using OS4-style logic (process + peer + link)
	info.State = m.matrixV2.DetermineState(StateInputs{
		HasNDMS:        false, // skip NDMS branch
		ProcessRunning: info.ProcessRunning,
		LinkUp:         linkUp,
		HasPeer:        info.HasPeer,
	})

	// 4. Add backend type
	info.BackendType = m.backend.Type().String()

	return info
}

// sysfsDeviceExists checks if a network device exists (via sysfs).
func (m *ManagerImpl) sysfsDeviceExists(ifaceName string) bool {
	_, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", ifaceName))
	return err == nil
}

// sysfsLinkUp checks if a network interface link is up via sysfs operstate.
// WireGuard/AmneziaWG interfaces report "unknown" when running normally
// (no carrier sense), "down" after `ip link set down`.
// Returns true for any operstate except "down".
func (m *ManagerImpl) sysfsLinkUp(ifaceName string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/operstate", ifaceName))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) != "down"
}

// buildDetails creates a human-readable description of the state.
func (m *ManagerImpl) buildDetails(info tunnel.StateInfo) string {
	switch info.State {
	case tunnel.StateNotCreated:
		return "Tunnel has not been created (no OpkgTun in NDMS)"

	case tunnel.StateStopped:
		return "Tunnel is stopped (OpkgTun exists, process dead, interface down)"

	case tunnel.StateRunning:
		if info.HasHandshake {
			return fmt.Sprintf("Tunnel is running (RX: %d, TX: %d)", info.RxBytes, info.TxBytes)
		}
		return "Tunnel is running (no recent handshake)"

	case tunnel.StateBroken:
		return m.buildBrokenDetails(info)

	case tunnel.StateStarting:
		return "Tunnel is starting"

	case tunnel.StateStopping:
		return "Tunnel is stopping"

	case tunnel.StateNeedsStart:
		return "NDMS intent: up, but process not running (needs start)"

	case tunnel.StateNeedsStop:
		return "NDMS intent: disabled, but process still alive (needs stop)"

	case tunnel.StateDisabled:
		return "Tunnel is disabled (NDMS intent: down, all clean)"

	default:
		return "Unknown state"
	}
}

// buildBrokenDetails explains why the tunnel is in broken state.
func (m *ManagerImpl) buildBrokenDetails(info tunnel.StateInfo) string {
	var reasons []string

	if info.ProcessRunning && !info.InterfaceUp {
		reasons = append(reasons, "process running but interface down")
	}
	if !info.ProcessRunning && info.InterfaceUp {
		reasons = append(reasons, "interface up but process dead")
	}
	if info.ProcessRunning && info.InterfaceUp && !info.HasPeer {
		reasons = append(reasons, "running but no peer configured")
	}
	if !info.OpkgTunExists && info.ProcessRunning {
		reasons = append(reasons, "process running but OpkgTun missing from NDMS")
	}

	if len(reasons) == 0 {
		return "Tunnel is in inconsistent state"
	}

	return fmt.Sprintf("Broken: %s", reasons[0])
}

// Ensure ManagerImpl implements Manager interface.
var _ Manager = (*ManagerImpl)(nil)
