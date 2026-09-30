package tgwebproxy

import (
	"os"
	"time"
)

const (
	CurrentSchemaVersion = 2

	DefaultListenPort     = 8085
	DefaultAdminPort      = 8086
	DefaultDirectPort     = 8443
	DefaultRawPort        = 2398
	DefaultPublicHostname = ""
	DefaultDirectHost     = ""
	DefaultBackend        = "127.0.0.1:2398"
	DefaultCarrierMode    = "get"
	DefaultUpstreamDevice = ""
	DefaultTlsDomain      = ""
	DefaultLegacyGraceTTL = 7 * 24 * time.Hour

	ScenarioDirectFakeTLS = "direct_fake_tls"
	ScenarioCDNHTTP       = "cdn_http"
	ScenarioDual          = "dual"

	PhaseStaged      = "staged"
	PhaseApplying    = "applying"
	PhaseCommitted   = "committed"
	PhaseRollingBack = "rolling_back"
)

type Config struct {
	SchemaVersion   int    `json:"schema_version"`
	Enabled         bool   `json:"enabled"`
	Scenario        string `json:"scenario,omitempty"`       // direct_fake_tls, cdn_http, dual
	ListenPort      int    `json:"listen_port"`              // Default: 8085 (tproxy-server)
	AdminPort       int    `json:"admin_port"`               // Default: 8086 (tproxy-server metrics)
	PublicHostname  string `json:"public_hostname"`          // Public CDN ingress hostname
	DirectHost      string `json:"direct_host"`              // Direct MTProxy hostname or IP
	DirectPort      int    `json:"direct_port"`              // Default: 8443 (telemt Fake-TLS)
	Secret          string `json:"secret"`                   // 16 bytes hex (32 chars)
	LegacySecret    string `json:"legacy_secret"`            // 16 bytes hex (32 chars)
	LegacyExpiresAt string `json:"legacy_expires_at"`        // RFC3339 timestamp
	Backend         string `json:"backend"`                  // Default: 127.0.0.1:2398 (raw telemt)
	CarrierMode     string `json:"carrier_mode"`             // Default: get
	UpstreamDevice  string `json:"upstream_device"`          // Egress interface (optional)
	TlsDomain       string `json:"tls_domain"`               // Fake-TLS SNI domain
}

func (c Config) IsDirectEnabled() bool {
	if !c.Enabled {
		return false
	}
	return c.Scenario == ScenarioDirectFakeTLS || c.Scenario == ScenarioDual || c.Scenario == ""
}

func (c Config) IsRawEnabled() bool {
	if !c.Enabled {
		return false
	}
	return c.Scenario == ScenarioCDNHTTP || c.Scenario == ScenarioDual || c.Scenario == ""
}

func (c Config) IsWebEnabled() bool {
	if !c.Enabled {
		return false
	}
	return c.Scenario == ScenarioCDNHTTP || c.Scenario == ScenarioDual || c.Scenario == ""
}

// ManagedIngressConfig represents full-state configuration managed by server ingress coordinator.
type ManagedIngressConfig struct {
	Enabled        bool   `json:"enabled"`
	ListenPort     int    `json:"listen_port"`
	PublicHostname string `json:"public_hostname"`
}

type PublicConfig struct {
	SchemaVersion      int    `json:"schema_version"`
	Enabled            bool   `json:"enabled"`
	Scenario           string `json:"scenario,omitempty"`
	ListenPort         int    `json:"listen_port"`
	AdminPort          int    `json:"admin_port"`
	PublicHostname     string `json:"public_hostname"`
	DirectHost         string `json:"direct_host"`
	DirectPort         int    `json:"direct_port"`
	SecretMasked       string `json:"secret_masked"`
	Backend            string `json:"backend"`
	CarrierMode        string `json:"carrier_mode"`
	UpstreamDevice     string `json:"upstream_device"`
	TlsDomain          string `json:"tls_domain"`
	LegacyActive       bool   `json:"legacy_active"`
	LegacyExpiresAt    string `json:"legacy_expires_at,omitempty"`
	LegacySecretMasked string `json:"legacy_secret_masked,omitempty"`
}

