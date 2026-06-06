package managed

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// RestoreOptions controls per-batch behaviour of Service.Restore.
type RestoreOptions struct {
	// AllowRenumber: when true and the target slot Wireguard<N> is taken
	// by a DIFFERENT server (matched by public-key derived from the
	// supplied private key), Restore picks the next free Wireguard<M>
	// slot and creates the server there. When false, such a server is
	// returned with action="conflict".
	AllowRenumber bool `json:"allowRenumber"`
}

// RestoreOutcome is the per-server report from Service.Restore.
type RestoreOutcome struct {
	Name       string   `json:"name"`                 // original Wireguard<N> from the input
	NewName    string   `json:"newName,omitempty"`    // populated when action == "renamed"
	Action     string   `json:"action"`               // created|merged|renamed|conflict|failed
	AddedPeers int      `json:"addedPeers,omitempty"` // for merged
	Conflicts  []string `json:"conflicts,omitempty"`  // human-readable reasons
	Error      string   `json:"error,omitempty"`      // only for failed
}

type restorePersistMode int

const (
	persistAdd restorePersistMode = iota
	persistUpdateExisting
	persistRenameExisting
)

// Restore reconciles the supplied list of managed-server snapshots against
// NDMS, per-server atomic with rollback on transient RCI errors. See
// docs/superpowers/specs/2026-05-14-managed-server-export-import-design.md
// §Per-server atomicity for the contract.
func (s *Service) Restore(ctx context.Context, in []ManagedServerExport, opts RestoreOptions) []RestoreOutcome {
	s.sysLog().Info("managed restore requested", "servers", len(in), "allowRenumber", opts.AllowRenumber, "mode", "import")
	s.appLog.Info("managed-restore-start", fmt.Sprintf("%d servers", len(in)), "Starting managed server restore from backup")
	return s.restoreWithMode(ctx, in, opts, false)
}

// RestoreDrift re-creates NDMS state for settings entries that are missing
// live interfaces. Unlike import restore, this path must prefer live NDMS
// presence over storage presence when deciding between create and merge.
func (s *Service) RestoreDrift(ctx context.Context, in []ManagedServerExport, opts RestoreOptions) []RestoreOutcome {
	s.sysLog().Info("managed drift restore requested", "servers", len(in), "allowRenumber", opts.AllowRenumber, "mode", "drift")
	s.appLog.Info("managed-restore-drift-start", fmt.Sprintf("%d servers", len(in)), "Starting drift recovery for managed servers")
	return s.restoreWithMode(ctx, in, opts, true)
}

func (s *Service) restoreWithMode(ctx context.Context, in []ManagedServerExport, opts RestoreOptions, driftMode bool) []RestoreOutcome {
	mode := "import"
	if driftMode {
		mode = "drift"
	}
	if batchConflicts := s.preflightBatch(in); len(batchConflicts) > 0 {
		s.sysLog().Warn("managed restore batch preflight failed", "mode", mode, "servers", len(in), "conflicts", len(batchConflicts))
		s.appLog.Warn("managed-restore-preflight-conflict", fmt.Sprintf("%d servers", len(in)), "Batch preflight found conflicts; restore aborted")
		return batchConflicts
	}
	out := make([]RestoreOutcome, 0, len(in))
	for _, sv := range in {
		out = append(out, s.restoreOne(ctx, sv, opts, driftMode))
	}
	created, merged, renamed, conflicts, failed := summarizeRestoreOutcomes(out)
	s.sysLog().Info("managed restore completed", "mode", mode, "servers", len(in), "created", created, "merged", merged, "renamed", renamed, "conflict", conflicts, "failed", failed)
	if failed > 0 || conflicts > 0 {
		s.appLog.Warn(
			"managed-restore-finished-with-issues",
			mode,
			fmt.Sprintf("Restore completed with issues: created=%d merged=%d renamed=%d conflict=%d failed=%d", created, merged, renamed, conflicts, failed),
		)
	} else {
		s.appLog.Info(
			"managed-restore-finished",
			mode,
			fmt.Sprintf("Restore completed: created=%d merged=%d renamed=%d", created, merged, renamed),
		)
	}
	return out
}

