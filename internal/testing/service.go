// Package testing provides tunnel testing operations.
package testing

import (
	"os"
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

const SysClassNet = "/sys/class/net"

var awgIDPattern = regexp.MustCompile(`^awgm?[0-9]+$`)

// IsAWGID returns true if the ID matches the AWG tunnel pattern.
// Matches both OS5-style "awg10" and OS4-style "awgm0" tunnel IDs.
func IsAWGID(id string) bool {
	return awgIDPattern.MatchString(id)
}

// Service provides tunnel testing operations.
type Service struct {
	awgStore *storage.AWGTunnelStore
	settings *storage.SettingsStore
	appLog   *logging.ScopedLogger
	// connTracker классифицирует исходы connectivity-проверок по туннелям:
	// в журнал попадают переходы (Warn на отказ, Info на восстановление),
	// повторы одного и того же исхода — только Debug.
	connTracker *logging.TransitionTracker
}

// NewService creates a new testing service.
func NewService(awgStore *storage.AWGTunnelStore, appLogger logging.AppLogger) *Service {
	return &Service{
		awgStore:    awgStore,
		appLog:      logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubTest),
		connTracker: logging.NewTransitionTracker(),
	}
}

// SetSettingsStore wires global settings for runtime test defaults.
func (s *Service) SetSettingsStore(settings *storage.SettingsStore) {
	s.settings = settings
}

// GetAWG returns an AWG tunnel by ID, or nil if not found.
func (s *Service) GetAWG(id string) *storage.AWGTunnel {
	tunnel, _ := s.awgStore.Get(id)
	return tunnel
}

// InterfaceExists checks if a network interface exists.
func (s *Service) InterfaceExists(iface string) bool {
	_, err := os.Stat(SysClassNet + "/" + iface)
	return err == nil
}

// GetInterfaceName returns the kernel interface name for a tunnel.
func (s *Service) GetInterfaceName(id string) (string, error) {
	if !s.isManagedTestTunnel(id) {
		return "", ErrInvalidTunnelID
	}
	return s.resolveIfaceName(id), nil
}

// CheckTunnelRunning validates that the tunnel is available for testing.
func (s *Service) CheckTunnelRunning(id string) error {
	if !s.isManagedTestTunnel(id) {
		return ErrInvalidTunnelID
	}

	iface := s.resolveIfaceName(id)
	if !s.InterfaceExists(iface) {
		return ErrTunnelNotRunning
	}

	return nil
}

// resolveIfaceName returns the kernel interface name for a tunnel,
// using NativeWG names (nwgN) for nativewg backend, wdtt-raw live iface otherwise.
func (s *Service) resolveIfaceName(id string) string {
	if stored := s.GetAWG(id); stored != nil {
		if stored.Backend == "nativewg" {
			return nwg.NewNWGNames(stored.NWGIndex).IfaceName
		}
		if stored.Backend == "wdtt-raw" && strings.TrimSpace(stored.RawKernelIface) != "" {
			return strings.TrimSpace(stored.RawKernelIface)
		}
	}
	return tunnel.NewNames(id).IfaceName
}

// isManagedTestTunnel reports whether id refers to an AWG-manager tunnel we can test.
func (s *Service) isManagedTestTunnel(id string) bool {
	if IsAWGID(id) {
		return true
	}
	if stored := s.GetAWG(id); stored != nil && stored.Backend == "wdtt-raw" {
		return true
	}
	return false
}

// GetWANInterface returns the active WAN kernel interface for a tunnel.
// Returns empty string if unknown (will fall back to default route).
func (s *Service) GetWANInterface(tunnelID string) string {
	t := s.GetAWG(tunnelID)
	if t == nil {
		return ""
	}
	return t.ActiveWAN
}

// GetEndpointIP extracts the server IP from the tunnel configuration.
func (s *Service) GetEndpointIP(id string) string {
	if !IsAWGID(id) {
		return ""
	}

	tunnel := s.GetAWG(id)
	if tunnel == nil {
		return ""
	}

	endpoint := tunnel.Peer.Endpoint
	if idx := strings.LastIndex(endpoint, ":"); idx != -1 {
		return endpoint[:idx]
	}
	return endpoint
}
