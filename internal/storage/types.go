package storage

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Settings represents /opt/etc/awg-manager/settings.json
type Settings struct {
	SchemaVersion int  `json:"schemaVersion,omitempty"`
	AuthEnabled   bool `json:"authEnabled"`
	// ApiKey is an opaque secret accepted in place of a session cookie via
	// `Authorization: Bearer <key>` header. Empty disables key-based access
	// (session is still required when AuthEnabled). Generated client-side
	// via crypto.randomUUID(); the server treats it as opaque.
	ApiKey string `json:"apiKey,omitempty"`
	// SessionTtlHours is the auth session lifetime in hours (sliding
	// window, extended on activity). Valid range 1..720, default 24
	// (migrateToV29). Read live by the session store, so shortening it
	// takes effect immediately for existing sessions; the browser cookie
	// Max-Age of already-issued sessions updates on next login (the
	// server-side expiry check is authoritative either way).
	SessionTtlHours int `json:"sessionTtlHours"`
	// EntwareAuthEnabled allows login with Entware system credentials
	// (/opt/etc/shadow) verified locally, without the NDMS /auth call
	// that generates router-side notifications. When the local check
	// fails for any reason, login falls back to the Keenetic path.
	EntwareAuthEnabled   bool              `json:"entwareAuthEnabled"`
	Server               ServerSettings    `json:"server"`
	PingCheck            PingCheckSettings `json:"pingCheck"`
	Logging              LoggingSettings   `json:"logging"`
	DisableMemorySaving  bool              `json:"disableMemorySaving"` // false = auto, true = soft mode
	Updates              UpdateSettings    `json:"updates"`
	Download             DownloadSettings  `json:"download"`
	DNSRoute             DNSRouteSettings  `json:"dnsRoute"`
	GeoFile              GeoFileSettings   `json:"geoFile"`
	ConnectivityCheckURL string            `json:"connectivityCheckUrl"`
	UsageLevel           string            `json:"usageLevel"`
	ServerInterfaces     []string          `json:"serverInterfaces,omitempty"`
	// ServerInterfaceMeta stores AWG Manager bookkeeping for built-in/marked
	// servers (NAT static-WAN for internet-only teardown). map[serverID].
	ServerInterfaceMeta map[string]ServerInterfaceMeta `json:"serverInterfaceMeta,omitempty"`
	// ServerPeerSecrets stores private keys for peers created via AWG Manager
	// on built-in/marked NDMS servers (NDMS itself does not retain client keys).
	// map[serverID]map[publicKey]ServerPeerSecret
	ServerPeerSecrets map[string]map[string]ServerPeerSecret `json:"serverPeerSecrets,omitempty"`
	ManagedServers    []ManagedServer                        `json:"managedServers,omitempty"`
	// ManagedServer is retained for one release as the migration source.
	// migrateManagedServers() moves it into ManagedServers[0] on first read
	// and clears it on the next save.
	ManagedServer             *ManagedServer        `json:"managedServer,omitempty"`
	ManagedPolicies           []string              `json:"managedPolicies,omitempty"`
	MonitoringExcludedTunnels []string              `json:"monitoringExcludedTunnels,omitempty"`
	SingboxRouter             SingboxRouterSettings `json:"singboxRouter"`
	// SingboxManuallyStopped is the sticky-stop intent: when true, the
	// daemon stays down even though tunnels are configured. Watchdog
	// reconciles only when this is false. Cleared by Control("start")
	// and Control("restart"); set by Control("stop").
	SingboxManuallyStopped bool `json:"singboxManuallyStopped,omitempty"`
	// CreateNDMSProxyForSingbox controls whether NDMS ProxyN/t2sN
	// interfaces are created per sing-box tunnel. When false, sing-box
	// works only through its internal router engine (deviceproxy / TUN /
	// internal inbounds), and NDMS-level routing rules cannot target it.
	// Default true (back-compat). See docs/superpowers/specs/
	// 2026-05-22-singbox-ndms-proxy-toggle-design.md.
	CreateNDMSProxyForSingbox bool `json:"createNDMSProxyForSingbox"`
	// ManagedPeerAllowIPsMigrated marks the one-time NDMS sweep that strips
	// the legacy default 0.0.0.0/0 from managed-server peers' allow-ips
	// (per-peer /32 only). New firmware rejects multiple peers sharing
	// 0.0.0.0/0 with "subnet overlaps with the other peer". Set after a
	// successful sweep; the sweep runs at startup while false.
	ManagedPeerAllowIPsMigrated bool `json:"managedPeerAllowIPsMigrated,omitempty"`
	// FakeIP is backend-managed operational state for sing-box fakeip-tun
	// mode (see FakeIPState). Pointer so it's absent from JSON when never
	// provisioned; nil = not provisioned. Written ONLY via SetFakeIPState.
	FakeIP *FakeIPState `json:"fakeip,omitempty"`
	// PolicyTun is backend-managed operational state for sing-box policy-tun
	// mode (see PolicyTunState). Pointer so it's absent from JSON when never
	// provisioned; nil = not provisioned. Written ONLY via SetPolicyTunState.
	PolicyTun *PolicyTunState `json:"policyTun,omitempty"`
	// DNSChainPreset is backend-managed state of the sing-box 1.14 DNS-chain
	// preset (see DNSChainPresetState). Pointer so it's absent from JSON when
	// never enabled; nil = no preset. Written ONLY via SetDNSChainPresetState.
	DNSChainPreset *DNSChainPresetState `json:"dnsChainPreset,omitempty"`
}

