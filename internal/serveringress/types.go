package serveringress

import (
	"context"
	"errors"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
)

type CDNRouteRule = cdndispatcher.CDNRouteRule
type CDNDispatcherConfig = cdndispatcher.Config

var (
	ErrRecoveryRequired        = errors.New("recovery required: coordinator has pending recovery or conflict")
	ErrCorruptJournal          = errors.New("corrupt transaction journal")
	ErrRecoveryConflict        = errors.New("recovery conflict: component state diverges from committed transaction")
	ErrLocked                  = ErrLockConflict
	ErrPlanStale               = errors.New("plan is stale: system state has changed")
	ErrPointOfNoReturn         = errors.New("point of no return: transaction cannot be cancelled during finalize")
	ErrKeenDNSConflict         = errors.New("KeenDNS publication name conflict")
	ErrKeenDNSUnavailable      = errors.New("KeenDNS subsystem unavailable or unconfigured")
	ErrKeenDNSReadbackMismatch = errors.New("KeenDNS publication readback verification failed")
)

type StateFingerprintFunc func(ctx context.Context) (string, error)
type ReadinessProbeFunc func(ctx context.Context, topology IngressTopology) error

// KeenDNSPublicationCandidate describes a desired KeenDNS 4th-level publication (`ip http proxy <label>`).
type KeenDNSPublicationCandidate struct {
	Label        string   `json:"label"`
	FQDN         string   `json:"fqdn,omitempty"`
	UpstreamHost string   `json:"upstream_host,omitempty"` // default "127.0.0.1"
	UpstreamPort int      `json:"upstream_port,omitempty"` // default 9009
	Services     []string `json:"services,omitempty"`      // e.g. ["xray", "tgwebproxy"]
}

const (
	KeenDNSActionCreate = "create"
	KeenDNSActionReuse  = "reuse"

	KeenDNSStatePrepared        = "prepared"
	KeenDNSStateReused          = "reused"
	KeenDNSStatePrecheckAborted = "precheck_aborted"
	KeenDNSStateWritePending    = "write_pending"
	KeenDNSStateApplied         = "applied"
	KeenDNSStateAmbiguous       = "ambiguous"

	KeenDNSPreStateAbsent = "absent"
)

// KeenDNSJournalRecord tracks the pre-mutation snapshot, expected post-state, and ownership of a KeenDNS publication in TransactionJournal.
type KeenDNSJournalRecord struct {
	Label                   string                                `json:"label"`
	FQDN                    string                                `json:"fqdn"`
	UpstreamHost            string                                `json:"upstream_host"`
	UpstreamPort            int                                   `json:"upstream_port"`
	ActionMode              string                                `json:"action_mode,omitempty"`
	State                   string                                `json:"state,omitempty"`
	ExistedBefore           bool                                  `json:"existed_before"`
	CreatedByTx             bool                                  `json:"created_by_tx"`
	WriteIntent             bool                                  `json:"write_intent,omitempty"`
	WriteAttempted          bool                                  `json:"write_attempted,omitempty"`
	PreConfigFingerprint    string                                `json:"pre_config_fingerprint,omitempty"`
	PreviousRaw             string                                `json:"previous_raw,omitempty"`
	ExpectedAppliedRaw      string                                `json:"expected_applied_raw,omitempty"`
	ExpectedPostFingerprint string                                `json:"expected_post_fingerprint,omitempty"`
	AppliedRaw              string                                `json:"applied_raw,omitempty"`
	AppliedFingerprint      string                                `json:"applied_fingerprint,omitempty"`
	Services                []string                              `json:"services,omitempty"`
	onBeforeWrite           func(rec *KeenDNSJournalRecord) error `json:"-"`
}

// SetBeforeWriteHook registers a callback invoked inside ApplyAndVerifyPublication
// only after the uncached precheck passes and immediately before UpsertPublication is called.
func (r *KeenDNSJournalRecord) SetBeforeWriteHook(fn func(rec *KeenDNSJournalRecord) error) {
	if r == nil {
		return
	}
	r.onBeforeWrite = fn
}

