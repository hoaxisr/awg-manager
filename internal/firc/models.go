package firc

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultSocketPath is the default Unix domain socket where fircd listens.
	DefaultSocketPath = "/tmp/firc/firc.sock"

	// DefaultConfigDir is the Entware configuration directory for fircd.
	DefaultConfigDir = "/opt/etc/firc"

	// DefaultFircdBinary is the default path to the fircd daemon binary.
	DefaultFircdBinary = "/opt/sbin/fircd"

	// AltFircdBinary is the fallback path used by standalone Entware firc packages.
	AltFircdBinary = "/opt/bin/fircd"

	// DefaultTunvlessBinary is the default path to the tunvless binary.
	DefaultTunvlessBinary = "/opt/sbin/tunvless"

	// AltTunvlessBinary is the fallback path used by standalone Entware firc packages.
	AltTunvlessBinary = "/opt/bin/tunvless"

	// DefaultDNSProxyAddr is the local DNS proxy address used by fircd.
	DefaultDNSProxyAddr = "127.0.0.1"

	// DefaultDNSProxyPort is the local DNS proxy port used by fircd.
	DefaultDNSProxyPort = 3553

	// DefaultMixedInboundPort is the local SOCKS5/HTTP inbound port (1099) kept active
	// for local Happ/Telegram/SOCKS5 proxy compatibility.
	DefaultMixedInboundPort = 1099

	// TunnelNodePool is the special node selector ("*") that routes a tunnel inbound
	// through the tunnel's active node pool rather than pinning a single node.
	TunnelNodePool = "*"
)

func parseYAMLSecondsNode(node *yaml.Node) (int, error) {
	if node == nil || node.Kind == 0 {
		return 0, nil
	}
	var iv int
	if err := node.Decode(&iv); err == nil {
		return iv, nil
	}
	var sv string
	if err := node.Decode(&sv); err != nil {
		return 0, err
	}
	sv = strings.TrimSpace(sv)
	if sv == "" {
		return 0, nil
	}
	if n, err := strconv.Atoi(sv); err == nil {
		return n, nil
	}
	d, err := time.ParseDuration(sv)
	if err != nil {
		return 0, err
	}
	return int(d.Seconds()), nil
}

func formatYAMLSeconds(sec int) string {
	if sec <= 0 {
		return "0s"
	}
	return strconv.Itoa(sec) + "s"
}

// TunnelUplink represents the uplink binding of a firc tunnel ("auto", "iface", or "tunnel").
type TunnelUplink struct {
	Kind string `json:"kind" yaml:"kind"`
	Ref  string `json:"ref,omitempty" yaml:"ref,omitempty"`
}

func (u TunnelUplink) MarshalJSON() ([]byte, error) {
	type plain TunnelUplink
	p := plain(u)
	p.Kind = strings.TrimSpace(p.Kind)
	p.Ref = strings.TrimSpace(p.Ref)
	if p.Kind == "" {
		p.Kind = "auto"
		p.Ref = ""
	}
	return json.Marshal(p)
}

func (u TunnelUplink) MarshalYAML() (interface{}, error) {
	kind := strings.TrimSpace(u.Kind)
	ref := strings.TrimSpace(u.Ref)
	if kind == "" || kind == "auto" {
		return "auto", nil
	}
	if ref != "" {
		return kind + ":" + ref, nil
	}
	return kind, nil
}

func (u *TunnelUplink) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var raw string
		if err := value.Decode(&raw); err != nil {
			return err
		}
		raw = strings.TrimSpace(raw)
		if raw == "" || raw == "auto" {
			u.Kind = "auto"
			u.Ref = ""
			return nil
		}
		if kind, ref, ok := strings.Cut(raw, ":"); ok {
			u.Kind = strings.TrimSpace(kind)
			u.Ref = strings.TrimSpace(ref)
			return nil
		}
		u.Kind = raw
		u.Ref = ""
		return nil
	}
	type rawUplink TunnelUplink
	var ru rawUplink
	if err := value.Decode(&ru); err != nil {
		return err
	}
	if strings.TrimSpace(ru.Kind) == "" {
		ru.Kind = "auto"
		ru.Ref = ""
	}
	*u = TunnelUplink(ru)
	return nil
}

// TunnelSource represents a single node link or subscription in a firc tunnel.
type TunnelSource struct {
	ID       string            `json:"id,omitempty" yaml:"id,omitempty"`
	Kind     string            `json:"kind" yaml:"kind"` // "link" | "subscription"
	Link     string            `json:"link,omitempty" yaml:"link,omitempty"`
	Name     string            `json:"name,omitempty" yaml:"name,omitempty"`
	URL      string            `json:"url,omitempty" yaml:"url,omitempty"`
	Interval int               `json:"interval,omitempty" yaml:"interval,omitempty"`
	Headers  map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
}