func (s *Service) restoreOne(ctx context.Context, sv ManagedServerExport, opts RestoreOptions, driftMode bool) RestoreOutcome {
	outcome := RestoreOutcome{Name: sv.InterfaceName}
	s.sysLog().Debug("managed restore server started", "interface", sv.InterfaceName, "peers", len(sv.Peers), "allowRenumber", opts.AllowRenumber, "driftMode", driftMode)

	if sv.PrivateKey == "" {
		outcome.Action = "failed"
		outcome.Error = "PrivateKey is empty in input; cannot restore without server key"
		s.sysLog().Warn("managed restore server rejected", "interface", sv.InterfaceName, "reason", outcome.Error)
		s.appLog.Warn("managed-restore-server-invalid", sv.InterfaceName, outcome.Error)
		return outcome
	}
	if _, err := derivePublicKeyFromPrivate(sv.PrivateKey); err != nil {
		outcome.Action = "conflict"
		outcome.Conflicts = []string{"invalid server private key: " + err.Error()}
		s.sysLog().Warn("managed restore server conflict", "interface", sv.InterfaceName, "reason", outcome.Conflicts[0])
		s.appLog.Warn("managed-restore-server-conflict", sv.InterfaceName, "Invalid server private key in backup input")
		return outcome
	}

	existingStorage, hasStorage := s.findStorageOccupant(sv.InterfaceName)
	liveExists, liveSameIdentity := s.liveInterfaceIdentity(ctx, sv)
	storageSameIdentity := hasStorage && samePubKey(existingStorage, sv)
	// Same-server identity + live interface → merge missing peers.
	if storageSameIdentity && liveSameIdentity {
		if conflicts := s.preflightMergePeers(existingStorage, sv); len(conflicts) > 0 {
			outcome.Action = "conflict"
			outcome.Conflicts = conflicts
			s.sysLog().Warn("managed restore merge preflight conflict", "interface", sv.InterfaceName, "conflicts", len(conflicts))
			s.appLog.Warn("managed-restore-merge-conflict", sv.InterfaceName, fmt.Sprintf("Merge preflight found %d conflict(s)", len(conflicts)))
			return outcome
		}
		if len(sv.ASC) > 0 {
			if err := s.applyASCOnMerge(ctx, existingStorage.InterfaceName, sv.ASC); err != nil {
				outcome.Action = "failed"
				outcome.Error = err.Error()
				s.sysLog().Error("managed restore merge ASC apply failed", "interface", sv.InterfaceName, "error", err)
				s.appLog.Error("managed-restore-merge-failed", sv.InterfaceName, "Failed to apply ASC params on merge path")
				return outcome
			}
		}
		added, err := s.applyMergePeers(ctx, existingStorage, sv)
		if err != nil {
			outcome.Action = "failed"
			outcome.Error = err.Error()
			s.sysLog().Error("managed restore merge failed", "interface", sv.InterfaceName, "error", err)
			s.appLog.Error("managed-restore-merge-failed", sv.InterfaceName, "Failed to merge missing peers into existing live server")
			return outcome
		}
		outcome.Action = "merged"
		outcome.AddedPeers = added
		s.sysLog().Info("managed restore merged peers", "interface", sv.InterfaceName, "addedPeers", added)
		return outcome
	}

	target := sv.InterfaceName
	renamed := false
	slotOccupiedByDifferent := (hasStorage && !storageSameIdentity) || (liveExists && !liveSameIdentity)
	if slotOccupiedByDifferent {
		if !opts.AllowRenumber {
			outcome.Action = "conflict"
			outcome.Conflicts = []string{
				fmt.Sprintf("slot %s is occupied by a different server; enable AllowRenumber to relocate", sv.InterfaceName),
			}
			s.sysLog().Warn("managed restore slot conflict", "interface", sv.InterfaceName, "allowRenumber", false)
			s.appLog.Warn("managed-restore-slot-conflict", sv.InterfaceName, "Target slot is occupied by a different server")
			return outcome
		}
		if s.queries == nil || s.queries.WGServers == nil {
			outcome.Action = "failed"
			outcome.Error = "renumber requested but WGServers queries layer unavailable"
			s.sysLog().Error("managed restore renumber unavailable", "interface", sv.InterfaceName, "error", outcome.Error)
			s.appLog.Error("managed-restore-renumber-failed", sv.InterfaceName, "Renumber requested but WG query layer unavailable")
			return outcome
		}
		idx, err := s.queries.WGServers.FindFreeIndex(ctx)
		if err != nil {
			outcome.Action = "failed"
			outcome.Error = fmt.Sprintf("find free index: %v", err)
			s.sysLog().Error("managed restore free index failed", "interface", sv.InterfaceName, "error", err)
			s.appLog.Error("managed-restore-renumber-failed", sv.InterfaceName, "Failed to find free Wireguard slot")
			return outcome
		}
		target = fmt.Sprintf("Wireguard%d", idx)
		renamed = true
		s.sysLog().Info("managed restore renumber selected", "from", sv.InterfaceName, "to", target)
		s.appLog.Full("managed-restore-renumber", sv.InterfaceName, fmt.Sprintf("Server will be restored as %s", target))
	}

	excludeIface := ""
	if storageSameIdentity && target == sv.InterfaceName {
		excludeIface = sv.InterfaceName
	}

	// Pre-flight all create/renumber paths.
	conflicts := s.preflight(ctx, sv, excludeIface)
	if len(conflicts) > 0 {
		outcome.Action = "conflict"
		outcome.Conflicts = conflicts
		s.sysLog().Warn("managed restore preflight conflict", "interface", sv.InterfaceName, "target", target, "conflicts", len(conflicts))
		s.appLog.Warn("managed-restore-preflight-conflict", sv.InterfaceName, fmt.Sprintf("Server preflight found %d conflict(s)", len(conflicts)))
		return outcome
	}

	persistMode := persistAdd
	if storageSameIdentity && target == sv.InterfaceName {
		persistMode = persistUpdateExisting
	}
	if storageSameIdentity && target != sv.InterfaceName {
		persistMode = persistRenameExisting
	}

	created, err := s.applyOne(ctx, target, sv, persistMode)
	if err != nil {
		if created {
			s.sysLog().Warn("managed restore cleanup after failure", "interface", target)
			s.cleanupInterface(ctx, target)
		}
		outcome.Action = "failed"
		outcome.Error = err.Error()
		s.sysLog().Error("managed restore apply failed", "interface", sv.InterfaceName, "target", target, "error", err)
		s.appLog.Error("managed-restore-apply-failed", sv.InterfaceName, "Failed to apply managed server restore transaction")
		return outcome
	}

	if renamed {
		outcome.Action = "renamed"
		outcome.NewName = target
		s.sysLog().Info("managed restore server restored with renumber", "from", sv.InterfaceName, "to", target)
	} else {
		outcome.Action = "created"
		s.sysLog().Info("managed restore server created", "interface", target)
	}
	return outcome
}

