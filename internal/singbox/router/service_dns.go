package router

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func (s *ServiceImpl) ListDNSServers(ctx context.Context) ([]DNSServer, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	return cfg.DNS.Servers, nil
}

func (s *ServiceImpl) AddDNSServer(ctx context.Context, srv DNSServer) error {
	srv = s.withLocalDNSBind(srv)
	if err := s.dnsBindAddressLocal(srv); err != nil {
		return err
	}
	return s.withConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.AddDNSServer(srv) })
}

func (s *ServiceImpl) UpdateDNSServer(ctx context.Context, tag string, srv DNSServer) error {
	srv = s.dropDerivedDNSBind(tag, srv)
	srv = s.withLocalDNSBind(srv)
	bindErr := s.dnsBindAddressLocal(srv)
	return s.withConfig(ctx, "dns-servers", func(c *RouterConfig) error {
		if bindErr != nil && !keepsDNSBindAddress(c, tag, srv) {
			return bindErr
		}
		if err := c.UpdateDNSServer(tag, srv); err != nil {
			return err
		}
		return s.captureDNSChainServerRename(tag, srv.Tag)
	})
}

// dnsBindAddressLocal — inet4_bind_address назначен интерфейсу роутера.
// Считается ДО мутатора: список адресов читается из ядра. Неразборчивый адрес
// и не-IPv4 пропускаются — их отвергнет validateDNSServer с точной причиной.
func (s *ServiceImpl) dnsBindAddressLocal(srv DNSServer) error {
	want, err := netip.ParseAddr(srv.Inet4BindAddress)
	if err != nil || !want.Is4() {
		return nil
	}
	addrs, err := s.deps.LocalAddrs()
	if err != nil {
		return fmt.Errorf("адреса интерфейсов роутера: %w", err)
	}
	if AddrIsLocal(addrs, want) {
		return nil
	}
	return fmt.Errorf("%w: %s не назначен ни одному интерфейсу роутера", ErrDNSBindAddress, want)
}

// AddrIsLocal — want назначен одному из интерфейсов (addrs из net.InterfaceAddrs).
func AddrIsLocal(addrs []net.Addr, want netip.Addr) bool {
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.Unmap() == want {
				return true
			}
		}
	}
	return false
}

// localDNSSelfBind — адрес отправителя для DNS-сервера на собственном адресе
// роутера (F702): под auto_detect_interface/default_interface sing-box биндит
// сокет к интерфейсу-владельцу адреса, и на ядре Keenetic запрос к своему
// адресу висит — DNS через sing-box мёртв целиком (стенд 21.10). Любой адрес
// отправителя снимает автопривязку; собственный — единственный, что пропустит
// dnsBindAddressLocal. Только пустое поле и без detour (с ним поля
// подключения игнорируются): явный выбор пользователя не трогаем.
func localDNSSelfBind(srv DNSServer, addrs []net.Addr) (string, bool) {
	if srv.Inet4BindAddress != "" || srv.Detour != "" || srv.Type == "local" || srv.Type == "fakeip" || srv.Tag == managedDNSDirectTag {
		return "", false
	}
	ip, err := netip.ParseAddr(srv.Server)
	if err != nil || !ip.Is4() || !AddrIsLocal(addrs, ip) {
		return "", false
	}
	return ip.String(), true
}

// dropDerivedDNSBind снимает выведенный адрес отправителя (равный адресу
// сервера, см. localDNSSelfBind), когда адрес сервера меняется, а адрес
// отправителя пришёл прежним: окно правки шлёт сохранённое обратно, и
// подстановка прилипла бы — запросы к новому серверу шли бы с LAN-адреса мимо
// выбора WAN. withLocalDNSBind затем вычислит его заново. Явный адрес
// (отличный от адреса сервера) — выбор пользователя, его не трогаем.
func (s *ServiceImpl) dropDerivedDNSBind(tag string, srv DNSServer) DNSServer {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return srv
	}
	for _, old := range cfg.DNS.Servers {
		if old.Tag == tag && old.Inet4BindAddress != "" && old.Inet4BindAddress == old.Server &&
			srv.Inet4BindAddress == old.Inet4BindAddress && srv.Server != old.Server {
			srv.Inet4BindAddress = ""
		}
	}
	return srv
}

