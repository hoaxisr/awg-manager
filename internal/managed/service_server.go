package managed

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Create creates a new managed WireGuard server interface.
func (s *Service) Create(ctx context.Context, req CreateServerRequest) (*storage.ManagedServer, error) {
	// Check no existing managed server
	if existing := s.settings.GetManagedServer(); existing != nil {
		return nil, fmt.Errorf("managed server already exists: %s", existing.InterfaceName)
	}

	// Validate
	if err := s.validateServerParams(req.Address, req.Mask, req.ListenPort); err != nil {
		return nil, err
	}

	// Find free index
	idx, err := s.ndms.FindFreeWireguardIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("find free index: %w", err)
	}
	ifaceName := fmt.Sprintf("Wireguard%d", idx)

	// Resolve mask to dotted notation for storage
	mask := s.resolveMask(req.Mask)

	// Create interface via RCI
	if err := s.rciCreateInterface(ctx, ifaceName); err != nil {
		return nil, fmt.Errorf("create interface: %w", err)
	}

	// Configure all properties in a single RCI call:
	// description, security-level, listen-port, ip address, name-servers, tcp adjust-mss, up
	if err := s.rciConfigureServer(ctx, ifaceName, ManagedServerDescription, req.Address, mask, req.ListenPort); err != nil {
		s.cleanupInterface(ctx, ifaceName)
		return nil, fmt.Errorf("configure interface: %w", err)
	}

	// Enable NAT by default
	if err := s.rciSetNAT(ctx, ifaceName, true); err != nil {
		s.cleanupInterface(ctx, ifaceName)
		return nil, fmt.Errorf("enable NAT: %w", err)
	}

	// Save NDMS config
	if err := s.rciSave(ctx); err != nil {
		s.log.Warn("failed to save NDMS config after server creation", "error", err)
	}

	// Save to storage
	server := &storage.ManagedServer{
		InterfaceName: ifaceName,
		Address:       req.Address,
		Mask:          mask,
		ListenPort:    req.ListenPort,
		Endpoint:      req.Endpoint,
		DNS:           req.DNS,
		MTU:           req.MTU,
		NATEnabled:    true,
		Peers:         []storage.ManagedPeer{},
	}
	if err := s.settings.SaveManagedServer(server); err != nil {
		s.cleanupInterface(ctx, ifaceName)
		return nil, fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("managed server created", "interface", ifaceName, "address", req.Address, "port", req.ListenPort)
	s.appLog.Info("create", ifaceName, fmt.Sprintf("Managed server created on %s", ifaceName))
	return server, nil
}

// Update updates the managed server's address and/or listen port.
func (s *Service) Update(ctx context.Context, req UpdateServerRequest) error {
	server := s.settings.GetManagedServer()
	if server == nil {
		return fmt.Errorf("no managed server exists")
	}

	if err := s.validateServerParams(req.Address, req.Mask, req.ListenPort); err != nil {
		return err
	}

	mask := s.resolveMask(req.Mask)

	// Update listen port if changed
	if req.ListenPort != server.ListenPort {
		if err := s.rciSetListenPort(ctx, server.InterfaceName, req.ListenPort); err != nil {
			return fmt.Errorf("set listen-port: %w", err)
		}
	}

	// Update address if changed
	if req.Address != server.Address || mask != server.Mask {
		// Remove old address first
		if err := s.rciRemoveAddress(ctx, server.InterfaceName, server.Address, server.Mask); err != nil {
			s.log.Warn("failed to remove old IP address", "error", err, "address", server.Address)
		}
		// Set new address
		if err := s.rciSetAddress(ctx, server.InterfaceName, req.Address, mask); err != nil {
			return fmt.Errorf("set address: %w", err)
		}
	}

	// Save NDMS
	if err := s.rciSave(ctx); err != nil {
		s.log.Warn("failed to save NDMS config after server update", "error", err)
	}

	// Update storage. Required fields (Address, Mask, ListenPort) were
	// validated above. Optional fields (Endpoint, DNS, MTU) must be
	// preserved from existing when the caller omits them in the request —
	// Go's json decoder cannot distinguish "absent" from "zero value", so
	// a payload missing Endpoint/DNS/MTU would otherwise wipe them.
	server.Address = req.Address
	server.Mask = mask
	server.ListenPort = req.ListenPort
	if req.Endpoint != "" {
		server.Endpoint = req.Endpoint
	}
	if req.DNS != "" {
		server.DNS = req.DNS
	}
	if req.MTU != 0 {
		server.MTU = req.MTU
	}
	if err := s.settings.SaveManagedServer(server); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("managed server updated", "interface", server.InterfaceName, "address", req.Address, "port", req.ListenPort)
	return nil
}

// SetNAT enables or disables NAT on the managed server interface.
func (s *Service) SetNAT(ctx context.Context, enabled bool) error {
	server := s.settings.GetManagedServer()
	if server == nil {
		return fmt.Errorf("no managed server exists")
	}

	if err := s.rciSetNAT(ctx, server.InterfaceName, enabled); err != nil {
		return fmt.Errorf("set NAT: %w", err)
	}

	if err := s.rciSave(ctx); err != nil {
		s.log.Warn("failed to save NDMS config after NAT change", "error", err)
	}

	server.NATEnabled = enabled
	if err := s.settings.SaveManagedServer(server); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("managed server NAT changed", "interface", server.InterfaceName, "enabled", enabled)
	return nil
}

