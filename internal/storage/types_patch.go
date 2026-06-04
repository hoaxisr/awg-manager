// internal/storage/types_patch.go
package storage

// SettingsPatch is the partial-update DTO mirroring Settings field-by-field
// with pointer types. nil = "field absent in payload, leave existing value
// untouched"; non-nil = "explicit value, apply".
//
// Mirrors Settings 1:1 by JSON tag — same wire format. The old
// zero-value-restore defense in api/settings.go is replaced by
// storage.ApplyPatch which copies only non-nil fields.
//
// IMPORTANT: every new field in Settings MUST also be added here as a
// pointer with the same json tag. The contract is a hand-maintained
// mirror — see TestSettingsPatchMirrorsSettings which fails the build
// when the two drift.
type SettingsPatch struct {
	SchemaVersion             *int                   `json:"schemaVersion,omitempty"`
	AuthEnabled               *bool                  `json:"authEnabled,omitempty"`
	ApiKey                    *string                `json:"apiKey,omitempty"`
	Server                    *ServerSettings        `json:"server,omitempty"`
	PingCheck                 *PingCheckSettings     `json:"pingCheck,omitempty"`
	Logging                   *LoggingSettingsPatch  `json:"logging,omitempty"`
	DisableMemorySaving       *bool                  `json:"disableMemorySaving,omitempty"`
	Updates                   *UpdateSettings        `json:"updates,omitempty"`
	Download                  *DownloadSettingsPatch `json:"download,omitempty"`
	DNSRoute                  *DNSRouteSettings      `json:"dnsRoute,omitempty"`
	ConnectivityCheckURL      *string                `json:"connectivityCheckUrl,omitempty"`
	UsageLevel                *string                `json:"usageLevel,omitempty"`
	ServerInterfaces          *[]string              `json:"serverInterfaces,omitempty"`
	ManagedServers            *[]ManagedServer       `json:"managedServers,omitempty"`
	ManagedServer             *ManagedServer         `json:"managedServer,omitempty"`
	ManagedPolicies           *[]string              `json:"managedPolicies,omitempty"`
	MonitoringExcludedTunnels *[]string              `json:"monitoringExcludedTunnels,omitempty"`
	SingboxRouter             *SingboxRouterSettings `json:"singboxRouter,omitempty"`
	SingboxManuallyStopped    *bool                  `json:"singboxManuallyStopped,omitempty"`
	CreateNDMSProxyForSingbox *bool                  `json:"createNDMSProxyForSingbox,omitempty"`
}

// DownloadSettingsPatch supports true partial updates for /settings/update
// download block. nil field means "preserve existing value".
type DownloadSettingsPatch struct {
	RouteTag  *string `json:"routeTag,omitempty"`
	RouteKind *string `json:"routeKind,omitempty"`
}

// LoggingSettingsPatch supports true partial updates for /settings/update logging block.
// nil field means "preserve existing value".
type LoggingSettingsPatch struct {
	Enabled           *bool   `json:"enabled,omitempty"`
	MaxAge            *int    `json:"maxAge,omitempty"`
	LogLevel          *string `json:"logLevel,omitempty"`
	SingboxLogLevel   *string `json:"singboxLogLevel,omitempty"`
	AppMaxEntries     *int    `json:"appMaxEntries,omitempty"`
	SingboxMaxEntries *int    `json:"singboxMaxEntries,omitempty"`
}
