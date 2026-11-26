package serverwizard

import (
	"errors"
	"time"
)

// Standard error codes for API responses.
const (
	ErrCodePlanStale           = "PLAN_STALE"
	ErrCodeRecoveryRequired    = "RECOVERY_REQUIRED"
	ErrCodePortConflict        = "PORT_CONFLICT"
	ErrCodeEgressUnavailable   = "EGRESS_UNAVAILABLE"
	ErrCodeOperationInProgress = "OPERATION_IN_PROGRESS"
	ErrCodePlanNotFound        = "PLAN_NOT_FOUND"
	ErrCodePlanAlreadyUsed     = "PLAN_ALREADY_USED"
	ErrCodePlanExpired         = "PLAN_EXPIRED"
	ErrCodeUnauthorized        = "UNAUTHORIZED"
	ErrCodeCSRFInvalid         = "CSRF_INVALID"
	ErrCodeInvalidRequest      = "INVALID_REQUEST"
	ErrCodeRateLimited         = "RATE_LIMITED"
)

var (
	ErrPlanStale           = errors.New("plan is stale: system state has changed")
	ErrPlanNotFound        = errors.New("plan not found")
	ErrPlanAlreadyUsed     = errors.New("plan has already been used")
	ErrPlanExpired         = errors.New("plan has expired")
	ErrPortConflict        = errors.New("port conflict detected")
	ErrEgressUnavailable   = errors.New("selected egress is unavailable")
	ErrOperationInProgress = errors.New("operation already in progress")
	ErrRecoveryRequired    = errors.New("system in recovery_required state: manual intervention required")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrInvalidRequest      = errors.New("invalid request")
	ErrPlanInvalidState    = errors.New("plan is not in reserved state")
)

type CheckStatus string

const (
	CheckStatusOK            CheckStatus = "ok"
	CheckStatusWarning       CheckStatus = "warning"
	CheckStatusBlocked       CheckStatus = "blocked"
	CheckStatusNotApplicable CheckStatus = "not_applicable"
)

// PreflightCheck represents a single health or validation check before configuration.
type PreflightCheck struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Status      CheckStatus `json:"status"` // "ok", "warning", "blocked", "not_applicable"
	Message     string      `json:"message"`
	Details     string      `json:"details,omitempty"`
	Remediation string      `json:"remediation,omitempty"`
}

// PreflightResponse is returned by preflight validation.
type PreflightResponse struct {
	CanProceed  bool             `json:"can_proceed"`
	HasWarnings bool             `json:"has_warnings"`
	Checks      []PreflightCheck `json:"checks"`
	Fingerprint string           `json:"fingerprint"`
}

// PlanItem describes a single discrete action within a ChangePlan.
type PlanItem struct {
	Action      string `json:"action"` // "create", "modify", "enable", "disable", "keep"
	Target      string `json:"target"`
	Description string `json:"description"`
	OldValue    string `json:"old_value,omitempty"`
	NewValue    string `json:"new_value,omitempty"`
}

// ChangePlan encapsulates proposed changes and the state fingerprint when planned.
type ChangePlan struct {
	Summary          string     `json:"summary"`
	Items            []PlanItem `json:"items"`
	RestartRequired  bool       `json:"restart_required"`
	StateFingerprint string     `json:"state_fingerprint"`
}

