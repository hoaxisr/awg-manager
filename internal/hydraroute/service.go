package hydraroute

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// KernelIfaceResolver resolves tunnel IDs to kernel interface names.
type KernelIfaceResolver interface {
	GetKernelIfaceName(ctx context.Context, tunnelID string) (string, error)
}

// ndmsRCI is the minimal NDMS client interface used by Service.
type ndmsRCI interface {
	RCIPost(ctx context.Context, payload interface{}) (json.RawMessage, error)
	RCIGet(ctx context.Context, path string) (json.RawMessage, error)
}

// NativeImporter is implemented by the DNS route service to import native HydraRoute rules.
// This interface breaks the import cycle: hydraroute cannot import dnsroute directly.
type NativeImporter interface {
	ImportNativeRules(ctx context.Context, rules []NativeRule, ipBlocks []NativeIPBlock) (int, error)
}

// Service manages HydraRoute Neo integration: detection, config writes, daemon control.
type Service struct {
	resolver          KernelIfaceResolver
	log               *logger.Logger
	mu                sync.Mutex
	status            Status
	restartTimer      *time.Timer
	lastDomainContent string
	geodata           *GeoDataStore
	dnsListProvider   func() []DnsListInfo
	nativeImporter    NativeImporter
	ndms              ndmsRCI
}

// NewService creates a new HydraRoute service. Detects HRNeo on creation.
func NewService(resolver KernelIfaceResolver, log *logger.Logger) *Service {
	s := &Service{
		resolver: resolver,
		log:      log,
		status:   Detect(),
	}
	if s.status.Installed {
		s.log.Infof("hydraroute: detected (running=%v)", s.status.Running)
	}
	return s
}

// GetStatus returns cached detection status.
func (s *Service) GetStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// RefreshStatus re-detects HydraRoute and updates cached status.
func (s *Service) RefreshStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = Detect()
	return s.status
}

// Apply writes managed sections to domain.conf and ip.list, then schedules neo restart.
func (s *Service) Apply(ctx context.Context, lists []ManagedEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.status.Installed {
		return fmt.Errorf("HydraRoute Neo is not installed")
	}

	// Capture the current managed section before any writes for rollback.
	prevDomainManaged := s.lastDomainContent
	if prevDomainManaged == "" {
		// First Apply: read what's currently in the file so rollback restores it.
		prevDomainManaged = readManagedSection(domainConfPath)
	}

	domainContent := GenerateDomainConf(lists)
	ipContent := GenerateIPList(lists)

	if err := WriteManagedSection(domainConfPath, domainContent); err != nil {
		return fmt.Errorf("write domain.conf: %w", err)
	}

	if err := WriteManagedSection(ipListPath, ipContent); err != nil {
		_ = WriteManagedSection(domainConfPath, prevDomainManaged)
		return fmt.Errorf("write ip.list (domain.conf rolled back): %w", err)
	}

	s.lastDomainContent = domainContent
	s.scheduleRestart()
	return nil
}

// Remove clears all managed entries from HydraRoute config files.
func (s *Service) Remove(ctx context.Context) error {
	return s.Apply(ctx, nil)
}

// Control starts/stops/restarts the HydraRoute daemon.
func (s *Service) Control(action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.status.Installed {
		return fmt.Errorf("HydraRoute Neo is not installed")
	}

	switch action {
	case "start", "stop", "restart":
		result, err := exec.Run(context.Background(), neoCommand, action)
		if err != nil {
			return fmt.Errorf("neo %s: %w", action, exec.FormatError(result, err))
		}
		s.status = Detect()
		return nil
	default:
		return fmt.Errorf("unknown action: %s", action)
	}
}

// scheduleRestart debounces neo restart: resets timer on each call.
func (s *Service) scheduleRestart() {
	if s.restartTimer != nil {
		s.restartTimer.Stop()
	}
	s.restartTimer = time.AfterFunc(2*time.Second, func() {
		// Mark timer as completed before releasing the lock so a concurrent
		// scheduleRestart sees nil and creates a fresh timer rather than
		// stopping an already-fired one.
		s.mu.Lock()
		s.restartTimer = nil
		s.mu.Unlock()

		result, err := exec.Run(context.Background(), neoCommand, "restart")
		if err != nil {
			s.log.Warnf("hydraroute: neo restart failed: %v", exec.FormatError(result, err))
		} else {
			s.log.Infof("hydraroute: neo restarted")
		}
		s.mu.Lock()
		s.status = Detect()
		s.mu.Unlock()
	})
}

// readManagedSection reads the AWG-managed section (including markers) from the
// given file. Returns an empty string if the file doesn't exist or has no markers.
// Used to capture the current state for rollback before the first Apply.
func readManagedSection(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := string(raw)
	start := strings.Index(s, markerStart)
	end := strings.Index(s, markerEnd)
	if start < 0 || end < 0 || end <= start {
		return ""
	}
	endOfMarker := end + len(markerEnd)
	if endOfMarker < len(s) && s[endOfMarker] == '\n' {
		endOfMarker++
	}
	return s[start:endOfMarker]
}