func (s TunnelSource) MarshalJSON() ([]byte, error) {
	if s.Kind == "subscription" || (s.Kind == "" && s.URL != "") {
		return json.Marshal(struct {
			ID       string            `json:"id,omitempty"`
			Kind     string            `json:"kind"`
			Name     string            `json:"name,omitempty"`
			URL      string            `json:"url,omitempty"`
			Interval int               `json:"interval,omitempty"`
			Headers  map[string]string `json:"headers,omitempty"`
		}{
			ID:       s.ID,
			Kind:     "subscription",
			Name:     s.Name,
			URL:      s.URL,
			Interval: s.Interval,
			Headers:  s.Headers,
		})
	}
	return json.Marshal(struct {
		ID   string `json:"id,omitempty"`
		Kind string `json:"kind"`
		Link string `json:"link,omitempty"`
	}{
		ID:   s.ID,
		Kind: "link",
		Link: s.Link,
	})
}

func (s TunnelSource) MarshalYAML() (interface{}, error) {
	if s.Kind == "subscription" || (s.Kind == "" && s.URL != "") {
		type subSpec struct {
			Name     string            `yaml:"name,omitempty"`
			URL      string            `yaml:"url"`
			Interval string            `yaml:"interval,omitempty"`
			Headers  map[string]string `yaml:"headers,omitempty"`
		}
		var iv string
		if s.Interval > 0 {
			iv = formatYAMLSeconds(s.Interval)
		}
		return struct {
			ID           string  `yaml:"id,omitempty"`
			Subscription subSpec `yaml:"subscription"`
		}{
			ID: s.ID,
			Subscription: subSpec{
				Name:     s.Name,
				URL:      s.URL,
				Interval: iv,
				Headers:  s.Headers,
			},
		}, nil
	}
	return struct {
		ID   string `yaml:"id,omitempty"`
		Link string `yaml:"link"`
	}{
		ID:   s.ID,
		Link: s.Link,
	}, nil
}

func (s *TunnelSource) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		ID           string            `yaml:"id"`
		Kind         string            `yaml:"kind"`
		Link         string            `yaml:"link"`
		Name         string            `yaml:"name"`
		URL          string            `yaml:"url"`
		Interval     yaml.Node         `yaml:"interval"`
		Headers      map[string]string `yaml:"headers"`
		Subscription *struct {
			Name     string            `yaml:"name"`
			URL      string            `yaml:"url"`
			Interval yaml.Node         `yaml:"interval"`
			Headers  map[string]string `yaml:"headers"`
		} `yaml:"subscription"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	s.ID = raw.ID
	if raw.Subscription != nil {
		s.Kind = "subscription"
		s.Name = raw.Subscription.Name
		s.URL = raw.Subscription.URL
		s.Headers = raw.Subscription.Headers
		sec, err := parseYAMLSecondsNode(&raw.Subscription.Interval)
		if err != nil {
			return err
		}
		s.Interval = sec
		return nil
	}
	s.Kind = raw.Kind
	s.Link = raw.Link
	s.Name = raw.Name
	s.URL = raw.URL
	s.Headers = raw.Headers
	if s.Kind == "" {
		if s.Link != "" {
			s.Kind = "link"
		} else if s.URL != "" {
			s.Kind = "subscription"
		}
	}
	sec, err := parseYAMLSecondsNode(&raw.Interval)
	if err != nil {
		return err
	}
	s.Interval = sec
	return nil
}

// TunnelInbound represents a local SOCKS5 inbound listener bound to a tunnel.
type TunnelInbound struct {
	Port     uint16 `json:"port" yaml:"port"`
	Listen   string `json:"listen,omitempty" yaml:"listen,omitempty"`
	Node     string `json:"node,omitempty" yaml:"node,omitempty"`
	User     string `json:"user,omitempty" yaml:"user,omitempty"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`
}

// TunnelAdvanced holds advanced TLS, timeout, and kernelTcp settings for a tunnel.
type TunnelAdvanced struct {
	Timeout      int    `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Insecure     bool   `json:"insecure" yaml:"insecure"`
	CA           string `json:"ca,omitempty" yaml:"ca,omitempty"`
	Fingerprint  string `json:"fingerprint,omitempty" yaml:"fingerprint,omitempty"`
	KernelTCP    bool   `json:"kernelTcp" yaml:"kernelTcp"`
	KernelTCPSet bool   `json:"-" yaml:"-"`
}