// KeenDNSPublicationView represents an existing HTTP proxy / KeenDNS publication discovered on the router.
type KeenDNSPublicationView struct {
	Label                 string `json:"label"`
	FQDN                  string `json:"fqdn"`
	UpstreamProto         string `json:"upstream_proto,omitempty"`
	UpstreamHost          string `json:"upstream_host"`
	UpstreamPort          int    `json:"upstream_port"`
	DomainNDNS            bool   `json:"domain_ndns"`
	Public                bool   `json:"public"`
	Auth                  bool   `json:"auth"`
	CompatibleWithIngress bool   `json:"compatible_with_ingress"`
	RuntimeActive         bool   `json:"runtime_active"`
	ConflictReason        string `json:"conflict_reason,omitempty"`
}

// KeenDNSDiscovery summarizes KeenDNS availability, access mode, and existing 4th-level publications.
type KeenDNSDiscovery struct {
	State         string                   `json:"state"` // "supported", "unconfigured", "unsupported", "forbidden", "read_error"
	StatusMessage string                   `json:"status_message,omitempty"`
	BaseFQDN      string                   `json:"base_fqdn,omitempty"`
	Name          string                   `json:"name,omitempty"`
	Zone          string                   `json:"zone,omitempty"`
	AccessMode    string                   `json:"access_mode,omitempty"` // "direct", "cloud", "disabled"
	DirectAccess  bool                     `json:"direct_accessible"`
	AcmeV2        bool                     `json:"acme_v2"`
	SSLEnabled    bool                     `json:"ssl_enabled"`
	WANAddress    string                   `json:"wan_address,omitempty"`
	WANPrivate    bool                     `json:"wan_private,omitempty"`
	Publications  []KeenDNSPublicationView `json:"publications,omitempty"`
}

// KeenDNSOperator coordinates discovery, transactional preparation, verified apply, and rollback of KeenDNS publications.
type KeenDNSOperator interface {
	Discover(ctx context.Context, dispatcherPort int) (KeenDNSDiscovery, error)
	PreparePublication(ctx context.Context, req KeenDNSPublicationCandidate) (KeenDNSJournalRecord, error)
	ApplyAndVerifyPublication(ctx context.Context, rec *KeenDNSJournalRecord) error
	RollbackPublication(ctx context.Context, rec KeenDNSJournalRecord) error
}

type IngressTelegramCandidate struct {
	Enabled        bool
	Scenario       string
	NetworkPreset  string
	IsCDN          *bool
	DirectHost     string
	DirectPort     int
	TlsDomain      string
	ListenPort     int
	BackendPort    int
	AdminPort      int
	PublicHostname string
	CarrierMode    string
	UpstreamDevice string
	Secret         string `json:"-"` // MUST NOT be serialized into journals, plans, or logs
}

type IngressXrayCandidate struct {
	Enabled           bool
	ListenAddress     string
	ListenPort        int
	PublicDomain      string
	PublicPort        int
	ConnectionWay     string // "direct" or "external_https"
	OriginType        string // "public_address" or "cloud_address"
	OriginHost        string
	OriginScheme      string // "http" or "https"
	OriginPort        int
	Path              string
	Transport         string // "xhttp", "ws"
	Mode              string // "packet-up"
	UplinkMethod      string // "GET", "POST"
	PreserveTransport bool
	OutboundMode      string // "direct", "socks", "interface", "router"
	OutboundInterface string
	OutboundSocksPort int
	ClientRemark      string
	ClientUUID        string `json:"-"` // MUST NOT be serialized into journals, plans, or logs
}

