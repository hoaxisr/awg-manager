package wdtt

import (
	"context"
	"fmt"
	"strings"
)

const wdttOpkgClientDescription = "AWGM WDTT Raw Client"

func configReservedOpkgIndices(full Config, skipServerID, skipClientID string) map[int]bool {
	reserved := map[int]bool{}
	for _, srv := range full.Servers {
		if srv.ID == skipServerID {
			continue
		}
		if idx, ok := parseOpkgTunIndex(srv.Config.NdmsIface); ok {
			reserved[idx] = true
		}
	}
	for _, cl := range full.Clients {
		if cl.ID == skipClientID {
			continue
		}
		if idx, ok := parseOpkgTunIndex(cl.Config.NdmsIface); ok {
			reserved[idx] = true
		}
	}
	return reserved
}

func mergeOpkgIndexMaps(live, reserved map[int]bool) map[int]bool {
	out := map[int]bool{}
	for k, v := range live {
		if v {
			out[k] = true
		}
	}
	for k, v := range reserved {
		if v {
			out[k] = true
		}
	}
	return out
}

func allocateWdttOpkgIndexFrom(live map[int]bool) (int, error) {
	for i := wdttOpkgIndexMin; i <= wdttOpkgIndexMax; i++ {
		if !live[i] {
			return i, nil
		}
	}
	return 0, fmt.Errorf("нет свободного OpkgTun в диапазоне %d..%d", wdttOpkgIndexMin, wdttOpkgIndexMax)
}

func (s *Service) ensureClientOpkgIndex(ctx context.Context, id string, cfg ClientConfig) (ClientConfig, error) {
	if s.ndmsIfaces == nil || s.opkgIndices == nil {
		return cfg, fmt.Errorf("raw-клиент требует NDMS OpkgTun (Keenetic)")
	}
	if cfg.usesNDMSOpkgTun() {
		if idx, ok := parseOpkgTunIndex(cfg.NdmsIface); ok && isLegacyWdttOpkgIndex(idx) {
			if s.appLog != nil {
				s.appLog.Info("ndms", id, fmt.Sprintf("миграция raw client с legacy OpkgTun%d", idx))
			}
			_ = s.teardownClientOpkgTun(ctx, cfg)
			cfg.NdmsIface = ""
			cfg.RawIface = ""
		} else {
			return cfg, nil
		}
	}
	full, err := s.store.Load()
	if err != nil {
		return cfg, err
	}
	live, err := s.opkgIndices.LiveOpkgTunIndices(ctx)
	if err != nil {
		return cfg, fmt.Errorf("list opkgtun indices: %w", err)
	}
	live = mergeOpkgIndexMaps(live, configReservedOpkgIndices(full, "", id))
	idx, err := allocateWdttOpkgIndexFrom(live)
	if err != nil {
		return cfg, err
	}
	cfg.NdmsIface = opkgTunNDMSName(idx)
	cfg.RawIface = opkgTunKernelName(idx)
	if err := s.persistClientConfig(id, cfg); err != nil {
		return cfg, err
	}
	if s.appLog != nil {
		s.appLog.Info("ndms", id, fmt.Sprintf("raw client: выделен %s → %s", cfg.NdmsIface, cfg.RawIface))
	}
	return cfg, nil
}

func (s *Service) clientOpkgNDMSDescription(id string) string {
	inst, err := s.clientInstance(id)
	if err != nil {
		return TunnelNameFromClient(ClientInstance{Name: "WDTT"})
	}
	return TunnelNameFromClient(inst)
}

func (s *Service) syncClientOpkgNDMSDescription(ctx context.Context, id string, cfg ClientConfig) {
	if !cfg.usesNDMSOpkgTun() || s.ndmsIfaces == nil {
		return
	}
	desc := s.clientOpkgNDMSDescription(id)
	ndmsName := cfg.ndmsAccessIface()
	if err := s.ndmsIfaces.SetDescription(ctx, ndmsName, desc); err != nil && s.appLog != nil {
		s.appLog.Warn("ndms", id, "set description "+ndmsName+": "+err.Error())
	}
}