// DNSChainPresetState is backend-managed state of the DNS-chain preset
// (sing-box 1.14 evaluate/match_response chains). Written ONLY via
// SettingsStore.SetDNSChainPresetState — never by the settings API, so a
// settings PUT cannot clobber it (mirrors FakeIPState).
// Mode: "" (no preset) | "resilient" (race direct+proxy) | "antipoison"
// (re-route poisoned answers through the proxy resolver). DirectServer /
// ProxyServer are tags of existing DNS servers; PoisonCIDRs is the antipoison
// ip_cidr list (empty = defaultPoisonCIDRs seed).
type DNSChainPresetState struct {
	Mode         string   `json:"mode"`
	DirectServer string   `json:"directServer,omitempty"`
	ProxyServer  string   `json:"proxyServer,omitempty"`
	PoisonCIDRs  []string `json:"poisonCidrs,omitempty"`
}

// FakeIPState is backend-managed operational state for sing-box fakeip-tun
// mode. Written ONLY by the fakeip-tun lifecycle (Enable/Disable/reap) via
// SettingsStore.SetFakeIPState — never by the settings API. Index is the
// allocated OpkgTun index (iface "opkgtun<N>"), valid only when Provisioned;
// Inet4Range/Inet6Range record the pool ranges last applied so a pool change
// can invalidate the sing-box cache.
type FakeIPState struct {
	Provisioned bool   `json:"provisioned,omitempty"`
	Index       int    `json:"index,omitempty"`
	Inet4Range  string `json:"inet4Range,omitempty"`
	Inet6Range  string `json:"inet6Range,omitempty"`
}

// PolicyTunNATSegment — записанное ИСХОДНОЕ NAT-состояние сегмента перед
// применением source-preserve, чтобы teardown восстановил именно его, а не
// безусловный `ip nat`. PriorMode: "dynamic" | "static" | "none".
type PolicyTunNATSegment struct {
	Name           string `json:"name"`
	PriorMode      string `json:"priorMode"`
	PriorStaticWAN string `json:"priorStaticWan,omitempty"`
}

// PolicyTunState — backend-managed состояние policy-tun (зеркало FakeIPState).
// Пишется ТОЛЬКО lifecycle'ом (Enable/Disable/reap) через SetPolicyTunState.
type PolicyTunState struct {
	Provisioned bool                  `json:"provisioned,omitempty"`
	Index       int                   `json:"index,omitempty"`
	NATSegments []PolicyTunNATSegment `json:"natSegments,omitempty"`
}

type DownloadSettings struct {
	RouteTag  string `json:"routeTag"`            // default: "direct"
	RouteKind string `json:"routeKind,omitempty"` // default: "direct"
}