// preflight runs read-only conflict checks for one server. Returns a slice
// of human-readable conflict reasons; empty means OK to apply.
func (s *Service) preflight(ctx context.Context, sv ManagedServerExport, excludeIface string) []string {
	var reasons []string

	// Reuse Create-level server validation.
	if err := s.validateServerParams(ctx, sv.Address, sv.Mask, sv.ListenPort, excludeIface); err != nil {
		reasons = append(reasons, err.Error())
	}

	subnet, err := parseManagedSubnet(sv.Address, sv.Mask)
	if err != nil {
		reasons = append(reasons, fmt.Sprintf("subnet %s/%s: %v", sv.Address, sv.Mask, err))
		return reasons
	}

	seenPub := map[string]struct{}{}
	seenIP := map[string]struct{}{}
	serverIP := net.ParseIP(sv.Address)
	for _, peer := range sv.Peers {
		pub := strings.TrimSpace(peer.PublicKey)
		if pub == "" {
			reasons = append(reasons, "peer public key is empty")
			continue
		}
		if _, ok := seenPub[pub]; ok {
			reasons = append(reasons, fmt.Sprintf("duplicate peer public key: %s", pub))
		}
		seenPub[pub] = struct{}{}
		if peer.TunnelIP == "" {
			reasons = append(reasons, fmt.Sprintf("peer %s tunnel IP is empty", pub))
			continue
		}
		ip, _, err := net.ParseCIDR(peer.TunnelIP)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("peer %s tunnel IP %q: %v", pub, peer.TunnelIP, err))
			continue
		}
		if err := validatePeerTunnelIP(subnet, serverIP, ip); err != nil {
			reasons = append(reasons, fmt.Sprintf("peer %s %v", pub, err))
		}
		ipKey := ip.String()
		if _, ok := seenIP[ipKey]; ok {
			reasons = append(reasons, fmt.Sprintf("duplicate peer tunnel IP: %s", ipKey))
		}
		seenIP[ipKey] = struct{}{}
	}

	return reasons
}

