package vlessserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrMigrationConflict = errors.New("migration conflict: legacy configuration has unsupported or ambiguous topology")
	ErrAlreadyMigrated   = errors.New("already migrated")
)

type LegacyTopology string

const (
	TopologyA LegacyTopology = "A" // Direct: origin -> legacy public port (0.0.0.0 / empty)
	TopologyB LegacyTopology = "B" // Loopback: 127.0.0.1 + CDN dispatcher
	TopologyC LegacyTopology = "C" // Unsupported, corrupt, or ambiguous
)

type LegacyDiscoveryResult struct {
	Found               bool              `json:"found"`
	ConfigPath          string            `json:"config_path"`
	InitScriptPath      string            `json:"init_script_path"`
	InitScriptIsSymlink bool              `json:"init_script_is_symlink"`
	Topology            LegacyTopology    `json:"topology"`
	ListenAddress       string            `json:"listen_address"`
	ListenPort          int               `json:"listen_port"`
	PublicDomain        string            `json:"public_domain"`
	Path                string            `json:"path"`
	Mode                string            `json:"mode"`
	UplinkMethod        string            `json:"uplink_method"`
	OutboundMode        string            `json:"outbound_mode,omitempty"`
	OutboundSocksPort   int               `json:"outbound_socks_port,omitempty"`
	OutboundInterface   string            `json:"outbound_interface,omitempty"`
	Clients             []Client          `json:"clients"`
	Checksums           map[string]string `json:"checksums"`
	ConflictReason      string            `json:"conflict_reason,omitempty"`
}

type MigrationMarker struct {
	MigratedAt           string         `json:"migrated_at"`
	SourcePath           string         `json:"source_path"`
	SourceChecksum       string         `json:"source_checksum"`
	Topology             LegacyTopology `json:"topology"`
	OriginPort           int            `json:"origin_port"`
	AssignedLoopbackPort int            `json:"assigned_loopback_port"`
}

type RuntimeDecision struct {
	ActiveGeneration  string `json:"active_generation"` // always "new" (awgm-xhttpd)
	MigrationStatus   string `json:"migration_status"`  // "migrated" | "conflict"
	DecidedAt         string `json:"decided_at"`
	GenerationCounter int    `json:"generation_counter"`
	Reason            string `json:"reason,omitempty"`
}

type rawLegacyRouting struct {
	DomainStrategy string            `json:"domainStrategy"`
	Rules          []json.RawMessage `json:"rules"`
	Balancers      []json.RawMessage `json:"balancers"`
}

