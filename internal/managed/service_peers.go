package managed

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// AddPeer adds a new client peer to the managed server identified by id.
// Returns the created peer (including private key for .conf generation).
func (s *Service) AddPeer(ctx context.Context, id string, req AddPeerRequest) (*storage.ManagedPeer, error) {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return nil, fmt.Errorf("managed server not found: %s", id)
	}

	// Validate tunnel IP
	if err := s.validateTunnelIP(server, req.TunnelIP); err != nil {
		return nil, err
	}

	// Check tunnel IP not already used
	for _, p := range server.Peers {
		if p.TunnelIP == req.TunnelIP {
			return nil, fmt.Errorf("tunnel IP %s already in use", req.TunnelIP)
		}
	}

	// Generate keys
	privKey, pubKey, err := GenerateKeyPair(ctx)
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}

	psk, err := GeneratePresharedKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("generate PSK: %w", err)
	}

	// Parse tunnel IP
	ip, _, err := net.ParseCIDR(req.TunnelIP)
	if err != nil {
		return nil, fmt.Errorf("invalid tunnel IP: %w", err)
	}

	iface := server.InterfaceName

	// Add peer with all parameters in a single RCI call:
	// key, preshared-key, comment, allow-ips (/32 + 0.0.0.0/0), connect
	if err := s.rciAddPeer(ctx, iface, pubKey, psk, strings.TrimSpace(req.Description), ip.String()); err != nil {
		return nil, fmt.Errorf("add peer: %w", err)
	}

	// Save to storage
	peer := storage.ManagedPeer{
		PublicKey:    pubKey,
		PrivateKey:   privKey,
		PresharedKey: psk,
		Description:  req.Description,
		TunnelIP:     req.TunnelIP,
		DNS:          req.DNS,
		Enabled:      true,
	}
	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		sv.Peers = append(sv.Peers, peer)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("peer added", "interface", iface, "description", req.Description, "tunnelIP", req.TunnelIP)
	s.appLog.Info("add-peer", req.Description, fmt.Sprintf("Peer %s added", req.Description))
	return &peer, nil
}

// UpdatePeer updates an existing peer's description and/or tunnel IP.
func (s *Service) UpdatePeer(ctx context.Context, id, pubkey string, req UpdatePeerRequest) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}

	idx := s.findPeerIndex(server, pubkey)
	if idx < 0 {
		return fmt.Errorf("peer not found: %s", pubkey)
	}
	peer := &server.Peers[idx]
	iface := server.InterfaceName

	// Validate inputs BEFORE touching RCI or storage so we can fail clean.
	wantTunnelChange := req.TunnelIP != "" && req.TunnelIP != peer.TunnelIP
	if wantTunnelChange {
		if err := s.validateTunnelIP(server, req.TunnelIP); err != nil {
			return err
		}
		// Check not used by another peer
		for i, p := range server.Peers {
			if i != idx && p.TunnelIP == req.TunnelIP {
				return fmt.Errorf("tunnel IP %s already in use", req.TunnelIP)
			}
		}
	}

	// Apply RCI changes (tunnel IP, description) before persisting.
	if wantTunnelChange {
		oldIP, _, _ := net.ParseCIDR(peer.TunnelIP)
		newIP, _, err := net.ParseCIDR(req.TunnelIP)
		if err != nil {
			return fmt.Errorf("invalid tunnel IP: %w", err)
		}
		oldIPStr := ""
		if oldIP != nil {
			oldIPStr = oldIP.String()
		}
		if err := s.rciUpdatePeerAllowIPs(ctx, iface, pubkey, oldIPStr, newIP.String()); err != nil {
			return fmt.Errorf("update allow-ips: %w", err)
		}
	}

	if req.Description != peer.Description {
		if err := s.rciSetPeerComment(ctx, iface, pubkey, strings.TrimSpace(req.Description)); err != nil {
			s.log.Warn("failed to set peer comment", "error", err)
		}
	}

	// Persist mutations atomically.
	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		// Re-resolve under the storage lock — the index from the pre-lock copy
		// may be stale if another goroutine added/removed peers in between.
		i := s.findPeerIndex(sv, pubkey)
		if i < 0 {
			return fmt.Errorf("peer not found: %s", pubkey)
		}
		if wantTunnelChange {
			sv.Peers[i].TunnelIP = req.TunnelIP
		}
		sv.Peers[i].Description = req.Description
		sv.Peers[i].DNS = req.DNS
		return nil
	}); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("peer updated", "interface", iface, "pubkey", pubkey[:8]+"...")
	s.appLog.Full("update-peer", req.Description, fmt.Sprintf("Peer %s updated", req.Description))
	return nil
}