func (s *Service) findStorageOccupant(ifaceName string) (storage.ManagedServer, bool) {
	existing, ok := s.settings.GetManagedServerByID(ifaceName)
	if !ok || existing == nil {
		return storage.ManagedServer{}, false
	}
	return *existing, true
}

func (s *Service) liveInterfaceIdentity(ctx context.Context, sv ManagedServerExport) (exists bool, same bool) {
	if s.queries == nil || s.queries.Interfaces == nil {
		return false, false
	}
	iface, err := s.queries.Interfaces.Get(ctx, sv.InterfaceName)
	if err != nil || iface == nil {
		return false, false
	}
	if !strings.EqualFold(iface.Type, "wireguard") {
		return true, false
	}
	if s.queries.WGServers == nil {
		return true, false
	}
	liveWG, err := s.queries.WGServers.Get(ctx, sv.InterfaceName)
	if err != nil || liveWG == nil {
		return true, false
	}
	backupPub, err := derivePublicKeyFromPrivate(sv.PrivateKey)
	if err != nil {
		return true, false
	}
	if backupPub == "" || strings.TrimSpace(liveWG.PublicKey) == "" {
		return true, false
	}
	return true, strings.TrimSpace(liveWG.PublicKey) == backupPub
}

func derivePublicKeyFromPrivate(privateKey string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privateKey))
	if err != nil {
		return "", err
	}
	curve := ecdh.X25519()
	priv, err := curve.NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// samePubKey reports whether the existing server and the input describe
// the same server identity. Both sides come from the same kernel at some
// point, so identical private keys imply identical public keys without a
// derivation call.
func samePubKey(existing storage.ManagedServer, input ManagedServerExport) bool {
	if existing.PrivateKey == "" || input.PrivateKey == "" {
		return false
	}
	return existing.PrivateKey == input.PrivateKey
}