type rawLegacyOutbound struct {
	Tag           string          `json:"tag"`
	Protocol      string          `json:"protocol"`
	SendThrough   string          `json:"sendThrough"`
	ProxySettings json.RawMessage `json:"proxySettings"`
	Mux           *struct {
		Enabled bool `json:"enabled"`
	} `json:"mux"`
	Settings       json.RawMessage `json:"settings"`
	StreamSettings *struct {
		Network  string `json:"network"`
		Security string `json:"security"`
		Sockopt  *struct {
			Interface   string `json:"interface"`
			DialerProxy string `json:"dialerProxy"`
			Mark        int    `json:"mark"`
		} `json:"sockopt"`
	} `json:"streamSettings"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type rawLegacyXrayConfig struct {
	Inbounds []struct {
		Tag      string `json:"tag"`
		Listen   string `json:"listen"`
		Port     int    `json:"port"`
		Protocol string `json:"protocol"`
		Security string `json:"security"`
		Settings struct {
			Clients []struct {
				ID     string `json:"id"`
				Email  string `json:"email"`
				Flow   string `json:"flow"`
				Remark string `json:"remark"`
			} `json:"clients"`
		} `json:"settings"`
		StreamSettings struct {
			Network       string          `json:"network"`
			Security      string          `json:"security"`
			Reality       json.RawMessage `json:"realitySettings"`
			TLSSettings   json.RawMessage `json:"tlsSettings"`
			XTLSSettings  json.RawMessage `json:"xtlsSettings"`
			XHTTPSettings struct {
				Host             string `json:"host"`
				Path             string `json:"path"`
				Mode             string `json:"mode"`
				UplinkHTTPMethod string `json:"uplinkHTTPMethod"`
			} `json:"xhttpSettings"`
			SplitHTTPSettings struct {
				Host string `json:"host"`
				Path string `json:"path"`
				Mode string `json:"mode"`
			} `json:"splithttpSettings"`
		} `json:"streamSettings"`
		// Also support legacy ManagedConfig profile format ("managed": {...} or direct Inbound struct)
		Transport *struct {
			Network  string          `json:"network"`
			Security string          `json:"security"`
			TLS      json.RawMessage `json:"tls"`
			XHTTP    *struct {
				Host string `json:"host"`
				Path string `json:"path"`
				Mode string `json:"mode"`
			} `json:"xhttp"`
		} `json:"transport"`
		Reality json.RawMessage `json:"reality"`
		TLS     json.RawMessage `json:"tls"`
		Clients []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Flow  string `json:"flow"`
		} `json:"clients"`
	} `json:"inbounds"`
	Outbounds []rawLegacyOutbound `json:"outbounds"`
	Routing   *rawLegacyRouting   `json:"routing"`
	Managed   *struct {
		Inbounds  json.RawMessage `json:"inbounds"`
		Outbounds json.RawMessage `json:"outbounds"`
		Routing   json.RawMessage `json:"routing"`
	} `json:"managed"`
}

func hasNonEmptyJSONValue(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "null", "false", "{}", "[]":
		return false
	default:
		return true
	}
}

func discoverLegacyEgress(outbounds []rawLegacyOutbound, routing *rawLegacyRouting) (mode string, socksPort int, iface string, conflict string) {
	if routing != nil {
		if len(routing.Rules) > 0 || len(routing.Balancers) > 0 {
			return "", 0, "", "legacy routing rules/balancers are not supported for automatic migration"
		}
	}
	if len(outbounds) == 0 {
		return "", 0, "", "missing or empty outbounds in legacy config: cannot determine egress policy"
	}
	if len(outbounds) > 1 {
		return "", 0, "", fmt.Sprintf("multiple legacy outbounds (%d) are not supported for automatic migration", len(outbounds))
	}

	out := outbounds[0]
	if st := strings.TrimSpace(out.SendThrough); st != "" && st != "0.0.0.0" {
		return "", 0, "", fmt.Sprintf("unsupported legacy outbound sendThrough %q", st)
	}
	if hasNonEmptyJSONValue(out.ProxySettings) {
		return "", 0, "", "unsupported legacy outbound proxySettings chaining"
	}
	if out.Mux != nil && out.Mux.Enabled {
		return "", 0, "", "unsupported legacy outbound mux"
	}
	if out.StreamSettings != nil {
		if sec := strings.ToLower(strings.TrimSpace(out.StreamSettings.Security)); sec != "" && sec != "none" {
			return "", 0, "", fmt.Sprintf("unsupported legacy outbound stream security %q", out.StreamSettings.Security)
		}
		if netw := strings.ToLower(strings.TrimSpace(out.StreamSettings.Network)); netw != "" && netw != "tcp" && netw != "raw" {
			return "", 0, "", fmt.Sprintf("unsupported legacy outbound transport %q", out.StreamSettings.Network)
		}
		if out.StreamSettings.Sockopt != nil {
			if strings.TrimSpace(out.StreamSettings.Sockopt.DialerProxy) != "" {
				return "", 0, "", "unsupported legacy outbound sockopt.dialerProxy chaining"
			}
			if out.StreamSettings.Sockopt.Mark != 0 {
				return "", 0, "", fmt.Sprintf("unsupported legacy outbound sockopt.mark %d", out.StreamSettings.Sockopt.Mark)
			}
		}
	}

	proto := strings.ToLower(strings.TrimSpace(out.Protocol))
	switch proto {
	case "freedom", "direct":
		if hasNonEmptyJSONValue(out.Settings) {
			var fcfg struct {
				DomainStrategy string          `json:"domainStrategy"`
				Redirect       string          `json:"redirect"`
				Fragment       json.RawMessage `json:"fragment"`
				Noises         json.RawMessage `json:"noises"`
			}
			if err := json.Unmarshal(out.Settings, &fcfg); err != nil {
				return "", 0, "", fmt.Sprintf("malformed legacy %s outbound settings: %v", proto, err)
			}
			if strings.TrimSpace(fcfg.Redirect) != "" {
				return "", 0, "", fmt.Sprintf("unsupported legacy %s redirect %q", proto, fcfg.Redirect)
			}
			if hasNonEmptyJSONValue(fcfg.Fragment) || hasNonEmptyJSONValue(fcfg.Noises) {
				return "", 0, "", fmt.Sprintf("unsupported legacy %s fragment/noises settings", proto)
			}
			if strings.EqualFold(strings.TrimSpace(fcfg.DomainStrategy), "UseIPv6") {
				return "", 0, "", "unsupported legacy IPv6-only freedom domainStrategy \"UseIPv6\""
			}
		}
		if out.StreamSettings != nil && out.StreamSettings.Sockopt != nil {
			if boundIf := strings.TrimSpace(out.StreamSettings.Sockopt.Interface); boundIf != "" {
				return "interface", 0, boundIf, ""
			}
		}
		return "direct", 0, "", ""

	case "socks":
		if out.StreamSettings != nil && out.StreamSettings.Sockopt != nil && strings.TrimSpace(out.StreamSettings.Sockopt.Interface) != "" {
			return "", 0, "", "unsupported legacy SOCKS outbound with sockopt.interface"
		}
		type rawSocksServer struct {
			Address string            `json:"address"`
			Port    int               `json:"port"`
			Users   []json.RawMessage `json:"users"`
		}
		var scfg struct {
			Servers []rawSocksServer  `json:"servers"`
			Address string            `json:"address"`
			Port    int               `json:"port"`
			Users   []json.RawMessage `json:"users"`
			Version string            `json:"version"`
		}
		if hasNonEmptyJSONValue(out.Settings) {
			if err := json.Unmarshal(out.Settings, &scfg); err != nil {
				return "", 0, "", fmt.Sprintf("malformed legacy socks outbound settings: %v", err)
			}
		}
		if ver := strings.TrimSpace(scfg.Version); ver != "" && ver != "5" {
			return "", 0, "", fmt.Sprintf("unsupported legacy SOCKS version %q: only SOCKS5 is supported", ver)
		}
		var addr string
		var port int
		var users []json.RawMessage
		switch {
		case len(scfg.Servers) > 1:
			return "", 0, "", "multiple servers in legacy SOCKS outbound are not supported"
		case len(scfg.Servers) == 1:
			addr = scfg.Servers[0].Address
			port = scfg.Servers[0].Port
			users = scfg.Servers[0].Users
		case scfg.Address != "" || scfg.Port != 0 || len(scfg.Users) > 0:
			addr = scfg.Address
			port = scfg.Port
			users = scfg.Users
		case out.Address != "" || out.Port != 0:
			addr = out.Address
			port = out.Port
		default:
			return "", 0, "", "legacy SOCKS outbound is missing server address and port"
		}
		if len(users) > 0 {
			return "", 0, "", "authenticated legacy SOCKS outbound is not supported by awgm-xhttpd"
		}
		normAddr := strings.ToLower(strings.TrimSpace(addr))
		if normAddr != "127.0.0.1" && normAddr != "localhost" {
			return "", 0, "", fmt.Sprintf("unsupported non-loopback legacy SOCKS address %q: only 127.0.0.1/localhost is supported", addr)
		}
		if port < 1 || port > 65535 {
			return "", 0, "", fmt.Sprintf("invalid legacy SOCKS port %d", port)
		}
		return "socks", port, "", ""

	case "":
		return "", 0, "", "legacy outbound is missing protocol"
	default:
		return "", 0, "", fmt.Sprintf("unsupported legacy outbound protocol %q: only direct/freedom and local unauthenticated socks5 are supported", out.Protocol)
	}
}

// DiscoverLegacy inspects a legacy Xray/profile configuration without modifying or deleting any file.
// Unsupported protocols (vmess, trojan, shadowsocks), inbound TLS/Reality termination, unknown security,
// non-XHTTP transports (ws, grpc, tcp), complex/missing/unsupported outbounds, corrupt JSON, and
// ambiguous multi-inbound topologies return TopologyC with an explicit ConflictReason.
func DiscoverLegacy(cfgPath, initScriptPath string) (*LegacyDiscoveryResult, error) {
	res := &LegacyDiscoveryResult{
		ConfigPath:     cfgPath,
		InitScriptPath: initScriptPath,
		Checksums:      make(map[string]string),
	}

	if !fileExists(cfgPath) {
		return res, nil
	}
	res.Found = true

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read legacy config: %w", err)
	}
	h := sha256.Sum256(data)
	res.Checksums["config.json"] = hex.EncodeToString(h[:])

	if initScriptPath != "" && fileExists(initScriptPath) {
		if fi, err := os.Lstat(initScriptPath); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				res.InitScriptIsSymlink = true
				target, err := filepath.EvalSymlinks(initScriptPath)
				if err != nil {
					res.Topology = TopologyC
					res.ConflictReason = fmt.Sprintf("invalid init script symlink: %v", err)
					return res, nil
				}
				cleanTarget := filepath.Clean(target)
				cleanInitDir := filepath.Clean(filepath.Dir(initScriptPath))
				rel, err := filepath.Rel(cleanInitDir, cleanTarget)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
					res.Topology = TopologyC
					res.ConflictReason = fmt.Sprintf("symlink target %q escapes trusted init.d directory", cleanTarget)
					return res, nil
				}
			}
			initData, _ := os.ReadFile(initScriptPath)
			initSum := sha256.Sum256(initData)
			res.Checksums["init_script"] = hex.EncodeToString(initSum[:])
		}
	}

	var parsed rawLegacyXrayConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		res.Topology = TopologyC
		res.ConflictReason = fmt.Sprintf("malformed legacy JSON: %v", err)
		return res, nil
	}
	if parsed.Managed != nil {
		if len(parsed.Inbounds) == 0 && len(parsed.Managed.Inbounds) > 0 {
			var unwrap rawLegacyXrayConfig
			if err := json.Unmarshal([]byte(`{"inbounds":`+string(parsed.Managed.Inbounds)+`}`), &unwrap); err != nil {
				res.Topology = TopologyC
				res.ConflictReason = fmt.Sprintf("malformed managed inbounds JSON: %v", err)
				return res, nil
			} else {
				parsed.Inbounds = unwrap.Inbounds
			}
		}
		if len(parsed.Outbounds) == 0 && len(parsed.Managed.Outbounds) > 0 {
			if err := json.Unmarshal(parsed.Managed.Outbounds, &parsed.Outbounds); err != nil {
				res.Topology = TopologyC
				res.ConflictReason = fmt.Sprintf("malformed managed outbounds JSON: %v", err)
				return res, nil
			}
		}
		if parsed.Routing == nil && len(parsed.Managed.Routing) > 0 {
			var r rawLegacyRouting
			if err := json.Unmarshal(parsed.Managed.Routing, &r); err != nil {
				res.Topology = TopologyC
				res.ConflictReason = fmt.Sprintf("malformed managed routing JSON: %v", err)
				return res, nil
			}
			parsed.Routing = &r
		}
	}

	if len(parsed.Inbounds) == 0 {
		res.Topology = TopologyC
		res.ConflictReason = "no inbounds found in legacy config"
		return res, nil
	}

	var vlessInbounds []int
	for idx, in := range parsed.Inbounds {
		proto := strings.ToLower(strings.TrimSpace(in.Protocol))
		// Ignore internal metrics/api helper inbounds if tagged accordingly, reject user non-VLESS protocols.
		if proto == "dokodemo-door" && strings.EqualFold(in.Tag, "api") {
			continue
		}
		if proto != "vless" {
			res.Topology = TopologyC
			res.ConflictReason = fmt.Sprintf("unsupported legacy protocol %q: only VLESS over XHTTP is supported", in.Protocol)
			return res, nil
		}
		// Ignore only the auto-generated secondary timeweb-in mirror produced by older RenderRuntimeConfig.
		if strings.EqualFold(in.Tag, "timeweb-in") && len(parsed.Inbounds) > 1 {
			continue
		}
		vlessInbounds = append(vlessInbounds, idx)
	}

	if len(vlessInbounds) == 0 {
		res.Topology = TopologyC
		res.ConflictReason = "no VLESS inbound found in legacy config"
		return res, nil
	}
	if len(vlessInbounds) > 1 {
		res.Topology = TopologyC
		res.ConflictReason = "multiple VLESS inbounds found: ambiguous topology"
		return res, nil
	}

	in := parsed.Inbounds[vlessInbounds[0]]

	sec := strings.ToLower(strings.TrimSpace(in.StreamSettings.Security))
	if sec == "" {
		sec = strings.ToLower(strings.TrimSpace(in.Security))
	}
	if sec == "" && in.Transport != nil {
		sec = strings.ToLower(strings.TrimSpace(in.Transport.Security))
	}

	if sec == "reality" || hasNonEmptyJSONValue(in.StreamSettings.Reality) || hasNonEmptyJSONValue(in.Reality) {
		res.Topology = TopologyC
		res.ConflictReason = "unsupported legacy security \"reality\": only plain XHTTP/FiniteProto behind external TLS/CDN ingress is supported"
		return res, nil
	}
	if sec == "tls" || sec == "xtls" ||
		hasNonEmptyJSONValue(in.StreamSettings.TLSSettings) ||
		hasNonEmptyJSONValue(in.StreamSettings.XTLSSettings) ||
		hasNonEmptyJSONValue(in.TLS) ||
		(in.Transport != nil && hasNonEmptyJSONValue(in.Transport.TLS)) {
		res.Topology = TopologyC
		res.ConflictReason = "unsupported legacy inbound TLS termination: certificates/tlsSettings are not migrated automatically to plain-HTTP awgm-xhttpd"
		return res, nil
	}
	if sec != "" && sec != "none" {
		res.Topology = TopologyC
		res.ConflictReason = fmt.Sprintf("unsupported legacy stream security %q: only plain XHTTP (\"none\" or empty) behind external ingress is supported", sec)
		return res, nil
	}

	netType := strings.ToLower(strings.TrimSpace(in.StreamSettings.Network))
	if netType == "" && in.Transport != nil {
		netType = strings.ToLower(strings.TrimSpace(in.Transport.Network))
	}
	switch netType {
	case "", "xhttp", "splithttp":
	default:
		res.Topology = TopologyC
		res.ConflictReason = fmt.Sprintf("unsupported legacy transport %q: only \"xhttp\" is supported (ws/grpc/tcp are not supported)", netType)
		return res, nil
	}

	res.ListenAddress = strings.TrimSpace(in.Listen)
	res.ListenPort = in.Port
	res.PublicDomain = strings.TrimSpace(in.StreamSettings.XHTTPSettings.Host)
	res.Path = strings.TrimSpace(in.StreamSettings.XHTTPSettings.Path)
	res.Mode = strings.TrimSpace(in.StreamSettings.XHTTPSettings.Mode)
	res.UplinkMethod = strings.TrimSpace(in.StreamSettings.XHTTPSettings.UplinkHTTPMethod)

	if res.Path == "" && in.StreamSettings.SplitHTTPSettings.Path != "" {
		res.PublicDomain = strings.TrimSpace(in.StreamSettings.SplitHTTPSettings.Host)
		res.Path = strings.TrimSpace(in.StreamSettings.SplitHTTPSettings.Path)
		res.Mode = strings.TrimSpace(in.StreamSettings.SplitHTTPSettings.Mode)
	}
	if res.Path == "" && in.Transport != nil && in.Transport.XHTTP != nil {
		res.PublicDomain = strings.TrimSpace(in.Transport.XHTTP.Host)
		res.Path = strings.TrimSpace(in.Transport.XHTTP.Path)
		res.Mode = strings.TrimSpace(in.Transport.XHTTP.Mode)
	}

	seenUUID := make(map[string]bool)
	appendClient := func(id, remark, flow string, idx int) error {
		id = strings.TrimSpace(id)
		if id == "" || seenUUID[id] {
			return nil
		}
		if f := strings.ToLower(strings.TrimSpace(flow)); f != "" && f != "none" {
			return fmt.Errorf("unsupported legacy VLESS client flow %q (Vision/XTLS flows are not used over XHTTP)", flow)
		}
		seenUUID[id] = true
		if strings.TrimSpace(remark) == "" {
			remark = fmt.Sprintf("Client %d (Legacy)", idx+1)
		}
		cl := Client{
			ID:        id,
			Remark:    strings.TrimSpace(remark),
			Enabled:   true,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		_, _ = ensureClientFPCredentials(&cl)
		res.Clients = append(res.Clients, cl)
		return nil
	}

	for idx, cl := range in.Settings.Clients {
		rem := cl.Remark
		if rem == "" {
			rem = cl.Email
		}
		if err := appendClient(cl.ID, rem, cl.Flow, idx); err != nil {
			res.Topology = TopologyC
			res.ConflictReason = err.Error()
			return res, nil
		}
	}
	for idx, cl := range in.Clients {
		if err := appendClient(cl.ID, cl.Email, cl.Flow, idx); err != nil {
			res.Topology = TopologyC
			res.ConflictReason = err.Error()
			return res, nil
		}
	}

	egressMode, egressPort, egressIface, egressConflict := discoverLegacyEgress(parsed.Outbounds, parsed.Routing)
	if egressConflict != "" {
		res.Topology = TopologyC
		res.ConflictReason = egressConflict
		return res, nil
	}
	res.OutboundMode = egressMode
	res.OutboundSocksPort = egressPort
	res.OutboundInterface = egressIface

	if res.ListenAddress == "" || res.ListenAddress == "0.0.0.0" {
		res.Topology = TopologyA
	} else if res.ListenAddress == "127.0.0.1" {
		res.Topology = TopologyB
	} else {
		res.Topology = TopologyC
		res.ConflictReason = fmt.Sprintf("unsupported listen address %q", res.ListenAddress)
	}

	return res, nil
}

// BackupLegacyConfigFile writes a non-destructive .bak copy into dataDir/xray without deleting sourcePath.
func BackupLegacyConfigFile(sourcePath, dataDir string) error {
	if sourcePath == "" || !fileExists(sourcePath) {
		return nil
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read legacy config %s: %w", sourcePath, err)
	}
	xrayDir := filepath.Join(dataDir, "xray")
	if err := os.MkdirAll(xrayDir, 0700); err != nil {
		return fmt.Errorf("create xray dir: %w", err)
	}
	bakName := "legacy-config.json.bak"
	if filepath.Clean(filepath.Dir(sourcePath)) == filepath.Clean(xrayDir) {
		bakName = filepath.Base(sourcePath) + ".bak"
	}
	return atomicWriteFile(filepath.Join(xrayDir, bakName), data, 0600)
}

// MigrateLegacy converts discovered legacy configuration into managed vlessserver.Config idempotently.
// It NEVER deletes legacy files or user secrets/profiles; it writes a .bak backup alongside xray-server-settings.json.
func MigrateLegacy(ctx context.Context, disc *LegacyDiscoveryResult, dataDir string, loopbackPort int) (*Config, error) {
	_ = ctx
	if disc == nil || !disc.Found {
		return nil, errors.New("no legacy configuration discovered")
	}
	if disc.Topology == TopologyC {
		return nil, fmt.Errorf("%w: %s", ErrMigrationConflict, disc.ConflictReason)
	}
	if disc.Topology != TopologyA && disc.Topology != TopologyB {
		return nil, fmt.Errorf("%w: unsupported topology %q", ErrMigrationConflict, disc.Topology)
	}

	var outboundMode string
	var outboundSocksPort int
	var outboundInterface string
	switch strings.ToLower(strings.TrimSpace(disc.OutboundMode)) {
	case "direct":
		outboundMode = "direct"
	case "socks":
		if disc.OutboundSocksPort < 1 || disc.OutboundSocksPort > 65535 {
			return nil, fmt.Errorf("%w: invalid discovered SOCKS port %d", ErrMigrationConflict, disc.OutboundSocksPort)
		}
		outboundMode = "socks"
		outboundSocksPort = disc.OutboundSocksPort
	case "interface":
		outboundInterface = strings.TrimSpace(disc.OutboundInterface)
		if outboundInterface == "" {
			return nil, fmt.Errorf("%w: empty discovered outbound interface", ErrMigrationConflict)
		}
		outboundMode = "interface"
	default:
		return nil, fmt.Errorf("%w: missing or unsupported discovered outbound mode %q", ErrMigrationConflict, disc.OutboundMode)
	}

	if loopbackPort <= 0 {
		loopbackPort = 9008
	}

	xrayDir := filepath.Join(dataDir, "xray")
	if err := os.MkdirAll(xrayDir, 0700); err != nil {
		return nil, fmt.Errorf("create xray dir: %w", err)
	}

	settingsPath := filepath.Join(xrayDir, "xray-server-settings.json")
	markerPath := filepath.Join(xrayDir, "migration-marker.json")

	// Idempotent check: if already migrated from the exact same source checksum, return the saved Config cleanly.
	if fileExists(markerPath) && fileExists(settingsPath) {
		if mBytes, err := os.ReadFile(markerPath); err == nil {
			var existingMarker MigrationMarker
			if json.Unmarshal(mBytes, &existingMarker) == nil &&
				existingMarker.SourceChecksum != "" &&
				existingMarker.SourceChecksum == disc.Checksums["config.json"] {
				if sBytes, err := os.ReadFile(settingsPath); err == nil {
					var existingCfg Config
					if json.Unmarshal(sBytes, &existingCfg) == nil {
						return &existingCfg, nil
					}
				}
			}
		}
		return nil, ErrAlreadyMigrated
	}

	if err := BackupLegacyConfigFile(disc.ConfigPath, dataDir); err != nil {
		return nil, err
	}

	clients := make([]Client, len(disc.Clients))
	copy(clients, disc.Clients)
	_, _ = ensureClientsFPCredentials(clients)

	newCfg := &Config{
		Enabled:            true,
		ListenAddress:      "127.0.0.1",
		ListenPort:         loopbackPort,
		DispatcherPort:     disc.ListenPort,
		PublicDomain:       disc.PublicDomain,
		PublicPort:         443,
		Path:               disc.Path,
		Transport:          "xhttp",
		Mode:               disc.Mode,
		UplinkMethod:       disc.UplinkMethod,
		XmuxMaxConnections: 2,
		OutboundSocksPort:  outboundSocksPort,
		OutboundMode:       outboundMode,
		OutboundInterface:  outboundInterface,
		Clients:            clients,
	}

	if disc.Topology == TopologyB {
		if disc.ListenPort > 0 {
			newCfg.ListenPort = disc.ListenPort
		}
		newCfg.DispatcherPort = 9009
	}
	if newCfg.DispatcherPort <= 0 {
		newCfg.DispatcherPort = 9009
	}
	if newCfg.Path == "" {
		newCfg.Path = "/cdn-bridge/"
	}
	if newCfg.Mode == "" {
		newCfg.Mode = "packet-up"
	}
	if newCfg.UplinkMethod == "" {
		newCfg.UplinkMethod = "GET"
	}

	settingsData, err := json.MarshalIndent(newCfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	if err := atomicWriteFile(settingsPath, settingsData, 0600); err != nil {
		return nil, fmt.Errorf("write settings: %w", err)
	}

	marker := MigrationMarker{
		MigratedAt:           time.Now().UTC().Format(time.RFC3339),
		SourcePath:           disc.ConfigPath,
		SourceChecksum:       disc.Checksums["config.json"],
		Topology:             disc.Topology,
		OriginPort:           disc.ListenPort,
		AssignedLoopbackPort: newCfg.ListenPort,
	}
	markerData, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal marker: %w", err)
	}
	if err := atomicWriteFile(markerPath, markerData, 0600); err != nil {
		return nil, fmt.Errorf("write marker: %w", err)
	}

	_ = SaveRuntimeDecision(dataDir, RuntimeDecision{
		ActiveGeneration:  "new",
		MigrationStatus:   "migrated",
		DecidedAt:         time.Now().UTC().Format(time.RFC3339),
		GenerationCounter: 1,
	})

	return newCfg, nil
}

// GetRuntimeDecision reads runtime-decision.json or returns the canonical C-only default ("new" / "migrated").
func GetRuntimeDecision(dataDir string) (RuntimeDecision, error) {
	p := filepath.Join(dataDir, "xray", "runtime-decision.json")
	if !fileExists(p) {
		return RuntimeDecision{
			ActiveGeneration: "new",
			MigrationStatus:  "migrated",
		}, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return RuntimeDecision{}, err
	}
	var d RuntimeDecision
	if err := json.Unmarshal(data, &d); err != nil {
		return RuntimeDecision{}, err
	}
	if d.ActiveGeneration == "" || d.ActiveGeneration == "legacy" {
		d.ActiveGeneration = "new"
	}
	return d, nil
}

// SaveRuntimeDecision atomically writes runtime-decision.json.
func SaveRuntimeDecision(dataDir string, d RuntimeDecision) error {
	d.DecidedAt = time.Now().UTC().Format(time.RFC3339)
	if d.ActiveGeneration == "" || d.ActiveGeneration == "legacy" {
		d.ActiveGeneration = "new"
	}
	p := filepath.Join(dataDir, "xray", "runtime-decision.json")
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(p, data, 0600)
}
