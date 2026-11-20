package firc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
	"gopkg.in/yaml.v3"
)

// ErrSingboxValidatorUnavailable is returned when Sing-box candidate verification cannot run
// because no custom validator is wired and the Sing-box binary is empty, missing, or non-executable.
var ErrSingboxValidatorUnavailable = errors.New("firc: sing-box candidate validator unavailable")

const (
	maxStoredSnapshots     = 10
	wizardStateFileName    = "awgm-migration-state.json"
	snapshotsSubdirName    = "snapshots"
	savedSingboxBundleName = "saved-singbox-config.json"
)

// SnapshotFileEntry stores a single file's relative path and raw content inside a system snapshot.
type SnapshotFileEntry struct {
	Scope   string `json:"scope"`   // "singbox" | "awgm" | "firc"
	RelPath string `json:"relPath"` // e.g. "20-router.json", "pending/21-fakeip.json", "subscriptions.json", "tunnels.yaml"
	Mode    uint32 `json:"mode"`
	DataB64 string `json:"dataB64"`
}

// SnapshotMeta describes a saved system state snapshot for UI listing and restore.
type SnapshotMeta struct {
	ID                        string `json:"id"`
	CreatedAt                 int64  `json:"createdAt"`
	Label                     string `json:"label"`
	Trigger                   string `json:"trigger"` // "auto-pre-migrate" | "auto-pre-reverse" | "manual"
	ActiveEngine              string `json:"activeEngine"`
	SingboxTunnelsCount       int    `json:"singboxTunnelsCount"`
	SingboxSubscriptionsCount int    `json:"singboxSubscriptionsCount"`
	SingboxGroupsCount        int    `json:"singboxGroupsCount"`
	FircTunnelsCount          int    `json:"fircTunnelsCount"`
	FircGroupsCount           int    `json:"fircGroupsCount"`
	FilesCount                int    `json:"filesCount"`
	PostActionApplied         string `json:"postActionApplied,omitempty"`
	PostActionError           string `json:"postActionError,omitempty"`
	RecoveryRequired          bool   `json:"recoveryRequired,omitempty"`
}

// SystemSnapshot is the full persisted snapshot bundle stored on disk.
type SystemSnapshot struct {
	SnapshotMeta
	Files []SnapshotFileEntry `json:"files"`
}

// MigrationWizardState persists user preferences and history for the FIRC migration popup wizard.
type MigrationWizardState struct {
	DismissedAutoPopup bool   `json:"dismissedAutoPopup"`
	DismissedAt        int64  `json:"dismissedAt,omitempty"`
	LastMigratedAt     int64  `json:"lastMigratedAt,omitempty"`
	LastDirection      string `json:"lastDirection,omitempty"` // "singbox-to-firc" | "firc-to-singbox" | "snapshot-restore"
	LastSnapshotID     string `json:"lastSnapshotId,omitempty"`
	LastPostAction     string `json:"lastPostAction,omitempty"`
}

// SavedSingboxStatus describes whether a verified saved Sing-box configuration is available
// for Scenario 1 ("Return to saved Sing-box configuration without overwriting from FIRC").
type SavedSingboxStatus struct {
	Available          bool   `json:"available"`
	VerificationStatus string `json:"verificationStatus,omitempty"` // "verified" | "validator_unavailable" | "invalid" | "missing"
	Source             string `json:"source,omitempty"`             // "saved_bundle" | "snapshot"
	SnapshotID         string `json:"snapshotId,omitempty"`
	CreatedAt          int64  `json:"createdAt,omitempty"`
	Label              string `json:"label,omitempty"`
	TunnelsCount       int    `json:"tunnelsCount"`
	SubscriptionsCount int    `json:"subscriptionsCount"`
	GroupsCount        int    `json:"groupsCount"`
	FilesCount         int    `json:"filesCount"`
	UnavailableReason  string `json:"unavailableReason,omitempty"`
}

// SavedSingboxRestoreResult is returned when restoring the saved Sing-box configuration.
type SavedSingboxRestoreResult struct {
	Source            string             `json:"source"`
	RestoredFiles     int                `json:"restoredFiles"`
	SnapshotCreated   *SnapshotMeta      `json:"snapshotCreated,omitempty"`
	PostActionApplied string             `json:"postActionApplied,omitempty"`
	PostActionError   string             `json:"postActionError,omitempty"`
	RemovedEngine     *EnginePurgeResult `json:"removedEngine,omitempty"`
	RecoveryRequired  bool               `json:"recoveryRequired,omitempty"`
}

// MigrationOverviewResponse is returned by GET /api/firc/migrate/state for the startup popup and wizard modal.
type MigrationOverviewResponse struct {
	ShouldShowPopup           bool                    `json:"shouldShowPopup"`
	WizardState               MigrationWizardState    `json:"wizardState"`
	ActiveEngine              string                  `json:"activeEngine"`
	FircRunning               bool                    `json:"fircRunning"`
	FircInstalled             bool                    `json:"fircInstalled"`
	SingboxRunning            bool                    `json:"singboxRunning"`
	SingboxInstalled          bool                    `json:"singboxInstalled"`
	SingboxTunnelsCount       int                     `json:"singboxTunnelsCount"`
	SingboxSubscriptionsCount int                     `json:"singboxSubscriptionsCount"`
	FircTunnelsCount          int                     `json:"fircTunnelsCount"`
	FircGroupsCount           int                     `json:"fircGroupsCount"`
	Snapshots                 []SnapshotMeta          `json:"snapshots"`
	SavedSingbox              *SavedSingboxStatus     `json:"savedSingbox,omitempty"`
	ForwardPreview            *MigrationResult        `json:"forwardPreview,omitempty"`
	ReversePreview            *ReverseMigrationResult `json:"reversePreview,omitempty"`
}

// ReverseMigrationOptions controls FIRC -> Sing-box reverse migration.
type ReverseMigrationOptions struct {
	Mode              string   `json:"mode,omitempty"` // "merge" (default) | "replace"
	IncludeTunnels    *bool    `json:"includeTunnels,omitempty"`
	IncludeGroups     *bool    `json:"includeGroups,omitempty"`
	SelectedTunnelIDs []string `json:"selectedTunnelIds,omitempty"`
	SelectedGroupIDs  []string `json:"selectedGroupIds,omitempty"`
	PostFircAction    string   `json:"postFircAction,omitempty"` // "switch_to_singbox" | "switch_and_stop_firc" | "switch_and_uninstall_firc" | "keep_current"
	CreateSnapshot    *bool    `json:"createSnapshot,omitempty"`
}

func (o *ReverseMigrationOptions) includeTunnels() bool {
	return o == nil || o.IncludeTunnels == nil || *o.IncludeTunnels
}

func (o *ReverseMigrationOptions) includeGroups() bool {
	return o == nil || o.IncludeGroups == nil || *o.IncludeGroups
}

func (o *ReverseMigrationOptions) isReplaceMode() bool {
	return o != nil && strings.EqualFold(strings.TrimSpace(o.Mode), "replace")
}

func (o *ReverseMigrationOptions) shouldCreateSnapshot() bool {
	if o != nil && strings.EqualFold(strings.TrimSpace(o.PostFircAction), "switch_and_uninstall_firc") {
		return true
	}
	return o == nil || o.CreateSnapshot == nil || *o.CreateSnapshot
}

// ReverseMigratedTunnelItem summarizes a tunnel prepared for reverse migration into Sing-box.
type ReverseMigratedTunnelItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Device     string `json:"device"`
	Kind       string `json:"kind"` // "subscription" | "outbound"
	LinksCount int    `json:"linksCount"`
	SubURL     string `json:"subUrl,omitempty"`
	TargetTag  string `json:"targetTag"`
}

// ReverseMigratedGroupItem summarizes a FIRC group prepared for reverse migration into Sing-box.
type ReverseMigratedGroupItem struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	OutboundTag string   `json:"outboundTag"`
	RulesCount  int      `json:"rulesCount"`
	RuleSetURL  string   `json:"ruleSetUrl,omitempty"`
	Devices     []string `json:"devices,omitempty"`
}

// ReverseMigrationResult summarizes the items previewed or written during FIRC -> Sing-box migration.
type ReverseMigrationResult struct {
	Tunnels               []ReverseMigratedTunnelItem `json:"tunnels"`
	Groups                []ReverseMigratedGroupItem  `json:"groups"`
	OutboundsCreated      int                         `json:"outboundsCreated"`
	SubscriptionsMigrated int                         `json:"subscriptionsMigrated"`
	RulesMigrated         int                         `json:"rulesMigrated"`
	RuleSetsMigrated      int                         `json:"ruleSetsMigrated"`
	ClientRoutesMigrated  int                         `json:"clientRoutesMigrated"`
	Skipped               []string                    `json:"skipped,omitempty"`
	VerificationStatus    string                      `json:"verificationStatus,omitempty"` // "verified" | "validator_unavailable" | "invalid"
	VerificationNote      string                      `json:"verificationNote,omitempty"`
	SnapshotCreated       *SnapshotMeta               `json:"snapshotCreated,omitempty"`
	PostActionApplied     string                      `json:"postActionApplied,omitempty"`
	PostActionError       string                      `json:"postActionError,omitempty"`
	RemovedEngine         *EnginePurgeResult          `json:"removedEngine,omitempty"`
	RecoveryRequired      bool                        `json:"recoveryRequired,omitempty"`
}

func (o *Operator) wizardStatePath() string {
	return filepath.Join(o.configDir, wizardStateFileName)
}

func (o *Operator) snapshotsDir() string {
	return filepath.Join(o.configDir, snapshotsSubdirName)
}

func (o *Operator) savedSingboxConfigPath() string {
	return filepath.Join(o.configDir, savedSingboxBundleName)
}

// LoadMigrationWizardState loads the persisted migration wizard state from disk.
func (o *Operator) LoadMigrationWizardState() MigrationWizardState {
	data, err := os.ReadFile(o.wizardStatePath())
	if err != nil || len(data) == 0 {
		return MigrationWizardState{}
	}
	var st MigrationWizardState
	if err := json.Unmarshal(data, &st); err != nil {
		return MigrationWizardState{}
	}
	return st
}

