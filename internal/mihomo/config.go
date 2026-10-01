package mihomo

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"gopkg.in/yaml.v3"
)

// Config represents the root of a Clash/Mihomo YAML configuration.
type Config struct {
	Mode          string                            `yaml:"mode"`
	LogLevel      string                            `yaml:"log-level"`
	IPv6          bool                              `yaml:"ipv6"`
	AllowLan      bool                              `yaml:"allow-lan"`
	ExternalCtl   string                            `yaml:"external-controller"`
	RoutingMark   int                               `yaml:"routing-mark,omitempty"`
	Profile       Profile                           `yaml:"profile"`
	DNS           DNS                               `yaml:"dns"`
	Sniffer       *Sniffer                          `yaml:"sniffer,omitempty"`
	GeodataMode   bool                              `yaml:"geodata-mode"`
	GeodataLoader string                            `yaml:"geodata-loader,omitempty"`
	Proxies       []Proxy                           `yaml:"proxies,omitempty"`
	ProxyGroups   []ProxyGroup                      `yaml:"proxy-groups,omitempty"`
	ProxyProvider map[string]map[string]interface{} `yaml:"proxy-providers,omitempty"`
	RuleProvider  map[string]map[string]interface{} `yaml:"rule-providers,omitempty"`
	Rules         []string                          `yaml:"rules,omitempty"`
	TProxyPort    int                               `yaml:"tproxy-port,omitempty"`
	RedirPort     int                               `yaml:"redir-port,omitempty"`
	Port          int                               `yaml:"port,omitempty"`
	SocksPort     int                               `yaml:"socks-port,omitempty"`
	MixedPort     int                               `yaml:"mixed-port,omitempty"`
	Listeners     []Listener                        `yaml:"listeners,omitempty"`
	Tun           *Tun                              `yaml:"tun,omitempty"`
}

type Profile struct {
	StoreSelected bool `yaml:"store-selected"`
	StoreFakeIP   bool `yaml:"store-fake-ip"`
}

type Tun struct {
	Enable              bool     `yaml:"enable"`
	Stack               string   `yaml:"stack"`
	Device              string   `yaml:"device,omitempty"`
	AutoRoute           bool     `yaml:"auto-route"`
	AutoRedirect        bool     `yaml:"auto-redirect"`
	StrictRoute         bool     `yaml:"strict-route"`
	AutoDetectInterface bool     `yaml:"auto-detect-interface"`
	DNSHijack           []string `yaml:"dns-hijack,omitempty"`
	Inet4Address        []string `yaml:"inet4-address,omitempty"`
	Inet6Address        []string `yaml:"inet6-address,omitempty"`
	MTU                 int      `yaml:"mtu,omitempty"`
}

type DNS struct {
	Enable           bool              `yaml:"enable"`
	Listen           string            `yaml:"listen,omitempty"`
	DefaultNS        []string          `yaml:"default-nameserver,omitempty"`
	Nameserver       []string          `yaml:"nameserver,omitempty"`
	Fallback         []string          `yaml:"fallback,omitempty"`
	NameserverPolicy map[string]string `yaml:"nameserver-policy,omitempty"`
	Enhanced         string            `yaml:"enhanced-mode,omitempty"`
	FakeIPRange      string            `yaml:"fake-ip-range,omitempty"`
}

type Sniffer struct {
	Enable              bool           `yaml:"enable"`
	ForceDNSMapping     bool           `yaml:"force-dns-mapping"`
	ParsePureIP         bool           `yaml:"parse-pure-ip"`
	OverrideDestination bool           `yaml:"override-destination"`
	Sniff               SniffProtocols `yaml:"sniff"`
}

type SniffProtocols struct {
	HTTP SniffProtocol `yaml:"HTTP"`
	TLS  SniffProtocol `yaml:"TLS"`
	QUIC SniffProtocol `yaml:"QUIC"`
}

type SniffProtocol struct {
	Ports               []any `yaml:"ports"`
	OverrideDestination *bool `yaml:"override-destination,omitempty"`
}

type Proxy map[string]interface{}

type ProxyGroup struct {
	Name                string   `yaml:"name"`
	Type                string   `yaml:"type"`
	Proxies             []string `yaml:"proxies,omitempty"`
	Use                 []string `yaml:"use,omitempty"`
	URL                 string   `yaml:"url,omitempty"`
	Interval            int      `yaml:"interval,omitempty"`
	Lazy                *bool    `yaml:"lazy,omitempty"`
	Strategy            string   `yaml:"strategy,omitempty"`
	Tolerance           int      `yaml:"tolerance,omitempty"`
	Timeout             int      `yaml:"timeout,omitempty"`
	MaxFailedTimes      int      `yaml:"max-failed-times,omitempty"`
	DisableUDP          bool     `yaml:"disable-udp,omitempty"`
	IncludeAll          bool     `yaml:"include-all,omitempty"`
	IncludeAllProxies   bool     `yaml:"include-all-proxies,omitempty"`
	IncludeAllProviders bool     `yaml:"include-all-providers,omitempty"`
	Filter              string   `yaml:"filter,omitempty"`
	ExcludeFilter       string   `yaml:"exclude-filter,omitempty"`
	InterfaceName       string   `yaml:"interface-name,omitempty"`
	ExcludeType         string   `yaml:"exclude-type,omitempty"`
	ExpectedStatus      string   `yaml:"expected-status,omitempty"`
	Hidden              bool     `yaml:"hidden,omitempty"`
	Icon                string   `yaml:"icon,omitempty"`
}

type Provider struct {
	Type     string `yaml:"type"`
	Behavior string `yaml:"behavior"`
	URL      string `yaml:"url,omitempty"`
	Path     string `yaml:"path"`
	Interval int    `yaml:"interval,omitempty"`
	Format   string `yaml:"format,omitempty"`
	Proxy    string `yaml:"proxy,omitempty"`
}

type Listener struct {
	Name   string `yaml:"name"`
	Type   string `yaml:"type"`
	Port   int    `yaml:"port"`
	Listen string `yaml:"listen"`
	Proxy  string `yaml:"proxy,omitempty"`
	UDP    bool   `yaml:"udp,omitempty"`
}

type DeviceProxyListener struct {
	ID               string
	Port             int
	SelectedOutbound string
	Enabled          bool
}

type Rule struct {
	DomainSuffix []string
	Domain       []string
	IPCIDR       []string
	SourceIPCIDR []string
	RuleSet      []string
	Action       string
	Outbound     string
}

type DNSServerSpec struct {
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port,omitempty"`
	Detour     string `json:"detour,omitempty"`
	SNI        string `json:"sni,omitempty"`
}

type DNSRuleSpec struct {
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	Geosite       []string `json:"geosite,omitempty"`
	RuleSet       []string `json:"rule_set,omitempty"`
	Server        string   `json:"server,omitempty"`
}

type AdaptiveEgressConfig struct {
	Enabled       bool   `json:"enabled"`
	Device        string `json:"device"`
	SelectedGroup string `json:"selectedGroup"`
}

type NativeResources struct {
	Proxies             []Proxy
	ProxyProviders      map[string]map[string]interface{}
	ProxyGroups         []ProxyGroup
	Listeners           []Listener
	Rules               []string
	RuleProviders       map[string]map[string]interface{}
	DNSServers          []DNSServerSpec
	DNSRules            []DNSRuleSpec
	GroupsAuthoritative bool
	RulesAuthoritative  bool
	AdaptiveEgress      *AdaptiveEgressConfig
}

func FormatMihomoDNSServer(srv DNSServerSpec) string {
	var entry string
	switch strings.ToLower(srv.Type) {
	case "https", "doh":
		if !strings.HasPrefix(srv.Server, "https://") {
			if srv.SNI != "" && srv.SNI != srv.Server {
				entry = fmt.Sprintf("https://%s/dns-query", srv.SNI)
			} else {
				entry = fmt.Sprintf("https://%s/dns-query", srv.Server)
			}
		} else {
			entry = srv.Server
		}
	case "tls", "dot":
		if !strings.HasPrefix(srv.Server, "tls://") {
			p := 853
			if srv.ServerPort > 0 {
				p = srv.ServerPort
			}
			if srv.SNI != "" && srv.SNI != srv.Server {
				entry = fmt.Sprintf("tls://%s:%d", srv.SNI, p)
			} else {
				entry = fmt.Sprintf("tls://%s:%d", srv.Server, p)
			}
		} else {
			entry = srv.Server
		}
	case "quic", "doq":
		p := 853
		if srv.ServerPort > 0 {
			p = srv.ServerPort
		}
		entry = fmt.Sprintf("quic://%s:%d", srv.Server, p)
	case "udp", "":
		if srv.ServerPort > 0 && srv.ServerPort != 53 {
			entry = fmt.Sprintf("%s:%d", srv.Server, srv.ServerPort)
		} else {
			entry = srv.Server
		}
	default:
		entry = srv.Server
	}
	if srv.Detour != "" && srv.Detour != "direct" && srv.Detour != "DIRECT" {
		entry += "#" + srv.Detour
	}
	return entry
}