type SingboxRouterSettings struct {
	Enabled    bool   `json:"enabled"`
	PolicyName string `json:"policyName"`
	// DeviceMode controls which LAN devices are routed through sing-box.
	// "policy" (default) keeps the historical NDMS access-policy mark
	// filter. "all" installs unmarked PREROUTING jumps so every LAN
	// device that reaches the router netfilter path is filtered.
	DeviceMode string `json:"deviceMode,omitempty"`
	// RoutingMode selects the sing-box routing path:
	// "tproxy" (default) keeps the historical TPROXY/REDIRECT behavior;
	// "fakeip-tun" routes via a fake-IP DNS pool + tun device;
	// "policy-tun" captures traffic via an NDMS access policy + tun device.
	RoutingMode    string `json:"routingMode,omitempty"`
	SnifferEnabled bool   `json:"snifferEnabled"`
	// WANAutoDetect is the discriminator for the WAN-binding mode.
	// true (default) → sing-box uses route.auto_detect_interface; the
	// WANInterface field is ignored and must be empty (enforced by
	// validateSingboxRouterSettings).
	// false → sing-box pins outbound traffic to WANInterface via
	// route.default_interface; WANInterface must be a non-empty kernel
	// system-name (enforced by the same validator).
	// Two-field shape on purpose: an empty WANInterface string alone is
	// ambiguous ("not chosen yet" vs "auto"); the explicit bool makes
	// the intent unambiguous in storage and in every consumer.
	WANAutoDetect bool `json:"wanAutoDetect"`
	// WANInterface is the kernel system-name of the user-pinned WAN
	// (e.g. "ppp0", "eth3"). NEVER stores the NDMS interface ID — NDMS
	// IDs (ISP, PPPoE0, …) can change on interface re-creation, kernel
	// system-names don't. The UI layer translates between the two.
	// Only meaningful when WANAutoDetect == false.
	WANInterface string `json:"wanInterface,omitempty"`
	// BypassPresets lists named protocol presets to exclude from TPROXY/REDIRECT
	// (port-based) or to drive related behaviour. Valid values: "l2tp", "ntp",
	// "netbios-smb" (ports), "keendns" (managed DNS rewrite of the router's
	// own KeenDNS/CrazeDNS FQDN → LAN IP — not an iptables CIDR). Default for
	// fresh installs and post-v33 migrations includes "keendns".
	BypassPresets []string `json:"bypassPresets,omitempty"`
	// BypassExtraPorts is a user-supplied comma-separated list of extra port
	// exclusions in "PORT UDP|TCP" format (e.g. "51820 UDP, 1194 TCP").
	// Parsed at iptables generation time. Empty = no extras.
	BypassExtraPorts string `json:"bypassExtraPorts,omitempty"`
	// BypassExtraSubnets — пользовательский список IPv4 IP/CIDR через
	// запятую/пробел (напр. "203.0.113.0/24, 10.8.0.5"). Трафик к ним идёт
	// целиком мимо sing-box (включая DNS/53). Голый IP трактуется как /32.
	// Парсится в момент генерации правил. Пусто = нет исключений.
	BypassExtraSubnets string `json:"bypassExtraSubnets,omitempty"`
	// BypassGeoIPTags — geoip-теги из настроенных .dat, чьи CIDR уходят в WAN
	// мимо sing-box через ipset AWGM-BYPASS (та же семантика полного обхода,
	// что у BypassExtraSubnets, но масштаб geoip требует ipset вместо
	// дискретных правил). Пусто — набора и правила нет.
	BypassGeoIPTags []string `json:"bypassGeoipTags,omitempty"`
	// IngressInterfaces — ref'ы интерфейсов, чей ingress-трафик заворачивается
	// в sing-box. Формат: "managed:Wireguard3" (резолвится в kernel-имя на
	// сборке спека) или "iface:nwg5" (kernel-имя как есть). Пусто = выключено.
	IngressInterfaces []string `json:"ingressInterfaces,omitempty"`
	// --- fakeip-tun engine settings (USER-editable) ---
	// These mirror the static fakeip-tun engine knobs (default
	// DefaultFakeIPTunParams) but persisted + validated so the UI can edit them.
	// Unlike FakeIPState (backend-managed operational state) these are user
	// intent, defaulted by NormalizeSingboxRouterSettings.
	//
	// FakeIPStack selects the sing-tun stack: "gvisor" (default, robust) or
	// "system" (lower CPU/RAM; on this kernel REQUIRES gso:false — set
	// automatically by the config builder).
	FakeIPStack string `json:"fakeipStack,omitempty"`
	// FakeIPPool4 is the fakeip v4 pool CIDR (default "198.18.0.0/15").
	FakeIPPool4 string `json:"fakeipPool4,omitempty"`
	// FakeIPPool6 is the fakeip v6 pool CIDR (default "fc00::/18"); "" disables v6.
	FakeIPPool6 string `json:"fakeipPool6,omitempty"`
	// FakeIPMTU is the tun MTU (default 1500).
	FakeIPMTU int `json:"fakeipMtu,omitempty"`
	// FakeIPRealServer is the true upstream resolver the engine-managed "real"
	// DNS server forwards to (default "1.1.1.1"). Must be a plain IP address —
	// the fakeip topology resolves every domain through "real" itself, so a
	// domain upstream could never bootstrap. Captured from a user edit of the
	// "real" server address in the fakeip DNS panel (issue #487) or set
	// directly via the settings API.
	FakeIPRealServer string `json:"fakeipRealServer,omitempty"`
	// UDPTimeout задаёт таймаут UDP-сессий в tproxy-in / fakeip tun-in inbound
	// (формат Go duration, например "5m0s", "10m0s"). Пустая строка = значение по
	// умолчанию (DefaultUDPTimeout, 5m). Увеличение помогает играм и другим
	// UDP-приложениям, которые могут молчать дольше и терять сессию.
	UDPTimeout string `json:"udpTimeout,omitempty"`
	// QoSClasses lists DSCP-based QoS traffic classes (issue #371). Each
	// enabled class gets its own iptables `-m dscp` dispatch (mangle TPROXY +
	// nat REDIRECT), a dedicated pair of sing-box inbounds and a managed route
	// rule sending the class to Outbound. Only meaningful when RoutingMode ==
	// "tproxy". Validated by router.NormalizeSingboxRouterSettings: at most 8
	// classes, DSCP 0-63 unique across classes, Name ≤ 32 chars, Outbound
	// non-empty. Empty slice = feature off; no schema migration needed (the
	// zero value is the correct default, same as BypassPresets).
	QoSClasses []SingboxQoSClass `json:"qosClasses,omitempty"`
	// PolicyTunSourcePreserve: в режиме policy-tun переводить выбранные сегменты
	// на static-NAT (no ip nat + ip static → WAN), чтобы sing-box видел реальные
	// LAN-source (иначе маскарад: все клиенты = tun-адрес). Default false.
	PolicyTunSourcePreserve bool `json:"policyTunSourcePreserve,omitempty"`
	// PolicyTunNATSegments — выбранные пользователем сегменты для source-preserve
	// (редактируемый предпоказ в UI). Пусто при выключенной опции.
	PolicyTunNATSegments []string `json:"policyTunNatSegments,omitempty"`
}