func (a *TunnelAdvanced) UnmarshalJSON(data []byte) error {
	var raw struct {
		Timeout     json.RawMessage `json:"timeout"`
		Insecure    bool            `json:"insecure"`
		CA          string          `json:"ca"`
		Fingerprint string          `json:"fingerprint"`
		KernelTCP   *bool           `json:"kernelTcp"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.Timeout) > 0 && string(raw.Timeout) != "null" {
		var sec int
		if err := json.Unmarshal(raw.Timeout, &sec); err == nil {
			a.Timeout = sec
		} else {
			var str string
			if err := json.Unmarshal(raw.Timeout, &str); err == nil {
				s := strings.TrimSuffix(strings.TrimSpace(str), "s")
				if v, pErr := strconv.Atoi(s); pErr == nil {
					a.Timeout = v
				}
			}
		}
	}
	a.Insecure = raw.Insecure
	a.CA = raw.CA
	a.Fingerprint = raw.Fingerprint
	if raw.KernelTCP != nil {
		a.KernelTCP = *raw.KernelTCP
		a.KernelTCPSet = true
	} else {
		a.KernelTCP = true
		a.KernelTCPSet = true
	}
	return nil
}

func (a TunnelAdvanced) MarshalYAML() (interface{}, error) {
	var timeoutStr string
	if a.Timeout > 0 {
		timeoutStr = formatYAMLSeconds(a.Timeout)
	}
	return struct {
		Timeout     string `yaml:"timeout,omitempty"`
		Insecure    bool   `yaml:"insecure"`
		CA          string `yaml:"ca,omitempty"`
		Fingerprint string `yaml:"fingerprint,omitempty"`
		KernelTCP   bool   `yaml:"kernelTcp"`
	}{
		Timeout:     timeoutStr,
		Insecure:    a.Insecure,
		CA:          a.CA,
		Fingerprint: a.Fingerprint,
		KernelTCP:   a.KernelTCP,
	}, nil
}

func (a *TunnelAdvanced) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Timeout     yaml.Node `yaml:"timeout"`
		Insecure    bool      `yaml:"insecure"`
		CA          string    `yaml:"ca"`
		Fingerprint string    `yaml:"fingerprint"`
		KernelTCP   *bool     `yaml:"kernelTcp"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	sec, err := parseYAMLSecondsNode(&raw.Timeout)
	if err != nil {
		return err
	}
	a.Timeout = sec
	a.Insecure = raw.Insecure
	a.CA = raw.CA
	a.Fingerprint = raw.Fingerprint
	if raw.KernelTCP != nil {
		a.KernelTCP = *raw.KernelTCP
		a.KernelTCPSet = true
	} else {
		a.KernelTCP = true
		a.KernelTCPSet = true
	}
	return nil
}

// Tunnel represents a firc proxy tunnel entry in tunnels.yaml / /api/v1/tunnels.
type Tunnel struct {
	ID          string          `json:"id" yaml:"id"`
	Device      string          `json:"device" yaml:"device"`
	Description string          `json:"description,omitempty" yaml:"description,omitempty"`
	Enable      bool            `json:"enable" yaml:"enable"`
	Uplink      TunnelUplink    `json:"uplink" yaml:"uplink"`
	EndpointDNS []string        `json:"endpointDns,omitempty" yaml:"endpointDns,omitempty"`
	Sources     []TunnelSource  `json:"sources" yaml:"sources"`
	Filter      string          `json:"filter,omitempty" yaml:"filter,omitempty"`
	Order       []string        `json:"order,omitempty" yaml:"order,omitempty"`
	Exclude     []string        `json:"exclude,omitempty" yaml:"exclude,omitempty"`
	Inbounds    []TunnelInbound `json:"inbounds,omitempty" yaml:"inbounds,omitempty"`
	Active      int             `json:"active,omitempty" yaml:"active,omitempty"`
	By          string          `json:"by,omitempty" yaml:"by,omitempty"` // "order" | "latency" | "first_byte"
	Interval    int             `json:"interval,omitempty" yaml:"interval,omitempty"`
	Silence     int             `json:"silence,omitempty" yaml:"silence,omitempty"`
	Advanced    TunnelAdvanced  `json:"advanced" yaml:"advanced"`
}

func (t Tunnel) MarshalJSON() ([]byte, error) {
	type plain Tunnel
	if t.EndpointDNS == nil {
		return json.Marshal(plain(t))
	}
	return json.Marshal(struct {
		plain
		EndpointDNS []string `json:"endpointDns"`
	}{plain(t), t.EndpointDNS})
}

func (t Tunnel) MarshalYAML() (interface{}, error) {
	var endpointDNS *[]string
	if t.EndpointDNS != nil {
		endpointDNS = &t.EndpointDNS
	}
	var intervalStr, silenceStr string
	if t.Interval > 0 {
		intervalStr = formatYAMLSeconds(t.Interval)
	}
	if t.Silence > 0 {
		silenceStr = formatYAMLSeconds(t.Silence)
	}
	return struct {
		ID          string          `yaml:"id"`
		Device      string          `yaml:"device"`
		Description string          `yaml:"description,omitempty"`
		Enable      bool            `yaml:"enable"`
		Uplink      TunnelUplink    `yaml:"uplink"`
		EndpointDNS *[]string       `yaml:"endpointDns,omitempty"`
		Sources     []TunnelSource  `yaml:"sources"`
		Filter      string          `yaml:"filter,omitempty"`
		Order       []string        `yaml:"order,omitempty"`
		Exclude     []string        `yaml:"exclude,omitempty"`
		Inbounds    []TunnelInbound `yaml:"inbounds,omitempty"`
		Active      int             `yaml:"active,omitempty"`
		By          string          `yaml:"by,omitempty"`
		Interval    string          `yaml:"interval,omitempty"`
		Silence     string          `yaml:"silence,omitempty"`
		Advanced    TunnelAdvanced  `yaml:"advanced"`
	}{
		ID:          t.ID,
		Device:      t.Device,
		Description: t.Description,
		Enable:      t.Enable,
		Uplink:      t.Uplink,
		EndpointDNS: endpointDNS,
		Sources:     t.Sources,
		Filter:      t.Filter,
		Order:       t.Order,
		Exclude:     t.Exclude,
		Inbounds:    t.Inbounds,
		Active:      t.Active,
		By:          t.By,
		Interval:    intervalStr,
		Silence:     silenceStr,
		Advanced:    t.Advanced,
	}, nil
}

func (t *Tunnel) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		ID          string          `yaml:"id"`
		Device      string          `yaml:"device"`
		Description string          `yaml:"description"`
		Enable      bool            `yaml:"enable"`
		Uplink      TunnelUplink    `yaml:"uplink"`
		EndpointDNS []string        `yaml:"endpointDns"`
		Sources     []TunnelSource  `yaml:"sources"`
		Filter      string          `yaml:"filter"`
		Order       []string        `yaml:"order"`
		Exclude     []string        `yaml:"exclude"`
		Inbounds    []TunnelInbound `yaml:"inbounds"`
		Active      int             `yaml:"active"`
		By          string          `yaml:"by"`
		Interval    yaml.Node       `yaml:"interval"`
		Silence     yaml.Node       `yaml:"silence"`
		Advanced    TunnelAdvanced  `yaml:"advanced"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	intervalSec, err := parseYAMLSecondsNode(&raw.Interval)
	if err != nil {
		return err
	}
	silenceSec, err := parseYAMLSecondsNode(&raw.Silence)
	if err != nil {
		return err
	}
	t.ID = raw.ID
	t.Device = raw.Device
	t.Description = raw.Description
	t.Enable = raw.Enable
	t.Uplink = raw.Uplink
	if strings.TrimSpace(t.Uplink.Kind) == "" {
		t.Uplink.Kind = "auto"
		t.Uplink.Ref = ""
	}
	t.EndpointDNS = raw.EndpointDNS
	t.Sources = raw.Sources
	t.Filter = raw.Filter
	t.Order = raw.Order
	t.Exclude = raw.Exclude
	t.Inbounds = raw.Inbounds
	t.Active = raw.Active
	t.By = raw.By
	t.Interval = intervalSec
	t.Silence = silenceSec
	t.Advanced = raw.Advanced
	return nil
}

// PutTunnelsResponse is returned by PUT /api/v1/tunnels.
type PutTunnelsResponse struct {
	Tunnels   []Tunnel `json:"tunnels"`
	Restarted []string `json:"restarted"`
	Updated   []string `json:"updated"`
}

// NodeState represents the live state of a single node inside a tunnel.
type NodeState struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	State      string `json:"state"` // "active" | "reserve" | "down" | "excluded" | "missing" | "skipped"
	Since      int64  `json:"since"`
	SkipReason string `json:"skipReason,omitempty"`
	V6         bool   `json:"v6,omitempty"`
}

// SubUsage represents traffic quota and expiration telemetry from a subscription header.
type SubUsage struct {
	Used   uint64 `json:"used"`
	Total  uint64 `json:"total"`
	Expire int64  `json:"expire"`
}

// SubState represents the live state of a subscription source inside a tunnel.
type SubState struct {
	Name     string    `json:"name"`
	Nodes    int       `json:"nodes"`
	LastOk   int64     `json:"lastOk"`
	LastTry  int64     `json:"lastTry"`
	Error    string    `json:"error,omitempty"`
	Fetching bool      `json:"fetching"`
	Usage    *SubUsage `json:"usage,omitempty"`
}

// InboundState represents the live state and throughput of a tunnel SOCKS5 inbound.
type InboundState struct {
	Port  uint16 `json:"port"`
	State string `json:"state"`
	Why   string `json:"why,omitempty"`
	RxBps uint64 `json:"rxBps"`
	TxBps uint64 `json:"txBps"`
}

// TunnelState represents the live runtime telemetry of a firc tunnel from GET /api/v1/tunnels/state.
type TunnelState struct {
	ID            string         `json:"id"`
	Device        string         `json:"device"`
	Status        string         `json:"status"` // "starting" | "up" | "no_node" | "backoff" | "bad_config" | "waiting" | "uplink_down" | "router" | "off"
	Why           string         `json:"why,omitempty"`
	Active        []string       `json:"active"`
	Nodes         []NodeState    `json:"nodes"`
	Since         int64          `json:"since"`
	BackoffS      int            `json:"backoffS"`
	LastExit      int            `json:"lastExit"`
	UplinkOk      bool           `json:"uplinkOk"`
	TCPPort       uint16         `json:"tcpPort"`
	TCPSkipped    []string       `json:"tcpSkipped"`
	TCPOff        string         `json:"tcpOff"`
	RxBps         uint64         `json:"rxBps"`
	TxBps         uint64         `json:"txBps"`
	Groups        []string       `json:"groups"`
	Inbounds      []InboundState `json:"inbounds"`
	Subscriptions []SubState     `json:"subscriptions"`
}

// ProbeNodeResult represents the result of probing a single node via POST /api/v1/tunnels/probe.
type ProbeNodeResult struct {
	Key         string `json:"key"`
	Ok          bool   `json:"ok"`
	HandshakeMs *int   `json:"handshakeMs"`
	FirstByteMs *int   `json:"firstByteMs"`
	Why         string `json:"why"`
}

// ProbeResponse is returned by POST /api/v1/tunnels/probe.
type ProbeResponse struct {
	Nodes []ProbeNodeResult `json:"nodes"`
}

// PreviewNode represents a node returned by POST /api/v1/tunnels/preview.
type PreviewNode struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	IsNew      bool   `json:"isNew"`
	Excluded   bool   `json:"excluded"`
	Missing    bool   `json:"missing"`
	OverCap    bool   `json:"overCap"`
	SkipReason string `json:"skipReason,omitempty"`
}

// PreviewSub represents subscription cache status in POST /api/v1/tunnels/preview.
type PreviewSub struct {
	Name   string `json:"name"`
	Cached bool   `json:"cached"`
}

// PreviewResponse is returned by POST /api/v1/tunnels/preview.
type PreviewResponse struct {
	Nodes         []PreviewNode `json:"nodes"`
	Matched       int           `json:"matched"`
	Total         int           `json:"total"`
	Subscriptions []PreviewSub  `json:"subscriptions"`
}

// Rule represents a routing rule inside a firc Group.
type Rule struct {
	ID               string `json:"id,omitempty" yaml:"id,omitempty"`
	Type             string `json:"type" yaml:"type"` // "domain" | "wild" | "DotWild" | "regex" | "subnet" | "subnet6" | "namespace"
	Rule             string `json:"rule" yaml:"rule"`
	Enable           bool   `json:"enable" yaml:"enable"`
	Proto            string `json:"proto,omitempty" yaml:"proto,omitempty"`
	Ports            string `json:"ports,omitempty" yaml:"ports,omitempty"`
	Comment          string `json:"comment,omitempty" yaml:"comment,omitempty"`
	Autolist         string `json:"autolist,omitempty" yaml:"autolist,omitempty"`
	AutolistByDomain bool   `json:"autolistByDomain,omitempty" yaml:"autolistByDomain,omitempty"`
}

// GroupDevices specifies client device selection (MACs, IPs, Keenetic policies) for a Group.
type GroupDevices struct {
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty" yaml:"deny,omitempty"`
}