// GenerateSidecarConfig builds a non-intercepting Mihomo instance used only
// to serve native proxy exports to loopback bridge listeners. It deliberately
// has no TProxy/redir/TUN or public local proxy ports, so sing-box can remain
// the transparent routing engine without blackholing ProxyN/t2sN consumers.
func GenerateSidecarConfig(native NativeResources) ([]byte, error) {
	rules := []string{"MATCH,DIRECT"}
	if native.AdaptiveEgress != nil && native.AdaptiveEgress.Enabled && native.AdaptiveEgress.SelectedGroup != "" {
		rules = []string{
			fmt.Sprintf("IN-TYPE,TUN,%s", native.AdaptiveEgress.SelectedGroup),
			"MATCH,DIRECT",
		}
	}
	cfg := Config{
		Mode:        "rule",
		LogLevel:    "info",
		IPv6:        false,
		AllowLan:    false,
		ExternalCtl: "127.0.0.1:9090",
		Profile:     Profile{StoreSelected: true, StoreFakeIP: false},
		DNS: DNS{
			Enable: true, DefaultNS: []string{"127.0.0.1", "77.88.8.8"}, Enhanced: "redir-host",
		},
		ProxyProvider: native.ProxyProviders,
		RuleProvider:  native.RuleProviders,
		Rules:         rules,
	}
	if native.AdaptiveEgress != nil && native.AdaptiveEgress.Enabled {
		dev := native.AdaptiveEgress.Device
		if dev == "" {
			dev = "awgsus0"
		}
		cfg.Tun = &Tun{
			Enable:              true,
			Stack:               "system",
			Device:              dev,
			AutoRoute:           false,
			AutoRedirect:        false,
			StrictRoute:         false,
			AutoDetectInterface: true,
			DNSHijack:           []string{},
			Inet4Address:        []string{"172.19.0.1/30"},
			MTU:                 1500,
		}
	}
	seen := make(map[string]struct{}, len(native.Proxies))
	for _, proxy := range native.Proxies {
		name, _ := proxy["name"].(string)
		proxyType, _ := proxy["type"].(string)
		if name == "" || proxyType == "" {
			return nil, fmt.Errorf("mihomo native proxy requires non-empty name and type")
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate mihomo proxy name %q", name)
		}
		seen[name] = struct{}{}
		cfg.Proxies = append(cfg.Proxies, proxy)
	}
	groupNames := make(map[string]bool, len(native.ProxyGroups))
	for _, group := range native.ProxyGroups {
		if group.Name == "" || group.Type == "" {
			return nil, fmt.Errorf("mihomo native proxy group requires non-empty name and type")
		}
		if groupNames[group.Name] {
			return nil, fmt.Errorf("duplicate mihomo proxy group %q", group.Name)
		}
		group.Type = mihomoProxyGroupType(group.Type)
		groupNames[group.Name] = true

		// In sidecar mode, only native proxies are present in cfg.Proxies.
		// Prune non-native or missing proxy members so sidecar validation passes.
		var sidecarProxies []string
		for _, member := range group.Proxies {
			norm := strings.TrimSpace(member)
			if strings.EqualFold(norm, "direct") {
				norm = "DIRECT"
			} else if strings.EqualFold(norm, "reject") || strings.EqualFold(norm, "block") {
				norm = "REJECT"
			}
			if _, ok := seen[norm]; ok || norm == "DIRECT" || norm == "REJECT" {
				sidecarProxies = append(sidecarProxies, norm)
			}
		}
		if len(sidecarProxies) == 0 && len(group.Use) == 0 &&
			!group.IncludeAll && !group.IncludeAllProxies && !group.IncludeAllProviders {
			sidecarProxies = []string{"DIRECT"}
		}
		group.Proxies = sidecarProxies

		cfg.ProxyGroups = append(cfg.ProxyGroups, group)
	}
	for _, listener := range native.Listeners {
		if listener.Name == "" || listener.Port < 1 || listener.Proxy == "" {
			return nil, fmt.Errorf("mihomo native listener requires name, port and proxy")
		}
		listener.Type = "mixed"
		listener.Listen = "127.0.0.1"
		listener.UDP = true
		cfg.Listeners = append(cfg.Listeners, listener)
	}

	if err := normalizeCompiledConfig(&cfg); err != nil {
		return nil, err
	}
	if err := validateCompiledConfig(&cfg); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&cfg); err != nil {
		return nil, fmt.Errorf("mihomo encode sidecar yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("mihomo close sidecar yaml: %w", err)
	}
	return buf.Bytes(), nil
}

// GenerateConfig maps abstract router settings, tunnels, and router rules to a Mihomo YAML byte slice.
func GenerateConfig(
	settings storage.SingboxRouterSettings,
	tunIface string,
	subProxies []map[string]any,
	rules []Rule,
	finalOutbound string,
	deviceProxies []DeviceProxyListener,
) ([]byte, error) {
	return GenerateConfigWithNative(settings, tunIface, subProxies, nil, rules, finalOutbound, deviceProxies)
}

// GenerateConfigWithNative adds already-Mihomo-shaped proxies without routing
// them through the legacy sing-box converter.
func GenerateConfigWithNative(
	settings storage.SingboxRouterSettings,
	tunIface string,
	subProxies []map[string]any,
	nativeProxies []Proxy,
	rules []Rule,
	finalOutbound string,
	deviceProxies []DeviceProxyListener,
) ([]byte, error) {
	return GenerateConfigWithResources(settings, tunIface, subProxies, NativeResources{Proxies: nativeProxies}, rules, finalOutbound, deviceProxies)
}

