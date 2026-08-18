package storage

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// migrateToV2 migrates settings from v1 to v2.
func (s *SettingsStore) migrateToV2(settings *Settings) error {
	// Migrate port from old port file
	s.migratePortFile(settings)

	// Set defaults for new fields if not set
	if settings.Server.Port == 0 {
		settings.Server.Port = DefaultPort
	}
	if settings.Server.Interface == "" {
		settings.Server.Interface = DefaultInterface
	}

	// Set PingCheck defaults
	if settings.PingCheck.Defaults.Method == "" {
		settings.PingCheck.Defaults.Method = "http"
	}
	if settings.PingCheck.Defaults.Target == "" {
		settings.PingCheck.Defaults.Target = DefaultPingCheckTarget
	}
	if settings.PingCheck.Defaults.Interval == 0 {
		settings.PingCheck.Defaults.Interval = 45
	}
	if settings.PingCheck.Defaults.DeadInterval == 0 {
		settings.PingCheck.Defaults.DeadInterval = 120
	}
	if settings.PingCheck.Defaults.FailThreshold == 0 {
		settings.PingCheck.Defaults.FailThreshold = 3
	}

	settings.SchemaVersion = 2
	return nil
}

// migrateToV3 migrates settings from v2 to v3.
func (s *SettingsStore) migrateToV3(settings *Settings) {
	// Set Logging defaults
	if settings.Logging.MaxAge == 0 {
		settings.Logging.MaxAge = 2
	}
	// Logging.Enabled defaults to false (zero value)

	settings.SchemaVersion = 3
}

// migrateToV4 migrates settings from v3 to v4.
func (s *SettingsStore) migrateToV4(settings *Settings) {
	// Previously set default BackendMode (removed in v13)
	settings.SchemaVersion = 4
}

// migrateToV5 migrates settings from v4 to v5.
func (s *SettingsStore) migrateToV5(settings *Settings) {
	settings.SchemaVersion = 5
}

// migrateToV6 migrates settings from v5 to v6.
func (s *SettingsStore) migrateToV6(settings *Settings) {
	// Enable update checks by default
	settings.Updates.CheckEnabled = true
	settings.SchemaVersion = 6
}

// migrateToV7 was a version bump for an experimental field that was
// removed before reaching production. Kept as a no-op so the schema
// ladder remains contiguous.
func (s *SettingsStore) migrateToV7(settings *Settings) {
	settings.SchemaVersion = 7
}

// migrateToV8 migrates settings from v7 to v8.
func (s *SettingsStore) migrateToV8(settings *Settings) {
	// v8 added ExcludedWANs (later removed) — bump version only
	settings.SchemaVersion = 8
}

// migrateToV9 migrates settings from v8 to v9.
func (s *SettingsStore) migrateToV9(settings *Settings) {
	// DNSRouteSettings zero value (disabled, interval 0) is correct default
	settings.SchemaVersion = 9
}

// migrateToV10 migrates settings from v9 to v10.
func (s *SettingsStore) migrateToV10(settings *Settings) {
	settings.SchemaVersion = 10
}

// migrateToV11 migrates settings from v10 to v11.
func (s *SettingsStore) migrateToV11(settings *Settings) {
	// ServerInterfaces zero value (nil) is correct default
	settings.SchemaVersion = 11
}

// migrateToV12 migrates settings from v11 to v12.
func (s *SettingsStore) migrateToV12(settings *Settings) {
	// ManagedServer zero value (nil) is correct default
	settings.SchemaVersion = 12
}

// migrateToV13 removes deprecated BackendMode (now per-tunnel).
func (s *SettingsStore) migrateToV13(settings *Settings) {
	settings.SchemaVersion = 13
}

// migrateToV14 historically set SingboxRouter refresh defaults for the new
// TProxy routing engine. Those fields were removed (rule-set refresh is now
// native to sing-box via update_interval); the migration only bumps the
// schema version to keep the chain intact.
func (s *SettingsStore) migrateToV14(settings *Settings) {
	settings.SchemaVersion = 14
}

// migrateToV15 wipes deprecated SingboxRouterSettings fields (Mode,
// ClientScope) and force-disables the router so the user re-selects
// a policy via the new policy-mode UI. Per redesign 2026-04-28:
// no users to preserve, simplest fail-safe.
func (s *SettingsStore) migrateToV15(settings *Settings) {
	settings.SingboxRouter.PolicyName = ""
	settings.SingboxRouter.Enabled = false
	settings.SchemaVersion = 15
}