// SingboxQoSClass is one DSCP-based QoS traffic class routed to a dedicated
// sing-box outbound (issue #371). DSCP is the 6-bit codepoint matched by
// iptables `-m dscp --dscp N`; Name is a user-facing label; Outbound is the
// sing-box outbound tag the class routes to; Enabled toggles the class
// without losing its configuration.
type SingboxQoSClass struct {
	DSCP     int    `json:"dscp"`
	Name     string `json:"name,omitempty"`
	Outbound string `json:"outbound"`
	Enabled  bool   `json:"enabled"`
	// Slot is the persisted 0-based listen-port slot (0..MaxQoSClasses-1)
	// the class's inbound pair derives from (router.QoSClassPorts). It is
	// backend-owned: the UI contract stays {dscp,name,outbound,enabled} —
	// clients PUT classes without slots and router.UpdateSettings
	// re-associates each incoming class with its stored slot by DSCP (the
	// unique key), so disabling or removing one class never shifts another
	// class's ports (a shift would RST/blackhole untouched flows). Brand-new
	// DSCPs get the first free slot.
	Slot int `json:"slot"`
}

// ManagedServer represents the user-created WireGuard server interface.
type ManagedServer struct {
	InterfaceName string   `json:"interfaceName"`         // e.g. "Wireguard3"
	Description   string   `json:"description,omitempty"` // user-facing display name, synced to NDMS interface description
	Address       string   `json:"address"`               // e.g. "10.0.0.1"
	Mask          string   `json:"mask"`                  // e.g. "255.255.255.0"
	ListenPort    int      `json:"listenPort"`
	Endpoint      string   `json:"endpoint,omitempty"` // custom endpoint (IP or domain); empty = WAN IP
	DNS           string   `json:"dns,omitempty"`      // custom DNS for client configs; empty = "1.1.1.1, 8.8.8.8"
	MTU           int      `json:"mtu,omitempty"`      // custom MTU for client configs; 0 = 1376
	NATEnabled    bool     `json:"natEnabled,omitempty"`
	NATMode       string   `json:"natMode,omitempty"`       // "full" | "internet-only" | "none"; source of truth, NATEnabled — производное
	NATStaticWAN  string   `json:"natStaticWan,omitempty"`  // WAN-iface (to-interface), на котором создан static NAT для internet-only; для детерминированного снятия
	NATStaticWANs []string `json:"natStaticWans,omitempty"` // выходы (to-interface), на которых создан static NAT для internet-only; источник правды, NATStaticWAN — legacy
	LANSegments   []string `json:"lanSegments,omitempty"`   // NDMS interface-name LAN-бриджей ("Home","Guest"), доступных peers
	// PrivateKey is the server's WireGuard private key. Populated by
	// Service.Create immediately after NDMS auto-generates the keypair,
	// or by Service.MigratePrivateKeys on first boot after upgrade for
	// pre-existing servers. Empty value means migration has not yet run
	// or the kernel device is unreachable; Export and drift-Restore skip
	// such entries with a clear outcome message.
	PrivateKey string `json:"privateKey,omitempty"`
	// Policy is the ip hotspot policy applied to this server's interface.
	// "none" = no policy (default-permit), "permit"/"deny" = literal RCI
	// values, anything else = IP Policy profile name (e.g. "Policy0").
	// Always serialized — empty string is normalized to "none" on read.
	Policy string        `json:"policy"`
	Peers  []ManagedPeer `json:"peers"`
	// Signature packets for client configs (not stored on NDMS server)
	I1 string `json:"i1,omitempty"`
	I2 string `json:"i2,omitempty"`
	I3 string `json:"i3,omitempty"`
	I4 string `json:"i4,omitempty"`
	I5 string `json:"i5,omitempty"`
	// ASC is a runtime-only backup/restore snapshot of numeric/header ASC
	// params (jc/jmin/jmax/s1/s2/s3/s4/h1/h2/h3/h4). Not persisted in
	// settings.json — NDMS remains source-of-truth for these fields.
	ASC json.RawMessage `json:"-"`
}

