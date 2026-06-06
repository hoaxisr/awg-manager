package managed

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

const (
	createPrivateKeyReadAttempts = 10
	createPrivateKeyReadDelay    = 200 * time.Millisecond
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

	// Read the auto-generated private key from the kernel and fail-fast if
	// unavailable. A managed server without persisted private key cannot be
	// exported/restored safely, so Create must not succeed in that state.
	privateKey, err := s.readCreatedServerPrivateKey(ctx, ifaceName)
	if err != nil {
		s.cleanupInterface(ctx, ifaceName)
		return nil, fmt.Errorf("read private key: %w", err)
	}

	// Generate/apply ASC params by default (backward-compatible) but allow
	// callers to opt out explicitly via generateAsc=false.
	if req.ShouldGenerateASC() {
		asc, err := s.generateDefaultASCParams()
		if err != nil {
			s.cleanupInterface(ctx, ifaceName)
			return nil, fmt.Errorf("generate ASC params: %w", err)
		}
		if err := s.applyASCParams(ctx, ifaceName, asc); err != nil {
			s.cleanupInterface(ctx, ifaceName)
			return nil, fmt.Errorf("apply ASC params: %w", err)
		}
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
		NATMode:       "full",
		PrivateKey:    privateKey,
		Peers:         []storage.ManagedPeer{},
	}
	if err := s.settings.AddManagedServer(server); err != nil {
		s.cleanupInterface(ctx, ifaceName)
		return nil, fmt.Errorf("save to storage: %w", err)
	}

	// Refresh InterfaceStore so a subsequent Create call sees the
	// freshly-created interface (subnet/listen-port conflict checks
	// rely on Interfaces.List). In production the NDMS ifcreated hook
	// reaches the same store via Dispatcher.OnCreated; this call also
	// covers the no-hook test path and any race where validation runs
	// before the hook arrives.
	if s.queries != nil && s.queries.Interfaces != nil {
		s.queries.Interfaces.InvalidateAll()
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

	// Refresh InterfaceStore so subsequent subnet/listen-port checks
	// see the new address/port. Mirrors the post-Create invalidate.
	if s.queries != nil && s.queries.Interfaces != nil {
		s.queries.Interfaces.InvalidateAll()
	}

	s.log.Info("managed server updated", "interface", server.InterfaceName, "address", req.Address, "port", req.ListenPort)
	return nil
}

// applyNATModeRaw применяет режим NAT к интерфейсу по RCI (без storage-записи).
// Переиспользуется restore (Task 4).
func (s *Service) applyNATModeRaw(ctx context.Context, ifaceName, mode string) error {
	switch mode {
	case "full":
		if err := s.rciSetNAT(ctx, ifaceName, true); err != nil {
			return fmt.Errorf("set NAT: %w", err)
		}
		s.removeStaticNAT(ctx, ifaceName)
	case "internet-only":
		if s.queries == nil || s.queries.Routes == nil {
			return fmt.Errorf("internet-only требует Routes-провайдер")
		}
		wan, err := s.queries.Routes.GetDefaultGatewayInterface(ctx)
		if err != nil {
			return fmt.Errorf("internet-only требует WAN (нет дефолт-маршрута): %w", err)
		}
		if err := s.rciSetNAT(ctx, ifaceName, false); err != nil {
			return fmt.Errorf("disable NAT: %w", err)
		}
		if err := s.rciSetStaticNAT(ctx, ifaceName, wan, true); err != nil {
			return fmt.Errorf("set static NAT: %w", err)
		}
	case "none":
		if err := s.rciSetNAT(ctx, ifaceName, false); err != nil {
			return fmt.Errorf("disable NAT: %w", err)
		}
		s.removeStaticNAT(ctx, ifaceName)
	default:
		return fmt.Errorf("неизвестный NAT-режим: %q", mode)
	}
	return nil
}

// SetNATMode sets the NAT mode (full/internet-only/none) on the managed server.
func (s *Service) SetNATMode(ctx context.Context, id, mode string) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}
	if err := s.applyNATModeRaw(ctx, server.InterfaceName, mode); err != nil {
		return err
	}
	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		sv.NATMode = mode
		sv.NATEnabled = mode == "full"
		return nil
	}); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}
	s.log.Info("managed server NAT mode changed", "interface", server.InterfaceName, "mode", mode)
	return nil
}

