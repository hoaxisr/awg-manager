package managed

import (
	"context"
	"fmt"
	"net"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Create creates a new managed WireGuard server interface and persists it.
// Multiple managed servers may coexist; the only collision check is on the
// allocated NDMS interface name.
func (s *Service) Create(ctx context.Context, req CreateServerRequest) (*storage.ManagedServer, error) {
	// Validate
	if err := s.validateServerParams(ctx, req.Address, req.Mask, req.ListenPort, ""); err != nil {
		return nil, err
	}

	// Find free index
	idx, err := s.queries.WGServers.FindFreeIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("find free index: %w", err)
	}
	ifaceName := fmt.Sprintf("Wireguard%d", idx)

	// Resolve mask to dotted notation for storage
	mask := s.resolveMask(req.Mask)

	// Description: default to ManagedServerDescription when caller omits one,
	// preserving the legacy hardcoded value so existing behaviour is unchanged.
	description := req.Description
	if description == "" {
		description = ManagedServerDescription
	}

	// Create interface via RCI
	if err := s.rciCreateInterface(ctx, ifaceName); err != nil {
		return nil, fmt.Errorf("create interface: %w", err)
	}

	// Configure all properties in a single RCI call:
	// description, security-level, listen-port, ip address, name-servers, tcp adjust-mss, up
	if err := s.rciConfigureServer(ctx, ifaceName, description, req.Address, mask, req.ListenPort); err != nil {
		s.cleanupInterface(ctx, ifaceName)
		return nil, fmt.Errorf("configure interface: %w", err)
	}

	// Enable NAT by default
	if err := s.rciSetNAT(ctx, ifaceName, true); err != nil {
		s.cleanupInterface(ctx, ifaceName)
		return nil, fmt.Errorf("enable NAT: %w", err)
	}

	// Save to storage
	server := storage.ManagedServer{
		InterfaceName: ifaceName,
		Description:   description,
		Address:       req.Address,
		Mask:          mask,
		ListenPort:    req.ListenPort,
		Endpoint:      req.Endpoint,
		DNS:           req.DNS,
		MTU:           req.MTU,
		NATEnabled:    true,
		Peers:         []storage.ManagedPeer{},
	}
	if err := s.settings.AddManagedServer(server); err != nil {
		s.cleanupInterface(ctx, ifaceName)
		return nil, fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("managed server created", "interface", ifaceName, "address", req.Address, "port", req.ListenPort)
	s.appLog.Info("create", ifaceName, fmt.Sprintf("Managed server created on %s", ifaceName))
	saved := server
	return &saved, nil
}

// Update updates the managed server's address and/or listen port.
func (s *Service) Update(ctx context.Context, id string, req UpdateServerRequest) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}

	if err := s.validateServerParams(ctx, req.Address, req.Mask, req.ListenPort, server.InterfaceName); err != nil {
		return err
	}

	mask := s.resolveMask(req.Mask)

	// Build the set of NDMS-side mutations and send them in a single atomic
	// RCI POST. Either every change applies or the whole payload is rejected,
	// so router and storage cannot end up partially diverged on a multi-leg
	// edit. Description's empty current value is treated as the legacy
	// default (ManagedServerDescription) for the changed-check so the first
	// edit on a pre-Description-field server doesn't spuriously emit a
	// no-op rename to the default.
	changes := updateServerChanges{}
	if req.Description != nil {
		currentDesc := server.Description
		if currentDesc == "" {
			currentDesc = ManagedServerDescription
		}
		newDesc := *req.Description
		if newDesc == "" {
			newDesc = ManagedServerDescription
		}
		if newDesc != currentDesc {
			changes.descriptionSet = true
			changes.description = newDesc
		}
	}
	if req.ListenPort != server.ListenPort {
		changes.portSet = true
		changes.port = req.ListenPort
	}
	if req.Address != server.Address || mask != server.Mask {
		changes.addressChanged = true
		changes.oldAddress = server.Address
		changes.oldMask = server.Mask
		changes.newAddress = req.Address
		changes.newMask = mask
	}
	if err := s.rciUpdateServer(ctx, server.InterfaceName, changes); err != nil {
		return fmt.Errorf("update server: %w", err)
	}

	// Update storage. Required fields (Address, Mask, ListenPort) were
	// validated above. Optional fields (Description, Endpoint, DNS, MTU)
	// use pointer semantics: nil = preserve existing, non-nil = set
	// (including empty/zero, which CLEARS the field).
	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		sv.Address = req.Address
		sv.Mask = mask
		sv.ListenPort = req.ListenPort
		if req.Description != nil {
			sv.Description = *req.Description
		}
		if req.Endpoint != nil {
			sv.Endpoint = *req.Endpoint
		}
		if req.DNS != nil {
			sv.DNS = *req.DNS
		}
		if req.MTU != nil {
			sv.MTU = *req.MTU
		}
		return nil
	}); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("managed server updated", "interface", server.InterfaceName, "address", req.Address, "port", req.ListenPort)
	return nil
}

// SetNAT enables or disables NAT on the managed server interface.
func (s *Service) SetNAT(ctx context.Context, id string, enabled bool) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}

	if err := s.rciSetNAT(ctx, server.InterfaceName, enabled); err != nil {
		return fmt.Errorf("set NAT: %w", err)
	}

	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		sv.NATEnabled = enabled
		return nil
	}); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("managed server NAT changed", "interface", server.InterfaceName, "enabled", enabled)
	return nil
}