func GenerateConfigWithResources(
	settings storage.SingboxRouterSettings,
	tunIface string,
	subProxies []map[string]any,
	native NativeResources,
	rules []Rule,
	finalOutbound string,
	deviceProxies []DeviceProxyListener,
) ([]byte, error) {
	var nameservers []string
	var defaultNS []string
	var fallbacks []string

	for _, srv := range native.DNSServers {
		if srv.Server == "" && srv.Type != "local" {
			continue
		}
		formatted := FormatMihomoDNSServer(srv)
		if formatted == "" {
			continue
		}
		if srv.Tag == "dns-direct" || srv.Tag == "bootstrap" || srv.Detour == "" || srv.Detour == "direct" {
			nameservers = append(nameservers, formatted)
			if srv.Server != "" && !strings.Contains(srv.Server, "/") {
				defaultNS = append(defaultNS, srv.Server)
			}
		} else {
			fallbacks = append(fallbacks, formatted)
		}
	}

	if len(nameservers) == 0 {
		nameservers = []string{"127.0.0.1", "77.88.8.8"}
	}
	if len(defaultNS) == 0 {
		defaultNS = []string{"127.0.0.1", "77.88.8.8"}
	}
	// Note: do not inject 1.1.1.1/8.8.8.8 into fallbacks when none are configured.
	// Hardcoding foreign DoH servers breaks DNS resolution on cellular white-lists
	// and causes healthcheck timeouts for direct outbounds.

	dnsServerByTag := make(map[string]string)
	for _, srv := range native.DNSServers {
		dnsServerByTag[srv.Tag] = FormatMihomoDNSServer(srv)
	}

	nameserverPolicy := make(map[string]string)
	for _, r := range native.DNSRules {
		target, ok := dnsServerByTag[r.Server]
		if !ok || target == "" {
			continue
		}
		for _, g := range r.Geosite {
			nameserverPolicy["geosite:"+g] = target
		}
		for _, rs := range r.RuleSet {
			cleanRS := strings.TrimSpace(rs)
			if cleanRS == "" {
				continue
			}
			lower := strings.ToLower(cleanRS)
			if strings.HasPrefix(lower, "geoip-") || strings.HasPrefix(lower, "geoip:") ||
				strings.HasSuffix(lower, "-cidr") || strings.HasSuffix(lower, "_cidr") ||
				lower == "susanin" || strings.HasPrefix(lower, "susanin-") || strings.HasPrefix(lower, "susanin_") {
				continue
			}
			if rp, ok := native.RuleProviders[cleanRS]; ok {
				if b, ok := rp["behavior"].(string); ok && strings.EqualFold(b, "ipcidr") {
					continue
				}
				nameserverPolicy["rule-set:"+cleanRS] = target
				continue
			}
			if strings.HasPrefix(lower, "geosite-") {
				nameserverPolicy["geosite:"+strings.TrimPrefix(lower, "geosite-")] = target
			} else if strings.HasPrefix(lower, "geosite:") {
				nameserverPolicy["geosite:"+strings.TrimPrefix(lower, "geosite:")] = target
			} else {
				nameserverPolicy["geosite:"+lower] = target
			}
		}
		for _, d := range r.Domain {
			nameserverPolicy[d] = target
		}
		for _, ds := range r.DomainSuffix {
			nameserverPolicy["+."+ds] = target
		}
		for _, dk := range r.DomainKeyword {
			nameserverPolicy["domain:"+dk] = target
		}
	}

	// Auto-route DNS for all proxy routing rules to the tunnel DNS server when no explicit DNS rule exists
	var defaultTunnelDNS string
	for _, srv := range native.DNSServers {
		if srv.Detour != "" && srv.Detour != "direct" && srv.Detour != "DIRECT" {
			defaultTunnelDNS = FormatMihomoDNSServer(srv)
			break
		}
	}
	if defaultTunnelDNS == "" && len(fallbacks) > 0 {
		defaultTunnelDNS = fallbacks[0]
	}

	if defaultTunnelDNS != "" {
		if native.RulesAuthoritative && len(native.Rules) > 0 {
			for _, rLine := range native.Rules {
				parts := strings.Split(rLine, ",")
				if len(parts) < 3 {
					continue
				}
				ruleType := strings.ToUpper(strings.TrimSpace(parts[0]))
				payload := strings.TrimSpace(parts[1])
				targetOutbound := strings.TrimSpace(parts[2])
				if targetOutbound == "" || strings.EqualFold(targetOutbound, "DIRECT") || strings.EqualFold(targetOutbound, "REJECT") {
					continue
				}

				switch ruleType {
				case "GEOSITE":
					key := "geosite:" + strings.ToLower(payload)
					if _, exists := nameserverPolicy[key]; !exists {
						nameserverPolicy[key] = defaultTunnelDNS
					}
				case "DOMAIN-SUFFIX":
					key := "+." + strings.ToLower(payload)
					if _, exists := nameserverPolicy[key]; !exists {
						nameserverPolicy[key] = defaultTunnelDNS
					}
				case "DOMAIN":
					key := strings.ToLower(payload)
					if _, exists := nameserverPolicy[key]; !exists {
						nameserverPolicy[key] = defaultTunnelDNS
					}
				case "DOMAIN-KEYWORD":
					key := "domain:" + strings.ToLower(payload)
					if _, exists := nameserverPolicy[key]; !exists {
						nameserverPolicy[key] = defaultTunnelDNS
					}
				case "RULE-SET":
					clean := strings.TrimSpace(payload)
					lower := strings.ToLower(clean)
					if strings.HasPrefix(lower, "geoip-") || strings.HasPrefix(lower, "geoip:") ||
						strings.HasSuffix(lower, "-cidr") || strings.HasSuffix(lower, "_cidr") ||
						lower == "susanin" || strings.HasPrefix(lower, "susanin-") || strings.HasPrefix(lower, "susanin_") {
						continue
					}
					if rp, ok := native.RuleProviders[clean]; ok {
						if b, ok := rp["behavior"].(string); ok && strings.EqualFold(b, "ipcidr") {
							continue
						}
						key := "rule-set:" + clean
						if _, exists := nameserverPolicy[key]; !exists {
							nameserverPolicy[key] = defaultTunnelDNS
						}
						continue
					}
					key := "geosite:" + strings.TrimPrefix(strings.TrimPrefix(lower, "geosite-"), "geosite:")
					if _, exists := nameserverPolicy[key]; !exists {
						nameserverPolicy[key] = defaultTunnelDNS
					}
				}
			}
		} else {
			for _, r := range rules {
				if r.Outbound == "" || strings.EqualFold(r.Outbound, "direct") || strings.EqualFold(r.Action, "reject") {
					continue
				}
				for _, rs := range r.RuleSet {
					clean := strings.TrimSpace(rs)
					lower := strings.ToLower(clean)
					if strings.HasPrefix(lower, "geoip-") || strings.HasPrefix(lower, "geoip:") ||
						strings.HasSuffix(lower, "-cidr") || strings.HasSuffix(lower, "_cidr") ||
						lower == "susanin" || strings.HasPrefix(lower, "susanin-") || strings.HasPrefix(lower, "susanin_") {
						continue
					}
					if rp, ok := native.RuleProviders[clean]; ok {
						if b, ok := rp["behavior"].(string); ok && strings.EqualFold(b, "ipcidr") {
							continue
						}
						key := "rule-set:" + clean
						if _, exists := nameserverPolicy[key]; !exists {
							nameserverPolicy[key] = defaultTunnelDNS
						}
						continue
					}
					key := "geosite:" + strings.TrimPrefix(strings.TrimPrefix(lower, "geosite-"), "geosite:")
					if _, exists := nameserverPolicy[key]; !exists {
						nameserverPolicy[key] = defaultTunnelDNS
					}
				}
				for _, d := range r.Domain {
					key := strings.ToLower(d)
					if _, exists := nameserverPolicy[key]; !exists {
						nameserverPolicy[key] = defaultTunnelDNS
					}
				}
				for _, ds := range r.DomainSuffix {
					key := "+." + strings.ToLower(ds)
					if _, exists := nameserverPolicy[key]; !exists {
						nameserverPolicy[key] = defaultTunnelDNS
					}
				}
			}
		}
		if settings.KeeneticCloudTunnel && strings.TrimSpace(settings.KeeneticCloudOutbound) != "" {
			cloudDomains := []string{
				"keenetic.com", "keenetic.io", "keenetic.net", "keenetic.ru",
				"keenetic.pro", "keenetic.link", "keenetic.name", "keenetic.cloud",
				"netcraze.io", "netcraze.net", "netcraze.pro", "netcraze.ru", "netcraze.com", "netcraze.cloud",
				"crazedns.ru", "crazedns.com", "crazedns.net",
				"omni.ru", "knt9.xyz",
			}
			if defaultTunnelDNS != "" {
				for _, d := range cloudDomains {
					nameserverPolicy["+."+d] = defaultTunnelDNS
				}
			}
			nameserverPolicy["my.keenetic.net"] = "127.0.0.1"
			nameserverPolicy["my.netcraze.net"] = "127.0.0.1"
		}
	}

	trafficMode := "rule"
	if settings.MihomoTrafficMode != "" {
		trafficMode = settings.MihomoTrafficMode
	}

	cfg := Config{
		Mode:        trafficMode,
		LogLevel:    "info",
		AllowLan:    true,
		ExternalCtl: "127.0.0.1:9090", // Standard clash API port
		RoutingMark: 666,
		Profile:     Profile{StoreSelected: true, StoreFakeIP: true},
		DNS: DNS{
			Enable:           true,
			Listen:           "127.0.0.1:1053",
			DefaultNS:        defaultNS,
			Nameserver:       nameservers,
			Fallback:         fallbacks,
			NameserverPolicy: nameserverPolicy,
		},
		GeodataMode:   true,
		GeodataLoader: "standard",
		// The top-level listener is intentional: tproxy-port provides the
		// TCP/UDP transparent socket that the shared AWGM netfilter contract
		// expects on 51271 across all packaged Mihomo architectures.
		TProxyPort: 51271,
		RedirPort:  51272, // Reuse sing-box redirect port
		Port:       settings.MihomoHTTPPort,
		SocksPort:  settings.MihomoSOCKSPort,
		MixedPort:  settings.MihomoMixedPort,
	}
	cfg.ProxyProvider = native.ProxyProviders
	cfg.RuleProvider = native.RuleProviders

	if settings.SusaninEnabled {
		if cfg.RuleProvider == nil {
			cfg.RuleProvider = make(map[string]map[string]interface{})
		} else {
			clone := make(map[string]map[string]interface{}, len(cfg.RuleProvider)+1)
			for k, v := range cfg.RuleProvider {
				clone[k] = v
			}
			cfg.RuleProvider = clone
		}
		cfg.RuleProvider["susanin"] = map[string]interface{}{
			"type":     "file",
			"behavior": "classical",
			"format":   "yaml",
			"path":     "./rules/susanin.yaml",
			"interval": 86400,
		}
	}

	if settings.SnifferEnabled {
		overrideProto := true
		cfg.Sniffer = &Sniffer{
			Enable:              true,
			ForceDNSMapping:     true,
			ParsePureIP:         true,
			OverrideDestination: false,
			Sniff: SniffProtocols{
				HTTP: SniffProtocol{Ports: []any{80, "8080-8880"}, OverrideDestination: &overrideProto},
				TLS:  SniffProtocol{Ports: []any{443, 8443}, OverrideDestination: &overrideProto},
				QUIC: SniffProtocol{Ports: []any{443, 8443}, OverrideDestination: &overrideProto},
			},
		}
	} else {
		cfg.Sniffer = nil
	}

	cfg.IPv6 = false

	if native.AdaptiveEgress != nil && native.AdaptiveEgress.Enabled {
		dev := native.AdaptiveEgress.Device
		if dev == "" {
			dev = "awgsus0"
		}
		cfg.Tun = &Tun{
			Enable:              true,
			Stack:               "system",
			Device:              dev,
			AutoRoute:           false,
			AutoRedirect:        false,
			StrictRoute:         false,
			AutoDetectInterface: true,
			DNSHijack:           []string{},
			Inet4Address:        []string{"172.19.0.1/30"},
			MTU:                 1500,
		}
		if settings.RoutingMode == "fakeip-tun" || settings.RoutingMode == "policy-tun" {
			cfg.TProxyPort = 0
			cfg.RedirPort = 0
		}
		cfg.DNS.Enhanced = "redir-host"
	} else if settings.RoutingMode == "fakeip-tun" || settings.RoutingMode == "policy-tun" {
		cfg.DNS.Enhanced = "fake-ip"
		cfg.DNS.FakeIPRange = settings.FakeIPPool4 // E.g. "198.18.0.0/15"
		cfg.TProxyPort = 0
		cfg.RedirPort = 0

		cfg.Tun = &Tun{
			Enable:              true,
			Stack:               "gvisor",
			Device:              tunIface,
			AutoRoute:           false,
			AutoRedirect:        false,
			StrictRoute:         false,
			AutoDetectInterface: false,
			DNSHijack:           []string{"any:53"},
		}
	} else {
		cfg.DNS.Enhanced = "redir-host"
	}
	groupIndex := make(map[string]int)
	upsertGroup := func(group ProxyGroup) {
		if index, exists := groupIndex[group.Name]; exists {
			cfg.ProxyGroups[index] = group
			return
		}
		groupIndex[group.Name] = len(cfg.ProxyGroups)
		cfg.ProxyGroups = append(cfg.ProxyGroups, group)
	}

	seenProxyNames := make(map[string]struct{}, len(subProxies)+len(native.Proxies))

	// 1. Native proxies take precedence and are authoritative in Mihomo mode
	for _, proxy := range native.Proxies {
		name, _ := proxy["name"].(string)
		pType, _ := proxy["type"].(string)
		if name == "" || pType == "" {
			return nil, fmt.Errorf("mihomo native proxy requires non-empty name and type")
		}
		if _, exists := seenProxyNames[name]; exists {
			return nil, fmt.Errorf("duplicate mihomo proxy name %q", name)
		}
		seenProxyNames[name] = struct{}{}
		cfg.Proxies = append(cfg.Proxies, proxy)
	}

	// 2. Map subscription proxies and composite groups (skipping duplicates & loopback bridges)
	for _, rawOb := range subProxies {
		pType, _ := rawOb["type"].(string)
		if pType == "selector" || pType == "urltest" || pType == "loadbalance" {
			if pg := ConvertSingboxToMihomoProxyGroup(rawOb); pg != nil {
				upsertGroup(*pg)
			}
		} else {
			// Skip loopback socks bridges (e.g. sing-box sidecar listeners pointing to 127.0.0.1)
			if srv, _ := rawOb["server"].(string); srv == "127.0.0.1" || srv == "localhost" {
				continue
			}
			p, err := convertSingboxToMihomoProxy(rawOb)
			if err != nil {
				return nil, err
			}
			if p != nil {
				name, _ := p["name"].(string)
				if name != "" {
					if _, exists := seenProxyNames[name]; exists {
						// Native proxy or earlier proxy takes precedence, avoid collision
						continue
					}
					seenProxyNames[name] = struct{}{}
				}
				cfg.Proxies = append(cfg.Proxies, p)
			}
		}
	}
	for i := range cfg.Proxies {
		if _, ok := cfg.Proxies[i]["routing-mark"]; !ok && cfg.RoutingMark != 0 {
			cfg.Proxies[i]["routing-mark"] = cfg.RoutingMark
		}
	}
	// Legacy shared groups remain a compatibility fallback while an existing
	// installation is moved to the native Mihomo model. Native definitions are
	// applied afterwards and therefore win by name without deleting the legacy
	// source of truth during an upgrade.
	if !native.GroupsAuthoritative {
		for _, pg := range settings.ProxyGroups {
			yamlGroup := ProxyGroup{
				Name:     pg.Name,
				Type:     mihomoProxyGroupType(pg.Type),
				URL:      pg.URL,
				Interval: pg.Interval,
				Lazy:     &pg.Lazy,
			}
			for _, member := range pg.Proxies {
				if member == "direct" {
					member = "DIRECT"
				} else if member == "block" {
					member = "REJECT"
				}
				yamlGroup.Proxies = append(yamlGroup.Proxies, member)
			}
			// Router storage is the authoritative definition when the same group
			// also appears as a serialized selector outbound.
			upsertGroup(yamlGroup)
		}
	}
	allGroupNames := make(map[string]bool)
	for _, g := range settings.ProxyGroups {
		if g.Name != "" {
			allGroupNames[g.Name] = true
		}
	}
	for _, g := range native.ProxyGroups {
		if g.Name != "" {
			allGroupNames[g.Name] = true
		}
	}
	for _, g := range cfg.ProxyGroups {
		if g.Name != "" {
			allGroupNames[g.Name] = true
		}
	}
	for _, group := range native.ProxyGroups {
		if group.Name == "" || group.Type == "" {
			return nil, fmt.Errorf("mihomo native proxy group requires non-empty name and type")
		}
		var validProxies []string
		for _, member := range group.Proxies {
			norm := strings.TrimSpace(member)
			if strings.EqualFold(norm, "direct") {
				norm = "DIRECT"
			} else if strings.EqualFold(norm, "reject") || strings.EqualFold(norm, "block") {
				norm = "REJECT"
			}
			if _, exists := seenProxyNames[norm]; exists || allGroupNames[norm] || norm == "DIRECT" || norm == "REJECT" {
				validProxies = append(validProxies, norm)
			}
		}
		if len(validProxies) == 0 && len(group.Use) == 0 &&
			!group.IncludeAll && !group.IncludeAllProxies && !group.IncludeAllProviders {
			validProxies = []string{"DIRECT"}
		}
		group.Proxies = validProxies
		upsertGroup(group)
	}
	for _, listener := range native.Listeners {
		if listener.Name == "" || listener.Port < 1 || listener.Proxy == "" {
			return nil, fmt.Errorf("mihomo native listener requires name, port and proxy")
		}
		listener.Type = "mixed"
		listener.Listen = "127.0.0.1"
		listener.UDP = true
		cfg.Listeners = append(cfg.Listeners, listener)
	}

	// Map device proxy instances to Clash listeners
	for _, dp := range deviceProxies {
		if !dp.Enabled {
			continue
		}
		outbound := dp.SelectedOutbound
		if outbound == "direct" {
			outbound = "DIRECT"
		} else if outbound == "block" {
			outbound = "REJECT"
		}

		cfg.Listeners = append(cfg.Listeners, Listener{
			Name:   "device-proxy-" + dp.ID,
			Type:   "mixed",
			Port:   dp.Port,
			Listen: "0.0.0.0",
			Proxy:  outbound,
		})
	}

	// Keenetic Cloud Tunnel rules
	if settings.KeeneticCloudTunnel && strings.TrimSpace(settings.KeeneticCloudOutbound) != "" {
		targetOutbound := strings.TrimSpace(settings.KeeneticCloudOutbound)
		if strings.EqualFold(targetOutbound, "direct") {
			targetOutbound = "DIRECT"
		} else if strings.EqualFold(targetOutbound, "block") {
			targetOutbound = "REJECT"
		}
		cfg.Rules = append(cfg.Rules,
			"DOMAIN,my.keenetic.net,DIRECT",
			"DOMAIN,my.netcraze.net,DIRECT",
		)
		cloudDomains := []string{
			"keenetic.com", "keenetic.io", "keenetic.net", "keenetic.ru",
			"keenetic.pro", "keenetic.link", "keenetic.name", "keenetic.cloud",
			"netcraze.io", "netcraze.net", "netcraze.pro", "netcraze.ru", "netcraze.com", "netcraze.cloud",
			"crazedns.ru", "crazedns.com", "crazedns.net",
			"omni.ru", "knt9.xyz",
		}
		for _, d := range cloudDomains {
			cfg.Rules = append(cfg.Rules, "DOMAIN-SUFFIX,"+d+","+targetOutbound)
		}
		cloudCIDRs := []string{
			"185.162.93.0/24",
			"95.213.212.0/24",
			"95.213.181.0/24",
			"87.228.71.0/24",
			"91.92.241.0/24",
			"193.107.216.0/24",
			"178.250.154.0/24",
			"178.72.134.0/24",
			"85.198.119.0/24",
			"37.0.127.0/24",
			"5.35.2.0/24",
			"84.38.177.0/24",
			"49.12.59.0/24",
			"167.233.7.0/24",
			"82.202.218.0/24",
			"82.202.0.0/16",
		}
		allCIDRs := append(slices.Clone(cloudCIDRs), settings.DynamicCloudCIDRs...)
		for _, cidr := range allCIDRs {
			cfg.Rules = append(cfg.Rules, "IP-CIDR,"+cidr+","+targetOutbound+",no-resolve")
		}
		cloudPorts := []string{"9", "3478", "3479", "4044", "5683", "5684"}
		for _, p := range cloudPorts {
			cfg.Rules = append(cfg.Rules, "DST-PORT,"+p+","+targetOutbound)
		}
	}

	// Native rules have the highest priority. Until ownership is explicitly
	// migrated, legacy rules remain below them as a safe fallback. This lets a
	// user build and test the Mihomo ruleset incrementally without the first
	// saved rule unexpectedly removing all existing routing.
	if native.RulesAuthoritative {
		cfg.Rules = append(cfg.Rules, native.Rules...)
	} else {
		cfg.Rules = append(cfg.Rules, native.Rules...)
		for _, r := range rules {
			for _, clashRule := range ConvertSingboxRuleToMihomo(r) {
				cfg.Rules = append(cfg.Rules, clashRule)
			}
		}
	}

	// Map final routing fallback
	if finalOutbound != "" {
		outbound := finalOutbound
		if outbound == "direct" {
			outbound = "DIRECT"
		} else if outbound == "block" {
			outbound = "REJECT"
		}
		cfg.Rules = append(cfg.Rules, "MATCH,"+outbound)
	} else {
		cfg.Rules = append(cfg.Rules, "MATCH,DIRECT")
	}

	if settings.SusaninEnabled {
		targetOutbound := strings.TrimSpace(settings.SusaninOutbound)
		if targetOutbound == "" {
			if len(cfg.ProxyGroups) > 0 {
				targetOutbound = cfg.ProxyGroups[0].Name
			} else if finalOutbound != "" {
				targetOutbound = finalOutbound
			} else {
				targetOutbound = "DIRECT"
			}
		}
		if strings.EqualFold(targetOutbound, "direct") {
			targetOutbound = "DIRECT"
		} else if strings.EqualFold(targetOutbound, "block") {
			targetOutbound = "REJECT"
		}
		susaninRule := fmt.Sprintf("RULE-SET,susanin,%s", targetOutbound)
		cfg.Rules = append([]string{susaninRule}, cfg.Rules...)
	}
	if native.AdaptiveEgress != nil && native.AdaptiveEgress.Enabled && native.AdaptiveEgress.SelectedGroup != "" {
		tunRule := fmt.Sprintf("IN-TYPE,TUN,%s", native.AdaptiveEgress.SelectedGroup)
		cfg.Rules = append([]string{tunRule}, cfg.Rules...)
	}

	if err := normalizeCompiledConfig(&cfg); err != nil {
		return nil, err
	}
	if err := validateCompiledConfig(&cfg); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&cfg); err != nil {
		return nil, fmt.Errorf("mihomo encode yaml: %w", err)
	}
	enc.Close()
	return buf.Bytes(), nil
}