type IngressTransactionParams struct {
	TxID                string
	ExpectedFingerprint string
	FingerprintFunc     StateFingerprintFunc
	ServerKind          string // "tgwebproxy" or "xray"
	Telegram            *IngressTelegramCandidate
	Xray                *IngressXrayCandidate
	KeenDNS             *KeenDNSPublicationCandidate
	ReadinessProbe      ReadinessProbeFunc
	OnPhaseChange       func(phase JournalPhase) error
	OnPointOfNoReturn   func(txID string) error
}

// IngressTopology describes the desired or current cross-component ingress configuration.
type IngressTopology struct {
	DispatcherEnabled       bool                         `json:"dispatcher_enabled"`
	DispatcherAddress       string                       `json:"dispatcher_address,omitempty"`
	DispatcherPort          int                          `json:"dispatcher_port"` // Origin proxy port (e.g. 9009)
	DispatcherRoutes        []cdndispatcher.CDNRouteRule `json:"dispatcher_routes,omitempty"`
	DispatcherDefaultAction string                       `json:"dispatcher_default_action,omitempty"`

	XrayEnabled        bool   `json:"xray_enabled"`
	XrayAddress        string `json:"xray_address"` // Loopback or listen address (e.g. "127.0.0.1")
	XrayPort           int    `json:"xray_port"`    // Dedicated port for Xray (e.g. 9008)
	XrayPublicHostname string `json:"xray_public_hostname,omitempty"`
	XrayPublicPort     int    `json:"xray_public_port,omitempty"`
	XrayPathPrefix     string `json:"xray_path_prefix"` // WebSocket prefix (e.g. "/cdn-bridge")
	XrayOutboundMode   string `json:"xray_outbound_mode,omitempty"`
	FircServerRouting  bool   `json:"firc_server_routing,omitempty"`

	TgEnabled        bool   `json:"tg_enabled"`
	TgScenario       string `json:"tg_scenario,omitempty"`
	TgDirectAddress  string `json:"tg_direct_address,omitempty"`
	TgDirectPort     int    `json:"tg_direct_port,omitempty"`
	TgRawAddress     string `json:"tg_raw_address,omitempty"`
	TgRawPort        int    `json:"tg_raw_port,omitempty"`
	TgWebAddress     string `json:"tg_web_address,omitempty"`
	TgWebPort        int    `json:"tg_web_port,omitempty"`
	TgPublicHostname string `json:"tg_public_hostname,omitempty"`

	// Backward compatibility fields
	PublicHostname string                `json:"public_hostname,omitempty"` // Domain for CDN ingress
	TgPort         int                   `json:"tg_port,omitempty"`         // Telegram MTProxy/WebProxy port (e.g. 8085)
	KeenDNS        *KeenDNSJournalRecord `json:"keendns,omitempty"`
}

// ResetOptions configures optional side-effect cleanup during per-service factory reset.
type ResetOptions struct {
	DeleteKeenDNS bool `json:"delete_keendns"`
}

// ResetResult describes the outcome of a per-service factory reset operation.
type ResetResult struct {
	Server          string   `json:"server"`
	Reset           bool     `json:"reset"`
	DispatcherReset bool     `json:"dispatcher_reset"`
	KeenDNSDeleted  []string `json:"keendns_deleted,omitempty"`
	KeenDNSSkipped  []string `json:"keendns_skipped,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

// FactoryResetter is an optional capability implemented by ingress services that support resetting to factory defaults.
type FactoryResetter interface {
	FactoryReset(ctx context.Context) error
}

// DispatcherSystemHookCleaner removes Keenetic nginx/netfilter hooks when the dispatcher is completely reset.
type DispatcherSystemHookCleaner interface {
	CleanupSystemHooks()
}

// KeenDNSResetOperator is an optional capability on KeenDNSOperator to safely delete non-shared ingress publications.
type KeenDNSResetOperator interface {
	DeleteIngressPublications(ctx context.Context, labels []string, allowedPorts []int) (deleted []string, skipped []string, warnings []string)
}
