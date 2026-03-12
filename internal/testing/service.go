// Package testing provides tunnel testing operations.
package testing

import (
	"os"
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
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
	log      *logger.Logger
}

// NewService creates a new testing service.
func NewService(awgStore *storage.AWGTunnelStore, log *logger.Logger) *Service {
	return &Service{
		awgStore: awgStore,
		log:      log,
	}
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

// GetInterface returns the network interface name for a tunnel.
func (s *Service) GetInterface(id string) (string, error) {
	if !IsAWGID(id) {
		return "", ErrInvalidTunnelID
	}
	return tunnel.NewNames(id).IfaceName, nil
}

// GetCurlOptions returns curl command-line arguments for routing traffic through the tunnel.
func (s *Service) GetCurlOptions(id string) ([]string, error) {
	if !IsAWGID(id) {
		return nil, ErrInvalidTunnelID
	}
	iface := tunnel.NewNames(id).IfaceName
	return []string{"--interface", iface}, nil
}

// CheckTunnelRunning validates that the tunnel is available for testing.
func (s *Service) CheckTunnelRunning(id string) error {
	if !IsAWGID(id) {
		return ErrInvalidTunnelID
	}

	iface := tunnel.NewNames(id).IfaceName
	if !s.InterfaceExists(iface) {
		return ErrTunnelNotRunning
	}

	return nil
}

// GetWANInterface returns the active WAN kernel interface for a tunnel.
// Returns empty string if unknown (will fall back to unbound curl).
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