// GroupResolve configures DNS resolution behavior for a Group.
type GroupResolve struct {
	Tunnel bool   `json:"tunnel" yaml:"tunnel"`
	Server string `json:"server,omitempty" yaml:"server,omitempty"`
}

// GroupCheck configures per-group service availability probing for multi-interface groups.
type GroupCheck struct {
	Mode             string `json:"mode,omitempty" yaml:"mode,omitempty"`
	URL              string `json:"url,omitempty" yaml:"url,omitempty"`
	IntervalSec      uint32 `json:"intervalSec,omitempty" yaml:"intervalSec,omitempty"`
	TimeoutMs        uint32 `json:"timeoutMs,omitempty" yaml:"timeoutMs,omitempty"`
	FailThreshold    uint32 `json:"failThreshold,omitempty" yaml:"failThreshold,omitempty"`
	RecoverThreshold uint32 `json:"recoverThreshold,omitempty" yaml:"recoverThreshold,omitempty"`
}

// GroupList configures an external rule list (.srs, sing-box JSON, Clash YAML, domain list) for a Group.
type GroupList struct {
	URL        string          `json:"url" yaml:"url"`
	Interval   uint32          `json:"interval,omitempty" yaml:"interval,omitempty"`
	LastUpdate int64           `json:"lastUpdate,omitempty" yaml:"lastUpdate,omitempty"`
	Overrides  []any           `json:"overrides,omitempty" yaml:"overrides,omitempty"`
	RulesTotal int             `json:"rulesTotal,omitempty" yaml:"-"`
	Sync       json.RawMessage `json:"sync,omitempty" yaml:"-"`
}