// WizardPlanRequest is the client submission describing desired wizard setup.
type WizardPlanRequest struct {
	Kind                        string `json:"kind"`                                    // "tgwebproxy", "xray", or "both"
	Scenario                    string `json:"scenario,omitempty"`                      // for tg: "direct_fake_tls", "cdn_http", "dual"
	DeviceType                  string `json:"device_type,omitempty"`                   // for xray: "phone", "pc", "router"
	Mode                        string `json:"mode,omitempty"`                          // for VLESS XHTTP: "xhttp_get", "split_http"
	PublicDomain                string `json:"public_domain,omitempty"`                 // e.g. "cdn.example.com"
	TgPublicDomain              string `json:"tg_public_domain,omitempty"`              // optional distinct domain for tg when kind=="both"
	PublicPort                  int    `json:"public_port,omitempty"`                   // e.g. 443
	Path                        string `json:"path,omitempty"`                          // e.g. "/cdn-bridge/"
	DispatcherPort              int    `json:"dispatcher_port,omitempty"`               // e.g. 9009
	ListenPort                  int    `json:"listen_port,omitempty"`                   // e.g. 9008 (or 8085 when kind=="tgwebproxy")
	TgListenPort                int    `json:"tg_listen_port,omitempty"`                // e.g. 8085 when kind=="both"
	DirectHost                  string `json:"direct_host,omitempty"`                   // e.g. "router.example.com"
	DirectPort                  int    `json:"direct_port,omitempty"`                   // e.g. 8443
	TlsDomain                   string `json:"tls_domain,omitempty"`                    // e.g. "gateway.icloud.com"
	UpstreamDevice              string `json:"upstream_device,omitempty"`               // e.g. "direct", "firc:router", "nwg1"
	TgUpstreamDevice            string `json:"tg_upstream_device,omitempty"`            // explicit egress for tg when kind=="both"
	ClientRemark                string `json:"client_remark,omitempty"`                 // e.g. "Основное устройство"
	CdnProfileID                string `json:"cdn_profile_id,omitempty"`                // e.g. "cdn_get", "cdn_ws", "direct"
	CarrierMode                 string `json:"carrier_mode,omitempty"`                  // for tg: "websocket", "get", "https-lanes"
	ConnectionWay               string `json:"connection_way,omitempty"`                // "direct" or "external_https"
	OriginType                  string `json:"origin_type,omitempty"`                   // "public_address" or "cloud_address"
	OriginHost                  string `json:"origin_host,omitempty"`                   // origin server host (router IP / KeenDNS / DDNS)
	OriginScheme                string `json:"origin_scheme,omitempty"`                 // "http" or "https"
	OriginPort                  int    `json:"origin_port,omitempty"`                   // e.g. 9009
	PreserveTransport           bool   `json:"preserve_transport,omitempty"`            // preserve existing custom transport/mode/uplink
	RawTransport                string `json:"raw_transport,omitempty"`                 // existing transport
	RawMode                     string `json:"raw_mode,omitempty"`                      // existing mode
	RawUplinkMethod             string `json:"raw_uplink_method,omitempty"`             // existing uplink method
	PreserveExistingCredentials bool   `json:"preserve_existing_credentials,omitempty"` // preserve existing client UUID / TG secret on edit
	ExistingClientUUID          string `json:"existing_client_uuid,omitempty"`          // optional client UUID to preserve on edit
	KeenDNSMode                 string `json:"keendns_mode,omitempty"`                  // "manual", "existing", or "create"
	KeenDNSLabel                string `json:"keendns_label,omitempty"`                 // optional 4th-level KeenDNS label to create/reuse
	DisableOtherService         bool   `json:"disable_other_service,omitempty"`         // explicitly disable the non-selected service when switching from both to single
}

type PlanState string

const (
	PlanStateAvailable PlanState = "available"
	PlanStateReserved  PlanState = "reserved"
	PlanStateConsumed  PlanState = "consumed"
)

// ServerPlanRecord is stored on the server with TTL to prevent client plan tampering.
type ServerPlanRecord struct {
	PlanID           string              `json:"plan_id"`
	Kind             string              `json:"kind"`
	SessionID        string              `json:"session_id"`
	Request          WizardPlanRequest   `json:"request"`
	Desired          DesiredWizardConfig `json:"desired"`
	Plan             ChangePlan          `json:"plan"`
	StateFingerprint string              `json:"state_fingerprint"`
	ExpiresAt        time.Time           `json:"expires_at"`
	State            PlanState           `json:"state"`
	ReservedJobID    string              `json:"reserved_job_id,omitempty"`
	ConsumedJobID    string              `json:"consumed_job_id,omitempty"`
	Used             bool                `json:"used"`
	CreatedAt        time.Time           `json:"created_at"`
}

// JobPhase defines the lifecycle stage of an asynchronous apply job.
type JobPhase string