// withLocalDNSBind — localDNSSelfBind при сохранении сервера. Сбой чтения
// адресов роутера сохранение не роняет: подстановка лишь не случится.
func (s *ServiceImpl) withLocalDNSBind(srv DNSServer) DNSServer {
	if ip, err := netip.ParseAddr(srv.Server); err != nil || !ip.Is4() || s.deps.LocalAddrs == nil {
		return srv
	}
	addrs, err := s.deps.LocalAddrs()
	if err != nil {
		s.appLog.Warn("dns-servers", srv.Tag, "адреса интерфейсов роутера: "+err.Error()+" — адрес отправителя не подставлен")
		return srv
	}
	if bind, ok := localDNSSelfBind(srv, addrs); ok {
		srv.Inet4BindAddress = bind
	}
	return srv
}

// keepsDNSBindAddress — у записи tag в c тот же inet4_bind_address. Однажды
// принятый адрес Update не перепроверяет: адрес мог пропасть вместе с
// интерфейсом, и правка остальных полей от этого ломаться не должна — как
// keepsBind у direct-выходов (#961).
func keepsDNSBindAddress(c *RouterConfig, tag string, srv DNSServer) bool {
	for _, e := range c.DNS.Servers {
		if e.Tag == tag {
			return e.Inet4BindAddress == srv.Inet4BindAddress
		}
	}
	return false
}

func (s *ServiceImpl) DeleteDNSServer(ctx context.Context, tag string, force bool) error {
	return s.withConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.DeleteDNSServer(tag, force) })
}

func (s *ServiceImpl) MoveDNSServer(ctx context.Context, from, to int) error {
	return s.withConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.MoveDNSServer(from, to) })
}

func (s *ServiceImpl) ListDNSRules(ctx context.Context) ([]DNSRule, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	return s.ruleSetMaterializer().restoreConfig(cfg).DNS.Rules, nil
}

func (s *ServiceImpl) AddDNSRule(ctx context.Context, r DNSRule) error {
	return s.withConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.AddDNSRule(r) })
}

func (s *ServiceImpl) UpdateDNSRule(ctx context.Context, index int, r DNSRule) error {
	return s.withConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.UpdateDNSRule(index, r) })
}

func (s *ServiceImpl) DeleteDNSRule(ctx context.Context, index int) error {
	return s.withConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.DeleteDNSRule(index) })
}

func (s *ServiceImpl) MoveDNSRule(ctx context.Context, from, to int) error {
	return s.withConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.MoveDNSRule(from, to) })
}

func (s *ServiceImpl) GetDNSGlobals(ctx context.Context) (string, string, string, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return "", "", "", err
	}
	return cfg.DNS.Final, cfg.DNS.Strategy, cfg.DNS.Timeout, nil
}

func (s *ServiceImpl) SetDNSGlobals(ctx context.Context, final, strategy, timeout string) error {
	return s.withConfig(ctx, "dns-globals", func(c *RouterConfig) error { return c.SetDNSGlobals(final, strategy, timeout) })
}

// GetDNSChainPreset возвращает состояние DNS-пресета (Mode "" = выключен).
func (s *ServiceImpl) GetDNSChainPreset(_ context.Context) (storage.DNSChainPresetState, error) {
	st, err := s.dnsChainPresetState()
	if err != nil || st == nil {
		return storage.DNSChainPresetState{}, err
	}
	return *st, nil
}

// SetDNSChainPreset включает/переключает/выключает DNS-пресет: валидирует st в
// контексте текущего конфига, сохраняет состояние и переассертит цепочку.
//
// Состояние пишется ДО persist'а конфига (осознанный trade-off staging-пути):
// пресет применяется сразу, а цепочка появится в active после Apply. Если
// пользователь сделает Discard, состояние останется — и цепочка вернётся при
// следующей мутации через ensure-хук withConfig.
func (s *ServiceImpl) SetDNSChainPreset(ctx context.Context, st storage.DNSChainPresetState) error {
	return s.withConfig(ctx, "dns-rules", func(c *RouterConfig) error {
		var stored *storage.DNSChainPresetState
		if st.Mode != "" {
			if _, err := dnsChainRules(c, &st); err != nil {
				return err
			}
			stored = &st
		}
		if err := s.deps.Settings.SetDNSChainPresetState(stored); err != nil {
			return fmt.Errorf("dns-пресет: save settings: %w", err)
		}
		// Выключение снимает цепочку именно здесь: ensure-хук withConfig при
		// пустом Mode — no-op. Для активного пресета хук повторит ensure
		// идемпотентно.
		return ensureDNSChainOverlay(c, stored)
	})
}