func (gl *GroupList) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		URL             string `yaml:"url"`
		Interval        uint32 `yaml:"interval"`
		LastUpdateCamel int64  `yaml:"lastUpdate"`
		LastUpdateSnake int64  `yaml:"last_update"`
		Overrides       []any  `yaml:"overrides"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	gl.URL = raw.URL
	gl.Interval = raw.Interval
	if raw.LastUpdateCamel != 0 {
		gl.LastUpdate = raw.LastUpdateCamel
	} else {
		gl.LastUpdate = raw.LastUpdateSnake
	}
	gl.Overrides = raw.Overrides
	return nil
}

// GroupIfaceState describes the health of one interface in a multi-interface failover group.
type GroupIfaceState struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Since int64  `json:"since"`
	Error string `json:"error,omitempty"`
}

// GroupInterface can serialize as a single string or an array of 1..4 interface names in firc.
type GroupInterface []string

func (gi GroupInterface) MarshalJSON() ([]byte, error) {
	if len(gi) == 0 {
		return json.Marshal("")
	}
	if len(gi) == 1 {
		return json.Marshal(gi[0])
	}
	return json.Marshal([]string(gi))
}

func (gi *GroupInterface) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		if single == "" {
			*gi = nil
		} else {
			*gi = GroupInterface{single}
		}
		return nil
	}
	var multi []string
	if err := json.Unmarshal(data, &multi); err != nil {
		return err
	}
	*gi = GroupInterface(multi)
	return nil
}

func (gi GroupInterface) MarshalYAML() (interface{}, error) {
	if len(gi) == 0 {
		return "", nil
	}
	if len(gi) == 1 {
		return gi[0], nil
	}
	return []string(gi), nil
}

func (gi *GroupInterface) UnmarshalYAML(value *yaml.Node) error {
	var single string
	if err := value.Decode(&single); err == nil {
		if single == "" {
			*gi = nil
		} else {
			*gi = GroupInterface{single}
		}
		return nil
	}
	var multi []string
	if err := value.Decode(&multi); err != nil {
		return err
	}
	*gi = GroupInterface(multi)
	return nil
}

// Group represents a routing group in groups.yaml / /api/v1/groups.
type Group struct {
	ID         string            `json:"id,omitempty" yaml:"id,omitempty"`
	Name       string            `json:"name" yaml:"name"`
	Interface  GroupInterface    `json:"interface" yaml:"interface"`
	Enable     bool              `json:"enable" yaml:"enable"`
	Color      string            `json:"color,omitempty" yaml:"color,omitempty"`
	Block      string            `json:"block,omitempty" yaml:"block,omitempty"`
	Live       bool              `json:"live,omitempty" yaml:"-"`
	LiveReason string            `json:"liveReason,omitempty" yaml:"-"`
	Active     string            `json:"active,omitempty" yaml:"-"`
	Interfaces []GroupIfaceState `json:"interfaces,omitempty" yaml:"-"`
	Devices    GroupDevices      `json:"devices" yaml:"devices"`
	Resolve    GroupResolve      `json:"resolve" yaml:"resolve"`
	Check      *GroupCheck       `json:"check,omitempty" yaml:"check,omitempty"`
	List       *GroupList        `json:"list,omitempty" yaml:"list,omitempty"`
	Rules      []Rule            `json:"rules,omitempty" yaml:"rules,omitempty"`
}

// NetfilterHealth represents GET /api/v1/system/netfilter.
type NetfilterHealth struct {
	UplinkDNSVersion  int    `json:"uplinkDnsVersion,omitempty"`
	Ok                bool   `json:"ok"`
	FirstWritePending bool   `json:"firstWritePending,omitempty"`
	Error             string `json:"error,omitempty"`
	Since             int64  `json:"since,omitempty"`
}

// SystemInterface represents an interface returned by GET /api/v1/system/interfaces.
type SystemInterface struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	NDMSName string `json:"ndmsName,omitempty"`
	Role     string `json:"role,omitempty"`
}

// SystemEventGroup identifies the matched FIRC group in a DNS or bypass event.
type SystemEventGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SystemEvent represents a log, DNS, or bypass event from GET /api/v1/system/events.
type SystemEvent struct {
	Seq      uint64            `json:"seq"`
	At       int64             `json:"at"`
	Kind     string            `json:"kind"` // "log" | "dns" | "bypass"
	Level    string            `json:"level,omitempty"`
	Source   string            `json:"source,omitempty"`
	Message  string            `json:"message,omitempty"`
	Client   string            `json:"client,omitempty"`
	Name     string            `json:"name,omitempty"`
	QType    string            `json:"qtype,omitempty"`
	RCode    string            `json:"rcode,omitempty"`
	Decision string            `json:"decision,omitempty"`
	Resolver string            `json:"resolver,omitempty"`
	Fake     string            `json:"fake,omitempty"`
	Reals    []string          `json:"reals,omitempty"`
	Group    *SystemEventGroup `json:"group,omitempty"`
}

// EventsResponse is returned by GET /api/v1/system/events.
type EventsResponse struct {
	Events  []SystemEvent `json:"events"`
	Next    uint64        `json:"next"`
	Dropped uint64        `json:"dropped"`
	Boot    string        `json:"boot"`
}

// DNSStatsOwner identifies the FIRC routing group that owns a DNS stats row.
type DNSStatsOwner struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Interface string `json:"interface"`
	Blocked   bool   `json:"blocked"`
}

// DNSStatsClient represents per-client query count in GET /api/v1/system/dnsstats.
type DNSStatsClient struct {
	Address string `json:"address"`
	Queries int    `json:"queries"`
}

// DNSStatsRow represents a single domain entry in GET /api/v1/system/dnsstats.
type DNSStatsRow struct {
	Name         string           `json:"name"`
	Queries      int              `json:"queries"`
	Blocked      int              `json:"blocked"`
	Last         int64            `json:"last"`
	Owner        *DNSStatsOwner   `json:"owner"`
	Clients      []DNSStatsClient `json:"clients"`
	ClientsTotal int              `json:"clientsTotal"`
}

// DNSStatsResponse is returned by GET /api/v1/system/dnsstats.
type DNSStatsResponse struct {
	Enabled bool          `json:"enabled"`
	Window  int           `json:"window"`
	Now     int64         `json:"now"`
	Since   int64         `json:"since"`
	Queries int           `json:"queries"`
	Blocked int           `json:"blocked"`
	Domains int           `json:"domains"`
	Rows    []DNSStatsRow `json:"rows"`
	Matched int           `json:"matched"`
	Offset  int           `json:"offset"`
}

// AutolistAddressGroup identifies a FIRC group associated with an autolist address row.
type AutolistAddressGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AutolistAddress represents a single address entry in GET /api/v1/system/autolist.
type AutolistAddress struct {
	Address  string                 `json:"address"`
	Outcome  string                 `json:"outcome"`
	How      string                 `json:"how"`
	Group    string                 `json:"group"`
	Proto    string                 `json:"proto"`
	Port     uint16                 `json:"port"`
	Name     string                 `json:"name"`
	PTR      string                 `json:"ptr"`
	PTRState string                 `json:"ptrState"`
	Why      string                 `json:"why"`
	Fails    uint32                 `json:"fails"`
	Clients  []string               `json:"clients"`
	FirstAt  int64                  `json:"firstAt"`
	LastAt   int64                  `json:"lastAt"`
	Groups   []AutolistAddressGroup `json:"groups"`
}

// AutolistCandidate represents a domain candidate detected by FIRC bypass diagnostics.
type AutolistCandidate struct {
	Domain  string   `json:"domain"`
	Host    string   `json:"host"`
	Why     string   `json:"why"`
	Fails   uint32   `json:"fails"`
	Clients []string `json:"clients"`
	LastAt  int64    `json:"lastAt"`
	State   string   `json:"state"`
	Notes   any      `json:"notes,omitempty"`
}

// AutolistProbeResult represents the outcome of a manual host probe in GET /api/v1/system/autolist.
type AutolistProbeResult struct {
	Host       string `json:"host"`
	At         int64  `json:"at"`
	Verdict    string `json:"verdict"`
	Why        string `json:"why"`
	Bytes      uint64 `json:"bytes,omitempty"`
	Error      string `json:"error,omitempty"`
	Candidate  string `json:"candidate,omitempty"`
	Resolver   string `json:"resolver,omitempty"`
	Address    string `json:"address,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
}