// SaveMigrationWizardState persists the migration wizard state atomically.
func (o *Operator) SaveMigrationWizardState(st MigrationWizardState) error {
	if err := os.MkdirAll(o.configDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteYAMLFile(o.wizardStatePath(), data, 0644)
}

// RecordMigrationPostAction records the post-action in MigrationWizardState only after confirmed success.
func (o *Operator) RecordMigrationPostAction(action string) {
	if strings.TrimSpace(action) == "" {
		return
	}
	st := o.LoadMigrationWizardState()
	st.LastPostAction = strings.TrimSpace(action)
	_ = o.SaveMigrationWizardState(st)
}

func resolveAwgmDataDir(singboxConfigDir string) string {
	if singboxConfigDir != "" {
		parent := filepath.Dir(singboxConfigDir)
		base := strings.ToLower(filepath.Base(parent))
		if base == "singbox" || base == "sing-box" {
			return filepath.Dir(parent)
		}
		for _, candidate := range []string{
			parent,
			filepath.Dir(parent),
		} {
			if _, err := os.Stat(filepath.Join(candidate, "settings.json")); err == nil {
				return candidate
			}
			if _, err := os.Stat(filepath.Join(candidate, "subscriptions.json")); err == nil {
				return candidate
			}
		}
		return parent
	}
	return "/opt/etc/awg-manager"
}

func (o *Operator) resolveSnapshotActiveEngine(singboxConfigDir, activeEngine string) string {
	eng := strings.TrimSpace(activeEngine)
	if eng != "" {
		if norm, err := normalizeCurrentEngineName(eng); err == nil {
			return norm
		}
		return eng
	}
	hooks := o.DefaultEngineHooks()
	if hooks.GetCurrentEngine != nil {
		if cur, err := hooks.GetCurrentEngine(context.Background()); err == nil && strings.TrimSpace(cur) != "" {
			if norm, err := normalizeCurrentEngineName(cur); err == nil {
				return norm
			}
			return strings.TrimSpace(cur)
		}
	}
	awgmDir := resolveAwgmDataDir(singboxConfigDir)
	if raw, err := os.ReadFile(filepath.Join(awgmDir, "settings.json")); err == nil && len(raw) > 0 {
		var doc struct {
			RoutingEngine string `json:"routingEngine"`
			SingboxRouter struct {
				RoutingEngine string `json:"routingEngine"`
			} `json:"singboxRouter"`
		}
		if json.Unmarshal(raw, &doc) == nil {
			cand := strings.TrimSpace(doc.SingboxRouter.RoutingEngine)
			if cand == "" {
				cand = strings.TrimSpace(doc.RoutingEngine)
			}
			if cand != "" {
				if norm, err := normalizeCurrentEngineName(cand); err == nil {
					return norm
				}
				return cand
			}
		}
	}
	return "sing-box"
}

func isValidSnapshotID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 128 {
		return false
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validateSnapshotRelPath(scope, relPath string) (string, error) {
	scope = strings.TrimSpace(scope)
	if scope != "singbox" && scope != "awgm" && scope != "firc" {
		return "", fmt.Errorf("invalid snapshot file scope %q", scope)
	}
	raw := strings.TrimSpace(relPath)
	if raw == "" || strings.Contains(raw, "\\") || strings.HasPrefix(raw, "/") || filepath.IsAbs(raw) {
		return "", fmt.Errorf("invalid snapshot relative path %q", relPath)
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(raw)))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != raw {
		return "", fmt.Errorf("invalid snapshot relative path %q", relPath)
	}
	switch scope {
	case "singbox":
		parts := strings.Split(cleaned, "/")
		if len(parts) == 1 {
			if !strings.HasSuffix(strings.ToLower(parts[0]), ".json") {
				return "", fmt.Errorf("disallowed singbox snapshot file %q", cleaned)
			}
		} else if len(parts) == 2 {
			if (parts[0] != "pending" && parts[0] != "disabled") || !strings.HasSuffix(strings.ToLower(parts[1]), ".json") {
				return "", fmt.Errorf("disallowed singbox snapshot file %q", cleaned)
			}
		} else {
			return "", fmt.Errorf("disallowed singbox snapshot file %q", cleaned)
		}
	case "awgm":
		switch cleaned {
		case "settings.json", "subscriptions.json", "subscription-groups.json", "awg3.json", "client-routes.json", "domain-routes.json":
		default:
			return "", fmt.Errorf("disallowed awgm snapshot file %q", cleaned)
		}
	case "firc":
		switch cleaned {
		case "config.yaml", "firc.conf", "tunnels.yaml", "groups.yaml", "awgm-ext.json":
		default:
			return "", fmt.Errorf("disallowed firc snapshot file %q", cleaned)
		}
	}
	return cleaned, nil
}

type snapshotSettingsRouterSchema struct {
	Enabled                 bool     `json:"enabled"`
	PolicyName              string   `json:"policyName"`
	DeviceMode              string   `json:"deviceMode"`
	RoutingMode             string   `json:"routingMode"`
	InterceptFamily         string   `json:"interceptFamily"`
	SnifferEnabled          bool     `json:"snifferEnabled"`
	WANAutoDetect           bool     `json:"wanAutoDetect"`
	WANInterface            string   `json:"wanInterface"`
	BypassPresets           []string `json:"bypassPresets"`
	BypassExtraPorts        string   `json:"bypassExtraPorts"`
	BypassExtraSubnets      string   `json:"bypassExtraSubnets"`
	BypassGeoIPTags         []string `json:"bypassGeoipTags"`
	IngressInterfaces       []string `json:"ingressInterfaces"`
	SSTPIngress             bool     `json:"sstpIngress"`
	FakeIPStack             string   `json:"fakeipStack"`
	FakeIPPool4             string   `json:"fakeipPool4"`
	FakeIPPool6             string   `json:"fakeipPool6"`
	FakeIPTunAddr4          string   `json:"fakeipTunAddr4"`
	FakeIPMTU               int      `json:"fakeipMtu"`
	FakeIPRealServer        string   `json:"fakeipRealServer"`
	UDPTimeout              string   `json:"udpTimeout"`
	UDPNATMax               int      `json:"udpNatMax"`
	QoSClasses              []any    `json:"qosClasses"`
	PolicyTunSourcePreserve bool     `json:"policyTunSourcePreserve"`
	PolicyTunNATSegments    []string `json:"policyTunNatSegments"`
	PolicyTunDescription    string   `json:"policyTunDescription"`
	CacheFileLocation       string   `json:"cacheFileLocation"`
	RoutingEngine           string   `json:"routingEngine"`
	MihomoMixedPort         int      `json:"mihomoMixedPort"`
	MihomoHTTPPort          int      `json:"mihomoHttpPort"`
	MihomoSOCKSPort         int      `json:"mihomoSocksPort"`
	MihomoTrafficMode       string   `json:"mihomoTrafficMode"`
	MihomoGlobalTarget      string   `json:"mihomoGlobalTarget"`
	ProxyGroups             []any    `json:"proxyGroups"`
	KeeneticCloudTunnel     bool     `json:"keeneticCloudTunnel"`
	KeeneticCloudOutbound   string   `json:"keeneticCloudOutbound"`
	DynamicCloudCIDRs       []string `json:"dynamicCloudCIDRs"`
	SusaninEnabled          bool     `json:"susaninEnabled"`
	SusaninOutbound         string   `json:"susaninOutbound"`
}

type snapshotSettingsSchema struct {
	SchemaVersion                 int                          `json:"schemaVersion"`
	AuthEnabled                   bool                         `json:"authEnabled"`
	ApiKey                        string                       `json:"apiKey"`
	SessionTtlHours               int                          `json:"sessionTtlHours"`
	McpEnabled                    bool                         `json:"mcpEnabled"`
	ObfuscatorRelayProcess        bool                         `json:"obfuscatorRelayProcess"`
	ObfuscatorKmodTripped         string                       `json:"obfuscatorKmodTripped"`
	ObfuscatorKmodOopsHash        string                       `json:"obfuscatorKmodOopsHash"`
	Server                        map[string]any               `json:"server"`
	PingCheck                     map[string]any               `json:"pingCheck"`
	Logging                       map[string]any               `json:"logging"`
	DisableMemorySaving           bool                         `json:"disableMemorySaving"`
	Updates                       map[string]any               `json:"updates"`
	Download                      map[string]any               `json:"download"`
	DNSRoute                      map[string]any               `json:"dnsRoute"`
	GeoFile                       map[string]any               `json:"geoFile"`
	ConnectivityCheckURL          string                       `json:"connectivityCheckUrl"`
	UsageLevel                    string                       `json:"usageLevel"`
	ServerInterfaces              []string                     `json:"serverInterfaces"`
	ForeignInterfaces             []string                     `json:"foreignInterfaces"`
	ServerInterfaceMeta           map[string]any               `json:"serverInterfaceMeta"`
	ServerPeerSecrets             map[string]any               `json:"serverPeerSecrets"`
	ManagedServers                []any                        `json:"managedServers"`
	ManagedServer                 map[string]any               `json:"managedServer"`
	ManagedPolicies               []string                     `json:"managedPolicies"`
	MonitoringExcludedTunnels     []string                     `json:"monitoringExcludedTunnels"`
	SingboxRouter                 snapshotSettingsRouterSchema `json:"singboxRouter"`
	RoutingEngine                 string                       `json:"routingEngine"`
	SingboxManuallyStopped        bool                         `json:"singboxManuallyStopped"`
	SingboxStoppedByEngine        bool                         `json:"singboxStoppedByEngine"`
	CreateNDMSProxyForSingbox     bool                         `json:"createNDMSProxyForSingbox"`
	SingboxBootstrapDNS           string                       `json:"singboxBootstrapDNS"`
	SingboxClashPort              int                          `json:"singboxClashPort"`
	ManagedPeerAllowIPsMigrated   bool                         `json:"managedPeerAllowIPsMigrated"`
	LegacyFakeIP                  map[string]any               `json:"fakeip"`
	LegacyPolicyTun               map[string]any               `json:"policyTun"`
	OpkgTun                       map[string]any               `json:"opkgTun"`
	DNSChainPreset                map[string]any               `json:"dnsChainPreset"`
	VKCalls                       map[string]any               `json:"vkCalls"`
	AmneziaPremiumMirrorURL       string                       `json:"amneziaPremiumMirrorUrl"`
	AmneziaPremiumKeyCipher       string                       `json:"amneziaPremiumKeyCipher"`
	AmneziaPremiumDeclaredCountry string                       `json:"amneziaPremiumDeclaredCountry"`
}

func validateSettingsJSONSemantics(data []byte) error {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return fmt.Errorf("invalid settings.json object: %w", err)
	}
	if len(rawMap) == 0 {
		return errors.New("settings.json cannot be an empty JSON object")
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var parsed snapshotSettingsSchema
	if err := dec.Decode(&parsed); err != nil {
		return fmt.Errorf("settings.json schema validation failed: %w", err)
	}

	checkEngine := func(eng string) error {
		e := strings.ToLower(strings.TrimSpace(eng))
		if e == "" {
			return nil
		}
		if e != "sing-box" && e != "singbox" && e != "mihomo" && e != "firc" {
			return fmt.Errorf("invalid routingEngine %q in settings.json", eng)
		}
		return nil
	}
	if err := checkEngine(parsed.RoutingEngine); err != nil {
		return err
	}
	if err := checkEngine(parsed.SingboxRouter.RoutingEngine); err != nil {
		return err
	}
	if rm := strings.TrimSpace(parsed.SingboxRouter.RoutingMode); rm != "" {
		if rm != "tproxy" && rm != "fakeip-tun" && rm != "policy-tun" {
			return fmt.Errorf("invalid singboxRouter.routingMode %q in settings.json", rm)
		}
	}
	if dm := strings.TrimSpace(parsed.SingboxRouter.DeviceMode); dm != "" {
		if dm != "policy" && dm != "all" {
			return fmt.Errorf("invalid singboxRouter.deviceMode %q in settings.json", dm)
		}
	}
	for _, p := range []int{
		parsed.SingboxRouter.MihomoMixedPort,
		parsed.SingboxRouter.MihomoHTTPPort,
		parsed.SingboxRouter.MihomoSOCKSPort,
		parsed.SingboxClashPort,
	} {
		if p < 0 || p > 65535 {
			return fmt.Errorf("invalid port %d in settings.json", p)
		}
	}
	if p4 := strings.TrimSpace(parsed.SingboxRouter.FakeIPPool4); p4 != "" {
		pfx, err := netip.ParsePrefix(p4)
		if err != nil || !pfx.Addr().Is4() {
			return fmt.Errorf("invalid singboxRouter.fakeipPool4 %q in settings.json", p4)
		}
	}
	if p6 := strings.TrimSpace(parsed.SingboxRouter.FakeIPPool6); p6 != "" {
		pfx, err := netip.ParsePrefix(p6)
		if err != nil || !pfx.Addr().Is6() {
			return fmt.Errorf("invalid singboxRouter.fakeipPool6 %q in settings.json", p6)
		}
	}
	return nil
}

type snapshotSubscriptionSchema struct {
	ID               string   `json:"id"`
	Name             string   `json:"name,omitempty"`
	Label            string   `json:"label,omitempty"`
	URL              string   `json:"url"`
	Inline           string   `json:"inline,omitempty"`
	Path             string   `json:"path,omitempty"`
	Enabled          *bool    `json:"enabled,omitempty"`
	OutboundTags     []string `json:"outbound_tags,omitempty"`
	Headers          []any    `json:"headers,omitempty"`
	UpdatedAt        any      `json:"updated_at,omitempty"`
	LastFetched      any      `json:"lastFetched,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
	LastErrorCamel   string   `json:"lastError,omitempty"`
	SelectorTag      string   `json:"selectorTag,omitempty"`
	InboundTag       string   `json:"inboundTag,omitempty"`
	ListenPort       int      `json:"listenPort,omitempty"`
	ProxyIndex       int      `json:"proxyIndex,omitempty"`
	MemberTags       []string `json:"memberTags,omitempty"`
	Members          []any    `json:"members,omitempty"`
	OrphanTags       []string `json:"orphanTags,omitempty"`
	RejectedMembers  []any    `json:"rejectedMembers,omitempty"`
	InfoItems        []any    `json:"infoItems,omitempty"`
	DismissedInfoIDs []string `json:"dismissedInfoIds,omitempty"`
	ActiveMember     string   `json:"activeMember,omitempty"`
	ExcludedTags     []string `json:"excludedTags,omitempty"`
	ExcludedMembers  []any    `json:"excludedMembers,omitempty"`
	FilterInclude    string   `json:"filterInclude,omitempty"`
	FilterExclude    string   `json:"filterExclude,omitempty"`
	FilteredMembers  []any    `json:"filteredMembers,omitempty"`
	Mode             string   `json:"mode,omitempty"`
	URLTest          any      `json:"urlTest,omitempty"`
	BindInterface    string   `json:"bindInterface,omitempty"`
	UplinkBinding    any      `json:"uplinkBinding,omitempty"`
	MLKEMOff         bool     `json:"realityMlkemOff,omitempty"`
	Detour           string   `json:"detour,omitempty"`
	RefreshHours     int      `json:"refreshHours,omitempty"`
	UserAgent        string   `json:"userAgent,omitempty"`
}

func validateSubscriptionsJSONSemantics(data []byte) ([]snapshotSubscriptionSchema, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var items []snapshotSubscriptionSchema
	if err := dec.Decode(&items); err != nil {
		return nil, fmt.Errorf("subscriptions.json schema validation failed: %w", err)
	}
	if items == nil {
		return nil, errors.New("subscriptions.json must be a JSON array")
	}
	for i, sub := range items {
		u := strings.TrimSpace(sub.URL)
		if u == "" {
			if strings.TrimSpace(sub.Inline) != "" || strings.TrimSpace(sub.Path) != "" {
				continue
			}
			return nil, fmt.Errorf("subscriptions.json[%d]: url is required", i)
		}
		parsedURL, err := url.Parse(u)
		if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
			return nil, fmt.Errorf("subscriptions.json[%d]: invalid subscription url %q", i, u)
		}
	}
	return items, nil
}

type snapshotClientRouteSchema struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Comment     string `json:"comment,omitempty"`
	MAC         string `json:"mac,omitempty"`
	IP          string `json:"ip,omitempty"`
	OutboundTag string `json:"outbound_tag,omitempty"`
	Outbound    string `json:"outbound,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

func (cr snapshotClientRouteSchema) effectiveOutboundTag() string {
	if t := strings.TrimSpace(cr.OutboundTag); t != "" {
		return t
	}
	return strings.TrimSpace(cr.Outbound)
}

func validateClientRoutesJSONSemantics(data []byte) ([]snapshotClientRouteSchema, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		var storeDoc struct {
			Routes []struct {
				ID             string `json:"id"`
				ClientIP       string `json:"clientIp"`
				ClientHostname string `json:"clientHostname"`
				TunnelID       string `json:"tunnelId"`
				Fallback       string `json:"fallback"`
				Enabled        *bool  `json:"enabled"`
			} `json:"routes"`
			Tables map[string]int `json:"tables"`
		}
		if err := dec.Decode(&storeDoc); err != nil {
			return nil, fmt.Errorf("client-routes.json schema validation failed: %w", err)
		}
		if storeDoc.Routes == nil {
			return nil, errors.New("client-routes.json object missing 'routes' array")
		}
		for i, cr := range storeDoc.Routes {
			if strings.TrimSpace(cr.TunnelID) == "" {
				return nil, fmt.Errorf("client-routes.json.routes[%d]: tunnelId is required", i)
			}
			ipStr := strings.TrimSpace(cr.ClientIP)
			if ipStr == "" {
				return nil, fmt.Errorf("client-routes.json.routes[%d]: clientIp is required", i)
			}
			if _, err := netip.ParseAddr(ipStr); err != nil {
				if _, pErr := netip.ParsePrefix(ipStr); pErr != nil {
					return nil, fmt.Errorf("client-routes.json.routes[%d]: invalid clientIp %q", i, ipStr)
				}
			}
		}
		return []snapshotClientRouteSchema{}, nil
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var items []snapshotClientRouteSchema
	if err := dec.Decode(&items); err != nil {
		return nil, fmt.Errorf("client-routes.json schema validation failed: %w", err)
	}
	if items == nil {
		return nil, errors.New("client-routes.json must be a JSON array")
	}
	for i, cr := range items {
		if cr.effectiveOutboundTag() == "" {
			return nil, fmt.Errorf("client-routes.json[%d]: outbound_tag is required", i)
		}
		if strings.TrimSpace(cr.MAC) == "" && strings.TrimSpace(cr.IP) == "" {
			return nil, fmt.Errorf("client-routes.json[%d]: mac or ip is required", i)
		}
		if ipStr := strings.TrimSpace(cr.IP); ipStr != "" {
			if _, err := netip.ParseAddr(ipStr); err != nil {
				if _, pErr := netip.ParsePrefix(ipStr); pErr != nil {
					return nil, fmt.Errorf("client-routes.json[%d]: invalid ip/cidr %q", i, ipStr)
				}
			}
		}
	}
	return items, nil
}

func validateSingboxOutboundSemantics(ob map[string]any) (string, error) {
	tag, _ := ob["tag"].(string)
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", errors.New("sing-box outbound missing non-empty 'tag'")
	}
	typ, _ := ob["type"].(string)
	typ = strings.ToLower(strings.TrimSpace(typ))
	if typ == "" {
		return "", fmt.Errorf("sing-box outbound %q missing 'type'", tag)
	}

	validTypes := map[string]bool{
		"vless": true, "vmess": true, "trojan": true, "shadowsocks": true,
		"wireguard": true, "hysteria": true, "hysteria2": true, "hy2": true,
		"tuic": true, "socks": true, "http": true, "direct": true, "block": true,
		"dns": true, "selector": true, "urltest": true, "fallback": true,
		"tor": true, "ssh": true, "anytls": true, "trusttunnel": true,
		"naive": true, "mieru": true, "shadowtls": true,
	}
	if !validTypes[typ] {
		return "", fmt.Errorf("sing-box outbound %q has unsupported type %q", tag, typ)
	}

	// Transport compatibility check
	if rawTransport, hasTransport := ob["transport"]; hasTransport && rawTransport != nil {
		trSupports := map[string]bool{
			"vless": true, "vmess": true, "trojan": true, "shadowsocks": true,
		}
		if !trSupports[typ] {
			return "", fmt.Errorf("sing-box outbound %q of type %q does not support transport field", tag, typ)
		}
		trMap, ok := rawTransport.(map[string]any)
		if !ok {
			return "", fmt.Errorf("sing-box outbound %q has non-object transport field", tag)
		}
		trType, _ := trMap["type"].(string)
		trType = strings.ToLower(strings.TrimSpace(trType))
		validTransports := map[string]bool{
			"ws": true, "grpc": true, "http": true, "httpupgrade": true, "quic": true, "xhttp": true,
		}
		if !validTransports[trType] {
			return "", fmt.Errorf("sing-box outbound %q has incompatible or unsupported transport type %q", tag, trType)
		}
		allowedTrKeys := map[string]bool{
			"type": true, "path": true, "headers": true, "max_early_data": true,
			"early_data_header_name": true, "service_name": true, "idle_timeout": true,
			"ping_timeout": true, "permit_without_stream": true, "host": true, "method": true,
			"mode": true, "download": true, "xmux": true, "scMaxEachPostBytes": true,
			"scMaxBufferedPosts": true, "scMinPostsIntervalMs": true, "noGRPCHeader": true,
			"xPaddingBytes": true, "keepAlivePeriod": true, "extra": true,
			"domain_strategy": true, "tls": true, "alpn": true,
			"x_padding_bytes": true, "no_grpc_header": true, "no_sse_header": true,
			"x_padding_obfs_mode": true, "x_padding_key": true, "x_padding_header": true,
			"x_padding_placement": true, "x_padding_method": true, "uplink_http_method": true,
			"session_placement": true, "session_key": true, "seq_placement": true,
			"seq_key": true, "uplink_data_placement": true, "uplink_data_key": true,
			"uplink_chunk_size": true, "sc_max_each_post_bytes": true,
			"sc_min_posts_interval_ms": true, "sc_max_buffered_posts": true,
			"sc_stream_up_server_secs": true, "session_id_table": true, "session_id_length": true,
		}
		for k := range trMap {
			if !allowedTrKeys[k] {
				return "", fmt.Errorf("sing-box outbound %q transport has unknown field %q", tag, k)
			}
		}
	}

	// Protocol-specific required fields
	switch typ {
	case "vless", "vmess", "trojan", "shadowsocks", "hysteria", "hysteria2", "hy2", "tuic", "socks", "http", "trusttunnel", "naive", "anytls", "shadowtls":
		srv, _ := ob["server"].(string)
		if strings.TrimSpace(srv) == "" {
			return "", fmt.Errorf("sing-box outbound %q (%s) missing required 'server'", tag, typ)
		}
		portFloat, ok := ob["server_port"].(float64)
		if !ok || int(portFloat) <= 0 || int(portFloat) > 65535 {
			return "", fmt.Errorf("sing-box outbound %q (%s) has invalid or missing 'server_port'", tag, typ)
		}
		if typ == "vless" || typ == "vmess" {
			uuid, _ := ob["uuid"].(string)
			if strings.TrimSpace(uuid) == "" {
				return "", fmt.Errorf("sing-box outbound %q (%s) missing required 'uuid'", tag, typ)
			}
		}
	case "mieru":
		srv, _ := ob["server"].(string)
		if strings.TrimSpace(srv) == "" {
			return "", fmt.Errorf("sing-box outbound %q (%s) missing required 'server'", tag, typ)
		}
		portFloat, ok := ob["server_port"].(float64)
		rawPorts, hasPorts := ob["server_ports"].([]any)
		if (!ok || int(portFloat) <= 0 || int(portFloat) > 65535) && (!hasPorts || len(rawPorts) == 0) {
			return "", fmt.Errorf("sing-box outbound %q (%s) has invalid or missing 'server_port'/'server_ports'", tag, typ)
		}
	case "wireguard":
		pk, _ := ob["private_key"].(string)
		if strings.TrimSpace(pk) == "" {
			return "", fmt.Errorf("sing-box wireguard outbound %q missing 'private_key'", tag)
		}
	case "selector", "urltest", "fallback":
		rawObs, ok := ob["outbounds"].([]any)
		if !ok || len(rawObs) == 0 {
			return "", fmt.Errorf("sing-box %s outbound %q requires non-empty 'outbounds' list", typ, tag)
		}
	}

	return tag, nil
}

func validateSingboxFragmentSemantics(relPath string, data []byte) error {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return fmt.Errorf("sing-box file %s must be a JSON object: %w", relPath, err)
	}
	if len(rawMap) == 0 {
		return fmt.Errorf("sing-box file %s cannot be an empty JSON object", relPath)
	}

	allowedTopKeys := map[string]bool{
		"log": true, "dns": true, "ntp": true, "inbounds": true, "outbounds": true,
		"route": true, "experimental": true, "endpoints": true, "services": true,
		"certificate": true, "http_clients": true,
	}
	for k := range rawMap {
		if !allowedTopKeys[k] {
			return fmt.Errorf("sing-box file %s contains unsupported top-level key %q", relPath, k)
		}
	}

	baseName := strings.ToLower(filepath.Base(relPath))
	if baseName == "10-tunnels.json" {
		if _, ok := rawMap["outbounds"]; !ok {
			return fmt.Errorf("sing-box mandatory file %s is missing 'outbounds' field", relPath)
		}
	}
	if baseName == "20-router.json" {
		if _, ok := rawMap["route"]; !ok {
			return fmt.Errorf("sing-box mandatory file %s is missing 'route' field", relPath)
		}
	}

	if rawOutbounds, ok := rawMap["outbounds"]; ok {
		var obs []map[string]any
		if err := json.Unmarshal(rawOutbounds, &obs); err != nil {
			return fmt.Errorf("sing-box file %s 'outbounds' must be an array of objects: %w", relPath, err)
		}
		seenTags := make(map[string]bool, len(obs))
		for _, ob := range obs {
			tag, err := validateSingboxOutboundSemantics(ob)
			if err != nil {
				return fmt.Errorf("in %s: %w", relPath, err)
			}
			if seenTags[tag] {
				return fmt.Errorf("in %s: duplicate outbound tag %q", relPath, tag)
			}
			seenTags[tag] = true
		}
	}

	if rawRoute, ok := rawMap["route"]; ok {
		var routeObj map[string]json.RawMessage
		if err := json.Unmarshal(rawRoute, &routeObj); err != nil {
			return fmt.Errorf("sing-box file %s 'route' must be an object: %w", relPath, err)
		}
		allowedRouteKeys := map[string]bool{
			"rules": true, "rule_set": true, "final": true, "auto_detect_interface": true,
			"override_android_vpn": true, "default_interface": true, "default_mark": true,
			"default_domain_resolver": true, "default_network_strategy": true, "default_fallback_network_type": true,
			"default_http_client": true, "default_domain_strategy": true,
			"default_network_interface_address": true, "find_process": true,
			"geoip": true, "geosite": true,
		}
		for k := range routeObj {
			if !allowedRouteKeys[k] {
				return fmt.Errorf("sing-box file %s route contains unknown key %q", relPath, k)
			}
		}
		if rawRules, hasRules := routeObj["rules"]; hasRules {
			var rules []map[string]any
			if err := json.Unmarshal(rawRules, &rules); err != nil {
				return fmt.Errorf("sing-box file %s route.rules must be an array of objects: %w", relPath, err)
			}
			for idx, r := range rules {
				ob, _ := r["outbound"].(string)
				act, _ := r["action"].(string)
				if strings.TrimSpace(ob) == "" && strings.TrimSpace(act) == "" {
					return fmt.Errorf("sing-box file %s route.rules[%d] missing both 'outbound' and 'action'", relPath, idx)
				}
			}
		}
	}

	return nil
}

