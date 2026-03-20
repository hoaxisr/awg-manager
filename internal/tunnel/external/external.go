// Package external provides functionality for working with external (unmanaged) tunnels.
// External tunnels are AWG interfaces that exist in the system but are not managed by awg-manager.
package external

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/sysinfo"
)

// TunnelInfo contains information about an external tunnel.
type TunnelInfo = sysinfo.ExternalTunnelInfo

// Service provides operations for external tunnels.
type Service struct {
	store         *storage.AWGTunnelStore
	settingsStore *storage.SettingsStore
	tunnelService service.Service
	log           *logger.Logger
}

// NewService creates a new external tunnel service.
func NewService(
	store *storage.AWGTunnelStore,
	settingsStore *storage.SettingsStore,
	tunnelSvc service.Service,
	log *logger.Logger,
) *Service {
	return &Service{
		store:         store,
		settingsStore: settingsStore,
		tunnelService: tunnelSvc,
		log:           log,
	}
}

// List returns tunnels that exist in the system but are not managed by awg-manager.
func (s *Service) List(ctx context.Context) ([]TunnelInfo, error) {
	// Get all system interfaces
	systemNumbers, err := sysinfo.ListSystemInterfaces()
	if err != nil {
		s.logWarn("list", "Failed to list system interfaces: "+err.Error())
		systemNumbers = []int{}
	}

	// Get managed tunnel numbers
	managedTunnels, err := s.store.List()
	if err != nil {
		return nil, fmt.Errorf("list managed tunnels: %w", err)
	}

	managed := make(map[int]bool)
	for _, t := range managedTunnels {
		numStr := tunnel.NewNames(t.ID).TunnelNum
		if num, err := strconv.Atoi(numStr); err == nil {
			managed[num] = true
		}
	}

	// Find external tunnels (in system but not managed).
	// Deduplicate by number: opkgtunX and awgX both produce the same number,
	// so without dedup the same interface would appear twice.
	seen := make(map[int]bool)
	var external []TunnelInfo
	for _, num := range systemNumbers {
		if managed[num] || seen[num] {
			continue
		}
		seen[num] = true

		// Get interface name
		names := tunnel.NewNames(fmt.Sprintf("awg%d", num))
		ifaceName := names.IfaceName

		// Check if it's an AWG interface
		info, isAWG := sysinfo.IsAWGInterface(ctx, ifaceName)
		if isAWG && info != nil {
			external = append(external, *info)
		}
	}

	return external, nil
}

// AdoptRequest contains parameters for adopting an external tunnel.
type AdoptRequest struct {
	InterfaceName string // Interface name (e.g., "opkgtun0" or "awg0")
	ConfContent   string // WireGuard .conf file content
	TunnelName    string // Optional name for the tunnel
}

// Adopt takes control of an external tunnel.
// The tunnel must be stopped (no peer in awg show) before adoption.
func (s *Service) Adopt(ctx context.Context, req AdoptRequest) (*service.TunnelWithStatus, error) {
	// Extract tunnel number from interface name
	tunnelNum, ok := sysinfo.ExtractInterfaceNumber(req.InterfaceName)
	if !ok {
		return nil, fmt.Errorf("invalid interface name: %s", req.InterfaceName)
	}

	// Check if tunnel is still running
	info, isAWG := sysinfo.IsAWGInterface(ctx, req.InterfaceName)
	if isAWG && info != nil && info.PublicKey != "" {
		return nil, fmt.Errorf("tunnel is still active - stop it in the external application and try again")
	}

	// Check if this number is already managed
	tunnelID := fmt.Sprintf("awg%d", tunnelNum)
	if s.store.Exists(tunnelID) {
		return nil, fmt.Errorf("tunnel %s is already managed", tunnelID)
	}

	// Parse the config
	t, err := config.Parse(req.ConfContent)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Set the specific ID to match the external interface
	t.ID = tunnelID
	if req.TunnelName != "" {
		t.Name = req.TunnelName
	}
	if t.Name == "" {
		t.Name = fmt.Sprintf("Imported %s", req.InterfaceName)
	}

	// Set defaults
	t.Type = "awg"
	t.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	t.Status = "stopped"
	t.Enabled = true

	// Validate
	if t.Interface.PrivateKey == "" {
		return nil, fmt.Errorf("missing privateKey")
	}
	if t.Peer.PublicKey == "" {
		return nil, fmt.Errorf("missing publicKey")
	}
	if t.Peer.Endpoint == "" {
		return nil, fmt.Errorf("missing endpoint")
	}

	// Initialize PingCheck config if globally enabled
	if t.PingCheck == nil && s.settingsStore != nil {
		settings, err := s.settingsStore.Get()
		if err == nil && settings.PingCheck.Enabled {
			defaults := settings.PingCheck.Defaults
			t.PingCheck = &storage.TunnelPingCheck{
				Enabled:       true,
				Method:        defaults.Method,
				Target:        defaults.Target,
				Interval:      defaults.Interval,
				DeadInterval:  defaults.DeadInterval,
				FailThreshold: defaults.FailThreshold,
				MinSuccess:    1,
				Timeout:       5,
				Restart:       true,
			}
		}
	}

	// Save to storage
	if err := s.store.Save(t); err != nil {
		return nil, fmt.Errorf("save tunnel: %w", err)
	}

	s.logInfo(t.ID, "Adopted external tunnel: "+t.Name)

	// Start the tunnel under awg-manager control
	if err := s.tunnelService.Start(ctx, t.ID); err != nil {
		s.logWarn(t.ID, "Failed to start adopted tunnel: "+err.Error())
		// Don't fail - tunnel is imported, just not started
	}

	return s.tunnelService.Get(ctx, t.ID)
}

func (s *Service) logInfo(target, message string) {
	if s.log != nil {
		s.log.Infof("[external] %s: %s", target, message)
	}
}

func (s *Service) logWarn(target, message string) {
	if s.log != nil {
		s.log.Warnf("[external] %s: %s", target, message)
	}
}
