package firc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// MigrationOptions controls which categories, router slots, and specific items are migrated from sing-box to firc.
type MigrationOptions struct {
	// Mode: "merge" (default: merge into existing FIRC tunnels & groups) or "replace" (overwrite existing tunnels/groups)
	Mode string `json:"mode,omitempty"`
	// RouterSource: "auto" (default: active slot first + fallback), "tproxy" (20-router.json), "fakeip" (21-fakeip.json), "both"
	RouterSource string `json:"routerSource,omitempty"`
	// Category toggles (nil defaults to true)
	IncludeTunnels       *bool `json:"includeTunnels,omitempty"`
	IncludeSubscriptions *bool `json:"includeSubscriptions,omitempty"`
	IncludeGroups        *bool `json:"includeGroups,omitempty"`
	IncludeClientRoutes  *bool `json:"includeClientRoutes,omitempty"`
	IncludeFakeIPConfig  *bool `json:"includeFakeIPConfig,omitempty"`
	// Fine-grained item filters (when non-nil, only items whose IDs are in the slice are migrated)
	SelectedTunnelIDs []string `json:"selectedTunnelIds,omitempty"`
	SelectedGroupIDs  []string `json:"selectedGroupIds,omitempty"`
	// PostSingboxAction: "switch_standby" | "switch_and_stop" | "switch_and_uninstall" | "keep_current"
	PostSingboxAction string `json:"postSingboxAction,omitempty"`
	// CreateSnapshot: when true (default for interactive migrations), creates a full system snapshot before applying.
	// Always enforced when PostSingboxAction is "switch_and_uninstall".
	CreateSnapshot *bool `json:"createSnapshot,omitempty"`
}

func (o *MigrationOptions) shouldCreateSnapshot() bool {
	if o != nil && strings.EqualFold(strings.TrimSpace(o.PostSingboxAction), "switch_and_uninstall") {
		return true
	}
	return o != nil && (o.CreateSnapshot == nil || *o.CreateSnapshot)
}

func (o *MigrationOptions) includeTunnels() bool {
	return o == nil || o.IncludeTunnels == nil || *o.IncludeTunnels
}

func (o *MigrationOptions) includeSubscriptions() bool {
	return o == nil || o.IncludeSubscriptions == nil || *o.IncludeSubscriptions
}

func (o *MigrationOptions) includeGroups() bool {
	return o == nil || o.IncludeGroups == nil || *o.IncludeGroups
}

func (o *MigrationOptions) includeClientRoutes() bool {
	return o == nil || o.IncludeClientRoutes == nil || *o.IncludeClientRoutes
}

func (o *MigrationOptions) includeFakeIPConfig() bool {
	return o == nil || o.IncludeFakeIPConfig == nil || *o.IncludeFakeIPConfig
}

func (o *MigrationOptions) routerSource() string {
	if o == nil {
		return "auto"
	}
	switch strings.ToLower(strings.TrimSpace(o.RouterSource)) {
	case "tproxy", "fakeip", "both":
		return strings.ToLower(strings.TrimSpace(o.RouterSource))
	default:
		return "auto"
	}
}

func (o *MigrationOptions) isReplaceMode() bool {
	return o != nil && strings.EqualFold(strings.TrimSpace(o.Mode), "replace")
}

// FakeIPMigrationInfo describes Sing-box FakeIP-TUN settings discovered in 21-fakeip.json / settings.json.
type FakeIPMigrationInfo struct {
	Detected      bool   `json:"detected"`
	SourceFile    string `json:"sourceFile,omitempty"`
	Inet4Range    string `json:"inet4Range,omitempty"`
	Inet6Range    string `json:"inet6Range,omitempty"`
	RealDNS       string `json:"realDns,omitempty"`
	RouteFinal    string `json:"routeFinal,omitempty"`
	DNSRulesCount int    `json:"dnsRulesCount,omitempty"`
	Applied       bool   `json:"applied,omitempty"`
}

// MigrationResult summarizes the tunnels and routing groups migrated from sing-box to firc.
type MigrationResult struct {
	Tunnels               []Tunnel             `json:"tunnels"`
	Groups                []Group              `json:"groups"`
	Skipped               []string             `json:"skipped,omitempty"`
	SubscriptionsMigrated int                  `json:"subscriptionsMigrated,omitempty"`
	RuleSetsMigrated      int                  `json:"ruleSetsMigrated,omitempty"`
	ClientRoutesMigrated  int                  `json:"clientRoutesMigrated,omitempty"`
	RouterSourcesUsed     []string             `json:"routerSourcesUsed,omitempty"`
	FakeIP                *FakeIPMigrationInfo `json:"fakeip,omitempty"`
	SnapshotCreated       *SnapshotMeta        `json:"snapshotCreated,omitempty"`
	ConfigMigrated        bool                 `json:"configMigrated"`
	FircStarted           bool                 `json:"fircStarted"`
	EngineSwitched        bool                 `json:"engineSwitched"`
	StartError            string               `json:"startError,omitempty"`
	SwitchError           string               `json:"switchError,omitempty"`
	PostActionApplied     string               `json:"postActionApplied,omitempty"`
	PostActionError       string               `json:"postActionError,omitempty"`
	RemovedEngine         *EnginePurgeResult   `json:"removedEngine,omitempty"`
	RecoveryRequired      bool                 `json:"recoveryRequired,omitempty"`
}

// MigrateFromSingboxConfigDir reads sing-box config.d fragments (10-tunnels.json,
// 15-awg.json, 16-awg3.json, 20-router.json, 21-fakeip.json, 21-router-tun.json, 25-adaptive-egress.json,
// 40-subscriptions.json) as well as subscriptions.json and client-routes.json from the
// parent awg-manager data directory, converting all supported proxy outbounds, subscriptions,
// and routing rules into firc Tunnels and Groups.
func MigrateFromSingboxConfigDir(singboxConfigDir string) (*MigrationResult, error) {
	return MigrateFromSingboxConfigDirWithOptions(singboxConfigDir, nil)
}

func loadFirstExistingSlot(singboxConfigDir string, candidates ...string) ([]byte, string) {
	for _, base := range candidates {
		for _, rel := range []string{base, filepath.Join("pending", base), filepath.Join("disabled", base)} {
			data, err := os.ReadFile(filepath.Join(singboxConfigDir, rel))
			if err == nil && len(data) > 0 {
				return data, filepath.ToSlash(rel)
			}
		}
	}
	return nil, ""
}