func validateFircTunnelsYAML(data []byte) ([]Tunnel, error) {
	var rawMap map[string]any
	if err := yaml.Unmarshal(data, &rawMap); err != nil || len(rawMap) == 0 {
		return nil, fmt.Errorf("tunnels.yaml must be a non-empty YAML mapping: %v", err)
	}
	if _, ok := rawMap["tunnels"]; !ok {
		return nil, errors.New("tunnels.yaml missing required top-level 'tunnels' key")
	}

	var doc tunnelsYAMLFile
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("tunnels.yaml schema validation failed: %w", err)
	}

	seenIDs := make(map[string]bool, len(doc.Tunnels))
	seenDevs := make(map[string]bool, len(doc.Tunnels))
	for i, t := range doc.Tunnels {
		id := strings.TrimSpace(t.ID)
		dev := strings.TrimSpace(t.Device)
		if id == "" {
			return nil, fmt.Errorf("tunnels.yaml[%d]: id is required", i)
		}
		if dev == "" {
			return nil, fmt.Errorf("tunnels.yaml[%d] (%s): device is required", i, id)
		}
		if seenIDs[id] {
			return nil, fmt.Errorf("tunnels.yaml: duplicate tunnel id %q", id)
		}
		if seenDevs[dev] {
			return nil, fmt.Errorf("tunnels.yaml: duplicate tunnel device %q", dev)
		}
		seenIDs[id] = true
		seenDevs[dev] = true

		for j, src := range t.Sources {
			kind := strings.ToLower(strings.TrimSpace(src.Kind))
			if kind != "link" && kind != "subscription" {
				return nil, fmt.Errorf("tunnels.yaml[%d] (%s) source[%d]: invalid kind %q", i, id, j, src.Kind)
			}
			if kind == "subscription" {
				u, err := url.Parse(strings.TrimSpace(src.URL))
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					return nil, fmt.Errorf("tunnels.yaml[%d] (%s) source[%d]: invalid subscription url %q", i, id, j, src.URL)
				}
			} else {
				lnk := strings.TrimSpace(src.Link)
				if lnk == "" {
					return nil, fmt.Errorf("tunnels.yaml[%d] (%s) source[%d]: empty link", i, id, j)
				}
				u, err := url.Parse(lnk)
				if err != nil || u.Scheme == "" || u.Host == "" {
					return nil, fmt.Errorf("tunnels.yaml[%d] (%s) source[%d]: malformed link %q", i, id, j, lnk)
				}
				scheme := strings.ToLower(u.Scheme)
				validSchemes := map[string]bool{
					"vless": true, "vmess": true, "trojan": true, "ss": true,
					"hy2": true, "hysteria2": true, "hysteria": true, "tuic": true,
					"wg": true, "awg": true, "socks": true, "socks5": true,
					"http": true, "https": true, "tt": true, "trusttunnel": true,
					"naive": true, "naive+https": true, "naive+quic": true,
					"anytls": true, "mieru": true, "mierus": true,
				}
				if !validSchemes[scheme] {
					return nil, fmt.Errorf("tunnels.yaml[%d] (%s) source[%d]: unsupported link scheme %q", i, id, j, scheme)
				}
				if scheme == "vless" {
					q := u.Query()
					if tr := strings.ToLower(strings.TrimSpace(q.Get("type"))); tr != "" {
						validVLESSNet := map[string]bool{
							"tcp": true, "ws": true, "grpc": true, "xhttp": true,
							"splithttp": true, "httpupgrade": true, "http": true,
						}
						if !validVLESSNet[tr] {
							return nil, fmt.Errorf("tunnels.yaml[%d] (%s) source[%d]: incompatible vless transport type %q", i, id, j, tr)
						}
					}
					if sec := strings.ToLower(strings.TrimSpace(q.Get("security"))); sec != "" {
						if sec != "none" && sec != "tls" && sec != "reality" {
							return nil, fmt.Errorf("tunnels.yaml[%d] (%s) source[%d]: incompatible vless security %q", i, id, j, sec)
						}
					}
				}
			}
		}
	}
	return doc.Tunnels, nil
}

func validateFircGroupsYAML(data []byte) ([]Group, error) {
	var rawMap map[string]any
	if err := yaml.Unmarshal(data, &rawMap); err != nil || len(rawMap) == 0 {
		return nil, fmt.Errorf("groups.yaml must be a non-empty YAML mapping: %v", err)
	}
	if _, ok := rawMap["groups"]; !ok {
		return nil, errors.New("groups.yaml missing required top-level 'groups' key")
	}

	var doc groupsYAMLFile
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("groups.yaml schema validation failed: %w", err)
	}

	seenIDs := make(map[string]bool, len(doc.Groups))
	for i, g := range doc.Groups {
		id := strings.TrimSpace(g.ID)
		if id == "" {
			return nil, fmt.Errorf("groups.yaml[%d]: id is required", i)
		}
		if strings.TrimSpace(g.Name) == "" {
			return nil, fmt.Errorf("groups.yaml[%d] (%s): name is required", i, id)
		}
		if seenIDs[id] {
			return nil, fmt.Errorf("groups.yaml: duplicate group id %q", id)
		}
		seenIDs[id] = true
		if len(g.Interface) == 0 {
			return nil, fmt.Errorf("groups.yaml[%d] (%s): interface list cannot be empty", i, id)
		}
		for _, iface := range g.Interface {
			if strings.TrimSpace(iface) == "" {
				return nil, fmt.Errorf("groups.yaml[%d] (%s): interface name cannot be empty", i, id)
			}
		}
		for j, r := range g.Rules {
			if strings.TrimSpace(r.Rule) == "" {
				return nil, fmt.Errorf("groups.yaml[%d] (%s) rule[%d]: empty rule value", i, id, j)
			}
			rt := strings.ToLower(strings.TrimSpace(r.Type))
			validRuleTypes := map[string]bool{
				"namespace": true, "domain": true, "wildcard": true, "wild": true, "dotwild": true,
				"full": true, "subnet": true, "subnet6": true, "cidr": true, "cidr6": true, "ip": true,
				"regex": true, "keyword": true, "geosite": true, "geoip": true, "mac": true, "src_ip": true,
			}
			if !validRuleTypes[rt] {
				return nil, fmt.Errorf("groups.yaml[%d] (%s) rule[%d]: invalid rule type %q", i, id, j, r.Type)
			}
			if rt == "subnet" || rt == "subnet6" || rt == "cidr" || rt == "cidr6" || rt == "ip" || rt == "src_ip" {
				val := strings.TrimSpace(r.Rule)
				if _, err := netip.ParsePrefix(val); err != nil {
					if _, aErr := netip.ParseAddr(val); aErr != nil {
						return nil, fmt.Errorf("groups.yaml[%d] (%s) rule[%d]: invalid IP/CIDR %q", i, id, j, val)
					}
				}
			}
		}
	}
	return doc.Groups, nil
}

type fircConfSchema struct {
	Version       string `yaml:"version"`
	ConfigVersion string `yaml:"configVersion"`
	App           struct {
		LogLevel string         `yaml:"logLevel"`
		HTTPWeb  map[string]any `yaml:"httpWeb"`
		Link     []string       `yaml:"link"`
		Update   map[string]any `yaml:"update"`
		Check    map[string]any `yaml:"check"`
		DNSProxy struct {
			Host            map[string]any `yaml:"host"`
			DisableRemap53  bool           `yaml:"disableRemap53"`
			DisableDropAAAA bool           `yaml:"disableDropAAAA"`
			DisableStats    bool           `yaml:"disableStats"`
			UnmatchedTTL    any            `yaml:"unmatchedTtl"`
			MaxIdleConns    int            `yaml:"maxIdleConns"`
			MaxConcurrent   int            `yaml:"maxConcurrent"`
			Timeout         any            `yaml:"timeout"`
			Upstream        struct {
				Address string `yaml:"address"`
				Port    int    `yaml:"port"`
			} `yaml:"upstream"`
		} `yaml:"dnsProxy"`
		Netfilter struct {
			IPTables            map[string]any `yaml:"iptables"`
			DisableIPv4         bool           `yaml:"disableIPv4"`
			DisableIPv6         bool           `yaml:"disableIPv6"`
			StartMarkTableIndex int64          `yaml:"startMarkTableIndex"`
		} `yaml:"netfilter"`
		FakeIP struct {
			V4Pool string `yaml:"v4Pool"`
			V6Pool string `yaml:"v6Pool"`
		} `yaml:"fakeip"`
		AddressPool struct {
			TTLClamp any `yaml:"ttlClamp"`
			MaxNames int `yaml:"maxNames"`
			V4       struct {
				Pool  string `yaml:"pool"`
				Chunk int    `yaml:"chunk"`
			} `yaml:"v4"`
			V6 struct {
				Pool  string `yaml:"pool"`
				Chunk int    `yaml:"chunk"`
			} `yaml:"v6"`
		} `yaml:"addressPool"`
	} `yaml:"app"`
}

func validateFircConfYAML(data []byte) error {
	var rawMap map[string]any
	if err := yaml.Unmarshal(data, &rawMap); err != nil || len(rawMap) == 0 {
		return fmt.Errorf("firc.conf must be a non-empty YAML mapping: %v", err)
	}
	appRaw, ok := rawMap["app"].(map[string]any)
	if !ok || len(appRaw) == 0 {
		return errors.New("firc.conf missing required non-empty 'app' mapping section")
	}
	var parsed fircConfSchema
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("firc.conf schema validation failed: %w", err)
	}
	for _, p4 := range []string{parsed.App.FakeIP.V4Pool, parsed.App.AddressPool.V4.Pool} {
		if strings.TrimSpace(p4) != "" {
			pfx, err := netip.ParsePrefix(strings.TrimSpace(p4))
			if err != nil || !pfx.Addr().Is4() {
				return fmt.Errorf("firc.conf invalid IPv4 FakeIP pool %q", p4)
			}
		}
	}
	for _, p6 := range []string{parsed.App.FakeIP.V6Pool, parsed.App.AddressPool.V6.Pool} {
		if strings.TrimSpace(p6) != "" {
			pfx, err := netip.ParsePrefix(strings.TrimSpace(p6))
			if err != nil || !pfx.Addr().Is6() {
				return fmt.Errorf("firc.conf invalid IPv6 FakeIP pool %q", p6)
			}
		}
	}
	if up := strings.TrimSpace(parsed.App.DNSProxy.Upstream.Address); up != "" {
		if net.ParseIP(up) == nil {
			if host, _, err := net.SplitHostPort(up); err != nil || net.ParseIP(host) == nil {
				return fmt.Errorf("firc.conf invalid dnsProxy.upstream.address %q", up)
			}
		}
	}
	return nil
}

func validateDecodedSnapshotFileContent(scope, relPath string, data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("empty content in snapshot file %s/%s", scope, relPath)
	}
	switch scope {
	case "awgm":
		switch relPath {
		case "settings.json":
			return validateSettingsJSONSemantics(data)
		case "subscriptions.json":
			_, err := validateSubscriptionsJSONSemantics(data)
			return err
		case "client-routes.json":
			_, err := validateClientRoutesJSONSemantics(data)
			return err
		case "subscription-groups.json", "awg3.json":
			var arr []any
			if err := json.Unmarshal(data, &arr); err != nil {
				return fmt.Errorf("corrupt JSON array in %s/%s: %w", scope, relPath, err)
			}
			return nil
		case "domain-routes.json":
			var v any
			if err := json.Unmarshal(data, &v); err != nil {
				return fmt.Errorf("corrupt JSON in %s/%s: %w", scope, relPath, err)
			}
			return nil
		}
	case "singbox":
		return validateSingboxFragmentSemantics(relPath, data)
	case "firc":
		switch relPath {
		case "tunnels.yaml":
			_, err := validateFircTunnelsYAML(data)
			return err
		case "groups.yaml":
			_, err := validateFircGroupsYAML(data)
			return err
		case "firc.conf":
			return validateFircConfYAML(data)
		case "awgm-ext.json":
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			var ext ExtConfig
			if err := dec.Decode(&ext); err != nil {
				return fmt.Errorf("awgm-ext.json schema validation failed: %w", err)
			}
			return nil
		case "config.yaml":
			var rawMap map[string]any
			if err := yaml.Unmarshal(data, &rawMap); err != nil || len(rawMap) == 0 {
				return fmt.Errorf("firc/config.yaml must be a non-empty YAML mapping: %v", err)
			}
			return nil
		}
	}
	return fmt.Errorf("unsupported snapshot file %s/%s", scope, relPath)
}

