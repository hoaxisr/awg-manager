package hydraroute

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// KernelIfaceResolver resolves tunnel IDs to kernel interface names.
type KernelIfaceResolver interface {
	GetKernelIfaceName(ctx context.Context, tunnelID string) (string, error)
}

// Service manages HydraRoute Neo integration: detection, config writes, daemon control.
type Service struct {
	resolver                 KernelIfaceResolver
	appLog                   *logging.ScopedLogger
	mu                       sync.Mutex
	status                   Status
	restartTimer             *time.Timer
	geodata                  *GeoDataStore
	dnsListProvider          func() []DnsListInfo
	queries                  *query.Queries
	policies                 *command.PolicyCommands
	lastError                string
	versionCached            string
	versionFetchedAt         time.Time
	versionBinaryFingerprint string
}

const versionCacheTTL = 5 * time.Minute

// NewService creates a new HydraRoute service. Detects HRNeo on creation.
func NewService(resolver KernelIfaceResolver, appLogger logging.AppLogger) *Service {
	s := &Service{
		resolver: resolver,
		appLog:   logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubHrNeo),
		status:   Detect(),
	}
	if s.status.Installed {
		s.appLog.Info("detect", "", fmt.Sprintf("HrNeo detected (running=%v)", s.status.Running))
		s.HealInvalidRuntimeConfig()
	}
	return s
}

func (s *Service) HealInvalidRuntimeConfig() {
	changed, chosen, err := HealInvalidRuntimeConfig()
	if err != nil {
		s.appLog.Warn("config-heal", "", "failed to heal invalid config: "+err.Error())
		return
	}
	if !changed {
		return
	}
	s.appLog.Warn("config-heal", "IpsetMaxElem", fmt.Sprintf("invalid or duplicate IpsetMaxElem healed to %d", chosen))
	if s.status.Installed && !s.status.Running {
		s.scheduleRestart("config-heal")
	}
}

// GetStatus returns cached detection status.
func (s *Service) GetStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.status
	if status.Running {
		status.LastError = ""
	} else {
		status.LastError = s.lastError
	}
	return status
}

// RefreshStatus re-detects HydraRoute and updates cached status.
func (s *Service) RefreshStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = Detect()
	s.status.Version = s.getVersionCachedLocked()
	if s.status.Running {
		s.status.LastError = ""
	} else {
		s.status.LastError = s.lastError
	}
	return s.status
}

// SetStatusForTest lets tests declare HR Neo "installed" without having
// the real daemon present on disk. Intended only for tests.
func (s *Service) SetStatusForTest(installed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Installed = installed
}

// Control starts/stops/restarts the HydraRoute daemon.
func (s *Service) Control(action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.status.Installed {
		err := fmt.Errorf("HydraRoute Neo is not installed")
		s.lastError = err.Error()
		return err
	}

	switch action {
	case "start", "stop", "restart":
		result, err := exec.Run(context.Background(), neoCommand, action)
		if err != nil {
			formatted := fmt.Errorf("neo %s: %w", action, exec.FormatError(result, err))
			s.lastError = formatted.Error()
			return formatted
		}
		s.status = Detect()
		s.status.Version = s.getVersionCachedLocked()
		s.lastError = ""
		return nil
	default:
		err := fmt.Errorf("unknown action: %s", action)
		s.lastError = err.Error()
		return err
	}
}

// scheduleRestart debounces neo restart: resets timer on each call.
//
// Central guard: при !Installed silently skip — это покрывает все
// callsite'ы (rules-write/config-heal/config-write/policy-order/geo-sync)
// одной защитой. Без guard'а AfterFunc через 2s делал fork/exec
// /opt/bin/neo restart, что на чистой системе без HR Neo приводило к
// шуму "neo restart failed: no such file or directory". Решение:
// systematic-debugging session 2026-05-23.
func (s *Service) scheduleRestart(reason string) {
	if !s.status.Installed {
		s.appLog.Debug("restart-schedule", "", "skipped: HR Neo не установлен (reason: "+reason+")")
		return
	}
	if s.restartTimer != nil {
		s.restartTimer.Stop()
	}
	s.appLog.Info("restart-schedule", "", "neo restart scheduled: "+reason)
	s.restartTimer = time.AfterFunc(2*time.Second, func() {
		// Mark timer as completed before releasing the lock so a concurrent
		// scheduleRestart sees nil and creates a fresh timer rather than
		// stopping an already-fired one.
		s.mu.Lock()
		s.restartTimer = nil
		s.mu.Unlock()

		result, err := exec.Run(context.Background(), neoCommand, "restart")
		if err != nil {
			s.appLog.Warn("restart", "neo", exec.FormatError(result, err).Error())
			s.appLog.Warn("restart", "", fmt.Sprintf("neo restart failed: %v", exec.FormatError(result, err)))
			s.mu.Lock()
			s.lastError = fmt.Sprintf("neo restart: %v", exec.FormatError(result, err))
			s.mu.Unlock()
		} else {
			s.appLog.Info("restart", "neo", "restarted")
			s.appLog.Info("restart", "", "neo restarted")
			s.mu.Lock()
			s.lastError = ""
			s.mu.Unlock()
		}
		s.mu.Lock()
		s.status = Detect()
		s.status.Version = s.getVersionCachedLocked()
		if s.status.Running {
			s.status.LastError = ""
		} else {
			s.status.LastError = s.lastError
		}
		s.mu.Unlock()
	})
}