func tokenizeRule(s string) []string {
	var tokens []string
	var cur strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
			cur.WriteRune(r)
		case ')':
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		case ',':
			if depth == 0 {
				tokens = append(tokens, strings.TrimSpace(cur.String()))
				cur.Reset()
			} else {
				cur.WriteRune(r)
			}
		default:
			cur.WriteRune(r)
		}
	}
	tokens = append(tokens, strings.TrimSpace(cur.String()))
	return tokens
}

func stripMatchingParens(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return s, false
	}
	depth := 0
	for i, r := range s {
		if r == '(' {
			depth++
		} else if r == ')' {
			depth--
			if depth == 0 && i < len(s)-1 {
				return s, false
			}
		}
	}
	if depth == 0 {
		return strings.TrimSpace(s[1 : len(s)-1]), true
	}
	return s, false
}

func parseCompositePayload(ruleType, payload string, depth int) error {
	if depth > 3 {
		return CompileErrorf("rule", ruleType, payload, "composite rule nesting depth exceeds limit (max 3)")
	}
	payload = strings.TrimSpace(payload)
	inner, ok := stripMatchingParens(payload)
	if !ok {
		return CompileErrorf("rule", ruleType, payload, "malformed %s rule payload, expected parentheses enclosing sub-rules", ruleType)
	}

	var children []string
	var cur strings.Builder
	depthCount := 0
	for _, r := range inner {
		switch r {
		case '(':
			depthCount++
			cur.WriteRune(r)
		case ')':
			if depthCount > 0 {
				depthCount--
			}
			cur.WriteRune(r)
		case ',':
			if depthCount == 0 {
				item := strings.TrimSpace(cur.String())
				if item != "" {
					children = append(children, item)
				}
				cur.Reset()
			} else {
				cur.WriteRune(r)
			}
		default:
			cur.WriteRune(r)
		}
	}
	if depthCount != 0 {
		return CompileErrorf("rule", ruleType, payload, "unbalanced parentheses in composite rule payload")
	}
	if item := strings.TrimSpace(cur.String()); item != "" {
		children = append(children, item)
	}

	if len(children) > 32 {
		return CompileErrorf("rule", ruleType, payload, "composite rule child count %d exceeds limit (max 32)", len(children))
	}
	if ruleType == "NOT" {
		if len(children) != 1 {
			return CompileErrorf("rule", "NOT", payload, "NOT rule requires exactly 1 child rule, got %d", len(children))
		}
	} else if ruleType == "AND" || ruleType == "OR" {
		if len(children) < 2 {
			return CompileErrorf("rule", ruleType, payload, "%s rule requires at least 2 child rules, got %d", ruleType, len(children))
		}
	}

	for _, child := range children {
		childStr, _ := stripMatchingParens(child)
		childStr = strings.TrimSpace(childStr)
		if childStr == "" {
			return CompileErrorf("rule", ruleType, payload, "empty child rule in composite rule")
		}
		childTokens := tokenizeRule(childStr)
		if len(childTokens) == 0 {
			return CompileErrorf("rule", ruleType, payload, "empty child rule in composite rule")
		}
		childType := strings.ToUpper(childTokens[0])
		if childType == "MATCH" {
			return CompileErrorf("rule", ruleType, payload, "MATCH rule is not allowed inside composite rules")
		}
		if childType == "SUB-RULE" {
			return CompileErrorf("rule", "SUB-RULE", payload, "SUB-RULE is not supported in AWG Manager")
		}
		if childType == "AND" || childType == "OR" || childType == "NOT" {
			if len(childTokens) != 2 {
				return CompileErrorf("rule", childType, childStr, "composite child rule %s must have exactly 2 fields (type and payload), got %d", childType, len(childTokens))
			}
			if err := parseCompositePayload(childType, childTokens[1], depth+1); err != nil {
				return err
			}
		} else {
			spec, ok := LookupRuleSpec(childType)
			if !ok {
				return CompileErrorf("rule", childType, childStr, "unsupported rule type %q in composite rule", childType)
			}
			expectedMin := spec.MinFields - 1
			expectedMax := spec.MaxFields - 1
			if len(childTokens) < expectedMin || len(childTokens) > expectedMax {
				return CompileErrorf("rule", childType, childStr, "child rule %s has invalid number of fields (expected between %d and %d, got %d)", childType, expectedMin, expectedMax, len(childTokens))
			}
			if spec.RequiresPayload {
				if len(childTokens) < 2 || strings.TrimSpace(childTokens[1]) == "" {
					return CompileErrorf("rule", childType, childStr, "child rule %s requires non-empty payload", childType)
				}
			}
			if len(childTokens) == expectedMax && spec.MaxFields > spec.MinFields {
				mod := strings.TrimSpace(childTokens[len(childTokens)-1])
				validMod := false
				for _, allowed := range spec.AllowedModifiers {
					if strings.EqualFold(mod, allowed) {
						validMod = true
						break
					}
				}
				if !validMod {
					return CompileErrorf("rule", childType, childStr, "child rule %s has unsupported modifier %q", childType, mod)
				}
			}
		}
	}
	return nil
}