// SetEnabled brings the managed server interface up or down.
func (s *Service) SetEnabled(ctx context.Context, enabled bool) error {
	server := s.settings.GetManagedServer()
	if server == nil {
		return fmt.Errorf("no managed server exists")
	}

	if enabled {
		if err := s.rciInterfaceUp(ctx, server.InterfaceName); err != nil {
			return fmt.Errorf("interface up: %w", err)
		}
	} else {
		if err := s.rciInterfaceDown(ctx, server.InterfaceName); err != nil {
			return fmt.Errorf("interface down: %w", err)
		}
	}

	if err := s.rciSave(ctx); err != nil {
		s.log.Warn("failed to save NDMS config after SetEnabled", "error", err)
	}

	s.log.Info("managed server toggled", "interface", server.InterfaceName, "enabled", enabled)
	return nil
}

// Delete removes the managed server and all its peers.
func (s *Service) Delete(ctx context.Context) error {
	server := s.settings.GetManagedServer()
	if server == nil {
		return fmt.Errorf("no managed server exists")
	}

	// Disable NAT if enabled
	if server.NATEnabled {
		_ = s.rciSetNAT(ctx, server.InterfaceName, false)
	}

	// Bring down
	_ = s.rciInterfaceDown(ctx, server.InterfaceName)

	// Delete interface (removes all peers too)
	_ = s.rciDeleteInterface(ctx, server.InterfaceName)

	// Save NDMS
	if err := s.rciSave(ctx); err != nil {
		s.log.Warn("failed to save NDMS config after server deletion", "error", err)
	}

	// Delete from storage
	if err := s.settings.DeleteManagedServer(); err != nil {
		return fmt.Errorf("delete from storage: %w", err)
	}

	s.log.Info("managed server deleted", "interface", server.InterfaceName)
	s.appLog.Info("delete", server.InterfaceName, "Managed server deleted")
	return nil
}

// DeleteIfExists deletes the managed server if one exists.
func (s *Service) DeleteIfExists(ctx context.Context) error {
	if s.Get() == nil {
		return nil
	}
	return s.Delete(ctx)
}

// Get returns the managed server from storage, or nil if not created.
func (s *Service) Get() *storage.ManagedServer {
	return s.settings.GetManagedServer()
}

// GetStats returns runtime statistics for the managed server and its peers from RCI.
func (s *Service) GetStats(ctx context.Context) (*ManagedServerStats, error) {
	server := s.settings.GetManagedServer()
	if server == nil {
		return nil, fmt.Errorf("no managed server exists")
	}

	wgServer, err := s.ndms.GetWireguardServer(ctx, server.InterfaceName)
	if err != nil {
		return nil, fmt.Errorf("get runtime data: %w", err)
	}

	peers := make([]ManagedPeerStats, 0, len(wgServer.Peers))
	for _, p := range wgServer.Peers {
		peers = append(peers, ManagedPeerStats{
			PublicKey:     p.PublicKey,
			Endpoint:      p.Endpoint,
			RxBytes:       p.RxBytes,
			TxBytes:       p.TxBytes,
			LastHandshake: p.LastHandshake,
			Online:        p.Online,
		})
	}

	return &ManagedServerStats{
		Status: wgServer.Status,
		Peers:  peers,
	}, nil
}

// GetInterfaceName returns the managed server's interface name, or "" if not created.
func (s *Service) GetInterfaceName() string {
	server := s.settings.GetManagedServer()
	if server == nil {
		return ""
	}
	return server.InterfaceName
}

func (s *Service) validateServerParams(address, mask string, port int) error {
	if net.ParseIP(address) == nil {
		return fmt.Errorf("invalid IP address: %s", address)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port: %d (must be 1-65535)", port)
	}
	// Validate mask: accept CIDR prefix length or dotted notation
	if _, err := strconv.Atoi(mask); err == nil {
		n, _ := strconv.Atoi(mask)
		if n < 8 || n > 30 {
			return fmt.Errorf("invalid mask: /%s (must be /8-/30)", mask)
		}
	} else if net.ParseIP(mask) == nil {
		return fmt.Errorf("invalid mask: %s", mask)
	}
	return nil
}

func (s *Service) resolveMask(mask string) string {
	if n, err := strconv.Atoi(mask); err == nil {
		// Convert CIDR prefix to dotted mask
		m := net.CIDRMask(n, 32)
		return net.IP(m).String()
	}
	return mask
}

func (s *Service) maskToPrefix(mask string) string {
	ip := net.ParseIP(mask)
	if ip == nil {
		return mask // already a prefix number
	}
	ones, _ := net.IPMask(ip.To4()).Size()
	return strconv.Itoa(ones)
}

func (s *Service) cleanupInterface(ctx context.Context, name string) {
	_ = s.rciDeleteInterface(ctx, name)
	_ = s.rciSave(ctx)
}
