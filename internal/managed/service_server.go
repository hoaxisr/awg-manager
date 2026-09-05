package managed

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
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
	// description, security-level, listen-port, ip address, mtu, name-servers, tcp adjust-mss, up
	if err := s.rciConfigureServer(ctx, ifaceName, description, req.Address, mask, req.ListenPort, effectiveMTU(req.MTU)); err != nil {
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
	// MTU follows the documented pointer contract: non-nil = set. The set is
	// emitted unconditionally (idempotent) — comparing against storage would
	// skip legacy servers whose interface never had an MTU applied.
	if req.MTU != nil {
		changes.mtuSet = true
		changes.mtu = effectiveMTU(*req.MTU)
	}
	if err := s.rciUpdateServer(ctx, server.InterfaceName, changes); err != nil {
		return fmt.Errorf("update server: %w", err)
	}

	// Сменилась подсеть → пересобрать LAN ACL под новую peer-подсеть, иначе
	// AWGM_<iface> продолжит permit'ить старый src-диапазон. Preflight в
	// applyLANSegmentsRaw гарантирует, что невалидный запрос (неизвестный
	// сегмент, недоступный каталог) не начнёт разрушать рабочий ACL.
	if changes.addressChanged && len(server.LANSegments) > 0 {
		if err := s.applyLANSegmentsRaw(ctx, server.InterfaceName, req.Address, mask, server.LANSegments); err != nil {
			// Роутер уже сменил подсеть (rciUpdateServer выше), но storage ещё
			// хранит старую — рассинхрон до следующего успешного Update. Это
			// fail-closed по доступу (ACL не пересобран → сегмент недоступен),
			// восстановимо повтором Update; явно логируем, чтобы не было тихо.
			s.log.Warn("LAN ACL rebuild failed after subnet change; router/storage out of sync until next Update",
				"error", err, "interface", server.InterfaceName, "newAddress", req.Address)
			return fmt.Errorf("rebuild LAN ACL after subnet change: %w", err)
		}
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
	s.appLog.Info("update", server.InterfaceName, fmt.Sprintf("Managed server updated on %s", server.InterfaceName))
	return nil
}

// natStaticTargets отдаёт выходы для static-NAT режима internet-only: ВСЕ
// интерфейсы с `ip global`. Один дефолт-WAN недостаточен: `no ip nat` снимает
// маскарад на каждом выходе сразу, и клиентский трафик сервера, уходящий
// политикой или селективом в VPN-интерфейс, оставался бы с приватным адресом
// источника. OpkgTun сознательно НЕ исключаются (в отличие от
// policyTunSNATTargets): семантика internet-only — «как full, но не в LAN», а
// full подменяет источник во всех OpkgTun и сегодня. running-config не
// прочитался или `ip global` нигде нет → fallback на дефолт-WAN (прежнее
// поведение).
func (s *Service) natStaticTargets(ctx context.Context) ([]string, error) {
	if s.queries != nil && s.queries.RunningConfig != nil {
		exits, err := s.queries.RunningConfig.GlobalEgressInterfaces(ctx)
		if err != nil {
			s.log.Warn("static-NAT цели деградировали до одного WAN: running-config недоступен", "error", err)
			s.appLog.Warn("nat", "internet-only", "running-config недоступен ("+err.Error()+"): static NAT только на WAN по умолчанию")
		}
		if err == nil && len(exits) == 0 {
			s.appLog.Warn("nat", "internet-only", "в running-config нет ни одного `ip global`: static NAT только на WAN по умолчанию")
		}
		if err == nil && len(exits) > 0 {
			return exits, nil
		}
	}
	if s.queries == nil || s.queries.Routes == nil {
		return nil, fmt.Errorf("internet-only требует Routes-провайдер")
	}
	wan, err := s.queries.Routes.GetDefaultGatewayInterface(ctx)
	if err != nil {
		return nil, fmt.Errorf("internet-only требует WAN (нет дефолт-маршрута): %w", err)
	}
	return []string{wan}, nil
}

// applyNATModeRaw applies a NAT mode via RCI (no storage write). Returns the
// list of exits a static-NAT rule was created on (nil for full/none) so the
// caller can persist it for deterministic teardown. prevWANs — previously
// persisted static exits to remove in full/none; empty means the server was
// never in internet-only, so there is no static rule to remove and we skip
// it (no speculative live-WAN lookup). In internet-only prevWANs are the
// tail of the previous enable: targets are re-queried live, and whatever is
// no longer among them gets removed. Reused by restore.
func (s *Service) applyNATModeRaw(ctx context.Context, ifaceName, mode string, prevWANs []string) ([]string, error) {
	switch mode {
	case "full":
		if err := s.rciSetNAT(ctx, ifaceName, true); err != nil {
			return nil, fmt.Errorf("set NAT: %w", err)
		}
		if len(prevWANs) > 0 { // только если ранее реально ставили static (internet-only)
			s.removeStaticNATs(ctx, ifaceName, prevWANs)
		}
		return nil, nil
	case "internet-only":
		targets, err := s.natStaticTargets(ctx)
		if err != nil {
			return nil, err
		}
		// Static NAT ПЕРВЫМ: при переходе из full обычный NAT держится
		// включённым до подтверждения static, поэтому сбой static не оставляет
		// iface вовсе без NAT. Откат возвращает состояние ДО вызова, а не
		// пустое: цели из prevWANs уже имели static (re-apply internet-only)
		// и их снятие увело бы интерфейс ниже исходного состояния — при уже
		// снятом `ip nat` они остались бы без подмены источника.
		applied := make([]string, 0, len(targets))
		rollback := func() {
			for _, a := range applied {
				if slices.Contains(prevWANs, a) {
					continue // стоял до вызова — оставляем
				}
				if rbErr := s.rciSetStaticNAT(ctx, ifaceName, a, false); rbErr != nil {
					s.log.Warn("internet-only rollback: remove static NAT failed", "error", rbErr, "interface", ifaceName, "target", a)
				}
			}
		}
		for _, t := range targets {
			if err := s.rciSetStaticNAT(ctx, ifaceName, t, true); err != nil {
				rollback()
				return nil, fmt.Errorf("set static NAT (%s): %w", t, err)
			}
			applied = append(applied, t)
		}
		if err := s.rciSetNAT(ctx, ifaceName, false); err != nil {
			rollback()
			return nil, fmt.Errorf("disable NAT: %w", err)
		}
		// Хвосты прошлого включения, не попавшие в новый список целей.
		for _, w := range prevWANs {
			if !slices.Contains(applied, w) {
				if rbErr := s.rciSetStaticNAT(ctx, ifaceName, w, false); rbErr != nil {
					s.log.Warn("internet-only: remove stale static NAT failed", "error", rbErr, "interface", ifaceName, "target", w)
				}
			}
		}
		return applied, nil
	case "none":
		if err := s.rciSetNAT(ctx, ifaceName, false); err != nil {
			return nil, fmt.Errorf("disable NAT: %w", err)
		}
		if len(prevWANs) > 0 { // только если ранее реально ставили static (internet-only)
			s.removeStaticNATs(ctx, ifaceName, prevWANs)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("неизвестный NAT-режим: %q", mode)
	}
}

// SetNATMode sets the NAT mode (full/internet-only/none) on the managed server.
func (s *Service) SetNATMode(ctx context.Context, id, mode string) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}
	wans, err := s.applyNATModeRaw(ctx, server.InterfaceName, mode, server.StaticNATList())
	if err != nil {
		return err
	}
	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		sv.NATMode = mode
		sv.NATEnabled = mode == "full"
		sv.NATStaticWANs = wans
		sv.NATStaticWAN = "" // источник правды теперь список
		return nil
	}); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}
	s.log.Info("managed server NAT mode changed", "interface", server.InterfaceName, "mode", mode)
	return nil
}