type socketCap struct {
	tcp bool
	udp bool
}

func listenerSocketCap(l Listener) (socketCap, error) {
	t := strings.ToLower(strings.TrimSpace(l.Type))
	switch t {
	case "mixed", "tproxy", "socks", "tunnel":
		return socketCap{tcp: true, udp: true}, nil
	case "redir", "http":
		return socketCap{tcp: true, udp: l.UDP}, nil
	case "":
		return socketCap{}, CompileErrorf("listener", l.Name, "", "listener %q has empty type", l.Name)
	default:
		return socketCap{}, CompileErrorf("listener", l.Name, "", "listener %q has unsupported type %q", l.Name, l.Type)
	}
}

func addressesOverlap(addr1, addr2 string) bool {
	addr1 = strings.TrimSpace(addr1)
	addr2 = strings.TrimSpace(addr2)
	isWildcard := func(a string) bool {
		return a == "" || a == "0.0.0.0" || a == "::" || a == "[::]" || a == "*"
	}
	if isWildcard(addr1) || isWildcard(addr2) {
		return true
	}
	return strings.EqualFold(addr1, addr2)
}

func protocolsOverlap(cap1, cap2 socketCap) bool {
	return (cap1.tcp && cap2.tcp) || (cap1.udp && cap2.udp)
}