// migrateToV16 introduces UsageLevel. Any user reaching this migration
// already has a working settings file (the file existed on disk before
// the upgrade), so they are an existing user — set advanced. Fresh
// installs never run this migration: defaultSettings() ships v16 with
// usageLevel="basic".
func (s *SettingsStore) migrateToV16(settings *Settings) {
	settings.UsageLevel = UsageLevelAdvanced
	settings.SchemaVersion = 16
}

// migrateToV17 introduces per-bucket buffer caps for the logging system.
// Existing installs default to 5000 entries each (matches the prior
// hardcoded MaxEntries that lived in internal/logging/buffer.go).
func (s *SettingsStore) migrateToV17(settings *Settings) {
	if settings.Logging.AppMaxEntries == 0 {
		settings.Logging.AppMaxEntries = 5000
	}
	if settings.Logging.SingboxMaxEntries == 0 {
		settings.Logging.SingboxMaxEntries = 5000
	}
	settings.SchemaVersion = 17
}

// migrateToV18 sets WANAutoDetect=true on existing installs to preserve
// the prior implicit behavior (no WAN binding in the config meant sing-box
// picked the route automatically — the same effect as
// auto_detect_interface=true that v18 makes explicit). WANInterface stays
// empty, which is the only valid combination for WANAutoDetect=true.
func (s *SettingsStore) migrateToV18(settings *Settings) {
	settings.SingboxRouter.WANAutoDetect = true
	settings.SingboxRouter.WANInterface = ""
	settings.SchemaVersion = 18
}

// migrateToV19 introduces singbox-router device scope and the sniffer
// toggle. Preserve historical behavior for existing installs:
// policy-marked devices only, with sing-box sniffing enabled.
func (s *SettingsStore) migrateToV19(settings *Settings) {
	settings.SingboxRouter.DeviceMode = "policy"
	settings.SingboxRouter.SnifferEnabled = true
	settings.SchemaVersion = 19
}

// migrateToV20 introduces CreateNDMSProxyForSingbox toggle. Existing
// installs already rely on ProxyN/t2sN being created — set true to
// preserve behaviour. Fresh installs ship v20 with default true via
// defaultSettings.
func (s *SettingsStore) migrateToV20(settings *Settings) {
	settings.CreateNDMSProxyForSingbox = true
	settings.SchemaVersion = 20
}

// migrateToV21 introduces Logging.SingboxLogLevel.
// Existing installs default to "trace" to preserve historical behavior.
func (s *SettingsStore) migrateToV21(settings *Settings) {
	if settings.Logging.LogLevel == "" {
		settings.Logging.LogLevel = "info"
	}
	if settings.Logging.SingboxLogLevel == "" {
		settings.Logging.SingboxLogLevel = DefaultSingboxLogLevel
	}
	settings.SchemaVersion = 21
}

// migrateToV22 introduces Download.RouteTag.
// Existing installs default to "direct".
func (s *SettingsStore) migrateToV22(settings *Settings) {
	if strings.TrimSpace(settings.Download.RouteTag) == "" {
		settings.Download.RouteTag = "direct"
	}
	settings.SchemaVersion = 22
}

// migrateToV23 introduces UpdateSettings.Channel. Existing installs default
// to the stable channel to preserve current behaviour.
func (s *SettingsStore) migrateToV23(settings *Settings) {
	if settings.Updates.Channel == "" {
		settings.Updates.Channel = "stable"
	}
	settings.SchemaVersion = 23
}

// migrateToV24 normalizes Download route shape and introduces RouteKind.
func (s *SettingsStore) migrateToV24(settings *Settings) {
	settings.Download.RouteTag = strings.TrimSpace(settings.Download.RouteTag)
	if settings.Download.RouteTag == "" {
		settings.Download.RouteTag = "direct"
	}
	if strings.TrimSpace(settings.Download.RouteKind) == "" && settings.Download.RouteTag == "direct" {
		settings.Download.RouteKind = "direct"
	}
	settings.SchemaVersion = 24
}

// migrateToV25 introduces ConnectivityCheckURL and self-heals empty ping targets.
func (s *SettingsStore) migrateToV25(settings *Settings) {
	settings.PingCheck.Defaults.Target = strings.TrimSpace(settings.PingCheck.Defaults.Target)
	if settings.PingCheck.Defaults.Target == "" {
		settings.PingCheck.Defaults.Target = DefaultPingCheckTarget
	}
	settings.ConnectivityCheckURL = strings.TrimSpace(settings.ConnectivityCheckURL)
	if settings.ConnectivityCheckURL == "" {
		settings.ConnectivityCheckURL = DefaultConnectivityCheckURL
	}
	settings.SchemaVersion = 25
}