// removeStaticNATs снимает ip static для интерфейса по сохранённому списку;
// пустой список — fallback на текущий дефолт-WAN (back-compat для серверов
// без сохранённых выходов). Best-effort.
func (s *Service) removeStaticNATs(ctx context.Context, ifaceName string, storedWANs []string) {
	wans := storedWANs
	if len(wans) == 0 {
		if s.queries == nil || s.queries.Routes == nil {
			return
		}
		wan, err := s.queries.Routes.GetDefaultGatewayInterface(ctx)
		if err != nil || wan == "" {
			return
		}
		wans = []string{wan}
	}
	for _, w := range wans {
		if err := s.rciSetStaticNAT(ctx, ifaceName, w, false); err != nil {
			s.log.Warn("remove static NAT failed", "error", err, "interface", ifaceName, "target", w)
		}
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
		s.removeStaticNATs(ctx, server.InterfaceName, server.StaticNATList())
	}
	if len(server.LANSegments) > 0 {
		// Teardown-only ветка applyLANSegmentsRaw: unbind + remove ACL (best-effort).
		_ = s.applyLANSegmentsRaw(ctx, server.InterfaceName, "", "", nil)
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
// access. После валидного плана и перед нашим unbind — снятие чужого permit-all
// `_WEBADMIN_` (stripForeignPermitAll).
// Empty segments = teardown (unbind+remove best-effort, errors logged only).
func (s *Service) applyLANSegmentsRaw(ctx context.Context, iface, addr, mask string, segments []string) error {
	acl := "AWGM_" + iface
	commandsWired := s.commands != nil && s.commands.Interfaces != nil

	if len(segments) == 0 {
		// Teardown best-effort: без подключённых команд снимать нечем (тестовые
		// литералы Service / деградация) — прежнее поведение сохраняем.
		if !commandsWired {
			return nil
		}
		if err := s.stripForeignPermitAll(ctx, iface); err != nil {
			s.appLog.Warn("lan-acl", iface, "permit-all не проверен/не снят: "+err.Error())
		}
		if err := s.commands.Interfaces.ACLUnbind(ctx, iface, acl); err != nil {
			s.log.Debug("unbind ACL (teardown)", "error", err, "iface", iface)
		}
		if err := s.commands.Interfaces.ACLRemove(ctx, acl); err != nil {
			s.log.Debug("remove ACL (teardown)", "error", err, "iface", iface)
		}
		return nil
	}
	if !commandsWired {
		return fmt.Errorf("ndms commands not wired")
	}
	aclCmd := s.commands.Interfaces

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

	if err := s.stripForeignPermitAll(ctx, iface); err != nil {
		s.appLog.Warn("lan-acl", iface, "permit-all не проверен/не снят: "+err.Error())
	}

	// Apply — destroy → rebuild. unbind/remove best-effort (ACL может ещё не
	// существовать), но больше не глушим молча.
	if err := aclCmd.ACLUnbind(ctx, iface, acl); err != nil {
		s.log.Debug("unbind ACL before rebuild", "error", err, "iface", iface)
	}
	if err := aclCmd.ACLRemove(ctx, acl); err != nil {
		s.log.Debug("remove ACL before rebuild", "error", err, "iface", iface)
	}
	for i, r := range plan {
		// Дубль толерируем (как SetPermitAllACL): best-effort remove выше мог
		// транзиентно не удалить старый идентичный список — состояние роутера
		// уже совпадает с планом, падать не за что (ревью).
		if err := aclCmd.ACLPermitIP(ctx, acl, r.srcSub, r.srcMask, r.dstSub, r.dstMask); err != nil && !command.IsACLDuplicate(err) {
			return fmt.Errorf("permit %s: %w", segments[i], err)
		}
	}
	return aclCmd.ACLBind(ctx, iface, acl)
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
		if cidr, err := parseManagedSubnet(b.Address, b.Mask); err == nil {
			subnet = cidr.String() // network CIDR, e.g. 10.10.10.0/24
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
	segs := strings.Join(segments, ", ")
	if segs == "" {
		segs = "none"
	}
	s.appLog.Info("lan-segments", server.InterfaceName, "LAN segments changed: "+segs)
	return nil
}