type SnapshotManifest struct {
	TxID             string            `json:"tx_id"`
	CreatedAt        string            `json:"created_at"`
	OldConfig        Config            `json:"old_config"`
	CandidateConfig  Config            `json:"candidate_config"`
	WasRunning       bool              `json:"was_running"`
	CandidateRunning bool              `json:"candidate_running"`
	State            string            `json:"state"` // "prepared", "active", "finalized", "rolled_back"
	Checksums        map[string]string `json:"checksums,omitempty"`
	SchemaVersion    int               `json:"schema_version"`
}

type Status struct {
	Installed                      bool   `json:"installed"`
	InstalledTelemt                bool   `json:"installed_telemt"`
	InstalledTproxy                bool   `json:"installed_tproxy"`
	Running                        bool   `json:"running"`
	PID                            int    `json:"pid"`
	DirectPID                      int    `json:"direct_pid,omitempty"`
	Port                           int    `json:"port"`
	DirectOnline                   bool   `json:"direct_online"`
	RawOnline                      bool   `json:"raw_online"`
	WebProxyOnline                 bool   `json:"webproxy_online"`
	BackendOnline                  bool   `json:"backend_online"`
	BackendAddr                    string `json:"backend_addr,omitempty"`
	UpstreamStatus                 string `json:"upstream_status"` // "ok", "degraded", "interface_down"
	CarrierMode                    string `json:"carrier_mode"`
	PublicHost                     string `json:"public_host"`
	DirectHost                     string `json:"direct_host"`
	DirectPort                     int    `json:"direct_port"`
	TlsDomain                      string `json:"tls_domain"`
	SecretMasked                   string `json:"secret_masked"`
	LegacySecretMasked             string `json:"legacy_secret_masked"`
	LegacyActive                   bool   `json:"legacy_active"`
	LegacyExpiresAt                string `json:"legacy_expires_at"`
	LegacyExpiredPendingReconcile bool   `json:"legacy_expired_pending_reconcile"`
	RecoveryRequired               bool   `json:"recovery_required"`
	LastStartupError               string `json:"last_startup_error,omitempty"`
}

type RevealData struct {
	Secret        string `json:"secret"`
	LegacySecret  string `json:"legacy_secret,omitempty"`
	DirectHost    string `json:"direct_host"`
	DirectPort    int    `json:"direct_port"`
	TlsDomain     string `json:"tls_domain"`
	TgURL         string `json:"tg_url"`
	TmeURL        string `json:"tme_url"`
	BridgeURL     string `json:"bridge_url"`
	MtproxySecret string `json:"mtproxy_secret"`
	MtproxyURL    string `json:"mtproxy_url"`
	MtproxyTmeURL string `json:"mtproxy_tme_url"`
}

type WorkerProcessState struct {
	Worker    string `json:"worker"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	PID       int    `json:"pid"`
	StartTime int64  `json:"start_time"`
}

type ManifestFile struct {
	TargetPath    string      `json:"target_path"`
	StagingPath   string      `json:"staging_path"`
	BackupPath    string      `json:"backup_path"`
	Sha256Old     string      `json:"sha256_old"`
	Sha256New     string      `json:"sha256_new"`
	ExistedBefore bool        `json:"existed_before"`
	Mode          os.FileMode `json:"mode"`
	UID           int         `json:"uid"`
	GID           int         `json:"gid"`
}

type TransactionManifest struct {
	TxID               string                        `json:"txid"`
	Phase              string                        `json:"phase"` // staged, applying, committed, rolling_back
	CreatedAt          string                        `json:"created_at"`
	Files              []ManifestFile                `json:"files"`
	ProcessStateBefore map[string]WorkerProcessState `json:"process_state_before"`
}