// DeletePeer removes a peer from the managed server.
func (s *Service) DeletePeer(ctx context.Context, id, pubkey string) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}

	idx := s.findPeerIndex(server, pubkey)
	if idx < 0 {
		return fmt.Errorf("peer not found: %s", pubkey)
	}

	peerName := server.Peers[idx].Description
	iface := server.InterfaceName

	// Remove via RCI
	if err := s.rciRemovePeer(ctx, iface, pubkey); err != nil {
		s.log.Warn("failed to remove peer via RCI", "error", err)
	}

	// Remove from storage
	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		// Re-resolve under the storage lock — the index from the pre-lock copy
		// may be stale if another goroutine added/removed peers in between.
		i := s.findPeerIndex(sv, pubkey)
		if i < 0 {
			return fmt.Errorf("peer not found: %s", pubkey)
		}
		sv.Peers = append(sv.Peers[:i], sv.Peers[i+1:]...)
		return nil
	}); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("peer deleted", "interface", iface, "pubkey", pubkey[:8]+"...")
	s.appLog.Info("delete-peer", peerName, fmt.Sprintf("Peer %s deleted", peerName))
	return nil
}

// TogglePeer enables or disables a peer.
func (s *Service) TogglePeer(ctx context.Context, id, pubkey string, enabled bool) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}

	idx := s.findPeerIndex(server, pubkey)
	if idx < 0 {
		return fmt.Errorf("peer not found: %s", pubkey)
	}

	iface := server.InterfaceName

	if err := s.rciSetPeerConnect(ctx, iface, pubkey, enabled); err != nil {
		return fmt.Errorf("toggle peer: %w", err)
	}

	peerName := server.Peers[idx].Description
	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		// Re-resolve under the storage lock — the index from the pre-lock copy
		// may be stale if another goroutine added/removed peers in between.
		i := s.findPeerIndex(sv, pubkey)
		if i < 0 {
			return fmt.Errorf("peer not found: %s", pubkey)
		}
		sv.Peers[i].Enabled = enabled
		return nil
	}); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("peer toggled", "interface", iface, "pubkey", pubkey[:8]+"...", "enabled", enabled)
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	s.appLog.Full("toggle-peer", peerName, fmt.Sprintf("Peer %s %s", peerName, state))
	return nil
}

func (s *Service) findPeerIndex(server *storage.ManagedServer, pubkey string) int {
	for i, p := range server.Peers {
		if p.PublicKey == pubkey {
			return i
		}
	}
	return -1
}

func (s *Service) validateTunnelIP(server *storage.ManagedServer, tunnelIP string) error {
	ip, _, err := net.ParseCIDR(tunnelIP)
	if err != nil {
		return fmt.Errorf("invalid tunnel IP (must be CIDR, e.g. 10.0.0.2/32): %w", err)
	}

	// Check it's in the server's subnet
	serverIP := net.ParseIP(server.Address)
	serverMask := net.IPMask(net.ParseIP(server.Mask).To4())
	if serverIP == nil || serverMask == nil {
		return nil // Skip subnet check if server address is unparseable
	}
	serverNet := &net.IPNet{IP: serverIP.Mask(serverMask), Mask: serverMask}

	if !serverNet.Contains(ip) {
		return fmt.Errorf("tunnel IP %s is not in server subnet %s", ip, serverNet)
	}

	// Must not be the server's own address
	if ip.Equal(serverIP) {
		return fmt.Errorf("tunnel IP cannot be the server's own address")
	}

	// Must not be network or broadcast address (for subnets larger than /31)
	ones, bits := serverNet.Mask.Size()
	if ones < bits-1 { // /31 and /32 have no network/broadcast
		networkAddr := serverNet.IP
		if ip.Equal(networkAddr) {
			return fmt.Errorf("tunnel IP %s is the network address", ip)
		}
		// Calculate broadcast: network OR (NOT mask)
		broadcast := make(net.IP, len(networkAddr))
		for i := range networkAddr {
			broadcast[i] = networkAddr[i] | ^serverNet.Mask[i]
		}
		if ip.Equal(broadcast) {
			return fmt.Errorf("tunnel IP %s is the broadcast address", ip)
		}
	}

	return nil
}