// CreateSystemSnapshot captures a complete snapshot of Sing-box, AWGM routing files, and FIRC configs.
func (o *Operator) CreateSystemSnapshot(singboxConfigDir, label, trigger, activeEngine string) (*SnapshotMeta, error) {
	now := time.Now()
	if strings.TrimSpace(trigger) == "" {
		trigger = "manual"
	}
	if strings.TrimSpace(label) == "" {
		switch trigger {
		case "auto-pre-migrate":
			label = "Авто-снапшот перед миграцией Sing-box → FIRC"
		case "auto-pre-reverse":
			label = "Авто-снапшот перед обратной миграцией FIRC → Sing-box"
		case "auto-pre-purge":
			label = "Авто-снапшот перед удалением движка и его туннелей"
		default:
			label = "Снапшот состояния системы"
		}
	}
	activeEngine = o.resolveSnapshotActiveEngine(singboxConfigDir, activeEngine)

	snapID := fmt.Sprintf("snap-%s-%04d", now.Format("20060102-150405"), now.Nanosecond()/1e5)
	var files []SnapshotFileEntry

	addFile := func(scope, baseDir, relPath string) error {
		if strings.TrimSpace(baseDir) == "" {
			return nil
		}
		full := filepath.Join(baseDir, filepath.FromSlash(relPath))
		st, err := os.Stat(full)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("stat %s (%s): %w", relPath, scope, err)
		}
		if st.IsDir() {
			return nil
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return fmt.Errorf("read %s (%s): %w", relPath, scope, err)
		}
		files = append(files, SnapshotFileEntry{
			Scope:   scope,
			RelPath: filepath.ToSlash(relPath),
			Mode:    uint32(st.Mode().Perm()),
			DataB64: base64.StdEncoding.EncodeToString(data),
		})
		return nil
	}

	// 1. Capture Sing-box config.d files (root, pending, disabled)
	if singboxConfigDir != "" {
		for _, sub := range []string{"", "pending", "disabled"} {
			dir := filepath.Join(singboxConfigDir, sub)
			entries, err := os.ReadDir(dir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("read singbox dir %s: %w", dir, err)
			}
			for _, ent := range entries {
				if ent.IsDir() || !strings.HasSuffix(strings.ToLower(ent.Name()), ".json") {
					continue
				}
				rel := ent.Name()
				if sub != "" {
					rel = sub + "/" + ent.Name()
				}
				if err := addFile("singbox", singboxConfigDir, rel); err != nil {
					return nil, err
				}
			}
		}
	}

	// 2. Capture AWGM dataDir files (settings.json, subscriptions.json, subscription-groups.json, awg3.json, client-routes.json, domain-routes.json)
	awgmDir := resolveAwgmDataDir(singboxConfigDir)
	for _, name := range []string{"settings.json", "subscriptions.json", "subscription-groups.json", "awg3.json", "client-routes.json", "domain-routes.json"} {
		if err := addFile("awgm", awgmDir, name); err != nil {
			return nil, err
		}
	}

	// 3. Capture FIRC configDir files (config.yaml, firc.conf, tunnels.yaml, groups.yaml, awgm-ext.json)
	if o.configDir != "" {
		for _, name := range []string{"config.yaml", "firc.conf", "tunnels.yaml", "groups.yaml", "awgm-ext.json"} {
			if err := addFile("firc", o.configDir, name); err != nil {
				return nil, err
			}
		}
	}

	if len(files) == 0 {
		return nil, errors.New("cannot create empty system snapshot: no configuration files found")
	}

	// Compute summary counts
	var sbTunnels, sbSubs, sbGroups int
	if singboxConfigDir != "" {
		if preview, err := MigrateFromSingboxConfigDir(singboxConfigDir); err == nil && preview != nil {
			sbTunnels = len(preview.Tunnels)
			sbSubs = preview.SubscriptionsMigrated
			sbGroups = len(preview.Groups)
		}
	}
	fircTunnels, _ := o.LoadTunnelsFile()
	fircGroups, _ := o.LoadGroupsFile()

	snap := SystemSnapshot{
		SnapshotMeta: SnapshotMeta{
			ID:                        snapID,
			CreatedAt:                 now.Unix(),
			Label:                     label,
			Trigger:                   trigger,
			ActiveEngine:              activeEngine,
			SingboxTunnelsCount:       sbTunnels,
			SingboxSubscriptionsCount: sbSubs,
			SingboxGroupsCount:        sbGroups,
			FircTunnelsCount:          len(fircTunnels),
			FircGroupsCount:           len(fircGroups),
			FilesCount:                len(files),
		},
		Files: files,
	}

	if err := os.MkdirAll(o.snapshotsDir(), 0700); err != nil {
		return nil, fmt.Errorf("create snapshots dir: %w", err)
	}
	snapPath := filepath.Join(o.snapshotsDir(), snapID+".json")
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot: %w", err)
	}
	if err := atomicWriteYAMLFile(snapPath, raw, 0600); err != nil {
		return nil, fmt.Errorf("write snapshot file: %w", err)
	}

	o.pruneOldSnapshots()
	return &snap.SnapshotMeta, nil
}

// SavePreFircSingboxConfig captures a dedicated Sing-box-only configuration bundle
// ("saved-singbox-config.json") before Sing-box -> FIRC or FIRC -> Sing-box migration overwrites
// Sing-box files, enabling Scenario 1 ("Return to saved Sing-box configuration").
func (o *Operator) SavePreFircSingboxConfig(singboxConfigDir string, overwrite bool) error {
	if strings.TrimSpace(singboxConfigDir) == "" || strings.TrimSpace(o.configDir) == "" {
		return nil
	}
	bundlePath := o.savedSingboxConfigPath()
	if !overwrite {
		if _, err := os.Stat(bundlePath); err == nil {
			return nil
		}
	}

	var files []SnapshotFileEntry
	addFile := func(scope, baseDir, relPath string) error {
		if strings.TrimSpace(baseDir) == "" {
			return nil
		}
		full := filepath.Join(baseDir, filepath.FromSlash(relPath))
		st, err := os.Stat(full)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if st.IsDir() {
			return nil
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		files = append(files, SnapshotFileEntry{
			Scope:   scope,
			RelPath: filepath.ToSlash(relPath),
			Mode:    uint32(st.Mode().Perm()),
			DataB64: base64.StdEncoding.EncodeToString(data),
		})
		return nil
	}

	hasSingboxFiles := false
	for _, sub := range []string{"", "pending", "disabled"} {
		dir := filepath.Join(singboxConfigDir, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(strings.ToLower(ent.Name()), ".json") {
				continue
			}
			rel := ent.Name()
			if sub != "" {
				rel = sub + "/" + ent.Name()
			}
			if err := addFile("singbox", singboxConfigDir, rel); err != nil {
				return err
			}
			hasSingboxFiles = true
		}
	}
	if !hasSingboxFiles {
		return nil
	}

	awgmDir := resolveAwgmDataDir(singboxConfigDir)
	for _, name := range []string{"subscriptions.json", "subscription-groups.json", "awg3.json", "client-routes.json", "domain-routes.json"} {
		if err := addFile("awgm", awgmDir, name); err != nil {
			return err
		}
	}

	var sbTunnels, sbSubs, sbGroups int
	if preview, err := MigrateFromSingboxConfigDir(singboxConfigDir); err == nil && preview != nil {
		sbTunnels = len(preview.Tunnels)
		sbSubs = preview.SubscriptionsMigrated
		sbGroups = len(preview.Groups)
	}

	bundle := SystemSnapshot{
		SnapshotMeta: SnapshotMeta{
			ID:                        "saved-singbox-config",
			CreatedAt:                 time.Now().Unix(),
			Label:                     "Сохранённая конфигурация Sing-box до миграции",
			Trigger:                   "saved-singbox",
			ActiveEngine:              "sing-box",
			SingboxTunnelsCount:       sbTunnels,
			SingboxSubscriptionsCount: sbSubs,
			SingboxGroupsCount:        sbGroups,
			FilesCount:                len(files),
		},
		Files: files,
	}
	if err := os.MkdirAll(o.configDir, 0755); err != nil {
		return err
	}
	// Only save the pre-FIRC bundle if it passes Sing-box preflight validation (contains valid 10-tunnels.json, etc.).
	if _, _, err := o.validateSnapshotEntriesPreflight(context.Background(), singboxConfigDir, files, true, "sing-box"); err != nil {
		return nil
	}
	if err := os.MkdirAll(o.configDir, 0755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteYAMLFile(bundlePath, raw, 0600)
}

func extractSingboxBundleEntries(files []SnapshotFileEntry) []SnapshotFileEntry {
	var sbEntries []SnapshotFileEntry
	for _, f := range files {
		if f.Scope == "singbox" || (f.Scope == "awgm" && f.RelPath != "settings.json") {
			sbEntries = append(sbEntries, f)
		}
	}
	return sbEntries
}

// loadVerifiedSavedSingboxReturnPoint resolves a verified pre-FIRC Sing-box return point
// strictly from either the dedicated saved bundle ("saved-singbox-config.json") or an
// explicit pre-migration snapshot ("auto-pre-migrate"). It NEVER selects "auto-pre-return-singbox",
// "auto-pre-reverse", arbitrary manual snapshots, or unverified live_disk files, and it
// validates the candidate entries before returning.
func (o *Operator) loadVerifiedSavedSingboxReturnPoint(ctx context.Context, singboxConfigDir string) (*SystemSnapshot, []SnapshotFileEntry, string, error) {
	var lastValidationErr error
	var unverifiedSnap *SystemSnapshot
	var unverifiedEntries []SnapshotFileEntry
	var unverifiedSrc string

	if o.configDir != "" {
		if raw, err := os.ReadFile(o.savedSingboxConfigPath()); err == nil && len(raw) > 0 {
			var snap SystemSnapshot
			if err := json.Unmarshal(raw, &snap); err == nil && len(snap.Files) > 0 {
				sbEntries := extractSingboxBundleEntries(snap.Files)
				if len(sbEntries) > 0 {
					if _, _, valErr := o.validateSnapshotEntriesPreflight(ctx, singboxConfigDir, sbEntries, true, "sing-box"); valErr == nil {
						return &snap, sbEntries, "saved_bundle", nil
					} else {
						lastValidationErr = fmt.Errorf("saved sing-box bundle validation failed: %w", valErr)
						if errors.Is(valErr, ErrSingboxValidatorUnavailable) && unverifiedSnap == nil {
							cp := snap
							unverifiedSnap = &cp
							unverifiedEntries = sbEntries
							unverifiedSrc = "saved_bundle"
						}
					}
				}
			} else if err != nil {
				lastValidationErr = fmt.Errorf("corrupt saved sing-box bundle: %w", err)
			}
		}
	}

	if list, err := o.ListSystemSnapshots(); err == nil {
		for _, meta := range list {
			// Only accept snapshots created right before forward migration (Sing-box -> FIRC).
			// Never select auto-pre-return-singbox, auto-pre-reverse, or arbitrary snapshots.
			if meta.Trigger != "auto-pre-migrate" {
				continue
			}
			raw, rErr := os.ReadFile(filepath.Join(o.snapshotsDir(), meta.ID+".json"))
			if rErr != nil {
				continue
			}
			var snap SystemSnapshot
			if json.Unmarshal(raw, &snap) != nil {
				continue
			}
			sbEntries := extractSingboxBundleEntries(snap.Files)
			if len(sbEntries) == 0 {
				continue
			}
			if _, _, valErr := o.validateSnapshotEntriesPreflight(ctx, singboxConfigDir, sbEntries, true, "sing-box"); valErr == nil {
				return &snap, sbEntries, "snapshot", nil
			} else {
				if lastValidationErr == nil {
					lastValidationErr = fmt.Errorf("pre-migrate snapshot %s validation failed: %w", meta.ID, valErr)
				}
				if errors.Is(valErr, ErrSingboxValidatorUnavailable) && unverifiedSnap == nil {
					cp := snap
					unverifiedSnap = &cp
					unverifiedEntries = sbEntries
					unverifiedSrc = "snapshot"
				}
			}
		}
	}

	if lastValidationErr != nil {
		return unverifiedSnap, unverifiedEntries, unverifiedSrc, lastValidationErr
	}
	return nil, nil, "", fmt.Errorf("%w: no verified pre-FIRC Sing-box return point found", os.ErrNotExist)
}

// GetSavedSingboxStatus returns metadata about the saved Sing-box configuration for Scenario 1.
func (o *Operator) GetSavedSingboxStatus(singboxConfigDir string) *SavedSingboxStatus {
	snap, sbEntries, src, err := o.loadVerifiedSavedSingboxReturnPoint(context.Background(), singboxConfigDir)
	if err == nil && snap != nil && len(sbEntries) > 0 {
		return &SavedSingboxStatus{
			Available:          true,
			VerificationStatus: "verified",
			Source:             src,
			SnapshotID:         snap.ID,
			CreatedAt:          snap.CreatedAt,
			Label:              snap.Label,
			TunnelsCount:       snap.SingboxTunnelsCount,
			SubscriptionsCount: snap.SingboxSubscriptionsCount,
			GroupsCount:        snap.SingboxGroupsCount,
			FilesCount:         len(sbEntries),
		}
	}
	if err != nil && errors.Is(err, ErrSingboxValidatorUnavailable) {
		st := &SavedSingboxStatus{
			Available:          false,
			VerificationStatus: "validator_unavailable",
			Source:             src,
			UnavailableReason:  fmt.Sprintf("Валидатор Sing-box недоступен (validator_unavailable): %v", err),
		}
		if snap != nil {
			st.SnapshotID = snap.ID
			st.CreatedAt = snap.CreatedAt
			st.Label = snap.Label
			st.TunnelsCount = snap.SingboxTunnelsCount
			st.SubscriptionsCount = snap.SingboxSubscriptionsCount
			st.GroupsCount = snap.SingboxGroupsCount
			st.FilesCount = len(sbEntries)
		}
		return st
	}
	reason := "Сохранённая до перехода конфигурация Sing-box не найдена"
	verStatus := "missing"
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		reason = fmt.Sprintf("Сохранённая конфигурация Sing-box непригодна: %v", err)
		verStatus = "invalid"
	}
	return &SavedSingboxStatus{
		Available:          false,
		VerificationStatus: verStatus,
		UnavailableReason:  reason,
	}
}

func (o *Operator) pruneOldSnapshots() {
	list, err := o.ListSystemSnapshots()
	if err != nil || len(list) <= maxStoredSnapshots {
		return
	}
	// Preserve at least the newest recovery anchor for auto-pre-purge and auto-pre-migrate
	// so manual snapshots never push away the only snapshot of a deleted engine.
	protected := make(map[string]bool, 2)
	for _, wantTrigger := range []string{"auto-pre-purge", "auto-pre-migrate"} {
		for _, m := range list {
			if m.Trigger == wantTrigger {
				protected[m.ID] = true
				break
			}
		}
	}
	remaining := len(list)
	for i := len(list) - 1; i >= 0 && remaining > maxStoredSnapshots; i-- {
		if protected[list[i].ID] {
			continue
		}
		_ = os.Remove(filepath.Join(o.snapshotsDir(), list[i].ID+".json"))
		remaining--
	}
}

// ListSystemSnapshots returns all available system snapshots sorted newest-first.
func (o *Operator) ListSystemSnapshots() ([]SnapshotMeta, error) {
	entries, err := os.ReadDir(o.snapshotsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []SnapshotMeta{}, nil
		}
		return nil, err
	}
	var out []SnapshotMeta
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(o.snapshotsDir(), ent.Name()))
		if err != nil {
			continue
		}
		var meta SnapshotMeta
		if err := json.Unmarshal(data, &meta); err == nil && meta.ID != "" {
			out = append(out, meta)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	if out == nil {
		out = []SnapshotMeta{}
	}
	return out, nil
}

// GetSystemSnapshotMeta reads only the metadata header of a stored snapshot.
func (o *Operator) GetSystemSnapshotMeta(id string) (*SnapshotMeta, error) {
	trimmed := strings.TrimSpace(id)
	if !isValidSnapshotID(trimmed) {
		return nil, fmt.Errorf("invalid snapshot id %q", id)
	}
	raw, err := os.ReadFile(filepath.Join(o.snapshotsDir(), trimmed+".json"))
	if err != nil {
		return nil, fmt.Errorf("read snapshot %q: %w", trimmed, err)
	}
	var snap SystemSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("decode snapshot %q: %w", trimmed, err)
	}
	if snap.ID == "" {
		snap.ID = trimmed
	}
	meta := snap.SnapshotMeta
	return &meta, nil
}

