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
// when the two drift. A struct-valued block gets its own *XxxPatch type
// (pointer fields), never *XxxSettings: ApplyPatch assigns a plain struct
// wholesale, and every field the caller omitted would be zeroed (F684).
//
// singboxRouter is not here: the router owns POST /singbox/router/settings,
// and through this generic surface a partial block would wipe bypass presets,
// FakeIP pools and QoS (F684).
type SettingsPatch struct {
	SchemaVersion               *int                    `json:"schemaVersion,omitempty"`
	AuthEnabled                 *bool                   `json:"authEnabled,omitempty"`
	ApiKey                      *string                 `json:"apiKey,omitempty"`
	SessionTtlHours             *int                    `json:"sessionTtlHours,omitempty"`
	McpEnabled                  *bool                   `json:"mcpEnabled,omitempty"`
	Server                      *ServerSettingsPatch    `json:"server,omitempty"`
	PingCheck                   *PingCheckSettingsPatch `json:"pingCheck,omitempty"`
	Logging                     *LoggingSettingsPatch   `json:"logging,omitempty"`
	DisableMemorySaving         *bool                   `json:"disableMemorySaving,omitempty"`
	Updates                     *UpdateSettingsPatch    `json:"updates,omitempty"`
	Download                    *DownloadSettingsPatch  `json:"download,omitempty"`
	DNSRoute                    *DNSRouteSettingsPatch  `json:"dnsRoute,omitempty"`
	GeoFile                     *GeoFileSettingsPatch   `json:"geoFile,omitempty"`
	ConnectivityCheckURL        *string                 `json:"connectivityCheckUrl,omitempty"`
	UsageLevel                  *string                 `json:"usageLevel,omitempty"`
	ServerInterfaces            *[]string               `json:"serverInterfaces,omitempty"`
	ManagedPolicies             *[]string               `json:"managedPolicies,omitempty"`
	MonitoringExcludedTunnels   *[]string               `json:"monitoringExcludedTunnels,omitempty"`
	SingboxManuallyStopped      *bool                   `json:"singboxManuallyStopped,omitempty"`
	CreateNDMSProxyForSingbox   *bool                   `json:"createNDMSProxyForSingbox,omitempty"`
	SingboxBootstrapDNS         *string                 `json:"singboxBootstrapDNS,omitempty"`
	SingboxClashPort            *int                    `json:"singboxClashPort,omitempty"`
	ManagedPeerAllowIPsMigrated *bool                   `json:"managedPeerAllowIPsMigrated,omitempty"`
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

// ServerSettingsPatch — блок server (F684): опущенное поле сохраняется.
// Без него {"server":{"port":N}} обнулял interfaces, и панель после
// рестарта слушала на всех интерфейсах.
type ServerSettingsPatch struct {
	Port       *int      `json:"port,omitempty"`
	Interface  *string   `json:"interface,omitempty"`
	Interfaces *[]string `json:"interfaces,omitempty"`
}

// PingCheckSettingsPatch — блок pingCheck (F684), defaults — тоже по полям.
type PingCheckSettingsPatch struct {
	Enabled  *bool                   `json:"enabled,omitempty"`
	Defaults *PingCheckDefaultsPatch `json:"defaults,omitempty"`
}

type PingCheckDefaultsPatch struct {
	Method        *string `json:"method,omitempty"`
	Target        *string `json:"target,omitempty"`
	Interval      *int    `json:"interval,omitempty"`
	DeadInterval  *int    `json:"deadInterval,omitempty"`
	FailThreshold *int    `json:"failThreshold,omitempty"`
}

// UpdateSettingsPatch — блок updates (F684).
type UpdateSettingsPatch struct {
	CheckEnabled            *bool   `json:"checkEnabled,omitempty"`
	Channel                 *string `json:"channel,omitempty"`
	AutoInstallEnabled      *bool   `json:"autoInstallEnabled,omitempty"`
	AutoInstallIntervalDays *int    `json:"autoInstallIntervalDays,omitempty"`
	AutoInstallTime         *string `json:"autoInstallTime,omitempty"`
	StatsEnabled            *bool   `json:"statsEnabled,omitempty"`
	SnapshotDisabled        *bool   `json:"snapshotDisabled,omitempty"`
}

// DNSRouteSettingsPatch — блок dnsRoute (F684).
type DNSRouteSettingsPatch struct {
	AutoRefreshEnabled   *bool   `json:"autoRefreshEnabled,omitempty"`
	RefreshIntervalHours *int    `json:"refreshIntervalHours,omitempty"`
	RefreshMode          *string `json:"refreshMode,omitempty"`
	RefreshDailyTime     *string `json:"refreshDailyTime,omitempty"`
}

// GeoFileSettingsPatch — блок geoFile (F684).
type GeoFileSettingsPatch struct {
	AutoRefreshEnabled   *bool   `json:"autoRefreshEnabled,omitempty"`
	RefreshIntervalHours *int    `json:"refreshIntervalHours,omitempty"`
	RefreshMode          *string `json:"refreshMode,omitempty"`
	RefreshDailyTime     *string `json:"refreshDailyTime,omitempty"`
}