// AutolistProbes summarizes active and recent manual host probes in GET /api/v1/system/autolist.
type AutolistProbes struct {
	UsedHour uint32                `json:"usedHour"`
	Queued   uint32                `json:"queued"`
	Running  any                   `json:"running"`
	Results  []AutolistProbeResult `json:"results"`
}

// AutolistStatus is returned by GET /api/v1/system/autolist.
type AutolistStatus struct {
	Enabled      bool                `json:"enabled"`
	Running      bool                `json:"running"`
	Failures     uint32              `json:"failures"`
	Listed       uint32              `json:"listed"`
	WhyOff       string              `json:"whyOff,omitempty"`
	Checking     bool                `json:"checking"`
	CheckedAt    int64               `json:"checkedAt"`
	SwitchedOff  uint32              `json:"switchedOff"`
	SwitchedOn   uint32              `json:"switchedOn"`
	Mode         string              `json:"mode"`
	Group        string              `json:"group"`
	GroupID      string              `json:"groupId,omitempty"`
	GroupName    string              `json:"groupName,omitempty"`
	Interface    string              `json:"interface,omitempty"`
	FailuresDay  uint32              `json:"failuresDay"`
	AmbiguousDay uint32              `json:"ambiguousDay"`
	WanWarning   any                 `json:"wanWarning,omitempty"`
	AddedHour    uint32              `json:"addedHour"`
	Addresses    []AutolistAddress   `json:"addresses"`
	Candidates   []AutolistCandidate `json:"candidates"`
	Probes       AutolistProbes      `json:"probes"`
}