func normalizeCompiledConfig(cfg *Config) error {
	for i := range cfg.ProxyGroups {
		cfg.ProxyGroups[i].Type = mihomoProxyGroupType(cfg.ProxyGroups[i].Type)
		for j, member := range cfg.ProxyGroups[i].Proxies {
			if strings.EqualFold(member, "direct") {
				cfg.ProxyGroups[i].Proxies[j] = "DIRECT"
			} else if strings.EqualFold(member, "block") || strings.EqualFold(member, "reject") {
				cfg.ProxyGroups[i].Proxies[j] = "REJECT"
			}
		}
	}

	for i := range cfg.Listeners {
		if strings.EqualFold(cfg.Listeners[i].Proxy, "direct") {
			cfg.Listeners[i].Proxy = "DIRECT"
		} else if strings.EqualFold(cfg.Listeners[i].Proxy, "block") || strings.EqualFold(cfg.Listeners[i].Proxy, "reject") {
			cfg.Listeners[i].Proxy = "REJECT"
		}
	}

	for _, pData := range cfg.ProxyProvider {
		if pVal, exists := pData["proxy"]; exists {
			if pStr, ok := pVal.(string); ok {
				if strings.EqualFold(pStr, "direct") {
					pData["proxy"] = "DIRECT"
				} else if strings.EqualFold(pStr, "reject") || strings.EqualFold(pStr, "block") {
					pData["proxy"] = "REJECT"
				}
			}
		}
	}

	for _, rpData := range cfg.RuleProvider {
		if rpVal, exists := rpData["proxy"]; exists {
			if rpStr, ok := rpVal.(string); ok {
				if strings.EqualFold(rpStr, "direct") {
					rpData["proxy"] = "DIRECT"
				} else if strings.EqualFold(rpStr, "reject") || strings.EqualFold(rpStr, "block") {
					rpData["proxy"] = "REJECT"
				}
			}
		}
	}

	for i, ruleStr := range cfg.Rules {
		parts := tokenizeRule(ruleStr)
		if len(parts) >= 3 && strings.EqualFold(parts[0], "RULE-SET") {
			name := strings.TrimSpace(parts[1])
			if _, exists := cfg.RuleProvider[name]; !exists {
				if strings.HasPrefix(name, "geosite-") {
					parts[0] = "GEOSITE"
					parts[1] = strings.TrimPrefix(name, "geosite-")
					cfg.Rules[i] = strings.Join(parts, ",")
				} else if strings.HasPrefix(name, "geosite:") {
					parts[0] = "GEOSITE"
					parts[1] = strings.TrimPrefix(name, "geosite:")
					cfg.Rules[i] = strings.Join(parts, ",")
				} else if strings.HasPrefix(name, "geoip-") {
					parts[0] = "GEOIP"
					parts[1] = strings.TrimPrefix(name, "geoip-")
					hasNoResolve := false
					for _, p := range parts[2:] {
						if strings.TrimSpace(p) == "no-resolve" {
							hasNoResolve = true
							break
						}
					}
					if !hasNoResolve {
						parts = append(parts, "no-resolve")
					}
					cfg.Rules[i] = strings.Join(parts, ",")
				} else if strings.HasPrefix(name, "geoip:") {
					parts[0] = "GEOIP"
					parts[1] = strings.TrimPrefix(name, "geoip:")
					hasNoResolve := false
					for _, p := range parts[2:] {
						if strings.TrimSpace(p) == "no-resolve" {
							hasNoResolve = true
							break
						}
					}
					if !hasNoResolve {
						parts = append(parts, "no-resolve")
					}
					cfg.Rules[i] = strings.Join(parts, ",")
				}
			}
		}
	}

	validDNSTarget := func(target string) bool {
		switch strings.ToUpper(target) {
		case "DIRECT", "REJECT", "GLOBAL", "PASS", "COMPATIBLE", "RULES":
			return true
		}
		for _, p := range cfg.Proxies {
			if name, _ := p["name"].(string); name == target {
				return true
			}
		}
		for _, g := range cfg.ProxyGroups {
			if g.Name == target {
				return true
			}
		}
		return false
	}

	for i, entry := range cfg.DNS.DefaultNS {
		norm, err := NormalizeAndValidateDNSServerURI(entry, validDNSTarget)
		if err != nil {
			return CompileErrorf("dns", "default-nameserver", entry, "invalid default-nameserver URI: %v", err)
		}
		cfg.DNS.DefaultNS[i] = norm
	}

	for i, entry := range cfg.DNS.Nameserver {
		norm, err := NormalizeAndValidateDNSServerURI(entry, validDNSTarget)
		if err != nil {
			return CompileErrorf("dns", "nameserver", entry, "invalid nameserver URI: %v", err)
		}
		cfg.DNS.Nameserver[i] = norm
	}

	for i, entry := range cfg.DNS.Fallback {
		norm, err := NormalizeAndValidateDNSServerURI(entry, validDNSTarget)
		if err != nil {
			return CompileErrorf("dns", "fallback", entry, "invalid fallback URI: %v", err)
		}
		cfg.DNS.Fallback[i] = norm
	}

	for k, entry := range cfg.DNS.NameserverPolicy {
		norm, err := NormalizeAndValidateDNSServerURI(entry, validDNSTarget)
		if err != nil {
			return CompileErrorf("dns", "nameserver-policy", entry, "invalid nameserver-policy URI for domain %q: %v", k, err)
		}
		cfg.DNS.NameserverPolicy[k] = norm
	}

	return nil
}