// DeleteSystemSnapshot deletes a snapshot by ID.
func (o *Operator) DeleteSystemSnapshot(id string) error {
	trimmed := strings.TrimSpace(id)
	if !isValidSnapshotID(trimmed) {
		return fmt.Errorf("invalid snapshot id %q", id)
	}
	path := filepath.Join(o.snapshotsDir(), trimmed+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RestoreSavedSingboxFiles restores only the Sing-box configuration files (singbox config.d +
// AWGM routing files subscriptions.json / client-routes.json / domain-routes.json) from the
// verified saved Sing-box return point without overwriting FIRC configs or settings.json.
func (o *Operator) RestoreSavedSingboxFiles(ctx context.Context, singboxConfigDir string, createSnapshot bool, activeEngine string) (*SavedSingboxRestoreResult, error) {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()

	if err := o.checkMutationAllowed(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(singboxConfigDir) == "" {
		return nil, errors.New("sing-box config directory is not configured")
	}

	// R2 & R3: Resolve and preflight-validate the saved return point BEFORE creating
	// the protective auto-pre-return-singbox snapshot so the protective snapshot can
	// never be mistaken for the return source, and invalid candidates abort before any side effects.
	_, sbEntries, src, err := o.loadVerifiedSavedSingboxReturnPoint(ctx, singboxConfigDir)
	if err != nil {
		return nil, fmt.Errorf("no valid saved sing-box configuration found: %w", err)
	}

	var createdSnap *SnapshotMeta
	if createSnapshot {
		snapMeta, snapErr := o.CreateSystemSnapshot(singboxConfigDir, "Авто-снапшот перед возвратом к сохранённому Sing-box", "auto-pre-return-singbox", activeEngine)
		if snapErr != nil {
			return nil, fmt.Errorf("create pre-return system snapshot: %w", snapErr)
		}
		createdSnap = snapMeta
	}

	restoredCount, err := o.applySnapshotFilesTransactionalWithOp(ctx, "return_saved_singbox", singboxConfigDir, sbEntries, true, false, "sing-box")
	if err != nil {
		return nil, err
	}

	wiz := o.LoadMigrationWizardState()
	wiz.LastMigratedAt = time.Now().Unix()
	wiz.LastDirection = "return-saved-singbox"
	if createdSnap != nil {
		wiz.LastSnapshotID = createdSnap.ID
	}
	_ = o.SaveMigrationWizardState(wiz)

	return &SavedSingboxRestoreResult{
		Source:          src,
		RestoredFiles:   restoredCount,
		SnapshotCreated: createdSnap,
	}, nil
}

type stagedRestoreFile struct {
	scope   string
	relPath string
	target  string
	mode    os.FileMode
	data    []byte
}

func checkSingboxBinaryExecutable(sbBin string) error {
	trimmed := strings.TrimSpace(sbBin)
	if trimmed == "" {
		return fmt.Errorf("%w: sing-box binary path is empty", ErrSingboxValidatorUnavailable)
	}
	st, err := os.Stat(trimmed)
	if err != nil {
		return fmt.Errorf("%w: stat sing-box binary %q: %v", ErrSingboxValidatorUnavailable, trimmed, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%w: sing-box binary %q is not a regular file", ErrSingboxValidatorUnavailable, trimmed)
	}
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(trimmed))
		switch ext {
		case ".exe", ".com":
			f, oErr := os.Open(trimmed)
			if oErr != nil {
				return fmt.Errorf("%w: open sing-box binary %q: %v", ErrSingboxValidatorUnavailable, trimmed, oErr)
			}
			var hdr [2]byte
			n, rErr := f.Read(hdr[:])
			_ = f.Close()
			if rErr != nil || n < 2 || hdr[0] != 'M' || hdr[1] != 'Z' {
				return fmt.Errorf("%w: sing-box binary %q is not a valid executable", ErrSingboxValidatorUnavailable, trimmed)
			}
		case ".bat", ".cmd":
			if st.Size() == 0 {
				return fmt.Errorf("%w: sing-box script %q is empty", ErrSingboxValidatorUnavailable, trimmed)
			}
		default:
			return fmt.Errorf("%w: sing-box binary %q does not have an executable extension on windows", ErrSingboxValidatorUnavailable, trimmed)
		}
		return nil
	}
	if st.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("%w: sing-box binary %q is not executable (mode %v)", ErrSingboxValidatorUnavailable, trimmed, st.Mode().Perm())
	}
	return nil
}

func (o *Operator) checkSingboxValidatorAvailable() error {
	o.mu.Lock()
	checkFn := o.singboxCheckFn
	sbBin := o.singboxBinary
	o.mu.Unlock()
	if checkFn != nil {
		return nil
	}
	return checkSingboxBinaryExecutable(sbBin)
}

// buildFinalSingboxRootManifest computes the exact root-level Sing-box config.d file set
// that will exist on disk after the operation completes (V2 unified manifest).
// When clearStaleSingbox is true (e.g. RestoreSystemSnapshot, RestoreSavedSingboxFiles),
// any existing root .json file in singboxConfigDir (including 00-base.json) that is absent
// from staged will be deleted on apply and is therefore excluded from the manifest.
// When clearStaleSingbox is false, non-replaced root .json files in singboxConfigDir are
// read into the manifest and any I/O or fault-injection error aborts immediately.
func (o *Operator) buildFinalSingboxRootManifest(
	singboxConfigDir string,
	staged []stagedRestoreFile,
	clearStaleSingbox bool,
) (map[string][]byte, error) {
	manifest := make(map[string][]byte)
	for _, sf := range staged {
		if sf.scope != "singbox" || strings.Contains(sf.relPath, "/") {
			continue
		}
		manifest[sf.relPath] = sf.data
	}
	if clearStaleSingbox || strings.TrimSpace(singboxConfigDir) == "" {
		return manifest, nil
	}

	o.mu.Lock()
	copyHook := o.singboxStageCopyHook
	o.mu.Unlock()

	dirEntries, err := os.ReadDir(singboxConfigDir)
	if err != nil {
		if os.IsNotExist(err) {
			return manifest, nil
		}
		return nil, fmt.Errorf("read sing-box config dir %s for candidate manifest: %w", singboxConfigDir, err)
	}
	for _, ent := range dirEntries {
		if ent.IsDir() || !strings.HasSuffix(strings.ToLower(ent.Name()), ".json") {
			continue
		}
		if _, replaced := manifest[ent.Name()]; replaced {
			continue
		}
		if copyHook != nil {
			if hErr := copyHook(ent.Name()); hErr != nil {
				return nil, fmt.Errorf("stage existing sing-box file %s: %w", ent.Name(), hErr)
			}
		}
		data, rErr := os.ReadFile(filepath.Join(singboxConfigDir, ent.Name()))
		if rErr != nil {
			return nil, fmt.Errorf("read existing sing-box file %s for candidate manifest: %w", ent.Name(), rErr)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, fmt.Errorf("existing sing-box file %s in %s is empty", ent.Name(), singboxConfigDir)
		}
		manifest[ent.Name()] = data
	}
	return manifest, nil
}

func (o *Operator) runSingboxCandidateCheck(
	ctx context.Context,
	finalRootManifest map[string][]byte,
) error {
	tmpDir, err := os.MkdirTemp("", "awgm-sb-candidate-*")
	if err != nil {
		return fmt.Errorf("create sing-box candidate staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	o.mu.Lock()
	copyHook := o.singboxStageCopyHook
	checkFn := o.singboxCheckFn
	sbBin := o.singboxBinary
	o.mu.Unlock()

	names := make([]string, 0, len(finalRootManifest))
	for name := range finalRootManifest {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if copyHook != nil {
			if hErr := copyHook(name); hErr != nil {
				return fmt.Errorf("stage sing-box candidate file %s: %w", name, hErr)
			}
		}
		dst := filepath.Join(tmpDir, filepath.FromSlash(name))
		if err := os.WriteFile(dst, finalRootManifest[name], 0600); err != nil {
			return fmt.Errorf("write staged sing-box file %s: %w", name, err)
		}
	}

	if checkFn != nil {
		if err := checkFn(ctx, tmpDir); err != nil {
			return fmt.Errorf("sing-box candidate check failed: %w", err)
		}
		return nil
	}

	if err := checkSingboxBinaryExecutable(sbBin); err != nil {
		return err
	}

	cCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cCtx, strings.TrimSpace(sbBin), "check", "-C", tmpDir)
	if out, cErr := cmd.CombinedOutput(); cErr != nil {
		return fmt.Errorf("sing-box check failed: %s: %w", strings.TrimSpace(string(out)), cErr)
	}
	return nil
}

func (o *Operator) validateSnapshotEntriesPreflight(
	ctx context.Context,
	singboxConfigDir string,
	entries []SnapshotFileEntry,
	clearStaleSingbox bool,
	requireEngineScope string,
) ([]stagedRestoreFile, map[string]bool, error) {
	if len(entries) == 0 {
		return nil, nil, errors.New("snapshot contains no files to restore")
	}

	awgmDir := resolveAwgmDataDir(singboxConfigDir)
	seenKeys := make(map[string]bool, len(entries))
	staged := make([]stagedRestoreFile, 0, len(entries))
	hasScope := map[string]bool{}
	stagedMap := make(map[string][]byte, len(entries))

	for _, f := range entries {
		cleanRel, err := validateSnapshotRelPath(f.Scope, f.RelPath)
		if err != nil {
			return nil, nil, err
		}
		key := f.Scope + ":" + cleanRel
		if seenKeys[key] {
			return nil, nil, fmt.Errorf("duplicate file entry %s in snapshot", key)
		}
		seenKeys[key] = true

		decoded, err := base64.StdEncoding.DecodeString(f.DataB64)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid base64 data for %s/%s: %w", f.Scope, cleanRel, err)
		}
		if err := validateDecodedSnapshotFileContent(f.Scope, cleanRel, decoded); err != nil {
			return nil, nil, err
		}

		var baseDir string
		switch f.Scope {
		case "singbox":
			baseDir = singboxConfigDir
		case "awgm":
			baseDir = awgmDir
		case "firc":
			baseDir = o.configDir
		}
		if strings.TrimSpace(baseDir) == "" {
			return nil, nil, fmt.Errorf("target directory for scope %q is not configured", f.Scope)
		}
		target := filepath.Join(baseDir, filepath.FromSlash(cleanRel))
		mode := os.FileMode(f.Mode) & 0777
		if mode == 0 {
			mode = 0644
		}
		staged = append(staged, stagedRestoreFile{
			scope:   f.Scope,
			relPath: cleanRel,
			target:  target,
			mode:    mode,
			data:    decoded,
		})
		hasScope[f.Scope] = true
		stagedMap[key] = decoded
	}

	reqEng := strings.ToLower(strings.TrimSpace(requireEngineScope))
	if reqEng == "sing-box" || reqEng == "singbox" {
		if !hasScope["singbox"] {
			return nil, nil, errors.New("snapshot for activeEngine sing-box contains no sing-box configuration files")
		}
	}
	if reqEng == "firc" {
		if !hasScope["firc"] {
			return nil, nil, errors.New("snapshot for activeEngine firc contains no firc configuration files")
		}
	}

	var finalSingboxRoot map[string][]byte
	// Cross-file semantic & mandatory file checks for Sing-box using the unified final root manifest (V2)
	if hasScope["singbox"] || seenKeys["awgm:subscriptions.json"] || seenKeys["awgm:client-routes.json"] {
		var mErr error
		finalSingboxRoot, mErr = o.buildFinalSingboxRootManifest(singboxConfigDir, staged, clearStaleSingbox)
		if mErr != nil {
			return nil, nil, mErr
		}

		if (reqEng == "sing-box" || reqEng == "singbox" || seenKeys["singbox:20-router.json"] || seenKeys["awgm:subscriptions.json"] || seenKeys["awgm:client-routes.json"]) && len(finalSingboxRoot["10-tunnels.json"]) == 0 {
			return nil, nil, errors.New("missing mandatory sing-box file singbox/10-tunnels.json")
		}

		validOutboundTags := map[string]bool{
			"direct":  true,
			"block":   true,
			"dns-out": true,
		}
		for _, rootData := range finalSingboxRoot {
			var doc struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			if json.Unmarshal(rootData, &doc) == nil {
				for _, ob := range doc.Outbounds {
					if tag, _ := ob["tag"].(string); strings.TrimSpace(tag) != "" {
						validOutboundTags[strings.TrimSpace(tag)] = true
					}
				}
			}
		}

		var parsedSubs []snapshotSubscriptionSchema
		if subData, ok := stagedMap["awgm:subscriptions.json"]; ok {
			subs, err := validateSubscriptionsJSONSemantics(subData)
			if err != nil {
				return nil, nil, err
			}
			parsedSubs = subs
			for _, s := range subs {
				if lbl := strings.TrimSpace(s.Label); lbl != "" {
					validOutboundTags[lbl] = true
				}
				if nm := strings.TrimSpace(s.Name); nm != "" {
					validOutboundTags[nm] = true
				}
				if sel := strings.TrimSpace(s.SelectorTag); sel != "" {
					validOutboundTags[sel] = true
				}
				for _, mt := range s.MemberTags {
					if m := strings.TrimSpace(mt); m != "" {
						validOutboundTags[m] = true
					}
				}
			}
		}

		// Verify all outbound references in staged Sing-box files
		for _, sf := range staged {
			if sf.scope != "singbox" || strings.Contains(sf.relPath, "/") {
				continue
			}
			var doc struct {
				Outbounds []map[string]any `json:"outbounds"`
				DNS       struct {
					Servers []map[string]any `json:"servers"`
				} `json:"dns"`
				Route struct {
					Rules []map[string]any `json:"rules"`
					Final string           `json:"final"`
				} `json:"route"`
			}
			if err := json.Unmarshal(sf.data, &doc); err != nil {
				return nil, nil, err
			}
			for _, ob := range doc.Outbounds {
				tag, _ := ob["tag"].(string)
				typ, _ := ob["type"].(string)
				if detour, _ := ob["detour"].(string); strings.TrimSpace(detour) != "" {
					if !validOutboundTags[strings.TrimSpace(detour)] {
						return nil, nil, fmt.Errorf("in %s: outbound %q detour references non-existent outbound %q", sf.relPath, tag, detour)
					}
				}
				if typ == "selector" || typ == "urltest" || typ == "fallback" {
					if def, _ := ob["default"].(string); strings.TrimSpace(def) != "" {
						if !validOutboundTags[strings.TrimSpace(def)] {
							return nil, nil, fmt.Errorf("in %s: %s outbound %q default references non-existent outbound %q", sf.relPath, typ, tag, def)
						}
					}
					if rawMembers, ok := ob["outbounds"].([]any); ok {
						for _, m := range rawMembers {
							mStr, _ := m.(string)
							if strings.TrimSpace(mStr) != "" && !validOutboundTags[strings.TrimSpace(mStr)] {
								return nil, nil, fmt.Errorf("in %s: %s outbound %q member references non-existent outbound %q", sf.relPath, typ, tag, mStr)
							}
						}
					}
				}
			}
			if fin := strings.TrimSpace(doc.Route.Final); fin != "" && !validOutboundTags[fin] {
				return nil, nil, fmt.Errorf("in %s: route.final references non-existent outbound %q", sf.relPath, fin)
			}
			for idx, r := range doc.Route.Rules {
				if obTag, _ := r["outbound"].(string); strings.TrimSpace(obTag) != "" {
					if !validOutboundTags[strings.TrimSpace(obTag)] {
						return nil, nil, fmt.Errorf("in %s: route.rules[%d] references non-existent outbound %q", sf.relPath, idx, obTag)
					}
				}
			}
			for idx, srv := range doc.DNS.Servers {
				if detour, _ := srv["detour"].(string); strings.TrimSpace(detour) != "" {
					if !validOutboundTags[strings.TrimSpace(detour)] {
						return nil, nil, fmt.Errorf("in %s: dns.servers[%d] detour references non-existent outbound %q", sf.relPath, idx, detour)
					}
				}
			}
		}

		// Verify subscriptions.json outbound_tags against validOutboundTags
		for i, s := range parsedSubs {
			for _, tag := range s.OutboundTags {
				if strings.TrimSpace(tag) != "" && !validOutboundTags[strings.TrimSpace(tag)] {
					return nil, nil, fmt.Errorf("subscriptions.json[%d] (%s) references non-existent outbound tag %q", i, s.Name, tag)
				}
			}
		}

		// Verify client-routes.json outbound_tag against validOutboundTags
		if crData, ok := stagedMap["awgm:client-routes.json"]; ok {
			crs, err := validateClientRoutesJSONSemantics(crData)
			if err != nil {
				return nil, nil, err
			}
			for i, cr := range crs {
				if tag := cr.effectiveOutboundTag(); tag != "" && !validOutboundTags[tag] {
					return nil, nil, fmt.Errorf("client-routes.json[%d] references non-existent outbound tag %q", i, tag)
				}
			}
		}
	}

	// Cross-file semantic & mandatory file checks for FIRC
	if hasScope["firc"] {
		var fircTunnels []Tunnel
		hasTunnelsYAML := false
		if tunData, ok := stagedMap["firc:tunnels.yaml"]; ok {
			tuns, err := validateFircTunnelsYAML(tunData)
			if err != nil {
				return nil, nil, err
			}
			fircTunnels = tuns
			hasTunnelsYAML = true
		}
		if grpData, ok := stagedMap["firc:groups.yaml"]; ok {
			grps, err := validateFircGroupsYAML(grpData)
			if err != nil {
				return nil, nil, err
			}
			tunDevs := make(map[string]bool, len(fircTunnels)+2)
			tunDevs["direct"] = true
			tunDevs["block"] = true
			for _, t := range fircTunnels {
				tunDevs[strings.TrimSpace(t.Device)] = true
			}
			for _, g := range grps {
				for _, iface := range g.Interface {
					iface = strings.TrimSpace(iface)
					if strings.HasPrefix(iface, "tunvless") || hasTunnelsYAML {
						if strings.HasPrefix(iface, "tunvless") && !hasTunnelsYAML {
							return nil, nil, fmt.Errorf("groups.yaml group %q references tunnel device %q, but firc/tunnels.yaml is missing from snapshot", g.Name, iface)
						}
						if strings.HasPrefix(iface, "tunvless") && !tunDevs[iface] {
							return nil, nil, fmt.Errorf("groups.yaml group %q references non-existent tunnel device %q", g.Name, iface)
						}
					}
				}
			}
		}
	}

	// Run Sing-box candidate check on staged directory BEFORE modifying any working files
	if hasScope["singbox"] {
		if err := o.runSingboxCandidateCheck(ctx, finalSingboxRoot); err != nil {
			return nil, nil, err
		}
	}

	return staged, hasScope, nil
}

func (o *Operator) verifyAppliedFircFilesReadback(staged []stagedRestoreFile) error {
	for _, sf := range staged {
		if sf.scope != "firc" {
			continue
		}
		got, err := os.ReadFile(sf.target)
		if err != nil {
			return fmt.Errorf("readback firc/%s after apply: %w", sf.relPath, err)
		}
		if !bytes.Equal(got, sf.data) {
			return fmt.Errorf("readback byte mismatch for firc/%s after apply", sf.relPath)
		}
		if err := validateDecodedSnapshotFileContent("firc", sf.relPath, got); err != nil {
			return fmt.Errorf("readback validation failed for firc/%s: %w", sf.relPath, err)
		}
	}
	return nil
}

func (o *Operator) applySnapshotFilesTransactional(
	ctx context.Context,
	singboxConfigDir string,
	entries []SnapshotFileEntry,
	clearStaleSingbox bool,
	reconcileFirc bool,
) (int, error) {
	return o.applySnapshotFilesTransactionalWithOp(ctx, "snapshot_restore", singboxConfigDir, entries, clearStaleSingbox, reconcileFirc, "")
}

func (o *Operator) applySnapshotFilesTransactionalWithOp(
	ctx context.Context,
	operationName string,
	singboxConfigDir string,
	entries []SnapshotFileEntry,
	clearStaleSingbox bool,
	reconcileFirc bool,
	requireEngineScope string,
) (int, error) {
	return o.applySnapshotFilesTransactionalWithBackups(ctx, operationName, singboxConfigDir, entries, clearStaleSingbox, reconcileFirc, requireEngineScope, nil)
}

func (o *Operator) applySnapshotFilesTransactionalWithBackups(
	ctx context.Context,
	operationName string,
	singboxConfigDir string,
	entries []SnapshotFileEntry,
	clearStaleSingbox bool,
	reconcileFirc bool,
	requireEngineScope string,
	capturedBackups *[]fileSnapshot,
) (int, error) {
	staged, hasScope, err := o.validateSnapshotEntriesPreflight(ctx, singboxConfigDir, entries, clearStaleSingbox, requireEngineScope)
	if err != nil {
		return 0, err
	}

	awgmDir := resolveAwgmDataDir(singboxConfigDir)
	backupMap := make(map[string]fileSnapshot)
	captureTarget := func(path string, defaultPerm os.FileMode) error {
		cleanPath := filepath.Clean(path)
		if _, ok := backupMap[cleanPath]; ok {
			return nil
		}
		snap, err := captureFileSnapshot(cleanPath, defaultPerm)
		if err != nil {
			return fmt.Errorf("backup existing file %s before restore: %w", cleanPath, err)
		}
		backupMap[cleanPath] = snap
		return nil
	}

	if (clearStaleSingbox || hasScope["singbox"]) && singboxConfigDir != "" {
		for _, sub := range []string{"", "pending", "disabled"} {
			dir := filepath.Join(singboxConfigDir, sub)
			dirEntries, err := os.ReadDir(dir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return 0, fmt.Errorf("read singbox dir %s for backup: %w", dir, err)
			}
			for _, ent := range dirEntries {
				if !ent.IsDir() && strings.HasSuffix(strings.ToLower(ent.Name()), ".json") {
					if err := captureTarget(filepath.Join(dir, ent.Name()), 0644); err != nil {
						return 0, err
					}
				}
			}
		}
	}
	if hasScope["awgm"] && awgmDir != "" {
		for _, name := range []string{"settings.json", "subscriptions.json", "subscription-groups.json", "awg3.json", "client-routes.json", "domain-routes.json"} {
			if err := captureTarget(filepath.Join(awgmDir, name), 0644); err != nil {
				return 0, err
			}
		}
	}
	if hasScope["firc"] && o.configDir != "" {
		for _, name := range []string{"config.yaml", "firc.conf", "tunnels.yaml", "groups.yaml", "awgm-ext.json"} {
			if err := captureTarget(filepath.Join(o.configDir, name), 0644); err != nil {
				return 0, err
			}
		}
	}
	for _, sf := range staged {
		if err := captureTarget(sf.target, sf.mode); err != nil {
			return 0, err
		}
	}

	curEng := o.resolveSnapshotActiveEngine(singboxConfigDir, "")
	allowLiveFirc := !o.HasProductionEngineHooks() || strings.EqualFold(curEng, "firc")
	running := reconcileFirc && allowLiveFirc && hasScope["firc"] && o.IsRunning(ctx)
	var prevTunnels []Tunnel
	var prevGroups []Group
	var prevExt ExtConfig
	if running {
		prevExt = o.LoadExtConfig()
		tuns, tErr := o.Client().GetTunnels(ctx)
		if tErr != nil {
			return 0, fmt.Errorf("capture live fircd tunnels before restore: %w", tErr)
		}
		grps, gErr := o.Client().GetGroups(ctx, true)
		if gErr != nil {
			return 0, fmt.Errorf("capture live fircd groups before restore: %w", gErr)
		}
		prevTunnels = tuns
		prevGroups = grps
	}

	backupSnaps := make([]fileSnapshot, 0, len(backupMap))
	for _, snap := range backupMap {
		backupSnaps = append(backupSnaps, snap)
	}
	journal, err := o.beginMigrationJournal(operationName, backupSnaps)
	if err != nil {
		return 0, fmt.Errorf("persist durable migration journal before %s: %w", operationName, err)
	}

	rollbackAll := func(cause error) error {
		var rbErrs []error
		if running {
			if _, tErr := o.SyncTunnels(ctx, prevTunnels, false); tErr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("rollback fircd tunnels: %w", tErr))
			}
			if _, gErr := o.SyncGroupsRuntimeOnly(ctx, prevGroups); gErr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("rollback fircd groups: %w", gErr))
			}
			if nfErr := o.ReconcileExtNetfilterStrict(ctx, prevExt, prevGroups); nfErr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("rollback firc netfilter: %w", nfErr))
			}
		}
		if fErr := rollbackMigrationJournalBackups(journal.Backups); fErr != nil {
			rbErrs = append(rbErrs, fErr)
		}
		if len(rbErrs) > 0 {
			reason := fmt.Sprintf("%s rollback failed: %v", operationName, errors.Join(rbErrs...))
			persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:         reason,
				TransitionID:   journal.JournalID,
				TargetEngine:   "firc",
				PreviousEngine: "firc",
				FailedStep:     operationName + "_rollback",
				LastCompleted:  journal.Stage,
			})
			return errors.Join(cause, fmt.Errorf("%w: %s", ErrRecoveryRequired, reason), errors.Join(rbErrs...), persistErr)
		}
		if jErr := o.cleanupRolledBackMigrationJournal(journal); jErr != nil {
			reason := fmt.Sprintf("%s rollback journal cleanup failed: %v", operationName, jErr)
			persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:         reason,
				TransitionID:   journal.JournalID,
				TargetEngine:   "firc",
				PreviousEngine: "firc",
				FailedStep:     operationName + "_rollback_journal_cleanup",
				LastCompleted:  "rolled_back",
			})
			return errors.Join(cause, fmt.Errorf("%w: %s", ErrRecoveryRequired, reason), jErr, persistErr)
		}
		return cause
	}

	stagedPathSet := make(map[string]bool, len(staged))
	for _, sf := range staged {
		stagedPathSet[filepath.Clean(sf.target)] = true
	}

	var stalePaths []string
	for cleanPath, snap := range backupMap {
		if !snap.exists || stagedPathSet[cleanPath] {
			continue
		}
		if !hasScope["firc"] && filepath.Base(cleanPath) == "settings.json" {
			continue
		}
		stalePaths = append(stalePaths, cleanPath)
	}
	sort.Strings(stalePaths)

	for delIdx, cleanPath := range stalePaths {
		if jErr := o.advanceMigrationJournal(journal, "removing_stale", delIdx, cleanPath); jErr != nil {
			return 0, rollbackAll(jErr)
		}
		if err := os.Remove(cleanPath); err != nil && !os.IsNotExist(err) {
			return 0, rollbackAll(fmt.Errorf("remove stale file %s during restore: %w", cleanPath, err))
		}
		if hErr := o.invokeMigrationStepHook(operationName, "removing_stale", delIdx, cleanPath); hErr != nil {
			return 0, rollbackAll(hErr)
		}
	}

	for writeIdx, sf := range staged {
		if jErr := o.advanceMigrationJournal(journal, "writing_files", writeIdx, sf.target); jErr != nil {
			return 0, rollbackAll(jErr)
		}
		if err := atomicWriteYAMLFile(sf.target, sf.data, sf.mode); err != nil {
			return 0, rollbackAll(fmt.Errorf("restore file %s/%s: %w", sf.scope, sf.relPath, err))
		}
		got, err := os.ReadFile(sf.target)
		if err != nil || !bytes.Equal(got, sf.data) {
			return 0, rollbackAll(fmt.Errorf("verify restored file %s/%s failed: %v", sf.scope, sf.relPath, err))
		}
		if hErr := o.invokeMigrationStepHook(operationName, "writing_files", writeIdx, sf.target); hErr != nil {
			return 0, rollbackAll(hErr)
		}
	}

	if hasScope["firc"] {
		if rbErr := o.verifyAppliedFircFilesReadback(staged); rbErr != nil {
			return 0, rollbackAll(rbErr)
		}
	}

	if running {
		if jErr := o.advanceMigrationJournal(journal, "syncing_runtime", 0, "fircd"); jErr != nil {
			return 0, rollbackAll(jErr)
		}
		tunnels, tErr := o.LoadTunnelsFile()
		if tErr != nil {
			return 0, rollbackAll(fmt.Errorf("load restored tunnels.yaml: %w", tErr))
		}
		groups, gErr := o.LoadGroupsFile()
		if gErr != nil {
			return 0, rollbackAll(fmt.Errorf("load restored groups.yaml: %w", gErr))
		}
		ext := o.LoadExtConfig()
		groups, _ = syncGroupsWithExt(groups, ext)
		if _, syncTErr := o.SyncTunnels(ctx, tunnels, true); syncTErr != nil {
			return 0, rollbackAll(fmt.Errorf("sync restored tunnels with fircd: %w", syncTErr))
		}
		updatedGroups, syncGErr := o.SyncGroupsRuntimeOnly(ctx, groups)
		if syncGErr != nil {
			return 0, rollbackAll(fmt.Errorf("sync restored groups with fircd: %w", syncGErr))
		}
		if nfErr := o.ReconcileExtNetfilterStrict(ctx, ext, updatedGroups); nfErr != nil {
			return 0, rollbackAll(fmt.Errorf("reconcile netfilter after snapshot restore: %w", nfErr))
		}
		if _, rtTErr := o.Client().GetTunnels(ctx); rtTErr != nil {
			return 0, rollbackAll(fmt.Errorf("verify live fircd tunnels readback after restore: %w", rtTErr))
		}
		if _, rtGErr := o.Client().GetGroups(ctx, false); rtGErr != nil {
			return 0, rollbackAll(fmt.Errorf("verify live fircd groups readback after restore: %w", rtGErr))
		}
	}

	committed, cErr := o.completeMigrationJournal(journal)
	if cErr != nil {
		if !committed {
			return 0, rollbackAll(cErr)
		}
		return 0, cErr
	}

	if capturedBackups != nil {
		*capturedBackups = backupSnaps
	}
	return len(staged), nil
}