// ServerInterfaceMeta tracks AWG Manager metadata for built-in/marked servers.
type ServerInterfaceMeta struct {
	NATStaticWAN  string   `json:"natStaticWan,omitempty"`  // WAN iface used by ip static
	NATStaticWANs []string `json:"natStaticWans,omitempty"` // выходы (to-interface), на которых создан static NAT для internet-only; источник правды, NATStaticWAN — legacy
	// Endpoint is the host (IP or domain) embedded in generated client .conf
	// files. Empty = resolve WAN IP at generation time.
	Endpoint string `json:"endpoint,omitempty"`
}

// StaticNATList отдаёт выходы, на которых стоит наш static NAT: новое
// поле-список, при его отсутствии — legacy одиночный WAN (записи до 2.18).
func (m *ManagedServer) StaticNATList() []string {
	if len(m.NATStaticWANs) > 0 {
		return m.NATStaticWANs
	}
	if m.NATStaticWAN != "" {
		return []string{m.NATStaticWAN}
	}
	return nil
}

// StaticNATList — см. ManagedServer.StaticNATList.
func (m *ServerInterfaceMeta) StaticNATList() []string {
	if len(m.NATStaticWANs) > 0 {
		return m.NATStaticWANs
	}
	if m.NATStaticWAN != "" {
		return []string{m.NATStaticWAN}
	}
	return nil
}

// ServerPeerSecret holds key material for a peer on a built-in/marked server.
type ServerPeerSecret struct {
	PrivateKey   string `json:"privateKey"`
	PresharedKey string `json:"presharedKey,omitempty"`
	Description  string `json:"description,omitempty"`
	TunnelIP     string `json:"tunnelIP,omitempty"`
}