const (
	JobPhasePending          JobPhase = "pending"
	JobPhasePreparing        JobPhase = "preparing"
	JobPhaseApplying         JobPhase = "applying"
	JobPhaseVerifying        JobPhase = "verifying"
	JobPhaseCommitting       JobPhase = "committing"
	JobPhaseSucceeded        JobPhase = "succeeded"
	JobPhaseCancelling       JobPhase = "cancelling"
	JobPhaseRollingBack      JobPhase = "rolling_back"
	JobPhaseCancelled        JobPhase = "cancelled"
	JobPhaseFailed           JobPhase = "failed"
	JobPhaseRecoveryRequired JobPhase = "recovery_required"
)

// JobStatusResponse is returned by safe public job polling. NO secrets are contained.
type JobStatusResponse struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	Phase           JobPhase  `json:"phase"`
	Progress        int       `json:"progress"` // 0 - 100
	CurrentStep     string    `json:"current_step"`
	Error           string    `json:"error,omitempty"`
	ErrorCode       string    `json:"error_code,omitempty"`
	ResultAvailable bool      `json:"result_available"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// RevealCredentials stores credentials revealed ONLY via authenticated, rate-limited endpoint.
type RevealCredentials struct {
	Kind        string `json:"kind"` // "tgwebproxy", "xray", or "both"
	VlessURL    string `json:"vless_url,omitempty"`
	HappJSON    string `json:"happ_json,omitempty"`
	SingboxJSON string `json:"singbox_json,omitempty"`
	MihomoYAML  string `json:"mihomo_yaml,omitempty"`
	UUID        string `json:"uuid,omitempty"`
	Remark      string `json:"remark,omitempty"`
	DirectLink  string `json:"direct_link,omitempty"`
	WebLink     string `json:"web_link,omitempty"`
	TgSecret    string `json:"tg_secret,omitempty"`
	// MtproxySecret — секрет для ручного ввода прямого MTProxy (ee + секрет +
	// домен Fake-TLS); пуст, когда прямого MTProxy нет.
	MtproxySecret     string `json:"mtproxy_secret,omitempty"`
	DirectHost        string `json:"direct_host,omitempty"`
	DirectPort        int    `json:"direct_port,omitempty"`
	PublicHost        string `json:"public_host,omitempty"`
	TgPublicHost      string `json:"tg_public_host,omitempty"`
	XrayEgress        string `json:"xray_egress,omitempty"`
	TgEgress          string `json:"tg_egress,omitempty"`
	OriginHost        string `json:"origin_host,omitempty"`
	OriginScheme      string `json:"origin_scheme,omitempty"`
	OriginPort        int    `json:"origin_port,omitempty"`
	KeenDNSFQDN       string `json:"keendns_fqdn,omitempty"`
	KeenDNSCertStatus string `json:"keendns_cert_status,omitempty"` // "ready", "pending", "invalid", "not_checked"
}

// ApplyRequest is the payload sent by the frontend to apply a planned configuration.
type ApplyRequest struct {
	PlanID string `json:"plan_id"`
}

type VerificationErrorCategory string

const (
	VerifyCategoryDNS        VerificationErrorCategory = "dns"
	VerifyCategoryTLS        VerificationErrorCategory = "tls"
	VerifyCategoryHTTPOrigin VerificationErrorCategory = "http_origin"
	VerifyCategoryAuth       VerificationErrorCategory = "auth"
	VerifyCategoryEgress     VerificationErrorCategory = "egress"
	VerifyCategoryTimeout    VerificationErrorCategory = "timeout"
	VerifyCategoryFirewall   VerificationErrorCategory = "firewall"
)

type VerificationCheck struct {
	ID            string                    `json:"id"`
	Service       string                    `json:"service"` // "xray", "tg_mtproto", "tg_web", "dispatcher", "egress"
	Title         string                    `json:"title"`
	Status        CheckStatus               `json:"status"` // "ok", "warning", "blocked", "not_applicable"
	Category      VerificationErrorCategory `json:"category,omitempty"`
	Message       string                    `json:"message"`
	Remediation   string                    `json:"remediation,omitempty"`
	TargetAddress string                    `json:"target_address,omitempty"`
}

type VerificationReport struct {
	Kind             string              `json:"kind"`
	CheckedAt        string              `json:"checked_at"`
	StateFingerprint string              `json:"state_fingerprint"`
	Scope            string              `json:"scope"`
	ScopeNote        string              `json:"scope_note"`
	OverallStatus    CheckStatus         `json:"overall_status"`
	Checks           []VerificationCheck `json:"checks"`
}
