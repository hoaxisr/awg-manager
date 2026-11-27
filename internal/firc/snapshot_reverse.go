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
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrSingboxValidatorUnavailable is returned when Sing-box candidate verification cannot run
// because no custom validator is wired and the Sing-box binary is empty, missing, or non-executable.
var ErrSingboxValidatorUnavailable = errors.New("firc: sing-box candidate validator unavailable")

const (
	maxStoredSnapshots  = 10
	wizardStateFileName = "awgm-migration-state.json"
	snapshotsSubdirName = "snapshots"
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
	Trigger                   string `json:"trigger"` // "auto-pre-migrate" | "auto-pre-purge" | "manual"; "auto-pre-reverse" — у старых снимков
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
	LastDirection      string `json:"lastDirection,omitempty"` // "singbox-to-firc" | "snapshot-restore"; "firc-to-singbox" — в старом состоянии
	LastSnapshotID     string `json:"lastSnapshotId,omitempty"`
	LastPostAction     string `json:"lastPostAction,omitempty"`
}

// MigrationOverviewResponse is returned by GET /api/firc/migrate/state for the startup popup and wizard modal.
type MigrationOverviewResponse struct {
	ShouldShowPopup           bool                 `json:"shouldShowPopup"`
	WizardState               MigrationWizardState `json:"wizardState"`
	ActiveEngine              string               `json:"activeEngine"`
	FircRunning               bool                 `json:"fircRunning"`
	FircInstalled             bool                 `json:"fircInstalled"`
	SingboxRunning            bool                 `json:"singboxRunning"`
	SingboxInstalled          bool                 `json:"singboxInstalled"`
	SingboxTunnelsCount       int                  `json:"singboxTunnelsCount"`
	SingboxSubscriptionsCount int                  `json:"singboxSubscriptionsCount"`
	FircTunnelsCount          int                  `json:"fircTunnelsCount"`
	FircGroupsCount           int                  `json:"fircGroupsCount"`
	Snapshots                 []SnapshotMeta       `json:"snapshots"`
	ForwardPreview            *MigrationResult     `json:"forwardPreview,omitempty"`
}

func (o *Operator) wizardStatePath() string {
	return filepath.Join(o.configDir, wizardStateFileName)
}

func (o *Operator) snapshotsDir() string {
	return filepath.Join(o.configDir, snapshotsSubdirName)
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
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
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
	GlobalOutbound          string   `json:"globalOutbound"`
	KeenDNSCloudOutbound    string   `json:"keenDNSCloudOutbound"`
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
	if p := parsed.SingboxClashPort; p < 0 || p > 65535 {
		return fmt.Errorf("invalid port %d in settings.json", p)
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

// pool — пул подставных адресов для ValidateGroups; nil — без этой проверки.
func validateFircGroupsYAML(data []byte, pool []netip.Prefix) ([]Group, error) {
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
	if err := ValidateGroups(doc.Groups, pool); err != nil {
		return nil, fmt.Errorf("groups.yaml: %w", err)
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
			_, err := validateFircGroupsYAML(data, nil) // пул известен только вместе с firc.conf: см. validateSnapshotEntriesPreflight
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
	if err := o.CheckNotForeign(); err != nil {
		return nil, err
	}
	now := time.Now()
	if strings.TrimSpace(trigger) == "" {
		trigger = "manual"
	}
	if strings.TrimSpace(label) == "" {
		switch trigger {
		case "auto-pre-migrate":
			label = "Авто-снапшот перед миграцией Sing-box → AWGM Route"
		case "auto-pre-reverse":
			label = "Авто-снапшот перед обратной миграцией AWGM Route → Sing-box"
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
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
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

// buildFinalSingboxRootManifest computes the exact root-level Sing-box config.d file set
// that will exist on disk after the restore (V2 unified manifest). Restore clears stale
// root files, so any existing root .json file (including 00-base.json) absent from staged
// is deleted on apply and is therefore excluded from the manifest.
func buildFinalSingboxRootManifest(staged []stagedRestoreFile) map[string][]byte {
	manifest := make(map[string][]byte)
	for _, sf := range staged {
		if sf.scope != "singbox" || strings.Contains(sf.relPath, "/") {
			continue
		}
		manifest[sf.relPath] = sf.data
	}
	return manifest
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
		finalSingboxRoot = buildFinalSingboxRootManifest(staged)

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
			pool := o.addressPool()
			if conf, ok := stagedMap["firc:firc.conf"]; ok {
				pool = o.addressPoolFor(conf)
			}
			grps, err := validateFircGroupsYAML(grpData, pool)
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

func (o *Operator) applySnapshotFilesTransactionalWithBackups(
	ctx context.Context,
	operationName string,
	singboxConfigDir string,
	entries []SnapshotFileEntry,
	reconcileFirc bool,
	requireEngineScope string,
	capturedBackups *[]fileSnapshot,
) (int, error) {
	staged, hasScope, err := o.validateSnapshotEntriesPreflight(ctx, singboxConfigDir, entries, requireEngineScope)
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

	if singboxConfigDir != "" {
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
		groups, _ = o.syncGroupsWithExt(ctx, groups, ext)
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
	if _, err := o.applySnapshotFilesTransactionalWithBackups(ctx, "snapshot_restore", singboxConfigDir, snap.Files, reconcileFirc, snap.ActiveEngine, &backupSnaps); err != nil {
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
				grps, _ = o.syncGroupsWithExt(rbCtx, grps, ext)
				updated, _ := o.SyncGroupsRuntimeOnly(rbCtx, grps)
				_ = o.ReconcileExtNetfilterStrict(rbCtx, ext, updated)
			}
		}
		return errors.Join(errs...)
	}

	return &snap.SnapshotMeta, rollbackFn, nil
}