// removeStaticNAT снимает ip static для интерфейса. WAN re-detect'ится
// (to-interface=имя стабильно при redial). Best-effort + nil-guard Routes.
func (s *Service) removeStaticNAT(ctx context.Context, ifaceName string) {
	if s.queries == nil || s.queries.Routes == nil {
		return
	}
	wan, err := s.queries.Routes.GetDefaultGatewayInterface(ctx)
	if err != nil || wan == "" {
		return
	}
	if err := s.rciSetStaticNAT(ctx, ifaceName, wan, false); err != nil {
		s.log.Warn("remove static NAT failed", "error", err, "interface", ifaceName)
	}
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

// RestartOrStart restarts a running managed server or starts a stopped one.
// The desired/persisted state is not changed: this only flips the NDMS
// interface state so clients connected through the server can recover without
// requiring a second frontend request.
func (s *Service) RestartOrStart(ctx context.Context, id string) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}

	stats, err := s.GetStats(ctx, id)
	wasUp := err == nil && stats != nil && stats.Status == "up"

	if wasUp {
		if err := s.rciInterfaceDown(ctx, server.InterfaceName); err != nil {
			return fmt.Errorf("interface down: %w", err)
		}
		time.Sleep(1200 * time.Millisecond)
	}

	if err := s.rciInterfaceUp(ctx, server.InterfaceName); err != nil {
		return fmt.Errorf("interface up: %w", err)
	}

	s.log.Info("managed server restart-or-start", "interface", server.InterfaceName, "wasUp", wasUp)
	s.appLog.Info("restart", server.InterfaceName, "Managed server restart/start command completed")
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
	if server.NATMode == "full" {
		if err := s.rciSetNAT(ctx, server.InterfaceName, false); err != nil {
			s.log.Warn("failed to disable NAT during delete", "error", err, "interface", server.InterfaceName)
			s.appLog.Warn("delete", server.InterfaceName, fmt.Sprintf("Failed to disable NAT before delete: %v (continuing)", err))
		}
	}
	if server.NATMode == "internet-only" {
		s.removeStaticNAT(ctx, server.InterfaceName)
	}
	if len(server.LANSegments) > 0 {
		acl := "AWGM_" + server.InterfaceName
		_ = s.rciAccessGroup(ctx, server.InterfaceName, acl, false)
		_ = s.rciAclRemove(ctx, acl)
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

	// Refresh InterfaceStore so its map drops the deleted entry
	// without waiting for the eventual ifdestroyed hook. Mirrors the
	// post-Create / post-Update invalidate.
	if s.queries != nil && s.queries.Interfaces != nil {
		s.queries.Interfaces.InvalidateAll()
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

func (s *Service) readCreatedServerPrivateKey(ctx context.Context, ifaceName string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= createPrivateKeyReadAttempts; attempt++ {
		kernelName := s.resolveKernelName(ctx, ifaceName)
		if kernelName == "" {
			lastErr = fmt.Errorf("kernel interface name is not available yet")
		} else {
			pk, err := readKernelPrivateKeyWith(ctx, kernelName, s.wgRun)
			if err != nil {
				lastErr = err
			} else if strings.TrimSpace(pk) == "" {
				lastErr = fmt.Errorf("empty private key returned for %s", kernelName)
			} else {
				return strings.TrimSpace(pk), nil
			}
		}

		if attempt == createPrivateKeyReadAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(createPrivateKeyReadDelay):
		}
	}
	return "", fmt.Errorf("cannot read private key after %d attempts: %w", createPrivateKeyReadAttempts, lastErr)
}

// permitRule — одно правило permit (src→dst) для ACL.
type permitRule struct {
	srcSub, srcMask, dstSub, dstMask string
}

// resolveLANSegmentsPlan валидирует peer-подсеть и каждый запрошенный сегмент
// против каталога бриджей БЕЗ обращения к роутеру. Возвращает правила permit
// или ошибку, если сегмент неизвестен/подсеть не парсится. Вызывать ДО
// удаления существующего ACL — тогда плохой запрос не ломает рабочий доступ.
func resolveLANSegmentsPlan(addr, mask string, segments []string, bridges []query.LANBridge) ([]permitRule, error) {
	cidr, err := parseManagedSubnet(addr, mask)
	if err != nil {
		return nil, fmt.Errorf("peer subnet: %w", err)
	}
	peerSub, peerMask := cidr.IP.String(), net.IP(cidr.Mask).String()
	byName := make(map[string]query.LANBridge, len(bridges))
	for _, b := range bridges {
		byName[b.Name] = b
	}
	rules := make([]permitRule, 0, len(segments))
	for _, seg := range segments {
		b, ok := byName[seg]
		if !ok {
			return nil, fmt.Errorf("LAN-сегмент %q не найден", seg)
		}
		segCidr, err := parseManagedSubnet(b.Address, b.Mask)
		if err != nil {
			return nil, fmt.Errorf("segment %q subnet: %w", seg, err)
		}
		rules = append(rules, permitRule{
			srcSub:  peerSub,
			srcMask: peerMask,
			dstSub:  segCidr.IP.String(),
			dstMask: net.IP(segCidr.Mask).String(),
		})
	}
	return rules, nil
}

// applyLANSegmentsRaw applies LAN-forward ACL rules to an interface without
// touching storage. Builds the full plan FIRST (no RCI); only after a valid
// plan does it destroy and rebuild, so a bad request never tears down working
// access. Empty segments = teardown (unbind+remove best-effort, errors logged only).
func (s *Service) applyLANSegmentsRaw(ctx context.Context, iface, addr, mask string, segments []string) error {
	acl := "AWGM_" + iface

	if len(segments) == 0 {
		if err := s.rciAccessGroup(ctx, iface, acl, false); err != nil {
			s.log.Debug("unbind ACL (teardown)", "error", err, "iface", iface)
		}
		if err := s.rciAclRemove(ctx, acl); err != nil {
			s.log.Debug("remove ACL (teardown)", "error", err, "iface", iface)
		}
		return nil
	}

	// Preflight — собрать план до единой мутации на роутере.
	if s.queries == nil || s.queries.Interfaces == nil {
		return fmt.Errorf("interface store not wired")
	}
	bridges, err := s.queries.Interfaces.ListLANBridges(ctx)
	if err != nil {
		return fmt.Errorf("list LAN bridges: %w", err)
	}
	plan, err := resolveLANSegmentsPlan(addr, mask, segments, bridges)
	if err != nil {
		return err // старый ACL не тронут
	}

	// Apply — destroy → rebuild. unbind/remove best-effort (ACL может ещё не
	// существовать), но больше не глушим молча.
	if err := s.rciAccessGroup(ctx, iface, acl, false); err != nil {
		s.log.Debug("unbind ACL before rebuild", "error", err, "iface", iface)
	}
	if err := s.rciAclRemove(ctx, acl); err != nil {
		s.log.Debug("remove ACL before rebuild", "error", err, "iface", iface)
	}
	for i, r := range plan {
		if err := s.rciAclPermit(ctx, acl, r.srcSub, r.srcMask, r.dstSub, r.dstMask); err != nil {
			return fmt.Errorf("permit %s: %w", segments[i], err)
		}
	}
	return s.rciAccessGroup(ctx, iface, acl, true)
}

// ListLANSegments returns the router's LAN bridge catalog for the UI picker.
func (s *Service) ListLANSegments(ctx context.Context) ([]LANSegmentDTO, error) {
	if s.queries == nil || s.queries.Interfaces == nil {
		return nil, fmt.Errorf("interface store not wired")
	}
	bridges, err := s.queries.Interfaces.ListLANBridges(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]LANSegmentDTO, 0, len(bridges))
	for _, b := range bridges {
		subnet := b.Address
		if ones, _ := net.IPMask(net.ParseIP(b.Mask).To4()).Size(); ones > 0 {
			subnet = fmt.Sprintf("%s/%d", b.Address, ones)
		}
		// Human-readable name = NDMS description (e.g. "LAN"); fall back to
		// the NDMS id (e.g. "Bridge0") when the bridge has no description.
		label := b.Description
		if label == "" {
			label = b.Name
		}
		out = append(out, LANSegmentDTO{Name: b.Name, Label: label, Subnet: subnet})
	}
	return out, nil
}

// SetLANSegments sets the LAN segments (by NDMS bridge name) that peers of
// the managed server are allowed to reach via ACL-based forwarding.
func (s *Service) SetLANSegments(ctx context.Context, id string, segments []string) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}
	if err := s.applyLANSegmentsRaw(ctx, server.InterfaceName, server.Address, server.Mask, segments); err != nil {
		return err
	}
	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		sv.LANSegments = segments
		return nil
	}); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}
	s.log.Info("managed server LAN segments changed", "interface", server.InterfaceName, "segments", segments)
	return nil
}