// applyOne is the per-server transaction: RCI create, configure, set
// private key, set NAT, add peers, persist to settings.json. The caller
// (restoreOne) wraps with cleanupInterface on error so any partial NDMS
// state is rolled back.
func (s *Service) applyOne(ctx context.Context, target string, sv ManagedServerExport, persistMode restorePersistMode) (bool, error) {
	s.sysLog().Debug("managed restore apply start", "interface", sv.InterfaceName, "target", target, "peers", len(sv.Peers), "persistMode", persistMode)
	if err := s.rciCreateInterface(ctx, target); err != nil {
		return false, fmt.Errorf("create interface: %w", err)
	}
	if err := s.rciConfigureServer(ctx, target, sv.Description, sv.Address, sv.Mask, sv.ListenPort); err != nil {
		return true, fmt.Errorf("configure interface: %w", err)
	}
	if err := s.rciSetPrivateKey(ctx, target, sv.PrivateKey); err != nil {
		return true, fmt.Errorf("set private key: %w", err)
	}
	mode := sv.NATMode
	if mode == "" { // старый бэкап без natMode
		if sv.NATEnabled {
			mode = "full"
		} else {
			mode = "none"
		}
	}
	sv.NATMode = mode // персист (saved := sv ниже)
	sv.NATEnabled = mode == "full"
	wan, err := s.applyNATModeRaw(ctx, target, mode, sv.NATStaticWAN)
	if err != nil {
		return true, fmt.Errorf("set NAT mode: %w", err)
	}
	sv.NATStaticWAN = wan
	if err := s.applyLANSegmentsRaw(ctx, target, sv.Address, sv.Mask, sv.LANSegments); err != nil {
		return true, fmt.Errorf("set LAN segments: %w", err)
	}
	if err := s.applyPolicy(ctx, target, sv.Policy); err != nil {
		return true, fmt.Errorf("set policy: %w", err)
	}
	if len(sv.ASC) > 0 {
		if err := s.applyASCParams(ctx, target, sv.ASC); err != nil {
			return true, fmt.Errorf("set ASC params: %w", err)
		}
	}
	for _, peer := range sv.Peers {
		ip, _, err := net.ParseCIDR(peer.TunnelIP)
		if err != nil {
			return true, fmt.Errorf("peer tunnel IP %q: %w", peer.TunnelIP, err)
		}
		if err := s.rciAddPeer(ctx, target, peer.PublicKey, peer.PresharedKey, peer.Description, ip.String(), peer.Enabled); err != nil {
			return true, fmt.Errorf("add peer %s: %w", peer.PublicKey, err)
		}
	}
	// Persist to settings.json under the (possibly renamed) target.
	saved := sv
	saved.InterfaceName = target
	saved.ASC = nil
	if len(sv.ASC) > 0 {
		if i1, i2, i3, i4, i5, err := extractASCSignatures(sv.ASC); err == nil {
			if i1 != "" {
				saved.I1 = i1
			}
			if i2 != "" {
				saved.I2 = i2
			}
			if i3 != "" {
				saved.I3 = i3
			}
			if i4 != "" {
				saved.I4 = i4
			}
			if i5 != "" {
				saved.I5 = i5
			}
		}
	}
	switch persistMode {
	case persistUpdateExisting:
		s.sysLog().Debug("managed restore persisting mode", "target", target, "mode", "update-existing")
		if err := s.settings.UpdateManagedServer(target, func(dst *storage.ManagedServer) error {
			*dst = saved
			return nil
		}); err != nil {
			return true, fmt.Errorf("save to storage: %w", err)
		}
	case persistRenameExisting:
		s.sysLog().Debug("managed restore persisting mode", "source", sv.InterfaceName, "target", target, "mode", "rename-existing")
		old, ok := s.settings.GetManagedServerByID(sv.InterfaceName)
		if !ok || old == nil {
			return true, fmt.Errorf("rename storage old server not found: %s", sv.InterfaceName)
		}
		if err := s.settings.AddManagedServer(saved); err != nil {
			return true, fmt.Errorf("rename storage add new: %w", err)
		}
		if err := s.settings.DeleteManagedServer(sv.InterfaceName); err != nil {
			s.sysLog().Warn("managed restore rename rollback started", "source", sv.InterfaceName, "target", target, "error", err)
			_ = s.settings.DeleteManagedServer(saved.InterfaceName)
			_ = s.settings.AddManagedServer(*old)
			return true, fmt.Errorf("rename storage delete old: %w", err)
		}
	default:
		s.sysLog().Debug("managed restore persisting mode", "target", target, "mode", "add")
		if err := s.settings.AddManagedServer(saved); err != nil {
			return true, fmt.Errorf("save to storage: %w", err)
		}
	}
	if s.queries != nil && s.queries.Interfaces != nil {
		s.queries.Interfaces.InvalidateAll()
	}
	return true, nil
}

