package hydraroute

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
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
	resolver        KernelIfaceResolver
	log             *logger.Logger
	mu              sync.Mutex
	status          Status
	restartTimer    *time.Timer
	geodata         *GeoDataStore
	dnsListProvider func() []DnsListInfo
	queries         *query.Queries
	policies        *command.PolicyCommands
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
		if s.log != nil {
			s.log.Infof("hydraroute: ip policy %s permit global %s order %d", policyName, iface, i)
		}
		if err := policies.PermitInterface(ctx, policyName, iface, i); err != nil {
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
	if s.log != nil {
		geoIP, geoSite := 0, 0
		if s.geodata != nil {
			ips, sites := s.geodata.GeoFilePaths()
			geoIP, geoSite = len(ips), len(sites)
		}
		s.log.Infof("hydraroute: sync geo files to config — %d geoip + %d geosite", geoIP, geoSite)
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
