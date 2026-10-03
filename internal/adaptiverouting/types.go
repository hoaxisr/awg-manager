package adaptiverouting

import "time"

type RoutingOwner string

const (
	RoutingOwnerNone    RoutingOwner = "none"
	RoutingOwnerSingbox RoutingOwner = "sing-box"
	RoutingOwnerMihomo  RoutingOwner = "mihomo"
	RoutingOwnerSusanin RoutingOwner = "susanin"
)

type EgressKind string

const (
	EgressKindKernelTunnel       EgressKind = "kernel-tunnel"
	EgressKindMihomoProxy        EgressKind = "mihomo-proxy"
	EgressKindMihomoSubscription EgressKind = "mihomo-subscription"
	EgressKindMihomoGroup        EgressKind = "mihomo-group"
	EgressKindSingboxSubscription EgressKind = "singbox-subscription"
	EgressKindSingboxOutbound    EgressKind = "singbox-outbound"
)

type EgressEngine string

const (
	EngineSystem  EgressEngine = "system"
	EngineMihomo  EgressEngine = "mihomo"
	EngineSingbox EgressEngine = "sing-box"
)

type EgressRef struct {
	Kind       EgressKind   `json:"kind"`       // kernel-tunnel | mihomo-proxy | mihomo-subscription | mihomo-group | singbox-outbound
	ResourceID string       `json:"resourceId"` // stable ID
	Engine     EgressEngine `json:"engine"`     // system | mihomo | sing-box
}

type Capabilities struct {
	TCP  bool `json:"tcp"`
	UDP  bool `json:"udp"`
	ICMP bool `json:"icmp"`
	IPv4 bool `json:"ipv4"`
	IPv6 bool `json:"ipv6"`
}

type ResolvedEgress struct {
	Ref               EgressRef    `json:"ref"`
	DisplayName       string       `json:"displayName"`
	Interface         string       `json:"interface"` // TUN or kernel interface (e.g. awgsus0, nwg0)
	Capabilities      Capabilities `json:"capabilities"`
	Digest            string       `json:"digest"`
	Available         bool         `json:"available"`
	UnavailableReason string       `json:"unavailableReason,omitempty"`
}

type SourceScope struct {
	Type       string   `json:"type"` // "all_lan" | "policy" | "interfaces" | "server_tunnel"
	PolicyID   string   `json:"policyId,omitempty"`
	Interfaces []string `json:"interfaces,omitempty"`
	TunnelTag  string   `json:"tunnelTag,omitempty"`
}

type DetectionSettings struct {
	FastIntervalSeconds   int `json:"fastIntervalSeconds"`   // default 1
	SoftIntervalSeconds   int `json:"softIntervalSeconds"`   // default 1
	JudgeIntervalSeconds  int `json:"judgeIntervalSeconds"`  // default 1
	HealthIntervalSeconds int `json:"healthIntervalSeconds"` // default 5
	TcpSynRetries         int `json:"tcpSynRetries"`         // default 2
	LateStallBytes        int `json:"lateStallBytes"`        // default 65536
}

type PersistenceConfig struct {
	OkTTLSeconds   int  `json:"okTtlSeconds"`   // default 0 (persistent until unhealthy)
	MaxEntries     int  `json:"maxEntries"`     // default 4096
	SeparateTcpUdp bool `json:"separateTcpUdp"` // default true
}

type DnsSettings struct {
	Enabled         bool     `json:"enabled"`
	Servers         []string `json:"servers"`
	RouteViaTunnel  bool     `json:"routeViaTunnel"`
	InterceptPort53 bool     `json:"interceptPort53"`
}