// SettingsResponse is returned by GET /api/v1/system/settings.
type SettingsResponse struct {
	Boot           string                 `json:"boot"`
	Restarting     bool                   `json:"restarting"`
	PendingRestart []string               `json:"pendingRestart"`
	Settings       map[string]interface{} `json:"settings"`
	Classes        map[string]string      `json:"classes"`
}

// RecoveryMarker is persisted atomically to recovery-state.json when an engine transition
// or runtime rollback fails, surviving process restarts until verified reconciliation succeeds.
type RecoveryMarker struct {
	Required           bool   `json:"required"`
	Reason             string `json:"reason"`
	TransitionID       string `json:"transitionId,omitempty"`
	ParentTransitionID string `json:"parentTransitionId,omitempty"`
	PreviousReason     string `json:"previousReason,omitempty"`
	TargetEngine       string `json:"targetEngine,omitempty"`
	PreviousEngine     string `json:"previousEngine,omitempty"`
	FailedStep         string `json:"failedStep,omitempty"`
	LastCompleted      string `json:"lastCompletedStep,omitempty"`
	LastPersistError   string `json:"lastPersistError,omitempty"`
	TimestampUnixMs    int64  `json:"timestampUnixMs"`
}

// ReconcileReport describes the verified steps performed by Operator.ReconcileRecovery.
type ReconcileReport struct {
	TargetEngine           string   `json:"targetEngine"`
	TargetResolutionSource string   `json:"targetResolutionSource"`
	StoppedEngines         []string `json:"stoppedEngines,omitempty"`
	StepsCompleted         []string `json:"stepsCompleted,omitempty"`
	TunnelsVerified        int      `json:"tunnelsVerified"`
	GroupsVerified         int      `json:"groupsVerified"`
	Compensated            bool     `json:"compensated,omitempty"`
	RestoredEngine         string   `json:"restoredEngine,omitempty"`
	Verified               bool     `json:"verified"`
	ClearedRecovery        bool     `json:"clearedRecovery"`
	RecoveryCleared        bool     `json:"recoveryCleared"`
	TimestampUnixMs        int64    `json:"timestampUnixMs"`
}