// RestoreSystemSnapshot restores all Sing-box, AWGM, and FIRC configuration files from a snapshot
// and reconciles the live FIRC daemon if running.
func (o *Operator) RestoreSystemSnapshot(ctx context.Context, id string, singboxConfigDir string) (*SnapshotMeta, error) {
	meta, _, err := o.RestoreSystemSnapshotWithRollback(ctx, id, singboxConfigDir, false)
	return meta, err
}

// RestoreSystemSnapshotWithRollback restores snapshot configuration files and returns
// an idempotent rollback closure that restores every modified/deleted/created file
// back to its exact pre-restore state if a subsequent engine switch fails cleanly.
// When switchEngine is true for a non-FIRC snapshot (e.g. activeEngine="sing-box"),
// live fircd runtime sync is skipped because SwitchEngineWithOptions will immediately
// switch away from and stop FIRC — preventing stale FIRC files inside a sing-box
// snapshot from rejecting the restore while fircd is still shutting down.
func (o *Operator) RestoreSystemSnapshotWithRollback(ctx context.Context, id string, singboxConfigDir string, switchEngine bool) (*SnapshotMeta, func(context.Context) error, error) {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()

	if err := o.checkMutationAllowed(); err != nil {
		return nil, nil, err
	}

	trimmed := strings.TrimSpace(id)
	if !isValidSnapshotID(trimmed) {
		return nil, nil, fmt.Errorf("invalid snapshot id %q", id)
	}
	path := filepath.Join(o.snapshotsDir(), trimmed+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read snapshot %q: %w", trimmed, err)
	}
	var snap SystemSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, nil, fmt.Errorf("decode snapshot %q: %w", trimmed, err)
	}
	if snap.ID != "" && snap.ID != trimmed {
		return nil, nil, fmt.Errorf("snapshot metadata id %q does not match requested id %q", snap.ID, trimmed)
	}
	if snap.ID == "" {
		snap.ID = trimmed
	}

	reconcileFirc := !switchEngine || strings.EqualFold(strings.TrimSpace(snap.ActiveEngine), "firc")
	wizSnap, wizErr := captureFileSnapshot(o.wizardStatePath(), 0644)
	var backupSnaps []fileSnapshot
	if _, err := o.applySnapshotFilesTransactionalWithBackups(ctx, "snapshot_restore", singboxConfigDir, snap.Files, true, reconcileFirc, snap.ActiveEngine, &backupSnaps); err != nil {
		return nil, nil, err
	}

	st := o.LoadMigrationWizardState()
	st.LastMigratedAt = time.Now().Unix()
	st.LastDirection = "snapshot-restore"
	st.LastSnapshotID = snap.ID
	_ = o.SaveMigrationWizardState(st)

	rollbackFn := func(rbCtx context.Context) error {
		o.mutationMu.Lock()
		defer o.mutationMu.Unlock()
		var errs []error
		for _, s := range backupSnaps {
			if rErr := restoreFileSnapshot(s); rErr != nil {
				errs = append(errs, rErr)
			}
		}
		if wizErr == nil {
			if wErr := restoreFileSnapshot(wizSnap); wErr != nil {
				errs = append(errs, wErr)
			}
		}
		if len(errs) == 0 && o.IsRunning(rbCtx) {
			if tuns, tErr := o.LoadTunnelsFile(); tErr == nil {
				_, _ = o.SyncTunnels(rbCtx, tuns, false)
			}
			if grps, gErr := o.LoadGroupsFile(); gErr == nil {
				ext := o.LoadExtConfig()
				grps, _ = syncGroupsWithExt(grps, ext)
				updated, _ := o.SyncGroupsRuntimeOnly(rbCtx, grps)
				_ = o.ReconcileExtNetfilterStrict(rbCtx, ext, updated)
			}
		}
		return errors.Join(errs...)
	}

	return &snap.SnapshotMeta, rollbackFn, nil
}

func buildReverseCandidateSnapshotEntries(tunnelsJSON, subsJSON, routerJSON, clientRoutesJSON []byte) []SnapshotFileEntry {
	var candidateEntries []SnapshotFileEntry
	if len(tunnelsJSON) > 0 {
		candidateEntries = append(candidateEntries, SnapshotFileEntry{
			Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644,
			DataB64: base64.StdEncoding.EncodeToString(tunnelsJSON),
		})
	}
	if len(subsJSON) > 0 {
		candidateEntries = append(candidateEntries, SnapshotFileEntry{
			Scope: "awgm", RelPath: "subscriptions.json", Mode: 0644,
			DataB64: base64.StdEncoding.EncodeToString(subsJSON),
		})
	}
	if len(routerJSON) > 0 {
		candidateEntries = append(candidateEntries, SnapshotFileEntry{
			Scope: "singbox", RelPath: "20-router.json", Mode: 0644,
			DataB64: base64.StdEncoding.EncodeToString(routerJSON),
		})
	}
	if len(clientRoutesJSON) > 0 {
		candidateEntries = append(candidateEntries, SnapshotFileEntry{
			Scope: "awgm", RelPath: "client-routes.json", Mode: 0644,
			DataB64: base64.StdEncoding.EncodeToString(clientRoutesJSON),
		})
	}
	return candidateEntries
}