// migrateNATModes sets NATMode from NATEnabled for servers that have no NATMode yet.
func migrateNATModes(s *Settings) {
	for i := range s.ManagedServers {
		if s.ManagedServers[i].NATMode == "" {
			if s.ManagedServers[i].NATEnabled {
				s.ManagedServers[i].NATMode = "full"
			} else {
				s.ManagedServers[i].NATMode = "none"
			}
		}
	}
}

func (s *SettingsStore) migrateToV26(settings *Settings) {
	migrateNATModes(settings)
	settings.SchemaVersion = 26
}

// migrateToV27 defaults the new sing-box RoutingMode to "tproxy" (existing
// behavior) and introduces GeoFileSettings — its zero value (auto-refresh
// disabled) is the intended default, so no action is needed for it beyond the
// version stamp. (Both effects landed independently as v27 on parallel branches;
// merged here. Idempotent: RoutingMode is also defaulted at runtime by
// NormalizeSingboxRouterSettings, so a config that took only the GeoFileSettings
// v27 path stays correct.)
func (s *SettingsStore) migrateToV27(settings *Settings) {
	if settings.SingboxRouter.RoutingMode == "" {
		settings.SingboxRouter.RoutingMode = "tproxy"
	}
	settings.SchemaVersion = 27
}

// migrateToV28 remaps Logging.SingboxLogLevel "trace" → "info" one-time.
// migrateToV21 stamped the then-default "trace" on every existing install, so
// on upgrade "trace" is overwhelmingly the old default, not a user choice —
// it self-inflicted hundreds of journal lines per minute on low-RAM routers.
// The rare user who deliberately picked trace has to re-enable it once;
// levels chosen after v21 (debug, warn, …) are left untouched.
func (s *SettingsStore) migrateToV28(settings *Settings) {
	if settings.Logging.SingboxLogLevel == "trace" {
		settings.Logging.SingboxLogLevel = "info"
	}
	settings.SchemaVersion = 28
}

// migrateToV29 introduces SessionTtlHours (issue #441), defaulting to the
// historical fixed 24h session lifetime, and EntwareAuthEnabled, whose zero
// value (false — keep NDMS-only login) is the intended default so no
// explicit action is needed beyond the version stamp.
func (s *SettingsStore) migrateToV29(settings *Settings) {
	if settings.SessionTtlHours == 0 {
		settings.SessionTtlHours = DefaultSessionTTLHours
	}
	settings.SchemaVersion = 29
}

// migrateToV30 lifts the legacy single Server.Interface into the new
// Server.Interfaces list (HTTP-listen settings). The old field keeps its
// value so a downgraded binary still binds where it used to; new code
// reads Interfaces only. An explicitly empty legacy Interface ("" = bind
// all) migrates to an empty list — same 0.0.0.0 semantics.
func (s *SettingsStore) migrateToV30(settings *Settings) {
	if len(settings.Server.Interfaces) == 0 && settings.Server.Interface != "" {
		settings.Server.Interfaces = []string{settings.Server.Interface}
	}
	settings.SchemaVersion = 30
}

// migrateToV31 introduces auto-install-by-schedule fields on UpdateSettings
// (issue #559). Existing installs default to disabled, every 7 days, 05:00
// window — AutoInstallEnabled zero value (false) is already the intended
// default, so only the two other fields need an explicit backfill.
func (s *SettingsStore) migrateToV31(settings *Settings) {
	if settings.Updates.AutoInstallIntervalDays == 0 {
		settings.Updates.AutoInstallIntervalDays = 7
	}
	if settings.Updates.AutoInstallTime == "" {
		settings.Updates.AutoInstallTime = "05:00"
	}
	settings.SchemaVersion = 31
}