// Status summarizes the installation, runtime, and configuration state of firc.
type Status struct {
	FircdInstalled    bool             `json:"fircdInstalled"`
	TunvlessInstalled bool             `json:"tunvlessInstalled"`
	Running           bool             `json:"running"`
	Selected          bool             `json:"selected"`
	RecoveryRequired  bool             `json:"recoveryRequired,omitempty"`
	RecoveryReason    string           `json:"recoveryReason,omitempty"`
	RecoveryMarker    *RecoveryMarker  `json:"recoveryMarker,omitempty"`
	SocketPath        string           `json:"socketPath"`
	DNSProxyEndpoint  string           `json:"dnsProxyEndpoint"`
	InboundPort       uint16           `json:"inboundPort"`
	TunnelsCount      int              `json:"tunnelsCount"`
	GroupsCount       int              `json:"groupsCount"`
	Netfilter         *NetfilterHealth `json:"netfilter,omitempty"`
	LastError         string           `json:"lastError,omitempty"`
}

// ProxyGroup defines an AWGM-managed fallback group of interfaces (up to 4) for FIRC.
type ProxyGroup struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Interfaces []string `json:"interfaces"`
}

// KeenDNSCloudConfig controls routing Keenetic Cloud / KeenDNS traffic through FIRC.
// LastSyncError and LastSyncAt are transient runtime status fields stripped before persistence.
type KeenDNSCloudConfig struct {
	Enabled       bool     `json:"enabled"`
	ProxyGroupID  string   `json:"proxyGroupId,omitempty"`
	Interfaces    []string `json:"interfaces,omitempty"`
	LastSyncError string   `json:"lastSyncError,omitempty"`
	LastSyncAt    int64    `json:"lastSyncAt,omitempty"`
}

// InboundServerItem describes a selectable AWGM or Keenetic system VPN server and its live status.
type InboundServerItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"` // "awgm" | "system"
	Running  bool   `json:"running"`
	Detail   string `json:"detail"`
}

// ServerRoutingConfig controls routing inbound VPN server traffic through FIRC rules.
type ServerRoutingConfig struct {
	EnabledServers   []string `json:"enabledServers"`
	AwgServer        bool     `json:"awgServer"`
	WdttServer       bool     `json:"wdttServer"`
	FreeTurnServer   bool     `json:"freeTurnServer"`
	VlessXhttpServer bool     `json:"vlessXhttpServer"`
}

// IsServerEnabled reports whether the given server ID is enabled in ServerRoutingConfig.
func (sr ServerRoutingConfig) IsServerEnabled(id string) bool {
	for _, s := range sr.EnabledServers {
		if s == id {
			return true
		}
	}
	switch id {
	case "awg_server":
		return sr.AwgServer
	case "wdtt_server":
		return sr.WdttServer
	case "freeturn_server":
		return sr.FreeTurnServer
	case "vless_xhttp_server":
		return sr.VlessXhttpServer
	}
	return false
}

// SetServerEnabled enables or disables the given server ID in ServerRoutingConfig.
func (sr *ServerRoutingConfig) SetServerEnabled(id string, enabled bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	var updated []string
	found := false
	for _, s := range sr.EnabledServers {
		if s == id {
			found = true
			if enabled {
				updated = append(updated, s)
			}
			continue
		}
		updated = append(updated, s)
	}
	if enabled && !found {
		updated = append(updated, id)
	}
	sr.EnabledServers = updated
	switch id {
	case "awg_server":
		sr.AwgServer = enabled
	case "wdtt_server":
		sr.WdttServer = enabled
	case "freeturn_server":
		sr.FreeTurnServer = enabled
	case "vless_xhttp_server":
		sr.VlessXhttpServer = enabled
	}
}

// ServerRoutingTxFaultHooks provides deterministic fault-injection hooks for testing
// transactional FIRC server routing participation in composite ingress transactions.
type ServerRoutingTxFaultHooks struct {
	BeforeSaveExt   func(txID string) error
	BeforeReconcile func(txID string) error
	BeforeRollback  func(txID string) error
}

// ExtConfig stores AWGM extensions for FIRC (Fallback Proxy Groups, KeenDNS Cloud, Inbound VPN Servers).
type ExtConfig struct {
	GlobalTarget     string              `json:"globalTarget,omitempty"`
	ProxyGroups      []ProxyGroup        `json:"proxyGroups"`
	KeenDNSCloud     KeenDNSCloudConfig  `json:"keenDnsCloud"`
	ServerRouting    ServerRoutingConfig `json:"serverRouting"`
	AvailableServers []InboundServerItem `json:"availableServers,omitempty"`
}