// ManagedPeer represents a client peer on the managed server.
type ManagedPeer struct {
	PublicKey    string `json:"publicKey"`
	PrivateKey   string `json:"privateKey"` // stored for .conf generation
	PresharedKey string `json:"presharedKey"`
	Description  string `json:"description"`
	TunnelIP     string `json:"tunnelIP"`      // e.g. "10.0.0.2/32"
	DNS          string `json:"dns,omitempty"` // per-peer DNS for .conf generation
	Enabled      bool   `json:"enabled"`
}

// ServerSettings contains HTTP server configuration.
type ServerSettings struct {
	Port int `json:"port"`
	// Interface is the legacy single bind interface. Superseded by
	// Interfaces (migrateToV30 copies it there); kept populated so a
	// downgrade to an older release still binds where it used to.
	// New code must read Interfaces only.
	Interface string `json:"interface"`
	// Interfaces lists kernel interface names (e.g. "br0") whose IPv4
	// addresses the HTTP server binds to (one listener per interface, plus
	// an always-on 127.0.0.1 listener for the NDMS reverse proxy and health
	// probes). Empty = bind all interfaces (0.0.0.0).
	Interfaces []string `json:"interfaces,omitempty"`
}

// PingCheckSettings contains global ping check configuration.
type PingCheckSettings struct {
	Enabled  bool              `json:"enabled"`
	Defaults PingCheckDefaults `json:"defaults"`
}

// PingCheckDefaults contains default values for tunnel ping checks.
type PingCheckDefaults struct {
	Method        string `json:"method"`        // "http" or "icmp"
	Target        string `json:"target"`        // ICMP target, default "8.8.8.8"
	Interval      int    `json:"interval"`      // check interval in seconds, default 45
	DeadInterval  int    `json:"deadInterval"`  // dead tunnel check interval in seconds, default 120
	FailThreshold int    `json:"failThreshold"` // failures before marking dead, default 3
}

// LoggingSettings contains application logging configuration.
type LoggingSettings struct {
	Enabled           bool   `json:"enabled"`           // default: false
	MaxAge            int    `json:"maxAge"`            // hours, default: 2 (shared by both buffers)
	LogLevel          string `json:"logLevel"`          // "warn", "info", "full", "debug"; default: "info"
	SingboxLogLevel   string `json:"singboxLogLevel"`   // "trace", "debug", "info", "warn", "error", "fatal", "panic"; default: "info"
	AppMaxEntries     int    `json:"appMaxEntries"`     // app-bucket buffer cap, default: 5000
	SingboxMaxEntries int    `json:"singboxMaxEntries"` // singbox-bucket buffer cap, default: 5000
}

// UpdateSettings contains auto-update configuration.
type UpdateSettings struct {
	CheckEnabled            bool   `json:"checkEnabled"`            // default: true
	Channel                 string `json:"channel"`                 // "stable" (default) | "develop"
	AutoInstallEnabled      bool   `json:"autoInstallEnabled"`      // default: false
	AutoInstallIntervalDays int    `json:"autoInstallIntervalDays"` // default: 7, valid 1-30
	AutoInstallTime         string `json:"autoInstallTime"`         // "HH:MM", default: "05:00"
}

// DNSRouteSettings contains DNS route auto-refresh configuration.
type DNSRouteSettings struct {
	AutoRefreshEnabled   bool   `json:"autoRefreshEnabled"`         // default: false
	RefreshIntervalHours int    `json:"refreshIntervalHours"`       // default: 0 (user must choose)
	RefreshMode          string `json:"refreshMode,omitempty"`      // "interval" (default/empty) or "daily"
	RefreshDailyTime     string `json:"refreshDailyTime,omitempty"` // "HH:MM" 24h format, e.g. "03:00"
}

// GeoFileSettings contains geo-file (geoip/geosite) auto-refresh configuration.
// Mirrors DNSRouteSettings. The download route is NOT stored here — background
// refresh reuses the global Download route via downloader.ResolveClient(ctx, nil).
type GeoFileSettings struct {
	AutoRefreshEnabled   bool   `json:"autoRefreshEnabled"`         // default: false
	RefreshIntervalHours int    `json:"refreshIntervalHours"`       // default: 0 (user chooses)
	RefreshMode          string `json:"refreshMode,omitempty"`      // "interval" (default) or "daily"
	RefreshDailyTime     string `json:"refreshDailyTime,omitempty"` // "HH:MM" 24h, e.g. "03:00"
}

