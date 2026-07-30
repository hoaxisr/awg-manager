package wdtt

import (
	"context"
	"fmt"
	"strings"

	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
)

const (
	// Диапазон 17..49: вне fakeip (0..9) и типичных awg10..16 / managed 100+.
	wdttOpkgIndexMin    = 17
	wdttOpkgIndexMax    = 49
	wdttLegacyIndexMin  = 90
	wdttOpkgDescription = "AWGM WDTT"
)

// OpkgTunIndexLister reports occupied OpkgTun indices (kernel ∪ NDMS).
type OpkgTunIndexLister interface {
	LiveOpkgTunIndices(ctx context.Context) (map[int]bool, error)
}

// OpkgTunExistChecker reports whether an NDMS OpkgTun id already exists.
type OpkgTunExistChecker interface {
	OpkgTunExists(ctx context.Context, ndmsName string) bool
}

func opkgTunNDMSName(index int) string {
	return fmt.Sprintf("OpkgTun%d", index)
}

func opkgTunKernelName(index int) string {
	return fmt.Sprintf("opkgtun%d", index)
}

func parseOpkgTunIndex(ndmsName string) (int, bool) {
	ndmsName = strings.TrimSpace(ndmsName)
	if !strings.HasPrefix(ndmsName, "OpkgTun") {
		return 0, false
	}
	var idx int
	if _, err := fmt.Sscanf(ndmsName, "OpkgTun%d", &idx); err != nil {
		return 0, false
	}
	return idx, true
}

func (cfg ServerConfig) kernelWGIface() string {
	if iface := strings.TrimSpace(cfg.WgIface); iface != "" {
		return iface
	}
	return DefaultWdttIface
}

func (cfg ServerConfig) ndmsAccessIface() string {
	if iface := strings.TrimSpace(cfg.NdmsIface); iface != "" {
		return iface
	}
	return DefaultWdttIface
}

func (cfg ServerConfig) usesNDMSOpkgTun() bool {
	_, ok := parseOpkgTunIndex(cfg.NdmsIface)
	return ok && strings.TrimSpace(cfg.WgIface) != "" && cfg.WgIface != DefaultWdttIface
}

func allocateWdttOpkgIndex(live map[int]bool) (int, error) {
	for i := wdttOpkgIndexMin; i <= wdttOpkgIndexMax; i++ {
		if !live[i] {
			return i, nil
		}
	}
	return 0, fmt.Errorf("нет свободного OpkgTun в диапазоне %d..%d", wdttOpkgIndexMin, wdttOpkgIndexMax)
}

func isLegacyWdttOpkgIndex(idx int) bool {
	return idx >= wdttLegacyIndexMin
}

func (s *Service) serverSupportsWgIface(ctx context.Context) bool {
	if s.wgIfaceFlagKnown {
		return s.wgIfaceFlagOK
	}
	s.wgIfaceFlagKnown = true
	if strings.TrimSpace(s.serverBin) == "" {
		return false
	}
	out, err := probeBinaryHelp(ctx, s.serverBin)
	if err != nil {
		return false
	}
	s.wgIfaceFlagOK = strings.Contains(out, "-wg-iface")
	return s.wgIfaceFlagOK
}

// ensureServerOpkgIndex picks/persists OpkgTun index (NdmsIface/WgIface).
// NDMS create/address happens later in prepareNDMSOpkgTun (после выделения).
func (s *Service) ensureServerOpkgIndex(ctx context.Context, id string, cfg ServerConfig) (ServerConfig, error) {
	if s.ndmsIfaces == nil || s.opkgIndices == nil {
		return cfg, nil
	}
	if !s.serverSupportsWgIface(ctx) {
		if s.appLog != nil {
			s.appLog.Warn("ndms", id, "wdtt-server без -wg-iface: NDMS OpkgTun недоступен, используется legacy wdtt0 + iptables")
		}
		return cfg, nil
	}

	if cfg.usesNDMSOpkgTun() {
		if idx, ok := parseOpkgTunIndex(cfg.NdmsIface); ok && isLegacyWdttOpkgIndex(idx) {
			if s.appLog != nil {
				s.appLog.Info("ndms", id, fmt.Sprintf("миграция с legacy OpkgTun%d → новый диапазон %d..%d", idx, wdttOpkgIndexMin, wdttOpkgIndexMax))
			}
			_ = s.teardownServerOpkgTun(ctx, cfg)
			cfg.NdmsIface = ""
			cfg.WgIface = ""
		} else {
			return cfg, nil
		}
	}

	live, err := s.opkgIndices.LiveOpkgTunIndices(ctx)
	if err != nil {
		return cfg, fmt.Errorf("list opkgtun indices: %w", err)
	}
	idx, err := allocateWdttOpkgIndex(live)
	if err != nil {
		return cfg, err
	}
	ndmsName := opkgTunNDMSName(idx)
	kernelName := opkgTunKernelName(idx)

	cfg.NdmsIface = ndmsName
	cfg.WgIface = kernelName
	if err := s.persistServerConfig(id, cfg); err != nil {
		return cfg, err
	}
	if s.appLog != nil {
		s.appLog.Info("ndms", id, fmt.Sprintf("выделен %s → %s", ndmsName, kernelName))
	}
	return cfg, nil
}

