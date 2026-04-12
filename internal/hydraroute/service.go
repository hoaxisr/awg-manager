package hydraroute

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// KernelIfaceResolver resolves tunnel IDs to kernel interface names.
type KernelIfaceResolver interface {
	GetKernelIfaceName(ctx context.Context, tunnelID string) (string, error)
}

// Service manages HydraRoute Neo integration: detection, config writes, daemon control.
type Service struct {
	resolver          KernelIfaceResolver
	log               *logger.Logger
	mu                sync.Mutex
	status            Status
	restartTimer      *time.Timer
	lastDomainContent string
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

	domainContent := GenerateDomainConf(lists)
	ipContent := GenerateIPList(lists)

	if err := WriteManagedSection(domainConfPath, domainContent); err != nil {
		return fmt.Errorf("write domain.conf: %w", err)
	}

	if err := WriteManagedSection(ipListPath, ipContent); err != nil {
		_ = WriteManagedSection(domainConfPath, s.lastDomainContent)
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
		result, err := exec.Run(context.Background(), neoBinary, action)
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
		result, err := exec.Run(context.Background(), neoBinary, "restart")
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