// MigrateFromSingboxConfigDirWithOptions performs selective migration from singboxConfigDir according to opts.
func MigrateFromSingboxConfigDirWithOptions(singboxConfigDir string, opts *MigrationOptions) (*MigrationResult, error) {
	res := &MigrationResult{
		Tunnels: []Tunnel{},
		Groups:  []Group{},
	}

	tagToIface := make(map[string]string)
	compositeMembers := make(map[string][]string)

	// 1. Read 15-awg.json and 16-awg3.json (or disabled counterparts) to map AWG v2/v3 outbound tags to kernel interfaces
	for _, base := range []string{"15-awg.json", "16-awg3.json"} {
		if data, _ := loadFirstExistingSlot(singboxConfigDir, base); len(data) > 0 {
			collectDirectBindInterfaces(data, tagToIface)
		}
	}

	// 2. Read 10-tunnels.json (or disabled/10-tunnels.json) and convert standalone proxy outbounds into firc Tunnels
	var allTunnels []Tunnel
	if data, _ := loadFirstExistingSlot(singboxConfigDir, "10-tunnels.json"); len(data) > 0 {
		collectCompositeOutbounds(data, tagToIface, compositeMembers)
		tunnels, skipped := migrateSingboxTunnelsFragment(data, tagToIface, len(allTunnels))
		allTunnels = append(allTunnels, tunnels...)
		if opts.includeTunnels() {
			res.Tunnels = append(res.Tunnels, tunnels...)
			res.Skipped = append(res.Skipped, skipped...)
		}
	}

	// 3. Migrate subscriptions from subscriptions.json and/or 40-subscriptions.json
	subTunnels, subSkipped, subCount := migrateSingboxSubscriptions(singboxConfigDir, tagToIface, len(allTunnels))
	allTunnels = append(allTunnels, subTunnels...)
	if opts.includeSubscriptions() {
		if len(subTunnels) > 0 {
			res.Tunnels = append(res.Tunnels, subTunnels...)
		}
		if len(subSkipped) > 0 {
			res.Skipped = append(res.Skipped, subSkipped...)
		}
		res.SubscriptionsMigrated = subCount
	}

	// Ensure tunnel IDs and devices are globally unique across standalone + subscription tunnels
	_ = deduplicateMigratedTunnels(allTunnels, tagToIface)
	res.Tunnels = deduplicateMigratedTunnels(res.Tunnels, tagToIface)
	if opts != nil && opts.SelectedTunnelIDs != nil {
		selectedSet := make(map[string]bool, len(opts.SelectedTunnelIDs))
		for _, id := range opts.SelectedTunnelIDs {
			selectedSet[strings.TrimSpace(id)] = true
		}
		filtered := make([]Tunnel, 0, len(res.Tunnels))
		for _, t := range res.Tunnels {
			if selectedSet[t.ID] {
				filtered = append(filtered, t)
			}
		}
		res.Tunnels = filtered
	}

	// 4. Read 25-adaptive-egress.json (or disabled/25-adaptive-egress.json) for composite egress outbounds
	if data, _ := loadFirstExistingSlot(singboxConfigDir, "25-adaptive-egress.json"); len(data) > 0 {
		collectCompositeOutbounds(data, tagToIface, compositeMembers)
	}

	// 5. Inspect 20-router.json and 21-fakeip.json (plus settings.json for FakeIP metadata)
	tproxyData, tproxyRel := loadFirstExistingSlot(singboxConfigDir, "20-router.json", "21-router-tun.json")
	fakeipData, fakeipRel := loadFirstExistingSlot(singboxConfigDir, "21-fakeip.json")

	fakeipMeta := extractFakeIPMetadata(singboxConfigDir, fakeipData, fakeipRel)
	if fakeipMeta.Detected {
		res.FakeIP = fakeipMeta
	}

	if opts.includeGroups() {
		type routerSlotCandidate struct {
			data []byte
			rel  string
		}
		var slots []routerSlotCandidate
		switch opts.routerSource() {
		case "tproxy":
			if len(tproxyData) > 0 {
				slots = append(slots, routerSlotCandidate{tproxyData, tproxyRel})
			}
		case "fakeip":
			if len(fakeipData) > 0 {
				slots = append(slots, routerSlotCandidate{fakeipData, fakeipRel})
			}
		case "both":
			if len(tproxyData) > 0 {
				slots = append(slots, routerSlotCandidate{tproxyData, tproxyRel})
			}
			if len(fakeipData) > 0 {
				slots = append(slots, routerSlotCandidate{fakeipData, fakeipRel})
			}
		default: // "auto": prefer active slot first, then fallback/secondary slot
			fakeipActive := fakeipRel == "21-fakeip.json" || fakeipRel == "pending/21-fakeip.json"
			if fakeipActive {
				if len(fakeipData) > 0 {
					slots = append(slots, routerSlotCandidate{fakeipData, fakeipRel})
				}
				if len(tproxyData) > 0 {
					slots = append(slots, routerSlotCandidate{tproxyData, tproxyRel})
				}
			} else {
				if len(tproxyData) > 0 {
					slots = append(slots, routerSlotCandidate{tproxyData, tproxyRel})
				}
				if len(fakeipData) > 0 {
					slots = append(slots, routerSlotCandidate{fakeipData, fakeipRel})
				}
			}
		}

		seenGroupIDs := make(map[string]bool)
		usedGroupNames := make(map[string]bool)
		for _, slot := range slots {
			groups, rsCount := migrateSingboxRouterFragment(slot.data, tagToIface, compositeMembers)
			if len(groups) == 0 {
				continue
			}
			res.RouterSourcesUsed = append(res.RouterSourcesUsed, slot.rel)
			res.RuleSetsMigrated += rsCount
			for _, g := range groups {
				if seenGroupIDs[g.ID] {
					continue
				}
				seenGroupIDs[g.ID] = true
				base := g.Name
				for k := 2; usedGroupNames[strings.ToLower(g.Name)]; k++ {
					suffix := fmt.Sprintf("-%d", k)
					maxBase := 15 - len(suffix)
					if len(base) > maxBase {
						g.Name = strings.Trim(base[:maxBase], "-_") + suffix
					} else {
						g.Name = base + suffix
					}
				}
				usedGroupNames[strings.ToLower(g.Name)] = true
				res.Groups = append(res.Groups, g)
			}
		}
	}

	// 6. Migrate device routes from client-routes.json if present
	if opts.includeClientRoutes() {
		clientGroups, clientCount := migrateClientRoutes(singboxConfigDir, tagToIface, compositeMembers, res.Groups)
		if clientCount > 0 {
			res.Groups = clientGroups
			res.ClientRoutesMigrated = clientCount
		}
	}

	if opts != nil && opts.SelectedGroupIDs != nil {
		selectedSet := make(map[string]bool, len(opts.SelectedGroupIDs))
		for _, id := range opts.SelectedGroupIDs {
			selectedSet[strings.TrimSpace(id)] = true
		}
		filtered := make([]Group, 0, len(res.Groups))
		for _, g := range res.Groups {
			if selectedSet[g.ID] {
				filtered = append(filtered, g)
			}
		}
		res.Groups = filtered
	}

	return res, nil
}

// PreviewFromSingbox computes the candidate firc Tunnels, Groups, and Skipped items
// from singboxConfigDir without writing files or mutating runtime state.
func (o *Operator) PreviewFromSingbox(singboxConfigDir string) (*MigrationResult, error) {
	return o.PreviewFromSingboxWithOptions(singboxConfigDir, nil)
}

// PreviewFromSingboxWithOptions computes the candidate firc Tunnels, Groups, FakeIP metadata, and Skipped items
// from singboxConfigDir with optional MigrationOptions without writing files or mutating runtime state.
func (o *Operator) PreviewFromSingboxWithOptions(singboxConfigDir string, opts *MigrationOptions) (*MigrationResult, error) {
	res, err := MigrateFromSingboxConfigDirWithOptions(singboxConfigDir, opts)
	if err != nil {
		return nil, err
	}
	if len(res.Tunnels) > 0 {
		for i := range res.Tunnels {
			NormalizeTunnelNodeRefs(&res.Tunnels[i])
		}
		res.Tunnels = EnsureInboundPort1099(res.Tunnels, o.inboundPort)
		for i := range res.Tunnels {
			if !res.Tunnels[i].Advanced.KernelTCP {
				res.Tunnels[i].Advanced.KernelTCP = true
			}
		}
	}
	return res, nil
}