// BuildEntries converts domain lists with resolved tunnel interfaces into ManagedEntry slice.
func (s *Service) BuildEntries(ctx context.Context, lists []ListInput) ([]ManagedEntry, error) {
	var entries []ManagedEntry
	for _, l := range lists {
		if len(l.Domains) == 0 && len(l.Subnets) == 0 {
			continue
		}
		if l.TunnelID == "" {
			continue
		}

		// Policy mode: use the policy name as the routing target directly,
		// no kernel interface resolution needed.
		if l.HRRouteMode == "policy" && l.HRPolicyName != "" {
			entries = append(entries, ManagedEntry{
				ListID:   l.ListID,
				ListName: l.ListName,
				Domains:  l.Domains,
				Subnets:  l.Subnets,
				Iface:    l.HRPolicyName,
			})
			continue
		}

		// Interface mode: resolve tunnel ID to kernel interface name.
		iface, err := s.resolver.GetKernelIfaceName(ctx, l.TunnelID)
		if err != nil {
			return nil, fmt.Errorf("resolve tunnel %s: %w", l.TunnelID, err)
		}
		entries = append(entries, ManagedEntry{
			ListID:   l.ListID,
			ListName: l.ListName,
			Domains:  l.Domains,
			Subnets:  l.Subnets,
			Iface:    iface,
		})
	}
	return entries, nil
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

// SetNativeImporter sets the NativeImporter used by ImportNative.
func (s *Service) SetNativeImporter(imp NativeImporter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nativeImporter = imp
}

// SetNDMS sets the NDMS RCI client used by EnsurePolicy.
func (s *Service) SetNDMS(client ndmsRCI) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ndms = client
}

// GetGeoData returns the current GeoDataStore.
func (s *Service) GetGeoData() *GeoDataStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.geodata
}

// ImportNative reads native (non-managed) rules from domain.conf and ip.list,
// delegates conversion to the NativeImporter (dnsroute.ServiceImpl), then
// removes the native blocks from the HydraRoute config files. Idempotent:
// lists whose name already exists in the store are skipped by the importer.
// Returns the number of newly imported lists.
func (s *Service) ImportNative(ctx context.Context) (int, error) {
	s.mu.Lock()
	installed := s.status.Installed
	importer := s.nativeImporter
	s.mu.Unlock()

	if !installed || importer == nil {
		return 0, nil
	}

	domainRules, err := ParseNativeDomainConf()
	if err != nil {
		return 0, fmt.Errorf("parse domain.conf: %w", err)
	}
	if len(domainRules) == 0 {
		return 0, nil
	}

	ipBlocks, _ := ParseNativeIPList()

	n, err := importer.ImportNativeRules(ctx, domainRules, ipBlocks)
	if err != nil {
		return 0, err
	}

	if n > 0 {
		_ = RemoveNativeFromDomainConf()
		_ = RemoveNativeFromIPList()
		s.log.Infof("hydraroute: imported %d native rules", n)
	}

	return n, nil
}

// EnsurePolicyInterfaces permits the given NDMS interfaces in the policy.
// HR Neo creates the policy itself; we only need to add interfaces.
func (s *Service) EnsurePolicyInterfaces(ctx context.Context, policyName string, ndmsIfaces []string) error {
	s.mu.Lock()
	client := s.ndms
	s.mu.Unlock()

	if client == nil {
		return fmt.Errorf("NDMS client not available")
	}

	for i, iface := range ndmsIfaces {
		payload := map[string]interface{}{
			"ip": map[string]interface{}{
				"policy": map[string]interface{}{
					policyName: map[string]interface{}{
						"permit": map[string]interface{}{
							"global":    true,
							"interface": iface,
							"order":     i + 1,
						},
					},
				},
			},
		}
		if _, err := client.RCIPost(ctx, payload); err != nil {
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

	if err := WriteConfig(cfg); err != nil {
		return err
	}

	s.scheduleRestart()
	return nil
}

// SetPolicyOrder updates only the PolicyOrder field in hrneo.conf and restarts.
func (s *Service) SetPolicyOrder(order []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := ReadConfig()
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	cfg.PolicyOrder = order

	if s.geodata != nil {
		geoIP, geoSite := s.geodata.GeoFilePaths()
		cfg.GeoIPFiles = geoIP
		cfg.GeoSiteFiles = geoSite
	}

	if err := WriteConfig(cfg); err != nil {
		return err
	}

	s.scheduleRestart()
	return nil
}

// SyncGeoFilesToConfig reads the current config and writes it back with updated geo file paths.
func (s *Service) SyncGeoFilesToConfig() error {
	cfg, err := ReadConfig()
	if err != nil {
		return err
	}
	return s.WriteConfig(cfg)
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