// PreviewReverseToSingbox computes what FIRC tunnels, subscriptions, and groups will be migrated back to Sing-box.
func (o *Operator) PreviewReverseToSingbox(ctx context.Context, singboxConfigDir string, opts *ReverseMigrationOptions) (*ReverseMigrationResult, error) {
	return o.previewReverseToSingbox(ctx, singboxConfigDir, opts, true)
}

// PreviewReverseToSingboxOffline computes the reverse migration preview strictly from
// FIRC configuration files on disk without probing fircd IPC. Used when FIRC is not selected.
func (o *Operator) PreviewReverseToSingboxOffline(ctx context.Context, singboxConfigDir string, opts *ReverseMigrationOptions) (*ReverseMigrationResult, error) {
	return o.previewReverseToSingbox(ctx, singboxConfigDir, opts, false)
}

func (o *Operator) previewReverseToSingbox(ctx context.Context, singboxConfigDir string, opts *ReverseMigrationOptions, allowLive bool) (*ReverseMigrationResult, error) {
	tunnels, groups := o.loadCurrentFircTunnelsAndGroups(ctx, allowLive)
	res, tunnelsJSON, subsJSON, routerJSON, clientRoutesJSON, err := buildReverseSingboxPayloads(tunnels, groups, singboxConfigDir, opts, false)
	if err != nil || res == nil {
		return res, err
	}
	candidateEntries := buildReverseCandidateSnapshotEntries(tunnelsJSON, subsJSON, routerJSON, clientRoutesJSON)
	if len(candidateEntries) > 0 && strings.TrimSpace(singboxConfigDir) != "" {
		if _, _, valErr := o.validateSnapshotEntriesPreflight(ctx, singboxConfigDir, candidateEntries, false, ""); valErr == nil {
			res.VerificationStatus = "verified"
		} else if errors.Is(valErr, ErrSingboxValidatorUnavailable) {
			res.VerificationStatus = "validator_unavailable"
			res.VerificationNote = valErr.Error()
		} else {
			res.VerificationStatus = "invalid"
			res.VerificationNote = valErr.Error()
		}
	} else {
		if vErr := o.checkSingboxValidatorAvailable(); vErr == nil {
			res.VerificationStatus = "verified"
		} else {
			res.VerificationStatus = "validator_unavailable"
			res.VerificationNote = vErr.Error()
		}
	}
	return res, nil
}

// ReverseMigrateToSingbox writes FIRC tunnels, subscriptions, and routing groups into Sing-box configuration files.
func (o *Operator) ReverseMigrateToSingbox(ctx context.Context, singboxConfigDir string, opts *ReverseMigrationOptions, activeEngine string) (*ReverseMigrationResult, error) {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()

	if err := o.checkMutationAllowed(); err != nil {
		return nil, err
	}
	if singboxConfigDir == "" {
		return nil, fmt.Errorf("sing-box config directory is not configured")
	}

	resolvedEng := o.resolveSnapshotActiveEngine(singboxConfigDir, activeEngine)
	allowLive := !o.HasProductionEngineHooks() || strings.EqualFold(resolvedEng, "firc")
	tunnels, groups := o.loadCurrentFircTunnelsAndGroups(ctx, allowLive)
	res, tunnelsJSON, subsJSON, routerJSON, clientRoutesJSON, err := buildReverseSingboxPayloads(tunnels, groups, singboxConfigDir, opts, true)
	if err != nil {
		return nil, err
	}

	// Build candidate SnapshotFileEntries and run full preflight + Sing-box candidate check BEFORE creating snapshot or writing files
	candidateEntries := buildReverseCandidateSnapshotEntries(tunnelsJSON, subsJSON, routerJSON, clientRoutesJSON)
	if len(candidateEntries) > 0 {
		if _, _, valErr := o.validateSnapshotEntriesPreflight(ctx, singboxConfigDir, candidateEntries, false, ""); valErr != nil {
			return nil, fmt.Errorf("reverse migration candidate validation failed: %w", valErr)
		}
	} else {
		if vErr := o.checkSingboxValidatorAvailable(); vErr != nil {
			return nil, fmt.Errorf("reverse migration candidate validation failed: %w", vErr)
		}
	}
	res.VerificationStatus = "verified"

	var createdSnap *SnapshotMeta
	if opts.shouldCreateSnapshot() {
		snap, err := o.CreateSystemSnapshot(singboxConfigDir, "", "auto-pre-reverse", activeEngine)
		if err != nil {
			return nil, fmt.Errorf("create pre-reverse system snapshot: %w", err)
		}
		createdSnap = snap
	}
	res.SnapshotCreated = createdSnap

	awgmDir := resolveAwgmDataDir(singboxConfigDir)
	if err := os.MkdirAll(singboxConfigDir, 0755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(awgmDir, 0755); err != nil {
		return nil, err
	}

	type targetWrite struct {
		path string
		data []byte
	}
	var writes []targetWrite
	if len(tunnelsJSON) > 0 {
		writes = append(writes, targetWrite{path: filepath.Join(singboxConfigDir, "10-tunnels.json"), data: tunnelsJSON})
	}
	if len(subsJSON) > 0 {
		writes = append(writes, targetWrite{path: filepath.Join(awgmDir, "subscriptions.json"), data: subsJSON})
	}
	if len(routerJSON) > 0 {
		writes = append(writes, targetWrite{path: filepath.Join(singboxConfigDir, "20-router.json"), data: routerJSON})
	}
	if len(clientRoutesJSON) > 0 {
		writes = append(writes, targetWrite{path: filepath.Join(awgmDir, "client-routes.json"), data: clientRoutesJSON})
	}

	allTargetPaths := []string{
		filepath.Join(singboxConfigDir, "10-tunnels.json"),
		filepath.Join(awgmDir, "subscriptions.json"),
		filepath.Join(singboxConfigDir, "20-router.json"),
		filepath.Join(awgmDir, "client-routes.json"),
	}
	snaps := make([]fileSnapshot, 0, len(allTargetPaths))
	for _, p := range allTargetPaths {
		s, sErr := captureFileSnapshot(p, 0644)
		if sErr != nil {
			return nil, fmt.Errorf("snapshot %s before reverse migration: %w", filepath.Base(p), sErr)
		}
		snaps = append(snaps, s)
	}

	journal, jErr := o.beginMigrationJournal("reverse_migrate", snaps)
	if jErr != nil {
		return nil, fmt.Errorf("persist durable migration journal before reverse_migrate: %w", jErr)
	}

	rollbackTargets := func(cause error) error {
		var rbErrs []error
		if rErr := rollbackMigrationJournalBackups(journal.Backups); rErr != nil {
			rbErrs = append(rbErrs, rErr)
		}
		if len(rbErrs) > 0 {
			reason := fmt.Sprintf("ReverseMigrateToSingbox rollback failed: %v", errors.Join(rbErrs...))
			persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:         reason,
				TransitionID:   journal.JournalID,
				TargetEngine:   "sing-box",
				PreviousEngine: "firc",
				FailedStep:     "reverse_migrate_rollback",
				LastCompleted:  journal.Stage,
			})
			return errors.Join(cause, fmt.Errorf("%w: %s", ErrRecoveryRequired, reason), errors.Join(rbErrs...), persistErr)
		}
		if jErr := o.cleanupRolledBackMigrationJournal(journal); jErr != nil {
			reason := fmt.Sprintf("ReverseMigrateToSingbox rollback journal cleanup failed: %v", jErr)
			persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:         reason,
				TransitionID:   journal.JournalID,
				TargetEngine:   "sing-box",
				PreviousEngine: "firc",
				FailedStep:     "reverse_migrate_rollback_journal_cleanup",
				LastCompleted:  "rolled_back",
			})
			return errors.Join(cause, fmt.Errorf("%w: %s", ErrRecoveryRequired, reason), jErr, persistErr)
		}
		return cause
	}

	for writeIdx, w := range writes {
		if err := o.advanceMigrationJournal(journal, "writing_files", writeIdx, w.path); err != nil {
			return nil, rollbackTargets(err)
		}
		if err := atomicWriteYAMLFile(w.path, w.data, 0644); err != nil {
			return nil, rollbackTargets(fmt.Errorf("write %s: %w", filepath.Base(w.path), err))
		}
		got, rErr := os.ReadFile(w.path)
		if rErr != nil || !bytes.Equal(got, w.data) {
			return nil, rollbackTargets(fmt.Errorf("verify written %s failed: %v", filepath.Base(w.path), rErr))
		}
		if hErr := o.invokeMigrationStepHook("reverse_migrate", "writing_files", writeIdx, w.path); hErr != nil {
			return nil, rollbackTargets(hErr)
		}
	}

	committed, cErr := o.completeMigrationJournal(journal)
	if cErr != nil {
		if !committed {
			return nil, rollbackTargets(cErr)
		}
		return nil, cErr
	}

	st := o.LoadMigrationWizardState()
	st.LastMigratedAt = time.Now().Unix()
	st.LastDirection = "firc-to-singbox"
	if createdSnap != nil {
		st.LastSnapshotID = createdSnap.ID
	}
	_ = o.SaveMigrationWizardState(st)

	return res, nil
}

func (o *Operator) loadCurrentFircTunnelsAndGroups(ctx context.Context, allowLive bool) ([]Tunnel, []Group) {
	if allowLive && o.IsRunning(ctx) {
		tunnels, terr := o.Client().GetTunnels(ctx)
		groups, gerr := o.Client().GetGroups(ctx, true)
		if terr == nil && gerr == nil {
			return tunnels, groups
		}
	}
	tunnels, _ := o.LoadTunnelsFile()
	groups, _ := o.LoadGroupsFile()
	return tunnels, groups
}

func collectExistingSingboxOutboundTags(singboxConfigDir string, include10Tunnels bool) map[string]bool {
	valid := map[string]bool{
		"direct":  true,
		"block":   true,
		"dns-out": true,
	}
	if singboxConfigDir == "" {
		return valid
	}
	entries, err := os.ReadDir(singboxConfigDir)
	if err != nil {
		return valid
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !include10Tunnels && entry.Name() == "10-tunnels.json" {
			continue
		}
		data, rErr := os.ReadFile(filepath.Join(singboxConfigDir, entry.Name()))
		if rErr != nil || len(data) == 0 {
			continue
		}
		var doc struct {
			Outbounds []map[string]any `json:"outbounds"`
		}
		if json.Unmarshal(data, &doc) == nil {
			for _, ob := range doc.Outbounds {
				if tag, _ := ob["tag"].(string); strings.TrimSpace(tag) != "" {
					valid[strings.TrimSpace(tag)] = true
				}
			}
		}
	}
	return valid
}