// mergePeers adds peers from sv that are not already present (by public
// key) on the live existing server. Returns the count actually added.
func (s *Service) preflightMergePeers(existing storage.ManagedServer, sv ManagedServerExport) []string {
	have := make(map[string]struct{}, len(existing.Peers))
	for _, p := range existing.Peers {
		have[p.PublicKey] = struct{}{}
	}
	var conflicts []string
	incomingPub := make(map[string]struct{}, len(sv.Peers))
	incomingIP := make(map[string]struct{}, len(sv.Peers))
	serverSubnet, err := parseManagedSubnet(existing.Address, existing.Mask)
	if err != nil {
		return []string{fmt.Sprintf("existing subnet %s/%s: %v", existing.Address, existing.Mask, err)}
	}
	serverIP := net.ParseIP(existing.Address)
	for _, peer := range sv.Peers {
		pub := strings.TrimSpace(peer.PublicKey)
		if pub == "" {
			conflicts = append(conflicts, "peer public key is empty")
			continue
		}
		if _, ok := incomingPub[pub]; ok {
			conflicts = append(conflicts, fmt.Sprintf("duplicate peer public key in import: %s", pub))
		}
		incomingPub[pub] = struct{}{}
		ip, _, err := net.ParseCIDR(peer.TunnelIP)
		if err != nil {
			conflicts = append(conflicts, fmt.Sprintf("peer tunnel IP %q: %v", peer.TunnelIP, err))
			continue
		}
		ipStr := ip.String()
		if _, ok := incomingIP[ipStr]; ok {
			conflicts = append(conflicts, fmt.Sprintf("duplicate peer tunnel IP in import: %s", ipStr))
		}
		incomingIP[ipStr] = struct{}{}
		if err := validatePeerTunnelIP(serverSubnet, serverIP, ip); err != nil {
			conflicts = append(conflicts, fmt.Sprintf("peer %s %v", pub, err))
		}
		if _, exists := have[pub]; exists {
			continue
		}
	}
	return conflicts
}

func (s *Service) applyMergePeers(ctx context.Context, existing storage.ManagedServer, sv ManagedServerExport) (int, error) {
	have := make(map[string]struct{}, len(existing.Peers))
	for _, p := range existing.Peers {
		have[p.PublicKey] = struct{}{}
	}
	added := 0
	var addedKeys []string
	var missingPeers []storage.ManagedPeer
	for _, peer := range sv.Peers {
		if _, ok := have[peer.PublicKey]; ok {
			continue
		}
		ip, _, err := net.ParseCIDR(peer.TunnelIP)
		if err != nil {
			return added, fmt.Errorf("peer tunnel IP %q: %w", peer.TunnelIP, err)
		}
		if err := s.rciAddPeer(ctx, existing.InterfaceName, peer.PublicKey, peer.PresharedKey, peer.Description, ip.String(), peer.Enabled); err != nil {
			for _, k := range addedKeys {
				_ = s.rciRemovePeer(ctx, existing.InterfaceName, k)
			}
			return added, fmt.Errorf("add peer %s: %w", peer.PublicKey, err)
		}
		addedKeys = append(addedKeys, peer.PublicKey)
		missingPeers = append(missingPeers, peer)
		have[peer.PublicKey] = struct{}{}
		added++
	}
	if added == 0 {
		s.sysLog().Debug("managed restore merge found no missing peers", "interface", existing.InterfaceName)
		return 0, nil
	}
	if err := s.settings.UpdateManagedServer(existing.InterfaceName, func(target *storage.ManagedServer) error {
		target.Peers = append(target.Peers, missingPeers...)
		return nil
	}); err != nil {
		s.sysLog().Warn("managed restore merge storage update failed; rolling back peers", "interface", existing.InterfaceName, "addedPeers", len(addedKeys), "error", err)
		for _, k := range addedKeys {
			_ = s.rciRemovePeer(ctx, existing.InterfaceName, k)
		}
		return added, fmt.Errorf("persist merged peer: %w", err)
	}
	s.sysLog().Info("managed restore merge persisted", "interface", existing.InterfaceName, "addedPeers", added)
	return added, nil
}

func (s *Service) applyASCOnMerge(ctx context.Context, ifaceName string, asc json.RawMessage) error {
	if err := s.applyASCParams(ctx, ifaceName, asc); err != nil {
		return fmt.Errorf("apply ASC params on merge: %w", err)
	}
	i1, i2, i3, i4, i5, err := extractASCSignatures(asc)
	if err != nil {
		s.sysLog().Warn("managed restore merge ASC signatures parse failed", "interface", ifaceName, "error", err)
		s.appLog.Warn("managed-restore-merge-asc-signatures", ifaceName, "ASC applied, but I1-I5 signatures could not be persisted: "+err.Error())
		return nil
	}
	if err := s.settings.UpdateManagedServer(ifaceName, func(target *storage.ManagedServer) error {
		target.I1 = i1
		target.I2 = i2
		target.I3 = i3
		target.I4 = i4
		target.I5 = i5
		return nil
	}); err != nil {
		return fmt.Errorf("persist ASC signatures on merge: %w", err)
	}
	return nil
}