// ConnectivityCheckConfig holds per-tunnel connectivity check settings.
type ConnectivityCheckConfig struct {
	Method     string `json:"method"`               // "http" (default), "ping", "handshake", "disabled"
	PingTarget string `json:"pingTarget,omitempty"` // IP address for ping method
}

// AWGTunnel represents AmneziaWG tunnel metadata.
type AWGTunnel struct {
	ID                 string                   `json:"id"`
	Name               string                   `json:"name"`
	Type               string                   `json:"type,omitempty"` // "awg"
	Enabled            bool                     `json:"enabled"`
	DefaultRoute       bool                     `json:"defaultRoute"`                 // Create NDMS default route (ip route default OpkgTunX)
	DefaultRouteSet    bool                     `json:"defaultRouteSet,omitempty"`    // Migration sentinel: false = field never saved, default to true
	ISPInterface       string                   `json:"ispInterface,omitempty"`       // Override ISP interface for endpoint route (empty = auto-detect)
	ISPInterfaceLabel  string                   `json:"ispInterfaceLabel,omitempty"`  // Human-readable name for UI display
	ResolvedEndpointIP string                   `json:"resolvedEndpointIP,omitempty"` // Persisted resolved endpoint IP for reliable cleanup
	ActiveWAN          string                   `json:"activeWAN,omitempty"`          // Persisted resolved WAN for WAN event matching
	StartedAt          string                   `json:"startedAt,omitempty"`          // RFC3339 timestamp of last successful start
	Backend            string                   `json:"backend,omitempty"`            // "nativewg" | "kernel" | "wdtt-raw" | "" (legacy=kernel)
	FreeTurnClientID   string                   `json:"freeTurnClientId,omitempty"`   // set when AWG tunnel is auto-created from freeturn:// import
	WdttClientID       string                   `json:"wdttClientId,omitempty"`       // set when AWG tunnel is auto-created from wdtt/qwdtt import
	RawKernelIface     string                   `json:"rawKernelIface,omitempty"`     // wdtt-raw: kernel TUN (e.g. wdttraw0 / opkgtun17)
	RawNdmsIface       string                   `json:"rawNdmsIface,omitempty"`       // wdtt-raw: NDMS OpkgTun name (e.g. OpkgTun17)
	NWGIndex           int                      `json:"nwgIndex"`                     // Wireguard{N} index, nativewg only (0 is valid!)
	CreatedAt          string                   `json:"createdAt"`
	Interface          AWGInterface             `json:"interface"`
	Peer               AWGPeer                  `json:"peer"`
	PingCheck          *TunnelPingCheck         `json:"pingCheck,omitempty"`
	ConnectivityCheck  *ConnectivityCheckConfig `json:"connectivityCheck,omitempty"`
}

// TunnelPingCheck contains per-tunnel ping check configuration.
type TunnelPingCheck struct {
	Enabled       bool   `json:"enabled"`
	Method        string `json:"method"`         // "icmp", "connect", "tls", "uri"
	Target        string `json:"target"`         // host to check
	Interval      int    `json:"interval"`       // check interval in seconds
	DeadInterval  int    `json:"deadInterval"`   // dead tunnel check interval (kernel only)
	FailThreshold int    `json:"failThreshold"`  // max fails before dead
	MinSuccess    int    `json:"minSuccess"`     // min successes to recover (nativewg, default 1)
	Timeout       int    `json:"timeout"`        // check timeout seconds (nativewg, default 5)
	Port          int    `json:"port,omitempty"` // port for connect/tls modes
	Restart       bool   `json:"restart"`        // restart tunnel on dead (nativewg)
}