func validateCompiledConfig(cfg *Config) error {
	proxyNames := make(map[string]bool, len(cfg.Proxies))
	for _, p := range cfg.Proxies {
		name, _ := p["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return CompileErrorf("proxy", "", "", "proxy has empty name")
		}
		if proxyNames[name] {
			return CompileErrorf("proxy", name, "", "duplicate proxy name %q", name)
		}
		proxyNames[name] = true
	}

	providerNames := make(map[string]bool, len(cfg.ProxyProvider))
	for pName := range cfg.ProxyProvider {
		pName = strings.TrimSpace(pName)
		if pName == "" {
			return CompileErrorf("proxy-provider", "", "", "proxy-provider has empty name")
		}
		providerNames[pName] = true
	}

	ruleProviderNames := make(map[string]bool, len(cfg.RuleProvider))
	for rpName := range cfg.RuleProvider {
		rpName = strings.TrimSpace(rpName)
		if rpName == "" {
			return CompileErrorf("rule-provider", "", "", "rule-provider has empty name")
		}
		ruleProviderNames[rpName] = true
	}

	groupNames := make(map[string]bool, len(cfg.ProxyGroups))
	for _, g := range cfg.ProxyGroups {
		gName := strings.TrimSpace(g.Name)
		if gName == "" {
			return CompileErrorf("proxy-group", "", "", "proxy group has empty name")
		}
		if groupNames[gName] {
			return CompileErrorf("proxy-group", gName, "", "duplicate proxy group name %q", gName)
		}
		if proxyNames[gName] {
			return CompileErrorf("proxy-group", gName, "", "proxy group name %q conflicts with existing proxy name", gName)
		}
		groupNames[gName] = true
	}

	validRuleTarget := func(target string) bool {
		switch target {
		case "DIRECT", "REJECT", "GLOBAL", "PASS", "COMPATIBLE":
			return true
		}
		return proxyNames[target] || groupNames[target]
	}

	validGroupMember := func(target string) bool {
		switch target {
		case "DIRECT", "REJECT":
			return true
		}
		return proxyNames[target] || groupNames[target]
	}

	validListenerTarget := func(target string) bool {
		switch target {
		case "DIRECT", "REJECT":
			return true
		}
		return proxyNames[target] || groupNames[target]
	}

	// Provider proxy validation
	for pName, pData := range cfg.ProxyProvider {
		if pVal, exists := pData["proxy"]; exists {
			pStr, ok := pVal.(string)
			if !ok {
				return CompileErrorf("proxy-provider", pName, "", "proxy-provider %q property 'proxy' must be a string", pName)
			}
			pStr = strings.TrimSpace(pStr)
			if pStr != "" && !validRuleTarget(pStr) {
				return CompileErrorf("proxy-provider", pName, "", "proxy-provider %q references unknown proxy target %q", pName, pStr)
			}
		}
	}

	for rpName, rpData := range cfg.RuleProvider {
		if rpVal, exists := rpData["proxy"]; exists {
			rpStr, ok := rpVal.(string)
			if !ok {
				return CompileErrorf("rule-provider", rpName, "", "rule-provider %q property 'proxy' must be a string", rpName)
			}
			rpStr = strings.TrimSpace(rpStr)
			if rpStr != "" && !validRuleTarget(rpStr) {
				return CompileErrorf("rule-provider", rpName, "", "rule-provider %q references unknown proxy target %q", rpName, rpStr)
			}
		}
	}

	for _, g := range cfg.ProxyGroups {
		if len(g.Proxies) == 0 && len(g.Use) == 0 && !g.IncludeAll && !g.IncludeAllProxies && !g.IncludeAllProviders {
			return CompileErrorf("proxy-group", g.Name, "", "proxy group %q has no proxies or providers", g.Name)
		}
		for _, member := range g.Proxies {
			member = strings.TrimSpace(member)
			if member == "" {
				return CompileErrorf("proxy-group", g.Name, "", "proxy group %q contains empty proxy member", g.Name)
			}
			if !validGroupMember(member) {
				return CompileErrorf("proxy-group", g.Name, "", "proxy group %q references unknown proxy %q", g.Name, member)
			}
		}
		for _, provider := range g.Use {
			provider = strings.TrimSpace(provider)
			if provider == "" {
				return CompileErrorf("proxy-group", g.Name, "", "proxy group %q contains empty use provider", g.Name)
			}
			if !providerNames[provider] {
				return CompileErrorf("proxy-group", g.Name, "", "proxy group %q references unknown proxy-provider %q", g.Name, provider)
			}
		}
	}

	// Group cycle detection (DFS with stack path)
	groupMap := make(map[string]ProxyGroup, len(cfg.ProxyGroups))
	for _, g := range cfg.ProxyGroups {
		groupMap[g.Name] = g
	}

	state := make(map[string]int, len(cfg.ProxyGroups)) // 0: unvisited, 1: visiting, 2: visited
	var visit func(node string, stack []string) error
	visit = func(node string, stack []string) error {
		state[node] = 1
		stack = append(stack, node)

		g := groupMap[node]
		for _, member := range g.Proxies {
			if groupNames[member] {
				if state[member] == 1 {
					cycleStart := 0
					for i, s := range stack {
						if s == member {
							cycleStart = i
							break
						}
					}
					cyclePath := append(slices.Clone(stack[cycleStart:]), member)
					return CompileErrorf("proxy-group", node, "", "circular group dependency: %s", strings.Join(cyclePath, " -> "))
				} else if state[member] == 0 {
					if err := visit(member, stack); err != nil {
						return err
					}
				}
			}
		}

		state[node] = 2
		return nil
	}

	for _, g := range cfg.ProxyGroups {
		if state[g.Name] == 0 {
			if err := visit(g.Name, nil); err != nil {
				return err
			}
		}
	}

	// Listener validation and conflict detection
	listenerNames := make(map[string]bool, len(cfg.Listeners))
	for _, l := range cfg.Listeners {
		lName := strings.TrimSpace(l.Name)
		if lName == "" {
			return CompileErrorf("listener", "", "", "listener requires non-empty name")
		}
		if listenerNames[lName] {
			return CompileErrorf("listener", lName, "", "duplicate listener name %q", lName)
		}
		listenerNames[lName] = true
		if l.Port < 1 || l.Port > 65535 {
			return CompileErrorf("listener", lName, "", "listener %q port %d out of valid range (1-65535)", lName, l.Port)
		}
		target := strings.TrimSpace(l.Proxy)
		if target == "" {
			return CompileErrorf("listener", lName, "", "listener %q requires non-empty proxy target", lName)
		}
		if !validListenerTarget(target) {
			return CompileErrorf("listener", lName, "", "listener %q references unknown target %q", lName, target)
		}
	}

	// Listener validation and capabilities collection
	listenerCaps := make([]socketCap, len(cfg.Listeners))
	for i, l := range cfg.Listeners {
		lCap, err := listenerSocketCap(l)
		if err != nil {
			return err
		}
		listenerCaps[i] = lCap
	}

	// Listener vs Listener conflict
	for i := 0; i < len(cfg.Listeners); i++ {
		l1 := cfg.Listeners[i]
		cap1 := listenerCaps[i]
		for j := i + 1; j < len(cfg.Listeners); j++ {
			l2 := cfg.Listeners[j]
			if l1.Port == l2.Port && addressesOverlap(l1.Listen, l2.Listen) {
				cap2 := listenerCaps[j]
				if protocolsOverlap(cap1, cap2) {
					return CompileErrorf("listener", l2.Name, "", "listener %q port %d conflicts with listener %q on %s", l2.Name, l2.Port, l1.Name, l1.Listen)
				}
			}
		}
	}

	// Global ports vs global ports conflict
	type globalPortEntry struct {
		name string
		port int
		cap  socketCap
	}
	var globalPorts []globalPortEntry
	if cfg.TProxyPort > 0 {
		globalPorts = append(globalPorts, globalPortEntry{name: "tproxy-port", port: cfg.TProxyPort, cap: socketCap{tcp: true, udp: true}})
	}
	if cfg.RedirPort > 0 {
		globalPorts = append(globalPorts, globalPortEntry{name: "redir-port", port: cfg.RedirPort, cap: socketCap{tcp: true, udp: false}})
	}
	if cfg.MixedPort > 0 {
		globalPorts = append(globalPorts, globalPortEntry{name: "mixed-port", port: cfg.MixedPort, cap: socketCap{tcp: true, udp: true}})
	}
	if cfg.Port > 0 {
		globalPorts = append(globalPorts, globalPortEntry{name: "port", port: cfg.Port, cap: socketCap{tcp: true, udp: false}})
	}
	if cfg.SocksPort > 0 {
		globalPorts = append(globalPorts, globalPortEntry{name: "socks-port", port: cfg.SocksPort, cap: socketCap{tcp: true, udp: true}})
	}

	for i := 0; i < len(globalPorts); i++ {
		for j := i + 1; j < len(globalPorts); j++ {
			gp1 := globalPorts[i]
			gp2 := globalPorts[j]
			if gp1.port == gp2.port && protocolsOverlap(gp1.cap, gp2.cap) {
				return CompileErrorf("port", gp2.name, "", "global %s port %d conflicts with %s", gp2.name, gp2.port, gp1.name)
			}
		}
	}

	// Listener vs Global Port conflict
	for i, l := range cfg.Listeners {
		lCap := listenerCaps[i]
		for _, gp := range globalPorts {
			if l.Port == gp.port && addressesOverlap(l.Listen, "0.0.0.0") && protocolsOverlap(lCap, gp.cap) {
				return CompileErrorf("listener", l.Name, "", "listener %q port %d conflicts with global %s", l.Name, l.Port, gp.name)
			}
		}
	}

	// Rule validation
	for _, ruleStr := range cfg.Rules {
		ruleStr = strings.TrimSpace(ruleStr)
		if ruleStr == "" {
			return CompileErrorf("rule", "", "", "empty rule")
		}
		tokens := tokenizeRule(ruleStr)
		if len(tokens) == 0 {
			return CompileErrorf("rule", "", ruleStr, "empty rule")
		}
		ruleType := strings.ToUpper(tokens[0])

		if ruleType == "SUB-RULE" {
			return CompileErrorf("rule", "SUB-RULE", ruleStr, "SUB-RULE is not supported in AWG Manager")
		}

		spec, ok := LookupRuleSpec(ruleType)
		if !ok {
			return CompileErrorf("rule", ruleType, ruleStr, "unsupported rule type %q", ruleType)
		}

		if len(tokens) < spec.MinFields || len(tokens) > spec.MaxFields {
			return CompileErrorf("rule", ruleType, ruleStr, "rule %q has invalid number of fields (expected between %d and %d, got %d)", ruleStr, spec.MinFields, spec.MaxFields, len(tokens))
		}

		if spec.RequiresPayload {
			if strings.TrimSpace(tokens[1]) == "" {
				return CompileErrorf("rule", ruleType, ruleStr, "rule %q requires non-empty payload", ruleStr)
			}
		}

		if ruleType == "RULE-SET" {
			payload := strings.TrimSpace(tokens[1])
			if !ruleProviderNames[payload] {
				return CompileErrorf("rule", payload, ruleStr, "rule %q references non-existent rule-set provider %q", ruleStr, payload)
			}
		}

		if ruleType == "AND" || ruleType == "OR" || ruleType == "NOT" {
			payload := strings.TrimSpace(tokens[1])
			if err := parseCompositePayload(ruleType, payload, 1); err != nil {
				return err
			}
		}

		target := strings.TrimSpace(tokens[spec.TargetIndex])
		if !validRuleTarget(target) {
			return CompileErrorf("rule", target, ruleStr, "rule %q references unknown target %q", ruleStr, target)
		}

		if len(tokens) > spec.TargetIndex+1 {
			for _, mod := range tokens[spec.TargetIndex+1:] {
				mod = strings.TrimSpace(mod)
				if mod == "" {
					continue
				}
				validMod := false
				for _, allowed := range spec.AllowedModifiers {
					if strings.EqualFold(mod, allowed) {
						validMod = true
						break
					}
				}
				if !validMod {
					return CompileErrorf("rule", ruleType, ruleStr, "rule %q has unsupported modifier %q", ruleStr, mod)
				}
			}
		}
	}

	return nil
}

func ConvertSingboxToMihomoProxy(ob map[string]any) Proxy {
	p, _ := convertSingboxToMihomoProxy(ob)
	return p
}