func mergeAndFilterForLiveFircd(prevTunnels []Tunnel, prevGroups []Group, candTunnels []Tunnel, candGroups []Group, skipped []string, inboundPort uint16) ([]Tunnel, []Group, []string) {
	usedIDs := make(map[string]int, len(prevTunnels))
	usedDevs := make(map[string]bool, len(prevTunnels))
	outTunnels := make([]Tunnel, 0, len(prevTunnels)+len(candTunnels))
	for i, t := range prevTunnels {
		usedIDs[t.ID] = i
		usedDevs[t.Device] = true
		outTunnels = append(outTunnels, t)
	}

	nextFreeDevice := func() string {
		for i := 0; i < 64; i++ {
			dev := fmt.Sprintf("tunvless%d", i)
			if !usedDevs[dev] {
				usedDevs[dev] = true
				return dev
			}
		}
		return "tunvless7"
	}

	devRemap := make(map[string]string)
	for _, t := range candTunnels {
		NormalizeTunnelNodeRefs(&t)
		if existingIdx, ok := usedIDs[t.ID]; ok {
			oldDev := t.Device
			t.Device = outTunnels[existingIdx].Device
			if oldDev != "" && oldDev != t.Device {
				devRemap[oldDev] = t.Device
			}
			t.Inbounds = PreserveOrRepairInbounds(outTunnels[existingIdx].Inbounds, t)
			outTunnels[existingIdx] = t
			continue
		}
		oldDev := t.Device
		t.Device = nextFreeDevice()
		if oldDev != "" && oldDev != t.Device {
			devRemap[oldDev] = t.Device
		}
		t.Inbounds = nil
		usedIDs[t.ID] = len(outTunnels)
		outTunnels = append(outTunnels, t)
	}
	for i := range outTunnels {
		NormalizeTunnelNodeRefs(&outTunnels[i])
	}
	if len(outTunnels) > 0 {
		outTunnels = EnsureInboundPort1099(outTunnels, inboundPort)
	}

	usedGroupNames := make(map[string]int, len(prevGroups))
	usedGroupIDs := make(map[string]int, len(prevGroups))
	outGroups := make([]Group, 0, len(prevGroups)+len(candGroups))
	for i, g := range prevGroups {
		usedGroupNames[strings.ToLower(g.Name)] = i
		usedGroupIDs[g.ID] = i
		outGroups = append(outGroups, g)
	}
	for _, g := range candGroups {
		dedupIfaces := make([]string, 0, len(g.Interface))
		for _, iface := range g.Interface {
			resolved := iface
			if mapped, ok := devRemap[resolved]; ok {
				resolved = mapped
			} else if strings.HasPrefix(resolved, "tunvless") && !usedDevs[resolved] && len(outTunnels) > 0 {
				resolved = outTunnels[0].Device
			}
			if resolved != "" && !slices.Contains(dedupIfaces, resolved) {
				dedupIfaces = append(dedupIfaces, resolved)
			}
		}
		g.Interface = GroupInterface(dedupIfaces)
		if existingIdx, ok := usedGroupIDs[g.ID]; ok {
			g.Name = outGroups[existingIdx].Name
			outGroups[existingIdx] = g
			continue
		}
		if existingIdx, ok := usedGroupNames[strings.ToLower(g.Name)]; ok {
			g.ID = outGroups[existingIdx].ID
			outGroups[existingIdx] = g
			continue
		}
		usedGroupNames[strings.ToLower(g.Name)] = len(outGroups)
		usedGroupIDs[g.ID] = len(outGroups)
		outGroups = append(outGroups, g)
	}
	return outTunnels, outGroups, skipped
}

// MigrateFromSingbox transactionally migrates sing-box config fragments from singboxConfigDir,
// persists the resulting tunnels and routing groups, and applies them to fircd when running.
// If any file write or runtime apply fails, both disk files and runtime state are rolled back;
// if rollback fails, recovery_required is set.
func (o *Operator) MigrateFromSingbox(ctx context.Context, singboxConfigDir string) (*MigrationResult, error) {
	return o.MigrateFromSingboxWithOptions(ctx, singboxConfigDir, nil)
}

// MigrateFromSingboxWithOptions transactionally migrates sing-box config fragments from singboxConfigDir
// according to opts (supporting merge/replace modes, selective tunnel/group IDs, and FakeIP pool/DNS settings).
func (o *Operator) MigrateFromSingboxWithOptions(ctx context.Context, singboxConfigDir string, opts *MigrationOptions) (*MigrationResult, error) {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()

	if err := o.checkMutationAllowed(); err != nil {
		return nil, err
	}
	res, err := o.PreviewFromSingboxWithOptions(singboxConfigDir, opts)
	if err != nil {
		return nil, err
	}

	_ = o.SavePreFircSingboxConfig(singboxConfigDir, false)

	activeEngine := o.resolveSnapshotActiveEngine(singboxConfigDir, "")
	running := false
	if !o.HasProductionEngineHooks() || strings.EqualFold(activeEngine, "firc") {
		running = o.IsRunning(ctx)
	}
	if activeEngine == "" || activeEngine == "sing-box" {
		if running && !o.HasProductionEngineHooks() {
			activeEngine = "firc"
		}
	}

	if opts.shouldCreateSnapshot() {
		snap, serr := o.CreateSystemSnapshot(singboxConfigDir, "", "auto-pre-migrate", activeEngine)
		if serr != nil {
			return nil, fmt.Errorf("create pre-migration system snapshot: %w", serr)
		}
		res.SnapshotCreated = snap
	}

	tunSnap, err := captureFileSnapshot(o.TunnelsPath(), 0600)
	if err != nil {
		return nil, fmt.Errorf("snapshot tunnels.yaml: %w", err)
	}
	grpSnap, err := captureFileSnapshot(o.GroupsPath(), 0644)
	if err != nil {
		return nil, fmt.Errorf("snapshot groups.yaml: %w", err)
	}
	confSnap, err := captureFileSnapshot(o.ConfigPath(), 0644)
	if err != nil {
		return nil, fmt.Errorf("snapshot firc.conf: %w", err)
	}

	var prevTunnels []Tunnel
	var prevGroups []Group
	if running {
		liveT, err := o.Client().GetTunnels(ctx)
		if err != nil {
			return nil, fmt.Errorf("capture live fircd tunnels snapshot before migration: %w", err)
		}
		prevTunnels = liveT
		liveG, err := o.Client().GetGroups(ctx, true)
		if err != nil {
			return nil, fmt.Errorf("capture live fircd groups snapshot before migration: %w", err)
		}
		prevGroups = liveG
	} else {
		prevTunnels, _ = o.LoadTunnelsFile()
		prevGroups, _ = o.LoadGroupsFile()
	}

	journal, jErr := o.beginMigrationJournal("forward_migrate", []fileSnapshot{tunSnap, grpSnap, confSnap})
	if jErr != nil {
		return nil, fmt.Errorf("persist durable migration journal before forward_migrate: %w", jErr)
	}

	rollbackAll := func(primaryErr error, failedStep string, restoreRuntimeTunnels, restoreRuntimeGroups bool) error {
		var rbErrs []error
		if running && restoreRuntimeGroups {
			if _, rerr := o.SyncGroupsRuntimeOnly(ctx, prevGroups); rerr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("restore runtime groups: %w", rerr))
			}
			if nfErr := o.ReconcileExtNetfilterStrict(ctx, o.LoadExtConfig(), prevGroups); nfErr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("restore netfilter rules: %w", nfErr))
			}
		}
		if running && restoreRuntimeTunnels {
			if _, rerr := o.SyncTunnels(ctx, prevTunnels, false); rerr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("restore runtime tunnels: %w", rerr))
			}
		}
		if ferr := rollbackMigrationJournalBackups(journal.Backups); ferr != nil {
			rbErrs = append(rbErrs, ferr)
		}
		if len(rbErrs) > 0 {
			reason := fmt.Sprintf("MigrateFromSingbox rollback failed: %v", errors.Join(rbErrs...))
			persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:         reason,
				TransitionID:   journal.JournalID,
				TargetEngine:   "firc",
				PreviousEngine: "firc",
				FailedStep:     failedStep,
				LastCompleted:  journal.Stage,
			})
			return errors.Join(primaryErr, fmt.Errorf("%w: %s", ErrRecoveryRequired, reason), errors.Join(rbErrs...), persistErr)
		}
		if jErr := o.cleanupRolledBackMigrationJournal(journal); jErr != nil {
			reason := fmt.Sprintf("MigrateFromSingbox rollback journal cleanup failed: %v", jErr)
			persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:         reason,
				TransitionID:   journal.JournalID,
				TargetEngine:   "firc",
				PreviousEngine: "firc",
				FailedStep:     failedStep + "_rollback_journal_cleanup",
				LastCompleted:  "rolled_back",
			})
			return errors.Join(primaryErr, fmt.Errorf("%w: %s", ErrRecoveryRequired, reason), jErr, persistErr)
		}
		return primaryErr
	}

	if !opts.isReplaceMode() && (running || opts != nil) {
		res.Tunnels, res.Groups, res.Skipped = mergeAndFilterForLiveFircd(prevTunnels, prevGroups, res.Tunnels, res.Groups, res.Skipped, o.inboundPort)
	}

	writeTunnels := len(res.Tunnels) > 0 || (opts.isReplaceMode() && opts.includeTunnels())
	if writeTunnels {
		if len(res.Tunnels) > 0 {
			for i := range res.Tunnels {
				NormalizeTunnelNodeRefs(&res.Tunnels[i])
			}
			res.Tunnels = EnsureInboundPort1099(res.Tunnels, o.inboundPort)
			if vErr := verifyMigratedTunnelsResolved(res.Tunnels); vErr != nil {
				return nil, rollbackAll(fmt.Errorf("verify migrated firc node references: %w", vErr), "verify_node_refs", false, false)
			}
		}
		if err := o.advanceMigrationJournal(journal, "writing_files", 0, o.TunnelsPath()); err != nil {
			return nil, rollbackAll(err, "journal_tunnels_yaml", false, false)
		}
		if err := o.SaveTunnelsFile(res.Tunnels); err != nil {
			return nil, rollbackAll(fmt.Errorf("save migrated firc tunnels: %w", err), "save_tunnels_yaml", false, false)
		}
		if loadedT, rErr := o.LoadTunnelsFile(); rErr != nil || len(loadedT) != len(res.Tunnels) {
			return nil, rollbackAll(fmt.Errorf("readback migrated tunnels.yaml failed: %v", rErr), "readback_tunnels_yaml", false, false)
		}
		if hErr := o.invokeMigrationStepHook("forward_migrate", "writing_files", 0, o.TunnelsPath()); hErr != nil {
			return nil, rollbackAll(hErr, "hook_tunnels_yaml", false, false)
		}
		if running {
			if _, err := o.SyncTunnels(ctx, res.Tunnels, true); err != nil {
				return nil, rollbackAll(fmt.Errorf("sync migrated firc tunnels: %w", err), "sync_tunnels", true, false)
			}
		}
	}

	writeGroups := len(res.Groups) > 0 || (opts.isReplaceMode() && opts.includeGroups())
	if writeGroups {
		if err := o.advanceMigrationJournal(journal, "writing_files", 1, o.GroupsPath()); err != nil {
			return nil, rollbackAll(err, "journal_groups_yaml", writeTunnels, false)
		}
		if err := o.SaveGroupsFile(res.Groups); err != nil {
			return nil, rollbackAll(fmt.Errorf("save migrated firc groups: %w", err), "save_groups_yaml", writeTunnels, false)
		}
		if loadedG, rErr := o.LoadGroupsFile(); rErr != nil || len(loadedG) != len(res.Groups) {
			return nil, rollbackAll(fmt.Errorf("readback migrated groups.yaml failed: %v", rErr), "readback_groups_yaml", writeTunnels, false)
		}
		if hErr := o.invokeMigrationStepHook("forward_migrate", "writing_files", 1, o.GroupsPath()); hErr != nil {
			return nil, rollbackAll(hErr, "hook_groups_yaml", writeTunnels, false)
		}
		if running {
			updatedGroups, err := o.SyncGroupsRuntimeOnly(ctx, res.Groups)
			if err != nil {
				return nil, rollbackAll(fmt.Errorf("sync migrated firc groups: %w", err), "sync_groups", writeTunnels, true)
			}
			if nfErr := o.ReconcileExtNetfilterStrict(ctx, o.LoadExtConfig(), updatedGroups); nfErr != nil {
				return nil, rollbackAll(fmt.Errorf("reconcile migrated firc netfilter: %w", nfErr), "reconcile_netfilter", writeTunnels, true)
			}
		}
	}

	if opts.includeFakeIPConfig() && res.FakeIP != nil && res.FakeIP.Detected {
		if err := o.advanceMigrationJournal(journal, "writing_files", 2, o.ConfigPath()); err != nil {
			return nil, rollbackAll(err, "journal_firc_conf", writeTunnels, writeGroups)
		}
		applied, fErr := o.applyMigratedFakeIPSettings(ctx, res.FakeIP, running)
		if fErr != nil {
			return nil, rollbackAll(fErr, "apply_fakeip_settings", writeTunnels, writeGroups)
		}
		if applied {
			if hErr := o.invokeMigrationStepHook("forward_migrate", "writing_files", 2, o.ConfigPath()); hErr != nil {
				return nil, rollbackAll(hErr, "hook_firc_conf", writeTunnels, writeGroups)
			}
		}
		res.FakeIP.Applied = applied
	}

	committed, cErr := o.completeMigrationJournal(journal)
	if cErr != nil {
		if !committed {
			return nil, rollbackAll(cErr, "complete_migration_journal", writeTunnels, writeGroups)
		}
		return nil, cErr
	}

	if opts != nil {
		st := o.LoadMigrationWizardState()
		st.LastMigratedAt = time.Now().Unix()
		st.LastDirection = "singbox-to-firc"
		if res.SnapshotCreated != nil {
			st.LastSnapshotID = res.SnapshotCreated.ID
		}
		_ = o.SaveMigrationWizardState(st)
	}

	return res, nil
}