func (s *Service) opkgTunExists(ctx context.Context, ndmsName string) bool {
	if s.opkgExist == nil {
		return false
	}
	return s.opkgExist.OpkgTunExists(ctx, ndmsName)
}

// prepareNDMSOpkgTun registers OpkgTun in NDMS and sets address/MTU/global
// before wdtt-server creates the kernel iface (порядок как AWG ColdStart).
func (s *Service) prepareNDMSOpkgTun(ctx context.Context, cfg ServerConfig) error {
	if !cfg.usesNDMSOpkgTun() || s.ndmsIfaces == nil {
		return nil
	}
	ndmsName := cfg.ndmsAccessIface()
	if !s.opkgTunExists(ctx, ndmsName) {
		if err := s.ndmsIfaces.CreateOpkgTunWithSecurityLevel(ctx, ndmsName, wdttOpkgDescription, "public"); err != nil {
			return fmt.Errorf("create %s: %w", ndmsName, err)
		}
	}
	if err := s.ndmsIfaces.SetAddress(ctx, ndmsName, DefaultWdttAddress, DefaultWdttMask); err != nil {
		return fmt.Errorf("set address %s: %w", ndmsName, err)
	}
	if err := s.ndmsIfaces.SetMTU(ctx, ndmsName, 1280); err != nil {
		return fmt.Errorf("set mtu %s: %w", ndmsName, err)
	}
	if err := s.ndmsIfaces.SetIPGlobal(ctx, ndmsName); err != nil && s.appLog != nil {
		s.appLog.Warn("ndms", ndmsName, "ip global: "+err.Error())
	}
	return nil
}

// activateNDMSOpkgTun brings NDMS iface up and applies firewall permit after
// kernel opkgtunN is live.
func (s *Service) activateNDMSOpkgTun(ctx context.Context, cfg ServerConfig) error {
	if !cfg.usesNDMSOpkgTun() || s.ndmsIfaces == nil {
		return nil
	}
	ndmsName := cfg.ndmsAccessIface()
	if err := s.ndmsIfaces.InterfaceUp(ctx, ndmsName); err != nil {
		return fmt.Errorf("iface up %s: %w", ndmsName, err)
	}
	if err := s.ndmsIfaces.SetPermitAllACL(ctx, ndmsName); err != nil {
		if s.appLog != nil {
			s.appLog.Warn("ndms", ndmsName, "firewall permit пропущен: "+err.Error())
		}
	}
	return nil
}

func (s *Service) teardownServerOpkgTun(ctx context.Context, cfg ServerConfig) error {
	if !cfg.usesNDMSOpkgTun() || s.ndmsIfaces == nil {
		return nil
	}
	ndmsName := cfg.ndmsAccessIface()
	_ = s.ndmsIfaces.InterfaceDown(ctx, ndmsName)
	_ = s.ndmsIfaces.RemovePermitAllACL(ctx, ndmsName)
	return s.ndmsIfaces.DeleteOpkgTun(ctx, ndmsName)
}

func (s *Service) persistServerConfig(id string, cfg ServerConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		return fmt.Errorf("сервер %q не найден", id)
	}
	full.Servers[idx].Config = cfg
	return s.store.Save(full)
}

func (s *Service) SetNDMSInterfaceCommands(c *ndmscommand.InterfaceCommands) {
	s.ndmsIfaces = c
}

func (s *Service) SetOpkgTunIndexLister(l OpkgTunIndexLister) {
	s.opkgIndices = l
}

func (s *Service) SetOpkgTunExistChecker(c OpkgTunExistChecker) {
	s.opkgExist = c
}

type RouterReconciler interface {
	Reconcile(ctx context.Context) error
}

func (s *Service) SetRouterReconciler(r RouterReconciler) {
	s.routerReconcile = r
}

func (s *Service) maybeReconcileRouter(ctx context.Context) {
	if s.routerReconcile == nil {
		return
	}
	if err := s.routerReconcile.Reconcile(ctx); err != nil && s.appLog != nil {
		s.appLog.Warn("router-reconcile", "", err.Error())
	}
}

var _ OpkgTunIndexLister = (*routerOpkgStub)(nil)

type routerOpkgStub struct {
	live map[int]bool
}

func (r *routerOpkgStub) LiveOpkgTunIndices(context.Context) (map[int]bool, error) {
	return r.live, nil
}