func (s *Service) applyPolicy(ctx context.Context, ifaceName, policy string) error {
	switch strings.TrimSpace(policy) {
	case "", "none":
		return nil
	default:
		return s.rciSetHotspotPolicy(ctx, ifaceName, policy)
	}
}

func (s *Service) preflightBatch(in []ManagedServerExport) []RestoreOutcome {
	byIndex := map[int][]string{}
	nameSeen := map[string]struct{}{}
	keySeen := map[string]string{}
	portSeen := map[int]string{}
	var subnets []struct {
		name string
		net  *net.IPNet
	}
	peerPubSeen := map[string]string{}

	addConflict := func(idx int, msg string) {
		byIndex[idx] = append(byIndex[idx], msg)
	}

	for i, sv := range in {
		if _, ok := nameSeen[sv.InterfaceName]; ok {
			addConflict(i, fmt.Sprintf("duplicate interfaceName in backup: %s", sv.InterfaceName))
		}
		nameSeen[sv.InterfaceName] = struct{}{}
		if strings.TrimSpace(sv.PrivateKey) != "" {
			if prev, ok := keySeen[sv.PrivateKey]; ok {
				addConflict(i, fmt.Sprintf("duplicate server private key in backup: %s and %s", prev, sv.InterfaceName))
			} else {
				keySeen[sv.PrivateKey] = sv.InterfaceName
			}
		}
		if prev, ok := portSeen[sv.ListenPort]; ok {
			addConflict(i, fmt.Sprintf("duplicate listen-port %d in backup (%s and %s)", sv.ListenPort, prev, sv.InterfaceName))
		} else {
			portSeen[sv.ListenPort] = sv.InterfaceName
		}
		cidr, err := parseManagedSubnet(sv.Address, sv.Mask)
		if err == nil {
			for _, s2 := range subnets {
				if findConflict(cidr, []usedSubnet{{label: s2.name, cidr: s2.net}}) != nil {
					addConflict(i, fmt.Sprintf("subnet %s overlaps with backup server %s (%s)", cidr.String(), s2.name, s2.net.String()))
				}
			}
			subnets = append(subnets, struct {
				name string
				net  *net.IPNet
			}{name: sv.InterfaceName, net: cidr})
		}
		for _, p := range sv.Peers {
			if prev, ok := peerPubSeen[p.PublicKey]; ok {
				addConflict(i, fmt.Sprintf("duplicate peer public key in backup: %s (%s and %s)", p.PublicKey, prev, sv.InterfaceName))
			} else if strings.TrimSpace(p.PublicKey) != "" {
				peerPubSeen[p.PublicKey] = sv.InterfaceName
			}
		}
	}
	if len(byIndex) == 0 {
		return nil
	}
	s.sysLog().Warn("managed restore batch conflict detected", "servers", len(in), "conflictedServers", len(byIndex))
	for i := range in {
		if _, ok := byIndex[i]; !ok {
			byIndex[i] = []string{"batch has conflicts; restore aborted"}
		}
	}
	out := make([]RestoreOutcome, 0, len(in))
	for i, sv := range in {
		out = append(out, RestoreOutcome{
			Name:      sv.InterfaceName,
			Action:    "conflict",
			Conflicts: byIndex[i],
		})
	}
	return out
}

func summarizeRestoreOutcomes(outcomes []RestoreOutcome) (created, merged, renamed, conflicts, failed int) {
	for _, o := range outcomes {
		switch o.Action {
		case "created":
			created++
		case "merged":
			merged++
		case "renamed":
			renamed++
		case "conflict":
			conflicts++
		case "failed":
			failed++
		}
	}
	return
}