// SetEnabled brings the managed server interface up or down.
func (s *Service) SetEnabled(ctx context.Context, id string, enabled bool) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
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

	s.log.Info("managed server toggled", "interface", server.InterfaceName, "enabled", enabled)
	return nil
}

// Delete removes the managed server and all its peers.
//
// Order matters: NDMS interface deletion happens FIRST. If it fails, storage
// stays intact so the next attempt can retry. Otherwise we'd leak an orphan
// kernel/NDMS interface with no storage entry to clean it up later — which is
// especially bad in the multi-server world (the user might re-create a server
// at the same Wireguard<N> slot and collide with the orphan).
//
// NAT removal and interface-down are best-effort: failing to undo NAT or to
// down the interface should not block deletion, since rciDeleteInterface will
// destroy both anyway. Errors are logged via appLog (visible in /logs).
func (s *Service) Delete(ctx context.Context, id string) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}

	// Disable NAT if enabled — best-effort. NAT cleanup is opportunistic;
	// rciDeleteInterface below removes the interface (and thus its NAT rule)
	// regardless.
	if server.NATEnabled {
		if err := s.rciSetNAT(ctx, server.InterfaceName, false); err != nil {
			s.log.Warn("failed to disable NAT during delete", "error", err, "interface", server.InterfaceName)
			s.appLog.Warn("delete", server.InterfaceName, fmt.Sprintf("Failed to disable NAT before delete: %v (continuing)", err))
		}
	}

	// Bring down — best-effort. rciDeleteInterface implies down.
	if err := s.rciInterfaceDown(ctx, server.InterfaceName); err != nil {
		s.log.Warn("failed to bring interface down during delete", "error", err, "interface", server.InterfaceName)
		s.appLog.Warn("delete", server.InterfaceName, fmt.Sprintf("Failed to bring interface down before delete: %v (continuing)", err))
	}

	// Delete interface (removes all peers too). This is the CRITICAL step:
	// if it fails we MUST NOT proceed with the storage delete, otherwise we
	// leak an orphan kernel/NDMS interface that has no storage entry to
	// retry the cleanup from.
	if err := s.rciDeleteInterface(ctx, server.InterfaceName); err != nil {
		s.appLog.Warn("delete", server.InterfaceName, fmt.Sprintf("Failed to delete NDMS interface: %v", err))
		return fmt.Errorf("delete interface: %w", err)
	}

	// Delete from storage
	if err := s.settings.DeleteManagedServer(id); err != nil {
		return fmt.Errorf("delete from storage: %w", err)
	}

	s.log.Info("managed server deleted", "interface", server.InterfaceName)
	s.appLog.Info("delete", server.InterfaceName, "Managed server deleted")
	return nil
}

// DeleteIfExists deletes every persisted managed server. Used by the cleanup
// service on uninstall — best-effort, errors on individual servers are
// returned but later servers still get a chance to be deleted.
func (s *Service) DeleteIfExists(ctx context.Context) error {
	servers := s.settings.GetManagedServers()
	if len(servers) == 0 {
		return nil
	}
	var firstErr error
	for _, sv := range servers {
		if err := s.Delete(ctx, sv.InterfaceName); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// List returns every persisted managed server.
func (s *Service) List() []storage.ManagedServer {
	return s.settings.GetManagedServers()
}

// Get returns the managed server with the given id, or an error if not found.
func (s *Service) Get(id string) (*storage.ManagedServer, error) {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return nil, fmt.Errorf("managed server not found: %s", id)
	}
	return server, nil
}

// GetStats returns runtime statistics for the managed server and its peers from RCI.
func (s *Service) GetStats(ctx context.Context, id string) (*ManagedServerStats, error) {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return nil, fmt.Errorf("managed server not found: %s", id)
	}

	wgServer, err := s.queries.WGServers.Get(ctx, server.InterfaceName)
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

func (s *Service) validateServerParams(ctx context.Context, address, mask string, port int, excludeIface string) error {
	if net.ParseIP(address) == nil {
		return fmt.Errorf("invalid IP address: %s", address)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port: %d (must be 1-65535)", port)
	}
	prefix, err := maskToPrefix(mask)
	if err != nil {
		return err
	}
	if prefix < 16 || prefix > 30 {
		return fmt.Errorf("invalid mask: /%d (must be /16-/30)", prefix)
	}

	if err := validateRFC1918(address); err != nil {
		return err
	}

	cidr, err := parseManagedSubnet(address, mask)
	if err != nil {
		return err
	}
	if err := validateHostAddress(address, cidr); err != nil {
		return err
	}

	if portConflict := findPortConflict(port, s.listUsedListenPorts(excludeIface)); portConflict != nil {
		return fmt.Errorf("listen-port %d уже используется managed-сервером %q", port, portConflict.iface)
	}

	used, err := s.listUsedSubnets(ctx, excludeIface)
	if err != nil {
		s.log.Warn("validateServerParams: cannot read interface list, skipping overlap check", "error", err)
		return nil
	}
	if conflict := findConflict(cidr, used); conflict != nil {
		return fmt.Errorf("подсеть %s пересекается с интерфейсом «%s» (%s)", cidr.String(), conflict.label, conflict.cidr.String())
	}
	return nil
}

func (s *Service) resolveMask(mask string) string {
	if prefix, err := maskToPrefix(mask); err == nil {
		m := net.CIDRMask(prefix, 32)
		return net.IP(m).String()
	}
	return mask
}

func (s *Service) cleanupInterface(ctx context.Context, name string) {
	_ = s.rciDeleteInterface(ctx, name)
}