func (s *Service) prepareClientNDMSOpkgTun(ctx context.Context, id string, cfg ClientConfig) error {
	if !cfg.usesNDMSOpkgTun() || s.ndmsIfaces == nil {
		return nil
	}
	ndmsName := cfg.ndmsAccessIface()
	desc := s.clientOpkgNDMSDescription(id)
	if !s.opkgTunExists(ctx, ndmsName) {
		// public uplink: иначе OpkgTun не попадает в политики/HydraRoute.
		if err := s.ndmsIfaces.CreateOpkgTunWithSecurityLevel(ctx, ndmsName, desc, "public"); err != nil {
			return fmt.Errorf("create %s: %w", ndmsName, err)
		}
	}
	mtu := 1300
	if err := s.ndmsIfaces.SetMTU(ctx, ndmsName, mtu); err != nil {
		return fmt.Errorf("set mtu %s: %w", ndmsName, err)
	}
	return nil
}

func (s *Service) activateClientNDMSOpkgTun(ctx context.Context, id string, cfg ClientConfig, conf RawConfPayload) error {
	if !cfg.usesNDMSOpkgTun() || s.ndmsIfaces == nil {
		return nil
	}
	ndmsName := cfg.ndmsAccessIface()
	mtu := conf.MTU
	if mtu < 576 {
		mtu = 1300
	}
	if err := s.ndmsIfaces.SetAddress(ctx, ndmsName, conf.ClientIP, DefaultRawClientMask); err != nil {
		return fmt.Errorf("set address %s: %w", ndmsName, err)
	}
	if err := s.ndmsIfaces.SetMTU(ctx, ndmsName, mtu); err != nil {
		return fmt.Errorf("set mtu %s: %w", ndmsName, err)
	}
	if err := s.ndmsIfaces.InterfaceUp(ctx, ndmsName); err != nil {
		return fmt.Errorf("iface up %s: %w", ndmsName, err)
	}
	if err := s.ensureClientOpkgUplinkNDMS(ctx, ndmsName); err != nil {
		return err
	}
	s.syncClientOpkgNDMSDescription(ctx, id, cfg)
	if err := s.ndmsIfaces.SetPermitAllACL(ctx, ndmsName); err != nil && s.appLog != nil {
		s.appLog.Warn("ndms", ndmsName, "firewall permit пропущен: "+err.Error())
	}
	return nil
}

// ensureClientOpkgUplinkNDMS делает raw-клиентский OpkgTun видимым как uplink
// (политики, HydraRoute) и маршрутизируемым NDMS.
func (s *Service) ensureClientOpkgUplinkNDMS(ctx context.Context, ndmsName string) error {
	if s.ndmsIfaces == nil {
		return nil
	}
	if err := s.ndmsIfaces.SetSecurityLevel(ctx, ndmsName, "public"); err != nil {
		return fmt.Errorf("security-level public %s: %w", ndmsName, err)
	}
	// После address/up — как managed AWG (operator_os5), не до адреса.
	if err := s.ndmsIfaces.SetIPGlobal(ctx, ndmsName); err != nil {
		return fmt.Errorf("ip global %s: %w", ndmsName, err)
	}
	return nil
}

func (s *Service) teardownClientOpkgTun(ctx context.Context, cfg ClientConfig) error {
	if !cfg.usesNDMSOpkgTun() {
		return nil
	}
	s.captureOpkgPolicyPermitsForConfig(ctx, cfg)
	return s.teardownOpkgTunByName(ctx, cfg.ndmsAccessIface(), "wdtt-raw-client")
}

func (s *Service) persistClientConfig(id string, cfg ClientConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		return fmt.Errorf("клиент %q не найден", id)
	}
	full.Clients[idx].Config = cfg
	return s.store.Save(full)
}

// ensureClientDeviceID выдаёт стабильный device-id на инстанс клиента.
// VPS выделяет raw IP по device_id (allocRawIP); «unknown» у всех → коллизия 10.70.0.2.
func (s *Service) ensureClientDeviceID(id string, cfg ClientConfig) (ClientConfig, error) {
	if strings.TrimSpace(cfg.DeviceID) != "" {
		return cfg, nil
	}
	cfg.DeviceID = "awgm-" + strings.TrimSpace(id)
	if cfg.DeviceID == "awgm-" {
		cfg.DeviceID = "awgm-default"
	}
	if err := s.persistClientConfig(id, cfg); err != nil {
		return cfg, err
	}
	if s.appLog != nil {
		s.appLog.Info("client", id, "device-id: "+cfg.DeviceID)
	}
	return cfg, nil
}