func (s *Service) getVersionCachedLocked() string {
	if !s.status.Installed {
		s.versionCached = ""
		s.versionFetchedAt = time.Time{}
		s.versionBinaryFingerprint = ""
		return ""
	}

	now := time.Now()
	currentFingerprint := hydraBinaryFingerprint()
	if currentFingerprint == "" {
		s.versionCached = ""
		s.versionFetchedAt = now
		s.versionBinaryFingerprint = ""
		return ""
	}
	fingerprintChanged := s.versionBinaryFingerprint != "" &&
		currentFingerprint != s.versionBinaryFingerprint

	if fingerprintChanged {
		s.versionCached = ""
		s.versionFetchedAt = time.Time{}
	}

	if !fingerprintChanged && !s.versionFetchedAt.IsZero() && now.Sub(s.versionFetchedAt) < versionCacheTTL {
		return s.versionCached
	}

	version := detectVersion(context.Background())
	s.versionCached = version
	s.versionFetchedAt = now
	s.versionBinaryFingerprint = currentFingerprint
	return s.versionCached
}

func hydraBinaryFingerprint() string {
	st, err := os.Stat(hrneoBinary)
	if err != nil || st.IsDir() {
		return ""
	}
	return fmt.Sprintf(
		"%s|%s|%s|%d",
		filepath.Clean(hrneoBinary),
		st.ModTime().UTC().Format(time.RFC3339Nano),
		st.Mode().String(),
		st.Size(),
	)
}

// SetGeoDataStore sets the GeoDataStore used for syncing geo file paths to config.
func (s *Service) SetGeoDataStore(gds *GeoDataStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.geodata = gds
}

// SetDnsListProvider sets the function that returns current DNS list info for ipset usage calculation.
func (s *Service) SetDnsListProvider(fn func() []DnsListInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dnsListProvider = fn
}

// SetQueries wires the NDMS Queries registry used to read ip policies.
func (s *Service) SetQueries(q *query.Queries) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = q
}

// SetPolicies wires the NDMS PolicyCommands used to permit interfaces in a policy.
func (s *Service) SetPolicies(p *command.PolicyCommands) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies = p
}

// GetGeoData returns the current GeoDataStore.
func (s *Service) GetGeoData() *GeoDataStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.geodata
}

// EnsurePolicyInterfaces permits the given NDMS interfaces in the policy.
// HR Neo creates the policy itself; we only need to add interfaces.
//
// Keenetic's `ip policy permit` order is 0-based: first permit MUST be
// added with order=0, the next with 1, and so on. Sending order=1 first
// triggers `Network::PolicyTable: <name>: invalid order: 1`.
//
// On an existing policy that already has permits, sending order=0 INSERTS
// at the front and shifts the previous permits back by one. Callers that
// permit into an existing policy should be aware they may silently change
// the policy's existing routing priority.
func (s *Service) EnsurePolicyInterfaces(ctx context.Context, policyName string, ndmsIfaces []string) error {
	s.mu.Lock()
	policies := s.policies
	s.mu.Unlock()

	if policies == nil {
		return fmt.Errorf("PolicyCommands not available")
	}

	for i, iface := range ndmsIfaces {
		s.appLog.Info("permit-iface", iface, fmt.Sprintf("ip policy %s permit global order %d", policyName, i))
		if err := policies.PermitInterface(ctx, policyName, iface, i); err != nil {
			s.appLog.Warn("permit-iface", iface, fmt.Sprintf("policy %s: %v", policyName, err))
			return fmt.Errorf("permit %s in policy %s: %w", iface, policyName, err)
		}
	}
	return nil
}

// ReadConfig reads and returns the current HydraRoute config.
func (s *Service) ReadConfig() (*Config, error) {
	return ReadConfig()
}