type Settings struct {
	Enabled           bool              `json:"enabled"`
	RoutingTableID    int               `json:"routingTableId"`   // default 105
	FwmarkMask        string            `json:"fwmarkMask"`       // default 0x30000000
	FwmarkTest        string            `json:"fwmarkTest"`       // default 0x10000000
	FwmarkOk          string            `json:"fwmarkOk"`         // default 0x20000000
	RulePriorityTest  int               `json:"rulePriorityTest"` // default 96
	RulePriorityOk    int               `json:"rulePriorityOk"`   // default 95
	Source            SourceScope       `json:"source"`
	PrimaryEgress     EgressRef         `json:"primaryEgress"`
	FallbackEgresses  []EgressRef       `json:"fallbackEgresses,omitempty"`
	FailurePolicy     string            `json:"failurePolicy"` // "direct" | "block"
	Detection         DetectionSettings `json:"detection"`
	Persistence       PersistenceConfig `json:"persistence"`
	DNS               DnsSettings       `json:"dns"`
	AlwaysFileEnabled bool              `json:"alwaysFileEnabled"`
	NeverFileEnabled  bool              `json:"neverFileEnabled"`
	AlwaysEntries     []string          `json:"alwaysEntries,omitempty"`
	NeverEntries      []string          `json:"neverEntries,omitempty"`
}

type OperationalState struct {
	AppliedGeneration string          `json:"appliedGeneration"`
	RoutingOwner      RoutingOwner    `json:"routingOwner"`
	ActiveEgress      *ResolvedEgress `json:"activeEgress,omitempty"`
	FallbackActive    bool            `json:"fallbackActive"`
	Status            string          `json:"status"` // "stopped" | "running" | "learning" | "degraded" | "recovery_required"
	LearnedTCPCount   int             `json:"learnedTcpCount"`
	LearnedUDPCount   int             `json:"learnedUdpCount"`
	TestingTCPCount   int             `json:"testingTcpCount"`
	TestingUDPCount   int             `json:"testingUdpCount"`
	AlwaysCount       int             `json:"alwaysCount"`
	NeverCount        int             `json:"neverCount"`
	LastReconcile     time.Time       `json:"lastReconcile"`
	LastError         string          `json:"lastError,omitempty"`
	RecoveryMarker    string          `json:"recoveryMarker,omitempty"`
	Installed         bool            `json:"installed"`
	Version           string          `json:"version,omitempty"`
	Binary            string          `json:"binary,omitempty"`
}

// AppliedConfig is the last configuration that reached a fully working
// runtime. It is deliberately stored separately from Settings: Settings is a
// user-editable draft, while AppliedConfig is the only configuration that may
// be restored automatically after a restart.
type AppliedConfig struct {
	Generation string         `json:"generation"`
	Settings   Settings       `json:"settings"`
	Egress     ResolvedEgress `json:"egress"`
	Committed  time.Time      `json:"committedAt"`
}

func DefaultSettings() Settings {
	return Settings{
		Enabled:          false,
		RoutingTableID:   105,
		FwmarkMask:       "0x30000000",
		FwmarkTest:       "0x10000000",
		FwmarkOk:         "0x20000000",
		RulePriorityTest: 96,
		RulePriorityOk:   95,
		Source: SourceScope{
			Type: "all_lan",
		},
		FailurePolicy: "direct",
		Detection: DetectionSettings{
			FastIntervalSeconds:   1,
			SoftIntervalSeconds:   1,
			JudgeIntervalSeconds:  1,
			HealthIntervalSeconds: 5,
			TcpSynRetries:         2,
			LateStallBytes:        65536,
		},
		Persistence: PersistenceConfig{
			OkTTLSeconds:   0,
			MaxEntries:     4096,
			SeparateTcpUdp: true,
		},
		DNS: DnsSettings{
			Enabled:         false,
			Servers:         []string{"1.1.1.1", "8.8.8.8"},
			RouteViaTunnel:  true,
			InterceptPort53:  true,
		},
		AlwaysFileEnabled: true,
		NeverFileEnabled:  true,
		AlwaysEntries:     []string{},
		NeverEntries:      []string{},
	}
}

type LogEvent struct {
	Timestamp       string `json:"timestamp"`
	Level           string `json:"level"`
	Action          string `json:"action"`
	Target          string `json:"target"`
	Message         string `json:"message"`
	Raw             string `json:"raw"`
	ResourceTitle   string `json:"resourceTitle,omitempty"`
	ResourceOrg     string `json:"resourceOrg,omitempty"`
	ResourceCountry string `json:"resourceCountry,omitempty"`
	ResourceCC      string `json:"resourceCc,omitempty"`
	ResourceIcon    string `json:"resourceIcon,omitempty"`
}