func collectDirectBindInterfaces(data []byte, tagToIface map[string]string) {
	var doc struct {
		Outbounds []struct {
			Type          string `json:"type"`
			Tag           string `json:"tag"`
			BindInterface string `json:"bind_interface"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return
	}
	for _, ob := range doc.Outbounds {
		if ob.Tag != "" && ob.BindInterface != "" {
			tagToIface[ob.Tag] = ob.BindInterface
		}
	}
}

func collectCompositeOutbounds(data []byte, tagToIface map[string]string, compositeMembers map[string][]string) {
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return
	}
	for _, ob := range doc.Outbounds {
		tag, _ := ob["tag"].(string)
		typ, _ := ob["type"].(string)
		if tag == "" {
			continue
		}
		if bi, _ := ob["bind_interface"].(string); bi != "" && (typ == "" || typ == "direct") {
			tagToIface[tag] = bi
			continue
		}
		if typ == "selector" || typ == "urltest" || typ == "fallback" {
			var members []string
			if def, _ := ob["default"].(string); def != "" {
				members = append(members, def)
			}
			if typ != "selector" || len(members) == 0 {
				if arr, ok := ob["outbounds"].([]any); ok {
					for _, item := range arr {
						if m, ok := item.(string); ok && m != "" && !slices.Contains(members, m) {
							members = append(members, m)
						}
					}
					if typ == "selector" && len(members) > 1 {
						members = members[:1]
					}
				}
			}
			if len(members) > 0 {
				compositeMembers[tag] = members
			}
		}
	}
}

func migrateSingboxTunnelsFragment(data []byte, tagToIface map[string]string, startIdx int) ([]Tunnel, []string) {
	var doc struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil
	}

	var tunnels []Tunnel
	var skipped []string
	idx := startIdx

	for _, raw := range doc.Outbounds {
		var ob map[string]any
		if err := json.Unmarshal(raw, &ob); err != nil {
			continue
		}
		typ, _ := ob["type"].(string)
		tag, _ := ob["tag"].(string)
		if typ == "" || typ == "direct" || typ == "block" || typ == "dns" || typ == "selector" || typ == "urltest" || typ == "fallback" {
			if bi, _ := ob["bind_interface"].(string); bi != "" && tag != "" {
				tagToIface[tag] = bi
			}
			continue
		}

		link, err := encodeSingboxOutboundToFircLink(raw, ob, tag)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%s): %v", tag, typ, err))
			continue
		}

		device := fmt.Sprintf("tunvless%d", idx)
		id := sanitizeFircID(tag)
		if id == "" {
			id = fmt.Sprintf("tun-%d", idx+1)
		}

		uplink := TunnelUplink{Kind: "auto"}
		if bi, _ := ob["bind_interface"].(string); strings.TrimSpace(bi) != "" {
			uplink = TunnelUplink{Kind: "iface", Ref: strings.TrimSpace(bi)}
		}

		tunnels = append(tunnels, Tunnel{
			ID:          id,
			Device:      device,
			Description: truncateUTF8(tag, 63),
			Enable:      true,
			Uplink:      uplink,
			Sources: []TunnelSource{
				{
					ID:   deterministicHex8ID("src:" + tag + ":" + link),
					Kind: "link",
					Name: tag,
					Link: link,
				},
			},
			Advanced: TunnelAdvanced{
				KernelTCP: true,
			},
		})
		if tag != "" {
			tagToIface[tag] = device
		}
		idx++
	}

	return tunnels, skipped
}

type storedSubscriptionEntry struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	URL           string `json:"url"`
	Inline        string `json:"inline"`
	Path          string `json:"path"`
	BindInterface string `json:"bindInterface"`
	RefreshHours  int    `json:"refreshHours"`
	Enabled       bool   `json:"enabled"`
	Headers       []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
	FilterInclude string   `json:"filterInclude"`
	SelectorTag   string   `json:"selectorTag"`
	MemberTags    []string `json:"memberTags"`
	ActiveMember  string   `json:"activeMember"`
	ExcludedTags  []string `json:"excludedTags"`
	Members       []struct {
		Tag   string `json:"tag"`
		Label string `json:"label"`
	} `json:"members"`
	ExcludedMembers []struct {
		Tag   string `json:"tag"`
		Label string `json:"label"`
	} `json:"excludedMembers"`
}

func findDataFile(singboxConfigDir, filename string) string {
	candidates := []string{
		filepath.Join(singboxConfigDir, filename),
		filepath.Join(filepath.Dir(singboxConfigDir), filename),
		filepath.Join(filepath.Dir(filepath.Dir(singboxConfigDir)), filename),
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func migrateSingboxSubscriptions(singboxConfigDir string, tagToIface map[string]string, startIdx int) ([]Tunnel, []string, int) {
	var tunnels []Tunnel
	var skipped []string
	idx := startIdx
	happKeysPath := ResolveHappKeysPath("", singboxConfigDir)

	// Load materialized outbounds from 40-subscriptions.json if present
	materializedByTag := make(map[string]json.RawMessage)
	materializedMetaByTag := make(map[string]map[string]any)
	var selectorOutbounds []map[string]any
	for _, rel := range []string{"40-subscriptions.json", filepath.Join("disabled", "40-subscriptions.json")} {
		data, err := os.ReadFile(filepath.Join(singboxConfigDir, rel))
		if err != nil {
			continue
		}
		var doc struct {
			Outbounds []json.RawMessage `json:"outbounds"`
		}
		if json.Unmarshal(data, &doc) == nil {
			for _, raw := range doc.Outbounds {
				var ob map[string]any
				if json.Unmarshal(raw, &ob) != nil {
					continue
				}
				tag, _ := ob["tag"].(string)
				typ, _ := ob["type"].(string)
				if tag == "" {
					continue
				}
				if typ == "selector" || typ == "urltest" || typ == "fallback" {
					selectorOutbounds = append(selectorOutbounds, ob)
					continue
				}
				if typ == "direct" || typ == "block" || typ == "dns" {
					continue
				}
				materializedByTag[tag] = raw
				materializedMetaByTag[tag] = ob
			}
		}
		break
	}

	coveredSelectors := make(map[string]bool)
	subsPath := findDataFile(singboxConfigDir, "subscriptions.json")
	if subsPath != "" {
		if data, err := os.ReadFile(subsPath); err == nil {
			var subs []storedSubscriptionEntry
			if err := json.Unmarshal(data, &subs); err != nil {
				var wrapper struct {
					Subscriptions []storedSubscriptionEntry `json:"subscriptions"`
				}
				if json.Unmarshal(data, &wrapper) == nil {
					subs = wrapper.Subscriptions
				}
			}
			for _, sub := range subs {
				label := strings.TrimSpace(sub.Label)
				if label == "" {
					label = sub.SelectorTag
				}
				if label == "" {
					label = "Subscription"
				}
				device := fmt.Sprintf("tunvless%d", idx)
				tunID := sanitizeFircID(sub.SelectorTag)
				if tunID == "" {
					tunID = sanitizeFircID(label)
				}
				if tunID == "" {
					tunID = fmt.Sprintf("sub-%d", idx+1)
				}

				uplink := TunnelUplink{Kind: "auto"}
				if bi := strings.TrimSpace(sub.BindInterface); bi != "" {
					uplink = TunnelUplink{Kind: "iface", Ref: bi}
				}

				var sources []TunnelSource
				var order []string
				var exclude []string

				if strings.TrimSpace(sub.URL) != "" {
					normURL, _, nerr := NormalizeSubscriptionURLForFirc(sub.URL, happKeysPath)
					if nerr == nil && isHTTPSubscriptionURL(normURL) {
						srcID := deterministicHex8ID("src:sub:" + sub.ID + ":" + normURL)
						hdrs := make(map[string]string)
						for _, h := range sub.Headers {
							if k := strings.TrimSpace(h.Name); k != "" && strings.TrimSpace(h.Value) != "" {
								hdrs[k] = strings.TrimSpace(h.Value)
							}
						}
						if len(hdrs) == 0 {
							hdrs = nil
						}
						intervalSec := sub.RefreshHours * 3600
						if intervalSec > 0 && intervalSec < 600 {
							intervalSec = 600
						}
						if intervalSec < 0 {
							intervalSec = 21600
						}
						sources = append(sources, TunnelSource{
							ID:       srcID,
							Kind:     "subscription",
							Name:     SanitizeFircSubName(label),
							URL:      normURL,
							Interval: intervalSec,
							Headers:  hdrs,
						})
						memberLabels := make([]string, len(sub.Members))
						for mi, m := range sub.Members {
							memberLabels[mi] = m.Label
						}
						memberKeys := CanonicalFircSourceNodeKeys(srcID, memberLabels)
						tagToMemberKey := make(map[string]string, len(sub.Members))
						for mi, m := range sub.Members {
							if m.Tag != "" && m.Label != "" {
								tagToMemberKey[m.Tag] = memberKeys[mi]
							}
						}
						if sub.ActiveMember != "" {
							if actKey := tagToMemberKey[sub.ActiveMember]; actKey != "" {
								order = append(order, actKey)
							}
						}
						for mi, m := range sub.Members {
							if m.Label == "" {
								continue
							}
							key := memberKeys[mi]
							if !slices.Contains(order, key) {
								order = append(order, key)
							}
						}
						for _, ex := range sub.ExcludedMembers {
							key := ""
							if ex.Tag != "" {
								key = tagToMemberKey[ex.Tag]
							}
							if key == "" && ex.Label != "" {
								key = CanonicalFircNodeKeyFromName(srcID, ex.Label)
							}
							if key != "" && !slices.Contains(exclude, key) {
								exclude = append(exclude, key)
							}
						}
					} else if nerr != nil && len(sub.MemberTags) == 0 {
						skipped = append(skipped, fmt.Sprintf("%s (subscription): %v", label, nerr))
						continue
					}
				}

				// Fallback or inline/file subscription: materialize from sub.Inline or 40-subscriptions.json
				if len(sources) == 0 {
					if strings.TrimSpace(sub.Inline) != "" {
						if parsed, perr := ParseInlineSourcesForFirc(sub.Inline, label, happKeysPath); perr == nil && len(parsed) > 0 {
							sources = append(sources, parsed...)
						}
					}
					if len(sources) == 0 {
						memberLabelByTag := make(map[string]string, len(sub.Members))
						for _, m := range sub.Members {
							memberLabelByTag[m.Tag] = m.Label
						}
						for _, mTag := range sub.MemberTags {
							raw, ok := materializedByTag[mTag]
							if !ok {
								continue
							}
							ob := materializedMetaByTag[mTag]
							mLabel := memberLabelByTag[mTag]
							if mLabel == "" {
								mLabel = mTag
							}
							link, lerr := encodeSingboxOutboundToFircLink(raw, ob, mLabel)
							if lerr != nil {
								continue
							}
							srcID := deterministicHex8ID("src:member:" + mTag)
							sources = append(sources, TunnelSource{
								ID:   srcID,
								Kind: "link",
								Name: mLabel,
								Link: link,
							})
							key := CanonicalFircNodeKeyFromLink(srcID, link)
							if mTag == sub.ActiveMember {
								order = append([]string{key}, order...)
							} else {
								order = append(order, key)
							}
						}
					}
				}

				if len(sources) == 0 {
					skipped = append(skipped, fmt.Sprintf("%s (subscription): нет доступных источников или узлов", label))
					continue
				}

				tun := Tunnel{
					ID:          tunID,
					Device:      device,
					Description: truncateUTF8(label, 63),
					Enable:      sub.Enabled,
					Uplink:      uplink,
					Sources:     sources,
					Filter:      strings.TrimSpace(sub.FilterInclude),
					Order:       order,
					Exclude:     exclude,
					Advanced: TunnelAdvanced{
						KernelTCP: true,
					},
				}
				tunnels = append(tunnels, tun)
				if sub.SelectorTag != "" {
					tagToIface[sub.SelectorTag] = device
					coveredSelectors[sub.SelectorTag] = true
				}
				if sub.ID != "" {
					tagToIface["sub-"+sub.ID] = device
					if len(sub.ID) >= 8 {
						tagToIface["sub-"+sub.ID[:8]] = device
					}
				}
				for _, mTag := range sub.MemberTags {
					tagToIface[mTag] = device
				}
				idx++
			}
		}
	}

	// Also migrate any selector/urltest groups in 40-subscriptions.json not covered by subscriptions.json
	for _, sel := range selectorOutbounds {
		selTag, _ := sel["tag"].(string)
		if selTag == "" || coveredSelectors[selTag] {
			continue
		}
		arr, ok := sel["outbounds"].([]any)
		if !ok || len(arr) == 0 {
			continue
		}
		var sources []TunnelSource
		var mTags []string
		for _, item := range arr {
			mTag, _ := item.(string)
			raw, exists := materializedByTag[mTag]
			if !exists {
				continue
			}
			ob := materializedMetaByTag[mTag]
			link, lerr := encodeSingboxOutboundToFircLink(raw, ob, mTag)
			if lerr != nil {
				continue
			}
			sources = append(sources, TunnelSource{
				ID:   deterministicHex8ID("src:40sub:" + mTag),
				Kind: "link",
				Name: mTag,
				Link: link,
			})
			mTags = append(mTags, mTag)
		}
		if len(sources) == 0 {
			continue
		}
		device := fmt.Sprintf("tunvless%d", idx)
		tunID := sanitizeFircID(selTag)
		if tunID == "" {
			tunID = fmt.Sprintf("sub-%d", idx+1)
		}
		tunnels = append(tunnels, Tunnel{
			ID:          tunID,
			Device:      device,
			Description: truncateUTF8(selTag, 63),
			Enable:      true,
			Uplink:      TunnelUplink{Kind: "auto"},
			Sources:     sources,
			Advanced: TunnelAdvanced{
				KernelTCP: true,
			},
		})
		tagToIface[selTag] = device
		for _, mTag := range mTags {
			tagToIface[mTag] = device
		}
		idx++
	}

	return tunnels, skipped, len(tunnels)
}

func deduplicateMigratedTunnels(tunnels []Tunnel, tagToIface map[string]string) []Tunnel {
	usedIDs := make(map[string]bool, len(tunnels))
	usedDevs := make(map[string]bool, len(tunnels))
	devRemap := make(map[string]string)
	out := make([]Tunnel, 0, len(tunnels))

	for i, t := range tunnels {
		baseID := t.ID
		if baseID == "" {
			baseID = fmt.Sprintf("tun-%d", i+1)
		}
		id := baseID
		for k := 2; usedIDs[id]; k++ {
			suffix := fmt.Sprintf("-%d", k)
			maxBase := 15 - len(suffix)
			if len(baseID) > maxBase {
				id = strings.Trim(baseID[:maxBase], "-_") + suffix
			} else {
				id = baseID + suffix
			}
		}
		usedIDs[id] = true
		t.ID = id

		dev := t.Device
		if dev == "" || usedDevs[dev] {
			for d := 0; d < 99; d++ {
				cand := fmt.Sprintf("tunvless%d", d)
				if !usedDevs[cand] {
					dev = cand
					break
				}
			}
		}
		if t.Device != "" && t.Device != dev {
			devRemap[t.Device] = dev
		}
		usedDevs[dev] = true
		t.Device = dev
		out = append(out, t)
	}

	if len(devRemap) > 0 {
		for k, v := range tagToIface {
			if mapped, ok := devRemap[v]; ok {
				tagToIface[k] = mapped
			}
		}
	}
	return out
}

func truncateUTF8(s string, maxBytes int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxBytes {
		return s
	}
	res := ""
	for _, r := range s {
		rs := string(r)
		if len(res)+len(rs) > maxBytes {
			break
		}
		res += rs
	}
	return strings.TrimSpace(res)
}

type sbRouterRule struct {
	Type         string         `json:"type"`
	Mode         string         `json:"mode"`
	Rules        []sbRouterRule `json:"rules"`
	Domain       []string       `json:"domain"`
	DomainSuffix []string       `json:"domain_suffix"`
	IPCIDR       []string       `json:"ip_cidr"`
	SourceIPCIDR []string       `json:"source_ip_cidr"`
	RuleSet      []string       `json:"rule_set"`
	IPIsPrivate  *bool          `json:"ip_is_private"`
	Action       string         `json:"action"`
	Outbound     string         `json:"outbound"`
	Server       string         `json:"server"`
}

func extractFakeIPMetadata(singboxConfigDir string, fakeipData []byte, fakeipRel string) *FakeIPMigrationInfo {
	info := &FakeIPMigrationInfo{}
	if len(fakeipData) > 0 {
		var doc struct {
			DNS struct {
				Servers []struct {
					Tag        string `json:"tag"`
					Type       string `json:"type"`
					Server     string `json:"server"`
					Inet4Range string `json:"inet4_range"`
					Inet6Range string `json:"inet6_range"`
				} `json:"servers"`
				Rules []sbRouterRule `json:"rules"`
			} `json:"dns"`
			Route struct {
				Final string `json:"final"`
			} `json:"route"`
		}
		if err := json.Unmarshal(fakeipData, &doc); err == nil {
			info.Detected = true
			info.SourceFile = fakeipRel
			info.RouteFinal = strings.TrimSpace(doc.Route.Final)
			for _, sv := range doc.DNS.Servers {
				if sv.Type == "fakeip" || sv.Tag == "fakeip" {
					if sv.Inet4Range != "" {
						info.Inet4Range = strings.TrimSpace(sv.Inet4Range)
					}
					if sv.Inet6Range != "" {
						info.Inet6Range = strings.TrimSpace(sv.Inet6Range)
					}
				} else if sv.Tag == "real" && sv.Server != "" {
					info.RealDNS = strings.TrimSpace(sv.Server)
				}
			}
			for _, r := range doc.DNS.Rules {
				if r.Server == "fakeip" {
					info.DNSRulesCount++
				}
			}
		}
	}

	if settingsPath := findDataFile(singboxConfigDir, "settings.json"); settingsPath != "" {
		if sData, err := os.ReadFile(settingsPath); err == nil {
			var sDoc struct {
				SingboxRouter struct {
					RoutingMode      string `json:"routingMode"`
					FakeIPPool4      string `json:"fakeipPool4"`
					FakeIPPool6      string `json:"fakeipPool6"`
					FakeIPRealServer string `json:"fakeipRealServer"`
				} `json:"singboxRouter"`
			}
			if json.Unmarshal(sData, &sDoc) == nil {
				sr := sDoc.SingboxRouter
				if strings.EqualFold(sr.RoutingMode, "fakeip-tun") {
					info.Detected = true
					if info.SourceFile == "" {
						info.SourceFile = "settings.json"
					}
				}
				if info.Inet4Range == "" && strings.TrimSpace(sr.FakeIPPool4) != "" {
					info.Inet4Range = strings.TrimSpace(sr.FakeIPPool4)
				}
				if info.Inet6Range == "" && strings.TrimSpace(sr.FakeIPPool6) != "" {
					info.Inet6Range = strings.TrimSpace(sr.FakeIPPool6)
				}
				if info.RealDNS == "" && strings.TrimSpace(sr.FakeIPRealServer) != "" {
					info.RealDNS = strings.TrimSpace(sr.FakeIPRealServer)
				}
			}
		}
	}
	return info
}

func ensureNestedMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok && existing != nil {
		return existing
	}
	created := make(map[string]any)
	parent[key] = created
	return created
}

func (o *Operator) applyMigratedFakeIPSettings(ctx context.Context, info *FakeIPMigrationInfo, running bool) (bool, error) {
	if info == nil || (!strings.Contains(info.Inet4Range, "/") && !strings.Contains(info.Inet6Range, "/") && info.RealDNS == "") {
		return false, nil
	}
	changes := make(map[string]interface{})
	if strings.Contains(info.Inet4Range, "/") {
		changes["app.addressPool.v4.pool"] = info.Inet4Range
	}
	if strings.Contains(info.Inet6Range, "/") {
		changes["app.addressPool.v6.pool"] = info.Inet6Range
	}
	if info.RealDNS != "" && net.ParseIP(info.RealDNS) != nil {
		changes["app.dnsProxy.upstream.address"] = info.RealDNS
	}
	if len(changes) == 0 {
		return false, nil
	}

	if err := o.EnsureConfigFiles(); err != nil {
		return false, fmt.Errorf("ensure firc.conf before applying FakeIP settings: %w", err)
	}
	confPath := o.ConfigPath()
	rawConf, err := os.ReadFile(confPath)
	if err != nil {
		return false, fmt.Errorf("read firc.conf for FakeIP settings: %w", err)
	}
	doc := make(map[string]any)
	if len(bytes.TrimSpace(rawConf)) > 0 {
		if err := yaml.Unmarshal(rawConf, &doc); err != nil {
			return false, fmt.Errorf("parse firc.conf for FakeIP settings: %w", err)
		}
	}
	appMap := ensureNestedMap(doc, "app")
	if strings.Contains(info.Inet4Range, "/") {
		fakeipMap := ensureNestedMap(appMap, "fakeip")
		fakeipMap["v4Pool"] = info.Inet4Range
		addrPoolMap := ensureNestedMap(appMap, "addressPool")
		v4Map := ensureNestedMap(addrPoolMap, "v4")
		v4Map["pool"] = info.Inet4Range
	}
	if strings.Contains(info.Inet6Range, "/") {
		fakeipMap := ensureNestedMap(appMap, "fakeip")
		fakeipMap["v6Pool"] = info.Inet6Range
		addrPoolMap := ensureNestedMap(appMap, "addressPool")
		v6Map := ensureNestedMap(addrPoolMap, "v6")
		v6Map["pool"] = info.Inet6Range
	}
	if info.RealDNS != "" && net.ParseIP(info.RealDNS) != nil {
		dnsProxyMap := ensureNestedMap(appMap, "dnsProxy")
		upstreamMap := ensureNestedMap(dnsProxyMap, "upstream")
		upstreamMap["address"] = info.RealDNS
	}
	outBytes, err := yaml.Marshal(doc)
	if err != nil {
		return false, fmt.Errorf("marshal firc.conf FakeIP settings: %w", err)
	}
	if err := atomicWriteYAMLFile(confPath, outBytes, 0644); err != nil {
		return false, fmt.Errorf("write firc.conf FakeIP settings: %w", err)
	}
	readBack, err := os.ReadFile(confPath)
	if err != nil || !bytes.Equal(readBack, outBytes) {
		return false, fmt.Errorf("verify firc.conf FakeIP settings readback failed: %v", err)
	}

	if running {
		if _, err := o.Client().PutSettings(ctx, changes); err != nil {
			return false, fmt.Errorf("apply migrated FakeIP settings to fircd runtime: %w", err)
		}
	}
	return true, nil
}

func resolveOutboundInterfaces(outbound string, tagToIface map[string]string, compositeMembers map[string][]string) GroupInterface {
	visited := make(map[string]bool)
	var collect func(tag string, out *[]string)
	collect = func(tag string, out *[]string) {
		if tag == "" || visited[tag] {
			return
		}
		visited[tag] = true
		if iface, ok := tagToIface[tag]; ok && iface != "" {
			if !slices.Contains(*out, iface) {
				*out = append(*out, iface)
			}
			return
		}
		if members, ok := compositeMembers[tag]; ok && len(members) > 0 {
			for _, m := range members {
				collect(m, out)
			}
			return
		}
		if iface, ok := tagToIface["awg-"+tag]; ok && iface != "" {
			if !slices.Contains(*out, iface) {
				*out = append(*out, iface)
			}
		}
	}
	var ifaces []string
	collect(outbound, &ifaces)
	if len(ifaces) > 0 {
		return GroupInterface(ifaces)
	}
	return GroupInterface{outbound}
}

func migrateSingboxRouterFragment(data []byte, tagToIface map[string]string, compositeMembers map[string][]string) ([]Group, int) {
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
		DNS       struct {
			Rules []sbRouterRule `json:"rules"`
		} `json:"dns"`
		Route struct {
			RuleSet []struct {
				Tag   string         `json:"tag"`
				Type  string         `json:"type"`
				URL   string         `json:"url"`
				Path  string         `json:"path"`
				Rules []sbRouterRule `json:"rules"`
			} `json:"rule_set"`
			Rules []sbRouterRule `json:"rules"`
			Final string         `json:"final"`
		} `json:"route"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, 0
	}

	if compositeMembers == nil {
		compositeMembers = make(map[string][]string)
	}
	collectCompositeOutbounds(data, tagToIface, compositeMembers)

	ruleSetURL := make(map[string]string)
	inlineRuleSets := make(map[string][]sbRouterRule)
	for _, rs := range doc.Route.RuleSet {
		if strings.HasPrefix(rs.URL, "http://") || strings.HasPrefix(rs.URL, "https://") {
			ruleSetURL[rs.Tag] = rs.URL
		} else if len(rs.Rules) > 0 {
			inlineRuleSets[rs.Tag] = rs.Rules
		}
	}

	usedGroupNames := make(map[string]bool)
	allocGroupName := func(raw string, fallbackIdx int) string {
		base := sanitizeFircID(raw)
		if base == "" {
			base = fmt.Sprintf("group-%d", fallbackIdx)
		}
		name := base
		for k := 2; usedGroupNames[name]; k++ {
			suffix := fmt.Sprintf("-%d", k)
			maxBase := 15 - len(suffix)
			if len(base) > maxBase {
				name = strings.Trim(base[:maxBase], "-_") + suffix
			} else {
				name = base + suffix
			}
		}
		usedGroupNames[name] = true
		return name
	}

	groupOrder := []string{}
	groupsByKey := make(map[string]*Group)
	ruleSetsCount := 0

	ensureGroup := func(key, nameHint, outbound string) *Group {
		if grp, ok := groupsByKey[key]; ok {
			return grp
		}
		grp := &Group{
			ID:        deterministicHex8ID("grp:" + key),
			Name:      allocGroupName(nameHint, len(groupOrder)+1),
			Interface: resolveOutboundInterfaces(outbound, tagToIface, compositeMembers),
			Enable:    true,
		}
		groupsByKey[key] = grp
		groupOrder = append(groupOrder, key)
		return grp
	}

	applyMatchersToGroup := func(outbound string, exactDomains, suffixDomains, cidrs, sourceIPs, ruleSets []string) {
		for _, rsTag := range ruleSets {
			if inl, ok := inlineRuleSets[rsTag]; ok {
				for _, ir := range inl {
					var subRuleSets []string
					collectRuleMatchers(ir, &exactDomains, &suffixDomains, &cidrs, &sourceIPs, &subRuleSets)
				}
			}
		}
		if len(exactDomains) == 0 && len(suffixDomains) == 0 && len(cidrs) == 0 && len(sourceIPs) == 0 && len(ruleSets) == 0 {
			return
		}
		grp := ensureGroup(outbound, outbound, outbound)
		for _, d := range exactDomains {
			grp.Rules = appendUniqueRule(grp.Rules, Rule{
				ID:     deterministicHex8ID("rule:" + grp.ID + ":domain:" + d),
				Type:   "domain",
				Rule:   d,
				Enable: true,
			})
		}
		for _, ds := range suffixDomains {
			grp.Rules = appendUniqueRule(grp.Rules, Rule{
				ID:     deterministicHex8ID("rule:" + grp.ID + ":namespace:" + ds),
				Type:   "namespace",
				Rule:   ds,
				Enable: true,
			})
		}
		for _, c := range cidrs {
			ruleType := "subnet"
			if strings.Contains(c, ":") {
				ruleType = "subnet6"
			}
			grp.Rules = appendUniqueRule(grp.Rules, Rule{
				ID:     deterministicHex8ID("rule:" + grp.ID + ":" + ruleType + ":" + c),
				Type:   ruleType,
				Rule:   c,
				Enable: true,
			})
		}
		for _, sip := range sourceIPs {
			sip = strings.TrimSuffix(sip, "/32")
			if !slices.Contains(grp.Devices.Allow, sip) {
				grp.Devices.Allow = append(grp.Devices.Allow, sip)
			}
		}
		for _, rsTag := range ruleSets {
			u, ok := ruleSetURL[rsTag]
			if !ok || u == "" {
				continue
			}
			if grp.List == nil {
				grp.List = &GroupList{
					URL:      u,
					Interval: 86400,
				}
				ruleSetsCount++
			} else if grp.List.URL != u {
				// FIRC supports one remote list per Group: create a dedicated Group for each additional .srs rule-set
				rsKey := outbound + ":rs:" + rsTag
				rsNameHint := strings.TrimPrefix(rsTag, "geosite-")
				rsNameHint = strings.TrimPrefix(rsNameHint, "geoip-")
				rsGrp := ensureGroup(rsKey, rsNameHint, outbound)
				if rsGrp.List == nil {
					rsGrp.List = &GroupList{
						URL:      u,
						Interval: 86400,
					}
					ruleSetsCount++
				}
			}
		}
	}

	for _, r := range doc.Route.Rules {
		if r.Action != "" && r.Action != "route" {
			continue
		}
		if r.IPIsPrivate != nil && *r.IPIsPrivate {
			continue
		}
		if r.Outbound == "" || r.Outbound == "direct" || r.Outbound == "block" || r.Outbound == "dns-out" {
			continue
		}

		var exactDomains, suffixDomains, cidrs, sourceIPs, ruleSets []string
		collectRuleMatchers(r, &exactDomains, &suffixDomains, &cidrs, &sourceIPs, &ruleSets)
		applyMatchersToGroup(r.Outbound, exactDomains, suffixDomains, cidrs, sourceIPs, ruleSets)
	}

	// In Sing-box FakeIP-TUN mode (21-fakeip.json), domains/rule_sets may be configured in dns.rules
	// (with server: "fakeip") and routed through route.final (when route.final is a proxy outbound).
	finalOutbound := strings.TrimSpace(doc.Route.Final)
	if finalOutbound != "" && finalOutbound != "direct" && finalOutbound != "block" && finalOutbound != "dns-out" {
		for _, dr := range doc.DNS.Rules {
			if dr.Server != "fakeip" {
				continue
			}
			var exactDomains, suffixDomains, cidrs, sourceIPs, ruleSets []string
			collectRuleMatchers(dr, &exactDomains, &suffixDomains, &cidrs, &sourceIPs, &ruleSets)
			applyMatchersToGroup(finalOutbound, exactDomains, suffixDomains, cidrs, sourceIPs, ruleSets)
		}
	}

	out := make([]Group, 0, len(groupOrder))
	for _, key := range groupOrder {
		grp := groupsByKey[key]
		if len(grp.Rules) == 0 && grp.List == nil && len(grp.Devices.Allow) == 0 {
			continue
		}
		out = append(out, *grp)
	}
	return out, ruleSetsCount
}

func migrateClientRoutes(singboxConfigDir string, tagToIface map[string]string, compositeMembers map[string][]string, existingGroups []Group) ([]Group, int) {
	crPath := findDataFile(singboxConfigDir, "client-routes.json")
	if crPath == "" {
		return existingGroups, 0
	}
	data, err := os.ReadFile(crPath)
	if err != nil {
		return existingGroups, 0
	}
	var doc struct {
		Routes []struct {
			ID       string `json:"id"`
			ClientIP string `json:"clientIp"`
			TunnelID string `json:"tunnelId"`
			Enabled  bool   `json:"enabled"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.Routes) == 0 {
		return existingGroups, 0
	}

	usedNames := make(map[string]bool, len(existingGroups))
	for _, g := range existingGroups {
		usedNames[strings.ToLower(g.Name)] = true
	}

	byTunnel := make(map[string][]string)
	var tunnelOrder []string
	count := 0
	for _, rt := range doc.Routes {
		if !rt.Enabled {
			continue
		}
		ip := strings.TrimSuffix(strings.TrimSpace(rt.ClientIP), "/32")
		tid := strings.TrimSpace(rt.TunnelID)
		if ip == "" || tid == "" {
			continue
		}
		if _, exists := byTunnel[tid]; !exists {
			tunnelOrder = append(tunnelOrder, tid)
		}
		if !slices.Contains(byTunnel[tid], ip) {
			byTunnel[tid] = append(byTunnel[tid], ip)
			count++
		}
	}
	if count == 0 {
		return existingGroups, 0
	}

	out := append([]Group(nil), existingGroups...)
	for _, tid := range tunnelOrder {
		ips := byTunnel[tid]
		ifaces := resolveOutboundInterfaces(tid, tagToIface, compositeMembers)
		base := sanitizeFircID("dev-" + tid)
		if base == "" {
			base = "devices"
		}
		name := base
		for k := 2; usedNames[name]; k++ {
			suffix := fmt.Sprintf("-%d", k)
			maxBase := 15 - len(suffix)
			if len(base) > maxBase {
				name = strings.Trim(base[:maxBase], "-_") + suffix
			} else {
				name = base + suffix
			}
		}
		usedNames[name] = true
		out = append(out, Group{
			ID:        deterministicHex8ID("grp:client-routes:" + tid),
			Name:      name,
			Interface: ifaces,
			Enable:    true,
			Devices: GroupDevices{
				Allow: ips,
			},
		})
	}
	return out, count
}

func collectRuleMatchers(r sbRouterRule, exactDomains, suffixDomains, cidrs, sourceIPs, ruleSets *[]string) {
	for _, d := range r.Domain {
		if d = strings.TrimSpace(d); d != "" {
			*exactDomains = append(*exactDomains, d)
		}
	}
	for _, ds := range r.DomainSuffix {
		ds = strings.TrimPrefix(strings.TrimSpace(ds), ".")
		if ds != "" {
			*suffixDomains = append(*suffixDomains, ds)
		}
	}
	for _, c := range r.IPCIDR {
		if c = strings.TrimSpace(c); c != "" {
			*cidrs = append(*cidrs, c)
		}
	}
	for _, sip := range r.SourceIPCIDR {
		if sip = strings.TrimSpace(sip); sip != "" {
			*sourceIPs = append(*sourceIPs, sip)
		}
	}
	for _, rs := range r.RuleSet {
		if rs = strings.TrimSpace(rs); rs != "" {
			*ruleSets = append(*ruleSets, rs)
		}
	}
	for _, sub := range r.Rules {
		collectRuleMatchers(sub, exactDomains, suffixDomains, cidrs, sourceIPs, ruleSets)
	}
}

func appendUniqueRule(dst []Rule, item Rule) []Rule {
	for _, existing := range dst {
		if existing.Type == item.Type && existing.Rule == item.Rule {
			return dst
		}
	}
	return append(dst, item)
}

func sanitizeFircID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	res := strings.Trim(b.String(), "-")
	if len(res) > 15 {
		res = strings.Trim(res[:15], "-_")
	}
	return res
}