func buildReverseSingboxPayloads(
	tunnels []Tunnel,
	groups []Group,
	singboxConfigDir string,
	opts *ReverseMigrationOptions,
	strictValidation bool,
) (*ReverseMigrationResult, []byte, []byte, []byte, []byte, error) {
	res := &ReverseMigrationResult{
		Tunnels: []ReverseMigratedTunnelItem{},
		Groups:  []ReverseMigratedGroupItem{},
	}

	selectedTunSet := make(map[string]bool)
	if opts != nil && opts.SelectedTunnelIDs != nil {
		for _, id := range opts.SelectedTunnelIDs {
			selectedTunSet[strings.TrimSpace(id)] = true
		}
	}
	selectedGrpSet := make(map[string]bool)
	if opts != nil && opts.SelectedGroupIDs != nil {
		for _, id := range opts.SelectedGroupIDs {
			selectedGrpSet[strings.TrimSpace(id)] = true
		}
	}

	devToTag := make(map[string]string)
	validOutboundTags := collectExistingSingboxOutboundTags(singboxConfigDir, !(opts.isReplaceMode() && opts.includeTunnels()))

	// Existing Sing-box outbounds, inbounds, and tunnel route rules if merge mode
	outbounds := []map[string]any{}
	existingTunnelInbounds := []map[string]any{}
	existingTunnelRules := []map[string]any{}
	seenTags := make(map[string]int)
	if !opts.isReplaceMode() && singboxConfigDir != "" {
		if data, _ := loadFirstExistingSlot(singboxConfigDir, "10-tunnels.json"); len(data) > 0 {
			var doc struct {
				Inbounds  []map[string]any `json:"inbounds"`
				Outbounds []map[string]any `json:"outbounds"`
				Route     struct {
					Rules []map[string]any `json:"rules"`
				} `json:"route"`
			}
			if json.Unmarshal(data, &doc) == nil {
				existingTunnelInbounds = append(existingTunnelInbounds, doc.Inbounds...)
				existingTunnelRules = append(existingTunnelRules, doc.Route.Rules...)
				for _, ob := range doc.Outbounds {
					tag, _ := ob["tag"].(string)
					if tag != "" {
						seenTags[tag] = len(outbounds)
						validOutboundTags[tag] = true
					}
					outbounds = append(outbounds, ob)
				}
			}
		}
	}

	// Existing subscriptions if merge mode
	awgmDir := resolveAwgmDataDir(singboxConfigDir)
	subsList := []map[string]any{}
	seenSubURLs := make(map[string]int)
	if !opts.isReplaceMode() && awgmDir != "" {
		if data, err := os.ReadFile(filepath.Join(awgmDir, "subscriptions.json")); err == nil && len(data) > 0 {
			var existing []map[string]any
			if json.Unmarshal(data, &existing) == nil {
				for _, s := range existing {
					u, _ := s["url"].(string)
					if u != "" {
						seenSubURLs[u] = len(subsList)
					}
					if lbl, _ := s["label"].(string); strings.TrimSpace(lbl) != "" {
						validOutboundTags[strings.TrimSpace(lbl)] = true
					}
					subsList = append(subsList, s)
				}
			}
		}
	}

	for _, tun := range tunnels {
		if len(tun.Sources) == 0 {
			continue
		}
		tagBase := strings.TrimSpace(tun.Description)
		if tagBase == "" {
			tagBase = tun.ID
		}

		if !opts.includeTunnels() {
			continue
		}
		if opts != nil && opts.SelectedTunnelIDs != nil && !selectedTunSet[tun.ID] {
			continue
		}

		hasSub := false
		var subURL string
		linksCount := 0
		createdForTunnel := 0
		resolvedTag := ""

		for sIdx, src := range tun.Sources {
			if src.Kind == "subscription" && strings.TrimSpace(src.URL) != "" {
				hasSub = true
				subURL = strings.TrimSpace(src.URL)
				label := strings.TrimSpace(src.Name)
				if label == "" {
					label = tagBase
				}
				entry := map[string]any{
					"id":      tun.ID,
					"label":   label,
					"url":     subURL,
					"enabled": tun.Enable,
				}
				if idx, ok := seenSubURLs[subURL]; ok {
					subsList[idx] = entry
				} else {
					seenSubURLs[subURL] = len(subsList)
					subsList = append(subsList, entry)
				}
				if resolvedTag == "" {
					resolvedTag = label
				}
				validOutboundTags[label] = true
				validOutboundTags[tagBase] = true
				createdForTunnel++
				res.SubscriptionsMigrated++
				continue
			}

			link := strings.TrimSpace(src.Link)
			if link == "" {
				continue
			}
			linksCount++
			obMap, err := convertFircLinkToSingboxOutbound(link, tagBase, src.Name, sIdx)
			if err != nil {
				msg := fmt.Sprintf("%s (%s): %v", tagBase, src.Name, err)
				if strictValidation {
					return nil, nil, nil, nil, nil, fmt.Errorf("cannot convert FIRC tunnel %s: %w", msg, err)
				}
				res.Skipped = append(res.Skipped, msg)
				continue
			}
			tag, _ := obMap["tag"].(string)
			if resolvedTag == "" && tag != "" {
				resolvedTag = tag
			}
			if tag != "" {
				validOutboundTags[tag] = true
			}
			if idx, ok := seenTags[tag]; ok {
				outbounds[idx] = obMap
			} else {
				seenTags[tag] = len(outbounds)
				outbounds = append(outbounds, obMap)
			}
			createdForTunnel++
			res.OutboundsCreated++
		}

		if createdForTunnel == 0 {
			continue
		}
		if resolvedTag == "" {
			resolvedTag = tagBase
		}
		devToTag[tun.Device] = resolvedTag
		validOutboundTags[resolvedTag] = true

		kind := "outbound"
		if hasSub {
			kind = "subscription"
		}
		res.Tunnels = append(res.Tunnels, ReverseMigratedTunnelItem{
			ID:         tun.ID,
			Name:       tagBase,
			Device:     tun.Device,
			Kind:       kind,
			LinksCount: linksCount,
			SubURL:     subURL,
			TargetTag:  resolvedTag,
		})
	}

	// Build 20-router.json and client-routes.json from FIRC groups
	routeRules := []map[string]any{}
	ruleSets := []map[string]any{}
	seenRuleSets := make(map[string]bool)
	clientRoutes := []map[string]any{}
	var existingRouterDoc map[string]any
	var existingRouteObj map[string]any

	if !opts.isReplaceMode() && singboxConfigDir != "" {
		if data, _ := loadFirstExistingSlot(singboxConfigDir, "20-router.json"); len(data) > 0 {
			var rawDoc map[string]any
			if json.Unmarshal(data, &rawDoc) == nil && len(rawDoc) > 0 {
				existingRouterDoc = rawDoc
				if rt, ok := rawDoc["route"].(map[string]any); ok && rt != nil {
					existingRouteObj = rt
				}
			}
			var doc struct {
				Route struct {
					Rules   []map[string]any `json:"rules"`
					RuleSet []map[string]any `json:"rule_set"`
				} `json:"route"`
			}
			if json.Unmarshal(data, &doc) == nil {
				routeRules = append(routeRules, doc.Route.Rules...)
				for _, rs := range doc.Route.RuleSet {
					if tag, _ := rs["tag"].(string); tag != "" {
						seenRuleSets[tag] = true
					}
					ruleSets = append(ruleSets, rs)
				}
			}
		}
		if awgmDir != "" {
			if data, err := os.ReadFile(filepath.Join(awgmDir, "client-routes.json")); err == nil && len(data) > 0 {
				var existingCR []map[string]any
				if json.Unmarshal(data, &existingCR) == nil {
					clientRoutes = append(clientRoutes, existingCR...)
				}
			}
		}
	}

	for _, grp := range groups {
		if grp.ID == KeenDNSGroupID {
			continue
		}
		if !opts.includeGroups() {
			continue
		}
		if opts != nil && opts.SelectedGroupIDs != nil && !selectedGrpSet[grp.ID] {
			continue
		}

		targetTag := resolveGroupTargetOutboundTag(grp, devToTag)
		if !validOutboundTags[targetTag] {
			msg := fmt.Sprintf("group %q (%s) references outbound/interface %q which is not included or not present in target Sing-box config", grp.Name, grp.ID, targetTag)
			if strictValidation {
				return nil, nil, nil, nil, nil, errors.New(msg)
			}
			res.Skipped = append(res.Skipped, msg)
			continue
		}
		var clientIPs []string
		for _, ip := range grp.Devices.Allow {
			ip = strings.TrimSpace(ip)
			if ip != "" && !slices.Contains(allManagedServerSubnets, ip) {
				clientIPs = append(clientIPs, ip)
			}
		}

		var domains, suffixes, keywords, regexes, cidrs []string
		for _, r := range grp.Rules {
			if !r.Enable {
				continue
			}
			val := strings.TrimSpace(r.Rule)
			if val == "" {
				continue
			}
			switch r.Type {
			case "domain":
				domains = append(domains, val)
			case "namespace":
				suffixes = append(suffixes, val)
			case "wildcard":
				cleaned := strings.TrimPrefix(val, "*.")
				if cleaned != val && !strings.Contains(cleaned, "*") {
					suffixes = append(suffixes, cleaned)
				} else {
					keywords = append(keywords, strings.ReplaceAll(val, "*", ""))
				}
			case "regex":
				regexes = append(regexes, val)
			case "subnet", "subnet6":
				cidrs = append(cidrs, val)
			}
		}

		var rsURL string
		if grp.List != nil && strings.TrimSpace(grp.List.URL) != "" {
			rsURL = strings.TrimSpace(grp.List.URL)
			rsTag := sanitizeFircID(grp.Name)
			if rsTag == "" {
				rsTag = "rs-" + grp.ID
			}
			if !seenRuleSets[rsTag] {
				seenRuleSets[rsTag] = true
				ruleSets = append(ruleSets, map[string]any{
					"tag":    rsTag,
					"type":   "remote",
					"format": "binary",
					"url":    rsURL,
				})
				res.RuleSetsMigrated++
			}
			rule := map[string]any{
				"rule_set": []string{rsTag},
				"outbound": targetTag,
			}
			if len(clientIPs) > 0 {
				rule["source_ip_cidr"] = clientIPs
			}
			routeRules = append(routeRules, rule)
			res.RulesMigrated++
		}

		if len(domains) > 0 || len(suffixes) > 0 || len(keywords) > 0 || len(regexes) > 0 || len(cidrs) > 0 {
			rule := map[string]any{
				"outbound": targetTag,
			}
			if len(domains) > 0 {
				rule["domain"] = domains
			}
			if len(suffixes) > 0 {
				rule["domain_suffix"] = suffixes
			}
			if len(keywords) > 0 {
				rule["domain_keyword"] = keywords
			}
			if len(regexes) > 0 {
				rule["domain_regex"] = regexes
			}
			if len(cidrs) > 0 {
				rule["ip_cidr"] = cidrs
			}
			if len(clientIPs) > 0 {
				rule["source_ip_cidr"] = clientIPs
			}
			routeRules = append(routeRules, rule)
			res.RulesMigrated += len(domains) + len(suffixes) + len(keywords) + len(regexes) + len(cidrs)
		} else if len(clientIPs) > 0 && rsURL == "" {
			for _, ip := range clientIPs {
				clientRoutes = append(clientRoutes, map[string]any{
					"ip":       strings.TrimSuffix(ip, "/32"),
					"outbound": targetTag,
					"enabled":  grp.Enable,
					"comment":  grp.Name,
				})
				res.ClientRoutesMigrated++
			}
		}

		res.Groups = append(res.Groups, ReverseMigratedGroupItem{
			ID:          grp.ID,
			Name:        grp.Name,
			OutboundTag: targetTag,
			RulesCount:  len(grp.Rules),
			RuleSetURL:  rsURL,
			Devices:     clientIPs,
		})
	}

	var tunnelsBytes, subsBytes, routerBytes, clientRoutesBytes []byte
	writeEmptyTunnels := opts.isReplaceMode() && opts.includeTunnels()
	writeEmptyGroups := opts.isReplaceMode() && opts.includeGroups()

	if len(outbounds) > 0 || writeEmptyTunnels {
		tunnelInbounds, tunnelRouteRules := ensureReverseTunnelInbounds(outbounds, existingTunnelInbounds, existingTunnelRules, singboxConfigDir)
		tunnelDoc := map[string]any{
			"inbounds":  tunnelInbounds,
			"outbounds": outbounds,
			"route": map[string]any{
				"rules": tunnelRouteRules,
			},
		}
		tunnelsBytes, _ = json.MarshalIndent(tunnelDoc, "", "  ")
	}
	if len(subsList) > 0 || writeEmptyTunnels {
		subsBytes, _ = json.MarshalIndent(subsList, "", "  ")
	}
	if len(routeRules) > 0 || len(ruleSets) > 0 || writeEmptyGroups {
		routerDoc := map[string]any{}
		for k, v := range existingRouterDoc {
			routerDoc[k] = v
		}
		routeObj := map[string]any{}
		for k, v := range existingRouteObj {
			routeObj[k] = v
		}
		routeObj["rules"] = routeRules
		if len(ruleSets) > 0 {
			routeObj["rule_set"] = ruleSets
		} else {
			delete(routeObj, "rule_set")
		}
		ensureRemoteRuleSetHTTPClient(routerDoc, routeObj, ruleSets)
		routerDoc["route"] = routeObj
		routerBytes, _ = json.MarshalIndent(routerDoc, "", "  ")
	}
	if len(clientRoutes) > 0 || writeEmptyGroups {
		clientRoutesBytes, _ = json.MarshalIndent(clientRoutes, "", "  ")
	}

	return res, tunnelsBytes, subsBytes, routerBytes, clientRoutesBytes, nil
}

// ensureRemoteRuleSetHTTPClient satisfies sing-box 1.14+ by guaranteeing an explicit
// top-level http_clients entry and route.default_http_client when remote rule-sets exist,
// stripping any legacy "detour": "direct" that sing-box 1.14+ rejects at startup.
func ensureRemoteRuleSetHTTPClient(routerDoc, routeObj map[string]any, ruleSets []map[string]any) {
	hasRemote := false
	for _, rs := range ruleSets {
		typ, _ := rs["type"].(string)
		urlVal, _ := rs["url"].(string)
		if strings.EqualFold(typ, "remote") || strings.TrimSpace(urlVal) != "" {
			hasRemote = true
			break
		}
	}
	if !hasRemote {
		return
	}

	var clients []map[string]any
	switch rawClients := routerDoc["http_clients"].(type) {
	case []any:
		for _, item := range rawClients {
			if m, ok := item.(map[string]any); ok {
				clients = append(clients, m)
			}
		}
	case []map[string]any:
		clients = append(clients, rawClients...)
	}

	tagSet := make(map[string]bool, len(clients))
	for _, c := range clients {
		if d, _ := c["detour"].(string); d == "direct" {
			delete(c, "detour")
		}
		if t, _ := c["tag"].(string); t != "" {
			tagSet[t] = true
		}
	}

	defClient, _ := routeObj["default_http_client"].(string)
	if defClient == "" || !tagSet[defClient] {
		const defaultTag = "rs-download"
		if !tagSet[defaultTag] {
			clients = append(clients, map[string]any{"tag": defaultTag})
		}
		routeObj["default_http_client"] = defaultTag
	}
	routerDoc["http_clients"] = clients
}

func inboundListenPort(m map[string]any) (int, bool) {
	switch v := m["listen_port"].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	default:
		return 0, false
	}
}

func collectOtherSlotInboundPorts(singboxConfigDir string) map[int]bool {
	used := make(map[int]bool)
	if singboxConfigDir == "" {
		return used
	}
	for _, dir := range []string{singboxConfigDir, filepath.Join(singboxConfigDir, "disabled")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, ent := range entries {
			if ent.IsDir() || ent.Name() == "10-tunnels.json" || !strings.HasSuffix(ent.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, ent.Name()))
			if err != nil {
				continue
			}
			var doc struct {
				Inbounds []map[string]any `json:"inbounds"`
			}
			if json.Unmarshal(raw, &doc) != nil {
				continue
			}
			for _, ib := range doc.Inbounds {
				if p, ok := inboundListenPort(ib); ok && p > 0 {
					used[p] = true
				}
			}
		}
	}
	return used
}

// ensureReverseTunnelInbounds preserves existing per-tunnel mixed inbounds/rules
// and synthesizes missing ones (1080+) so sing-box ProxyN reconciliation and Uplink
// Fallback probes always find valid ListenPort/ProxyInterface slots on 10-tunnels.json.
func ensureReverseTunnelInbounds(
	outbounds []map[string]any,
	existingInbounds []map[string]any,
	existingRules []map[string]any,
	singboxConfigDir string,
) ([]map[string]any, []map[string]any) {
	const firstTunnelPort = 1080
	userTags := make([]string, 0, len(outbounds))
	userTagSet := make(map[string]bool, len(outbounds))
	for _, ob := range outbounds {
		typ, _ := ob["type"].(string)
		if typ == "direct" || typ == "block" || typ == "dns" || typ == "selector" {
			continue
		}
		tag, _ := ob["tag"].(string)
		if tag == "" || userTagSet[tag] {
			continue
		}
		userTagSet[tag] = true
		userTags = append(userTags, tag)
	}

	usedPorts := collectOtherSlotInboundPorts(singboxConfigDir)
	inbounds := make([]map[string]any, 0, len(userTags))
	keptInboundTags := make(map[string]bool, len(userTags))
	hasInboundForTag := make(map[string]bool, len(userTags))

	for _, ib := range existingInbounds {
		tag, _ := ib["tag"].(string)
		port, okPort := inboundListenPort(ib)
		if strings.HasSuffix(tag, "-in") {
			outTag := strings.TrimSuffix(tag, "-in")
			if !userTagSet[outTag] || !okPort || port < firstTunnelPort || keptInboundTags[tag] || usedPorts[port] {
				continue
			}
			hasInboundForTag[outTag] = true
		}
		if okPort && port > 0 {
			usedPorts[port] = true
		}
		if tag != "" {
			keptInboundTags[tag] = true
		}
		inbounds = append(inbounds, ib)
	}

	for _, tag := range userTags {
		if hasInboundForTag[tag] {
			continue
		}
		port := firstTunnelPort
		for usedPorts[port] {
			port++
		}
		usedPorts[port] = true
		ibTag := tag + "-in"
		keptInboundTags[ibTag] = true
		inbounds = append(inbounds, map[string]any{
			"type":        "mixed",
			"tag":         ibTag,
			"listen":      "127.0.0.1",
			"listen_port": port,
		})
	}

	rules := make([]map[string]any, 0, len(userTags))
	hasRouteRule := make(map[string]bool, len(userTags))
	for _, r := range existingRules {
		inTag, _ := r["inbound"].(string)
		outTag, _ := r["outbound"].(string)
		if inTag != "" && !keptInboundTags[inTag] {
			continue
		}
		if inTag != "" && outTag != "" && inTag == outTag+"-in" {
			if hasRouteRule[outTag] {
				continue
			}
			hasRouteRule[outTag] = true
		}
		rules = append(rules, r)
	}
	for _, tag := range userTags {
		if hasRouteRule[tag] {
			continue
		}
		rules = append(rules, map[string]any{
			"inbound":  tag + "-in",
			"outbound": tag,
		})
	}
	return inbounds, rules
}

func resolveGroupTargetOutboundTag(grp Group, devToTag map[string]string) string {
	for _, iface := range grp.Interface {
		if tag, ok := devToTag[iface]; ok && tag != "" {
			return tag
		}
		if iface == "eth3" || iface == "wan" {
			return "direct"
		}
		if iface != "" {
			return iface
		}
	}
	return "direct"
}

func convertFircLinkToSingboxOutbound(link, tunnelName, sourceName string, idx int) (map[string]any, error) {
	tag := strings.TrimSpace(sourceName)
	if tag == "" {
		if idx == 0 {
			tag = tunnelName
		} else {
			tag = fmt.Sprintf("%s-%d", tunnelName, idx+1)
		}
	}

	if parsed, err := vlink.ParseLink(link); err == nil && parsed != nil && len(parsed.Outbound) > 0 {
		var m map[string]any
		if err := json.Unmarshal(parsed.Outbound, &m); err == nil {
			m["tag"] = tag
			return m, nil
		}
	}

	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https":
		host := u.Hostname()
		portStr := u.Port()
		port := 8080
		if scheme == "https" && portStr == "" {
			port = 443
		} else if portStr != "" {
			if p, perr := strconv.Atoi(portStr); perr == nil && p > 0 {
				port = p
			}
		}
		ob := map[string]any{
			"type":        "http",
			"tag":         tag,
			"server":      host,
			"server_port": port,
		}
		if u.User != nil {
			ob["username"] = u.User.Username()
			if pw, ok := u.User.Password(); ok {
				ob["password"] = pw
			}
		}
		if scheme == "https" {
			ob["tls"] = map[string]any{"enabled": true}
		}
		return ob, nil
	case "tt", "trusttunnel":
		host := u.Hostname()
		port := 443
		if portStr := u.Port(); portStr != "" {
			if p, perr := strconv.Atoi(portStr); perr == nil && p > 0 {
				port = p
			}
		}
		q := u.Query()
		tlsCfg := map[string]any{"enabled": true}
		if sni := strings.TrimSpace(q.Get("sni")); sni != "" {
			tlsCfg["server_name"] = sni
		}
		if q.Get("insecure") == "1" || strings.EqualFold(q.Get("insecure"), "true") {
			tlsCfg["insecure"] = true
		}
		if q.Get("record_fragment") == "1" || q.Get("fragment") == "1" || strings.EqualFold(q.Get("record_fragment"), "true") {
			tlsCfg["fragment"] = true
		}
		ob := map[string]any{
			"type":        "trusttunnel",
			"tag":         tag,
			"server":      host,
			"server_port": port,
			"quic":        q.Get("quic") == "1" || strings.EqualFold(q.Get("quic"), "true"),
			"tls":         tlsCfg,
		}
		if u.User != nil {
			if user := u.User.Username(); user != "" {
				ob["username"] = user
			}
			if pw, ok := u.User.Password(); ok && pw != "" {
				ob["password"] = pw
			}
		}
		return ob, nil
	case "socks", "socks5":
		host := u.Hostname()
		port := 1080
		if portStr := u.Port(); portStr != "" {
			if p, perr := strconv.Atoi(portStr); perr == nil && p > 0 {
				port = p
			}
		}
		ob := map[string]any{
			"type":        "socks",
			"tag":         tag,
			"server":      host,
			"server_port": port,
		}
		if u.User != nil {
			if user := u.User.Username(); user != "" {
				ob["username"] = user
			}
			if pw, ok := u.User.Password(); ok && pw != "" {
				ob["password"] = pw
			}
		}
		return ob, nil
	default:
		return nil, fmt.Errorf("unsupported reverse link scheme %q", scheme)
	}
}