// WriteConfig syncs geo file paths from geodata (if set), writes the config, and schedules a restart.
func (s *Service) WriteConfig(cfg *Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.geodata != nil {
		geoIP, geoSite := s.geodata.GeoFilePaths()
		cfg.GeoIPFiles = geoIP
		cfg.GeoSiteFiles = geoSite
	}
	effectiveMaxElem := cfg.IpsetMaxElem
	if cfg.IpsetMaxElem <= 0 {
		effectiveMaxElem = defaultMaxElem
		s.appLog.Warn("config-normalize", "IpsetMaxElem", "invalid value <=0 normalized to 65536")
	}
	s.appLog.Info(
		"config-write",
		"",
		fmt.Sprintf(
			"full config write: geoip=%d geosite=%d ipsetMaxElem=%d policyOrder=%d",
			len(cfg.GeoIPFiles),
			len(cfg.GeoSiteFiles),
			effectiveMaxElem,
			len(cfg.PolicyOrder),
		),
	)

	if err := WriteConfig(cfg); err != nil {
		s.appLog.Warn("config-write", "", "full config write failed: "+err.Error())
		return err
	}

	s.scheduleRestart("config-write")
	return nil
}

// SetPolicyOrder updates only PolicyOrder in hrneo.conf and restarts.
func (s *Service) SetPolicyOrder(order []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appLog.Info("policy-order", "", fmt.Sprintf("patch-only policy order write: entries=%d", len(order)))

	if err := WritePolicyOrderOnly(order); err != nil {
		s.appLog.Warn("policy-order", "", "patch-only policy order write failed: "+err.Error())
		return err
	}

	s.scheduleRestart("policy-order")
	return nil
}

// SyncGeoFilesToConfig updates only GeoIPFile/GeoSiteFile in hrneo.conf.
// При !Installed — no-op: если HR Neo удалили, мы не обновляем его
// конфиг "на будущее" (решено session 2026-05-23).
func (s *Service) SyncGeoFilesToConfig() error {
	s.mu.Lock()
	installed := s.status.Installed
	s.mu.Unlock()
	if !installed {
		s.appLog.Debug("sync-geo", "", "skipped: HR Neo не установлен")
		return nil
	}
	geoIP, geoSite := 0, 0
	var ips []string
	var sites []string
	s.mu.Lock()
	gds := s.geodata
	s.mu.Unlock()
	if gds == nil {
		s.appLog.Warn("sync-geo", "", "geo data store not initialized")
		return fmt.Errorf("geo data store not initialized")
	}
	ips, sites = gds.GeoFilePaths()
	geoIP, geoSite = len(ips), len(sites)
	s.appLog.Info("sync-geo", "", fmt.Sprintf("patch-only geo file sync: geoip=%d geosite=%d", geoIP, geoSite))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := WriteGeoFilesOnly(ips, sites); err != nil {
		s.appLog.Warn("sync-geo", "", "patch-only geo file sync failed: "+err.Error())
		return err
	}
	s.scheduleRestart("geo-sync")
	return nil
}

// RescanGeoFiles adopts geo paths from hrneo.conf that are not yet tracked.
func (s *Service) RescanGeoFiles() (int, error) {
	cfg, err := ReadConfig()
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	gds := s.geodata
	s.mu.Unlock()
	if gds == nil {
		return 0, fmt.Errorf("geo data store not initialized")
	}
	// Catalog-only: paths already live in hrneo.conf — no config rewrite or
	// neo restart (keeps tab-open rescan cheap).
	return gds.AdoptExternalFiles(cfg)
}

// CalculateIpsetUsage returns the current ipset usage per kernel interface.
func (s *Service) CalculateIpsetUsage() (*IpsetUsage, error) {
	cfg, err := ReadConfig()
	if err != nil {
		return nil, err
	}

	usage := &IpsetUsage{
		MaxElem: cfg.EffectiveMaxElem(),
		Usage:   make(map[string]int),
	}

	s.mu.Lock()
	provider := s.dnsListProvider
	gds := s.geodata
	s.mu.Unlock()

	if provider == nil || gds == nil {
		return usage, nil
	}

	// Build geoip tag→count lookup from all tracked geoip files (first file wins for duplicate tags).
	geoIPCount := make(map[string]int)
	geoIPFiles, _ := gds.GeoFilePaths()
	for _, path := range geoIPFiles {
		tags, err := gds.GetTags(path)
		if err != nil {
			continue
		}
		for _, t := range tags {
			key := strings.ToLower(t.Name)
			if _, exists := geoIPCount[key]; !exists {
				geoIPCount[key] = t.Count
			}
		}
	}

	lists := provider()
	for _, list := range lists {
		if list.TunnelID == "" {
			continue
		}

		iface, err := s.resolver.GetKernelIfaceName(context.Background(), list.TunnelID)
		if err != nil {
			continue
		}

		for _, subnet := range list.Subnets {
			lower := strings.ToLower(subnet)
			if strings.HasPrefix(lower, "geoip:") {
				tag := strings.TrimPrefix(lower, "geoip:")
				if count, ok := geoIPCount[tag]; ok {
					usage.Usage[iface] += count
				}
			} else {
				// Static CIDR counts as 1.
				usage.Usage[iface]++
			}
		}
	}

	return usage, nil
}