// AWGObfuscation groups all AmneziaWG obfuscation parameters into a
// dedicated value type. Comparable via `==`, which lets diff helpers
// stay future-proof: when a new obfuscation field appears, every
// comparison automatically picks it up. Embedded into AWGInterface so
// JSON serialization stays flat (no schema migration).
type AWGObfuscation struct {
	Qlen int    `json:"qlen"`
	Jc   int    `json:"jc"`
	Jmin int    `json:"jmin"`
	Jmax int    `json:"jmax"`
	S1   int    `json:"s1"`
	S2   int    `json:"s2"`
	S3   int    `json:"s3"`
	S4   int    `json:"s4"`
	H1   string `json:"h1"`
	H2   string `json:"h2"`
	H3   string `json:"h3"`
	H4   string `json:"h4"`
	I1   string `json:"i1,omitempty"`
	I2   string `json:"i2,omitempty"`
	I3   string `json:"i3,omitempty"`
	I4   string `json:"i4,omitempty"`
	I5   string `json:"i5,omitempty"`
	// AWG 3.0 device parameters (AmneziaWG kernel module feat/awg3). All kept
	// as strings: HeaderProtectionKey is a base64 key; the timing/padding
	// params are int-or-"min-max" ranges (u16_range_t) applied via awg setconf.
	HeaderProtectionKey    string `json:"headerProtectionKey,omitempty"`
	ContentPaddingAddition string `json:"contentPaddingAddition,omitempty"`
	RekeyAfterTime         string `json:"rekeyAfterTime,omitempty"`
	RekeyTimeout           string `json:"rekeyTimeout,omitempty"`
	RejectAfterTime        string `json:"rejectAfterTime,omitempty"`
	KeepaliveTimeout       string `json:"keepaliveTimeout,omitempty"`
	MaxHandshakeAttempts   string `json:"maxHandshakeAttempts,omitempty"`
	// AWG 3.1 device flags. Booleans rather than the string ranges above: the
	// value is binary and off equals the device default, so omitempty alone
	// keeps an untouched tunnel byte-identical in the store file.
	//
	// RandomTrailers is not negotiated on the wire — a 3.0 peer drops the
	// lengthened handshakes on its length check without a word, so both ends
	// have to run 3.1.
	RandomTrailers bool `json:"randomTrailers,omitempty"`
	DisableCookies bool `json:"disableCookies,omitempty"`
}

// AWGInterface contains AmneziaWG interface configuration.
type AWGInterface struct {
	PrivateKey     string `json:"privateKey"`
	Address        string `json:"address"`
	MTU            int    `json:"mtu"`
	DNS            string `json:"dns,omitempty"` // Comma-separated DNS servers (e.g., "1.1.1.1, 8.8.8.8")
	AWGObfuscation        // embedded — JSON keys stay flat (qlen, jc, jmin, ..., i5)
}

// AWGPeer contains AmneziaWG peer configuration.
type AWGPeer struct {
	PublicKey           string    `json:"publicKey"`
	PresharedKey        string    `json:"presharedKey,omitempty"`
	Endpoint            string    `json:"endpoint"`
	AllowedIPs          []string  `json:"allowedIPs"`
	PersistentKeepalive Keepalive `json:"persistentKeepalive"`
}

// Keepalive — значение PersistentKeepalive в секундах. В AWG 3.0 оно стало
// диапазоном "min-max", из которого пир берёт случайное значение на каждый
// взвод таймера, поэтому хранить int больше нельзя.
//
// Туннели, сохранённые до 3.0, лежат в JSON числом, и одиночное значение
// пишется обратно числом же: файл не меняет форму, и откат на прошлую версию
// продолжает его читать. Строка появляется только у настоящего диапазона.
type Keepalive string

func (k Keepalive) String() string { return string(k) }

// IsZero — значение не задано или явно выключено.
func (k Keepalive) IsZero() bool { return k == "" || k == "0" }

// IsRange — задан диапазон, а не одиночное значение.
func (k Keepalive) IsRange() bool { return strings.Contains(string(k), "-") }

// Single возвращает одиночное значение. NativeWG и NDMS принимают только его.
func (k Keepalive) Single() (int, bool) {
	n, err := strconv.Atoi(string(k))
	if err != nil {
		return 0, false
	}
	return n, true
}

func (k *Keepalive) UnmarshalJSON(data []byte) error {
	var number int
	if err := json.Unmarshal(data, &number); err == nil {
		*k = Keepalive(strconv.Itoa(number))
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*k = Keepalive(value)
	return nil
}

func (k Keepalive) MarshalJSON() ([]byte, error) {
	if k.IsZero() {
		return json.Marshal(0)
	}
	if n, ok := k.Single(); ok {
		return json.Marshal(n)
	}
	return json.Marshal(string(k))
}