// convertSingboxToMihomoProxy converts one sing-box outbound. Unsupported
// proxy protocols fail closed: silently dropping one can leave groups and
// routing rules pointing at a missing proxy, while substituting DIRECT would
// risk leaking traffic outside the tunnel.
func convertSingboxToMihomoProxy(ob map[string]any) (Proxy, error) {
	tag, _ := ob["tag"].(string)
	pType, _ := ob["type"].(string)
	if tag == "" || pType == "" {
		return nil, fmt.Errorf("mihomo outbound requires non-empty type and tag")
	}

	// A named direct outbound bound to an interface is a real Mihomo proxy
	// node. The unbound sing-box "direct" outbound maps to Mihomo's built-in
	// DIRECT target and must not be duplicated.
	if pType == "direct" {
		iface, _ := ob["bind_interface"].(string)
		if iface == "" {
			return nil, nil
		}
		return Proxy{
			"name":           tag,
			"type":           "direct",
			"udp":            true,
			"interface-name": iface,
		}, nil
	}

	// Skip non-proxy types.
	if pType == "block" || pType == "dns" || pType == "selector" || pType == "urltest" || pType == "loadbalance" {
		return nil, nil
	}

	p := Proxy{
		"name": tag,
	}

	getNestedMap := func(m map[string]any, key string) map[string]any {
		if val, ok := m[key]; ok {
			if nm, ok := val.(map[string]any); ok {
				return nm
			}
		}
		return nil
	}

	server, _ := ob["server"].(string)
	serverPort, _ := ob["server_port"].(float64)
	if serverPort == 0 {
		if ip, ok := ob["server_port"].(int); ok {
			serverPort = float64(ip)
		}
	}

	p["server"] = server
	p["port"] = int(serverPort)

	switch pType {
	case "socks":
		p["type"] = "socks5"
		if username, ok := ob["username"].(string); ok && username != "" {
			p["username"] = username
		}
		if password, ok := ob["password"].(string); ok && password != "" {
			p["password"] = password
		}
		p["udp"] = true

	case "shadowsocks":
		p["type"] = "ss"
		p["cipher"], _ = ob["method"].(string)
		p["password"], _ = ob["password"].(string)

	case "vmess":
		p["type"] = "vmess"
		p["uuid"], _ = ob["uuid"].(string)
		alterId, _ := ob["alter_id"].(float64)
		p["alterId"] = int(alterId)
		p["cipher"], _ = ob["security"].(string)
		if p["cipher"] == "" {
			p["cipher"] = "auto"
		}
		p["udp"] = true

	case "vless":
		p["type"] = "vless"
		p["uuid"], _ = ob["uuid"].(string)
		p["flow"], _ = ob["flow"].(string)
		p["udp"] = true

	case "trojan":
		p["type"] = "trojan"
		p["password"], _ = ob["password"].(string)
		p["udp"] = true

	case "hysteria2":
		p["type"] = "hysteria2"
		p["password"], _ = ob["password"].(string)
		if p["password"] == "" {
			p["password"], _ = ob["auth_str"].(string)
		}

	case "tuic":
		p["type"] = "tuic"
		p["uuid"], _ = ob["uuid"].(string)
		p["password"], _ = ob["password"].(string)

	case "wireguard":
		p["type"] = "wireguard"
		p["private-key"], _ = ob["private_key"].(string)
		p["public-key"], _ = ob["public_key"].(string)
		if mtu, ok := ob["mtu"].(float64); ok {
			p["mtu"] = int(mtu)
		}
		if ip, ok := ob["local_address"].(string); ok {
			p["ips"] = []string{ip}
		}

	case "trusttunnel":
		p["type"] = "trusttunnel"
		p["username"], _ = ob["username"].(string)
		p["password"], _ = ob["password"].(string)
		if quic, ok := ob["quic"].(bool); ok {
			p["quic"] = quic
		}
		if hc, ok := ob["health_check"].(bool); ok {
			p["health-check"] = hc
		}
		p["udp"] = true

	default:
		return nil, fmt.Errorf("mihomo does not support outbound %q of type %q", tag, pType)
	}

	// Map TLS options
	if tls := getNestedMap(ob, "tls"); tls != nil {
		enabled, _ := tls["enabled"].(bool)
		if enabled {
			p["tls"] = true
			if serverName, ok := tls["server_name"].(string); ok && serverName != "" {
				p["servername"] = serverName
				p["sni"] = serverName
			}
			if insecure, ok := tls["insecure"].(bool); ok {
				p["skip-cert-verify"] = insecure
			}
			if alpnVal, ok := tls["alpn"]; ok {
				if alpnList, ok := alpnVal.([]any); ok {
					var alpns []string
					for _, a := range alpnList {
						if s, ok := a.(string); ok {
							alpns = append(alpns, s)
						}
					}
					p["alpn"] = alpns
				}
			}
			if utls := getNestedMap(tls, "utls"); utls != nil {
				if fp, ok := utls["fingerprint"].(string); ok && fp != "" {
					p["client-fingerprint"] = fp
				}
			}
			if reality := getNestedMap(tls, "reality"); reality != nil {
				ro := map[string]any{}
				if pbk, ok := reality["public_key"].(string); ok && pbk != "" {
					ro["public-key"] = pbk
				}
				if sid, ok := reality["short_id"].(string); ok && sid != "" {
					ro["short-id"] = sid
				}
				p["reality-opts"] = ro
				if fp, ok := p["client-fingerprint"].(string); !ok || fp == "" {
					p["client-fingerprint"] = "chrome"
				}
			}
		}
	}

	if sniVal, ok := ob["sni"].(string); ok && sniVal != "" {
		p["sni"] = sniVal
		if _, ok := p["servername"]; !ok {
			p["servername"] = sniVal
		}
	}

	// Map Transport options
	if transport := getNestedMap(ob, "transport"); transport != nil {
		tType, _ := transport["type"].(string)
		switch tType {
		case "ws":
			p["network"] = "ws"
			opts := map[string]any{}
			if path, ok := transport["path"].(string); ok {
				opts["path"] = path
			}
			if headers := getNestedMap(transport, "headers"); headers != nil {
				opts["headers"] = headers
			}
			p["ws-opts"] = opts
		case "grpc":
			p["network"] = "grpc"
			opts := map[string]any{}
			if serviceName, ok := transport["service_name"].(string); ok {
				opts["grpc-service-name"] = serviceName
			}
			p["grpc-opts"] = opts
		case "http":
			p["network"] = "http"
			opts := map[string]any{}
			if path, ok := transport["path"].(string); ok {
				opts["path"] = path
			}
			if headers := getNestedMap(transport, "headers"); headers != nil {
				opts["headers"] = headers
			}
			p["http-opts"] = opts
		}
	}

	return p, nil
}

func ConvertSingboxToMihomoProxyGroup(ob map[string]any) *ProxyGroup {
	tag, _ := ob["tag"].(string)
	pType, _ := ob["type"].(string)
	if tag == "" || pType == "" {
		return nil
	}

	if pType != "selector" && pType != "urltest" && pType != "loadbalance" {
		return nil
	}

	pg := &ProxyGroup{
		Name: tag,
		Type: mihomoProxyGroupType(pType),
	}

	if outboundsVal, ok := ob["outbounds"]; ok {
		if ol, ok := outboundsVal.([]any); ok {
			for _, o := range ol {
				if s, ok := o.(string); ok {
					if s == "block" {
						s = "REJECT"
					} else if s == "dns" {
						s = "DIRECT"
					} else if s == "direct" {
						s = "DIRECT"
					}
					pg.Proxies = append(pg.Proxies, s)
				}
			}
		} else if sl, ok := outboundsVal.([]string); ok {
			for _, s := range sl {
				if s == "block" {
					s = "REJECT"
				} else if s == "dns" {
					s = "DIRECT"
				} else if s == "direct" {
					s = "DIRECT"
				}
				pg.Proxies = append(pg.Proxies, s)
			}
		}
	}

	initial, _ := ob["default"].(string)
	if initial == "" {
		initial, _ = ob["initial"].(string)
	}
	if initial != "" {
		for i, p := range pg.Proxies {
			if p == initial && i > 0 {
				pg.Proxies = append([]string{initial}, append(pg.Proxies[:i], pg.Proxies[i+1:]...)...)
				break
			}
		}
	}

	if pType == "urltest" {
		pg.URL = "https://www.gstatic.com/generate_204"
		pg.Interval = 60
		pg.Lazy = boolPtr(false)
		if u, ok := ob["url"].(string); ok && u != "" {
			pg.URL = u
		}
		if ivl, ok := ob["interval"].(string); ok && ivl != "" {
			// sing-box stores interval as duration string, e.g. "60s"
			if d, err := time.ParseDuration(ivl); err == nil && d > 0 {
				pg.Interval = int(d.Seconds())
			}
		} else if ivlNum, ok := ob["interval"].(float64); ok && ivlNum > 0 {
			pg.Interval = int(ivlNum)
		}
		if tol, ok := ob["tolerance"].(float64); ok && tol > 0 {
			pg.Tolerance = int(tol)
		}
	}

	return pg
}

func mihomoProxyGroupType(groupType string) string {
	switch groupType {
	case "selector", "select":
		return "select"
	case "urltest", "url-test":
		return "url-test"
	case "loadbalance", "load-balance":
		return "load-balance"
	default:
		return groupType
	}
}

func ConvertSingboxRuleToMihomo(r Rule) []string {
	if r.Action != "" && r.Action != "route" {
		return nil
	}

	outbound := r.Outbound
	if outbound == "direct" {
		outbound = "DIRECT"
	} else if outbound == "block" {
		outbound = "REJECT"
	}

	var clashRules []string

	for _, ds := range r.DomainSuffix {
		clashRules = append(clashRules, fmt.Sprintf("DOMAIN-SUFFIX,%s,%s", ds, outbound))
	}

	for _, d := range r.Domain {
		clashRules = append(clashRules, fmt.Sprintf("DOMAIN,%s,%s", d, outbound))
	}

	for _, ip := range r.IPCIDR {
		clashRules = append(clashRules, fmt.Sprintf("IP-CIDR,%s,%s", ip, outbound))
	}

	for _, sip := range r.SourceIPCIDR {
		clashRules = append(clashRules, fmt.Sprintf("SRC-IP-CIDR,%s,%s", sip, outbound))
	}

	for _, rs := range r.RuleSet {
		if strings.HasPrefix(rs, "geosite:") {
			tag := strings.TrimPrefix(rs, "geosite:")
			clashRules = append(clashRules, fmt.Sprintf("GEOSITE,%s,%s", tag, outbound))
		} else if strings.HasPrefix(rs, "geosite-") {
			tag := strings.TrimPrefix(rs, "geosite-")
			clashRules = append(clashRules, fmt.Sprintf("GEOSITE,%s,%s", tag, outbound))
		} else if strings.HasPrefix(rs, "geoip:") {
			tag := strings.TrimPrefix(rs, "geoip:")
			clashRules = append(clashRules, fmt.Sprintf("GEOIP,%s,%s,no-resolve", tag, outbound))
		} else if strings.HasPrefix(rs, "geoip-") {
			tag := strings.TrimPrefix(rs, "geoip-")
			clashRules = append(clashRules, fmt.Sprintf("GEOIP,%s,%s,no-resolve", tag, outbound))
		} else {
			clashRules = append(clashRules, fmt.Sprintf("RULE-SET,%s,%s", rs, outbound))
		}
	}

	return clashRules
}

func boolPtr(b bool) *bool { return &b }