// migrateToV32 normalizes the hand-written "0.0.0.0" pseudo-interface
// (#571: users set it pre-2.16 to mean "listen everywhere" and the old
// single-listener code accidentally honored it via its bind-all fallback).
// It is not an interface name: the multi-listener resolve skips it, leaving
// the daemon loopback-only. Dropping it from the list (empty list = 0.0.0.0)
// and blanking the legacy field ("" = bind all on a downgraded binary)
// preserves the intended semantics on both sides.
func (s *SettingsStore) migrateToV32(settings *Settings) {
	kept := settings.Server.Interfaces[:0]
	for _, iface := range settings.Server.Interfaces {
		if iface != "0.0.0.0" {
			kept = append(kept, iface)
		}
	}
	settings.Server.Interfaces = kept
	if settings.Server.Interface == "0.0.0.0" {
		settings.Server.Interface = ""
	}
	settings.SchemaVersion = 32
}

// migrateToV33 enables the keendns bypass preset by default. Append only
// when missing so an existing keendns entry is left intact.
func (s *SettingsStore) migrateToV33(settings *Settings) {
	has := false
	for _, name := range settings.SingboxRouter.BypassPresets {
		if name == "keendns" {
			has = true
			break
		}
	}
	if !has {
		settings.SingboxRouter.BypassPresets = append(settings.SingboxRouter.BypassPresets, "keendns")
	}
	settings.SchemaVersion = 33
}

// migrateManagedServers moves a legacy singular managedServer into the
// new ManagedServers slice. Idempotent. Caller holds s.mu.
func (s *SettingsStore) migrateManagedServers() {
	if s.settings == nil || s.settings.ManagedServer == nil {
		return
	}
	// Prepend so an existing slice (theoretically already migrated) keeps
	// its order — but in practice mass migration only fires once, when
	// the slice is empty.
	migrated := append([]ManagedServer{*s.settings.ManagedServer}, s.settings.ManagedServers...)
	s.settings.ManagedServers = migrated
	s.settings.ManagedServer = nil
}

// migratePortFile reads port from old port file and removes it.
func (s *SettingsStore) migratePortFile(settings *Settings) {
	portFile := filepath.Join(filepath.Dir(s.path), "port")
	data, err := os.ReadFile(portFile)
	if err != nil {
		return // No port file, use default
	}

	portStr := strings.TrimSpace(string(data))
	if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
		settings.Server.Port = port
	}

	// Remove old port file after successful migration
	os.Remove(portFile)
}

// migrateToV34 сливает зеркальные записи владения OpkgTun (ключи fakeip и
// policyTun) в единую opkgTun. Конфликт «обе записи» разрешается активным
// RoutingMode; NAT-записи проигравшей policy-записи не выбрасываются — они
// переезжают в payload (реап восстановит их и вне режима policy-tun).
// Потеря payload'а проигравшей fakeip-записи безопасна: пустой prev-диапазон
// заставляет FakeIPCacheNeedsReset сработать в сторону лишнего сброса кэша.
func (s *SettingsStore) migrateToV34(settings *Settings) {
	fk, pt := settings.LegacyFakeIP, settings.LegacyPolicyTun
	settings.LegacyFakeIP, settings.LegacyPolicyTun = nil, nil
	switch {
	case fk == nil && pt == nil:
		return
	case pt == nil, fk != nil && settings.SingboxRouter.RoutingMode == OpkgTunModeFakeIP:
		st := &OpkgTunState{Mode: OpkgTunModeFakeIP, Provisioned: fk.Provisioned, Index: fk.Index,
			FakeIP: &OpkgTunFakeIPData{Inet4Range: fk.Inet4Range, Inet6Range: fk.Inet6Range}}
		if pt != nil && len(pt.NATSegments) > 0 {
			st.PolicyTun = &OpkgTunPolicyData{NATSegments: pt.NATSegments}
		}
		settings.OpkgTun = st
	default:
		st := &OpkgTunState{Mode: OpkgTunModePolicyTun, Provisioned: pt.Provisioned, Index: pt.Index}
		if len(pt.NATSegments) > 0 {
			st.PolicyTun = &OpkgTunPolicyData{NATSegments: pt.NATSegments}
		}
		settings.OpkgTun = st
	}
}

// migrateToV35 материализует пустой FakeIPPool6 в явный дефолт: до v35
// нормализация дефолтила "" на каждом чтении, с v35 "" означает «v6 выключен».
// Литерал ЗАМОРОЖЕН намеренно (дубль DefaultFakeIPTunParams — миграции не
// дрейфуют с будущими дефолтами).
func (s *SettingsStore) migrateToV35(settings *Settings) {
	if settings.SingboxRouter.FakeIPPool6 == "" {
		settings.SingboxRouter.FakeIPPool6 = "fc00::/18"
	}
}
