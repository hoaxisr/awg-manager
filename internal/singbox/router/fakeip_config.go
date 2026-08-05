package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// ---------------------------------------------------------------------------
// Конфиг режимного слота fakeip (SlotFakeIP): его DNS-механизм и оверлей.
//
// АСИММЕТРИЯ ЗАПИСИ, о которую спотыкается любое чтение этого файла: после
// подэтапа 5D0 правки правил, наборов и outbound'ов идут в общий слот ЧЕРЕЗ
// STAGING (pending/21-routing.json, применяются кнопкой «Применить»), а DNS
// fakeip пишется в active НАПРЯМУЮ — persistFakeIPConfig → persistSlotDirect,
// то есть вступает в силу ближайшим reload без всякого «Применить».
//
// Так сделано намеренно: режимный слот целиком выводится из настроек и
// провижининга, черновика у него нет и быть не может (ApplyStaging знает
// только общий слот). Но на одной странице пользователь видит два разных
// контракта применения, и оба следствия приходится закрывать руками:
//
//   - guardFakeIPRuleSetRefs — DNS-правило не примет ссылку на набор, который
//     ещё лежит в черновике общего слота (иначе прямая запись DNS обгоняет
//     «Применить» и reload падает на неизвестном rule_set);
//   - фронт показывает баннер черновика на всех вкладках страницы fakeip, но
//     DNS-правки под него не подводит.
// ---------------------------------------------------------------------------

// cloneRouterConfig returns a deep copy of cfg via a JSON round-trip.
// ponytail: json round-trip deep copy — config is already JSON-serializable, no hand-written Clone.
func cloneRouterConfig(cfg *RouterConfig) (*RouterConfig, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("cloneRouterConfig: marshal: %w", err)
	}
	clone := NewEmptyConfig()
	if err := json.Unmarshal(data, clone); err != nil {
		return nil, fmt.Errorf("cloneRouterConfig: unmarshal: %w", err)
	}
	return clone, nil
}

// guardFakeIPLocked checks whether the user's mutation (from before to after)
// clobbered any engine-locked fakeip-tun field. It is diff-based: a fresh slot
// where before has no locked bits yet (first provision) passes through so the
// overlay can create them. Only an established config that LOSES or CHANGES a
// locked bit is rejected.
//
// All errors wrap ErrFakeIPLockedField so errors.Is(err, ErrFakeIPLockedField)
// holds upstream and the HTTP layer can map them to 4xx.
func guardFakeIPLocked(before, after *RouterConfig) error {
	// 1. real DNS server deleted/renamed.
	if hasDNSServerByTag(before, "real") && !hasDNSServerByTag(after, "real") {
		return fmt.Errorf("%w: dns server \"real\" is engine-locked (managed by fakeip-tun, cannot be deleted or renamed)", ErrFakeIPLockedField)
	}

	// 2. fakeip DNS server deleted.
	if hasDNSServerByType(before, "fakeip") && !hasDNSServerByType(after, "fakeip") {
		return fmt.Errorf("%w: dns server with type \"fakeip\" is engine-locked (managed by fakeip-tun, cannot be deleted)", ErrFakeIPLockedField)
	}

	// 3. hijack-dns rule removed.
	if hasHijackDNSRule(before) && !hasHijackDNSRule(after) {
		return fmt.Errorf("%w: route rule \"hijack-dns\" is engine-locked (managed by fakeip-tun, cannot be removed)", ErrFakeIPLockedField)
	}

	// 4. dns.final changed.
	if before.DNS.Final != "" && after.DNS.Final != before.DNS.Final {
		return fmt.Errorf("%w: dns.final is engine-locked to %q (managed by fakeip-tun, cannot be changed)", ErrFakeIPLockedField, before.DNS.Final)
	}

	// 5. default_domain_resolver changed.
	if before.Route.DefaultDomainResolver != nil {
		if after.Route.DefaultDomainResolver == nil || after.Route.DefaultDomainResolver.Server != before.Route.DefaultDomainResolver.Server {
			return fmt.Errorf("%w: route.default_domain_resolver is engine-locked (managed by fakeip-tun, cannot be changed or removed)", ErrFakeIPLockedField)
		}
	}

	// 6. real DNS server edited beyond its upstream address. The engine overlay
	// rewrites the whole entry ({tag, type:udp, server}) on every persist, so a
	// change to any other field (type, port, detour, ...) would return success
	// and then silently vanish (issue #487) — reject it instead. The upstream
	// address (Server) edit itself is allowed: fakeipWithConfig captures it
	// into settings (FakeIPRealServer) before the overlay runs, so it sticks.
	if b := findDNSServerByTag(before, "real"); b != nil {
		if a := findDNSServerByTag(after, "real"); a != nil {
			bc, ac := *b, *a
			ac.Server = bc.Server
			if !reflect.DeepEqual(bc, ac) {
				return fmt.Errorf("%w: dns server \"real\" is engine-locked (managed by fakeip-tun); only its upstream address (server) can be changed", ErrFakeIPLockedField)
			}
		}
	}

	return nil
}

// hasDNSServerByTag reports whether cfg has at least one DNSServer with the
// given Tag.
func hasDNSServerByTag(cfg *RouterConfig, tag string) bool {
	return findDNSServerByTag(cfg, tag) != nil
}

// findDNSServerByTag returns a pointer to the first DNSServer with the given
// Tag, or nil when absent.
func findDNSServerByTag(cfg *RouterConfig, tag string) *DNSServer {
	for i := range cfg.DNS.Servers {
		if cfg.DNS.Servers[i].Tag == tag {
			return &cfg.DNS.Servers[i]
		}
	}
	return nil
}

// hasDNSServerByType reports whether cfg has at least one DNSServer with the
// given Type.
func hasDNSServerByType(cfg *RouterConfig, typ string) bool {
	for _, sv := range cfg.DNS.Servers {
		if sv.Type == typ {
			return true
		}
	}
	return false
}

// hasHijackDNSRule reports whether cfg has at least one Route Rule with
// Action=="hijack-dns".
func hasHijackDNSRule(cfg *RouterConfig) bool {
	for _, r := range cfg.Route.Rules {
		if r.Action == "hijack-dns" {
			return true
		}
	}
	return false
}

// loadFakeIPConfig returns the fakeip-tun RouterConfig the user is currently
// editing. When the orchestrator is wired, it delegates to LoadEffective which
// prefers pending/ over active/ so UI callers always see the latest draft.
// Fakeip is orch-only in practice; when Orch is nil we return an empty config.
// ponytail: no legacy path for fakeip — it is orch-only from day one.
func (s *ServiceImpl) loadFakeIPConfig() (*RouterConfig, error) {
	if s.deps.Orch != nil {
		data, err := s.deps.Orch.LoadEffective(orchestrator.SlotFakeIP)
		if err != nil {
			return nil, fmt.Errorf("load fakeip config: %w", err)
		}
		if data == nil {
			return NewEmptyConfig(), nil
		}
		cfg := NewEmptyConfig()
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse fakeip config: %w", err)
		}
		if cfg.Inbounds == nil {
			cfg.Inbounds = []Inbound{}
		}
		if cfg.Outbounds == nil {
			cfg.Outbounds = []Outbound{}
		}
		if cfg.Route.RuleSet == nil {
			cfg.Route.RuleSet = []RuleSet{}
		}
		if cfg.Route.Rules == nil {
			cfg.Route.Rules = []Rule{}
		}
		if cfg.DNS.Servers == nil {
			cfg.DNS.Servers = []DNSServer{}
		}
		if cfg.DNS.Rules == nil {
			cfg.DNS.Rules = []DNSRule{}
		}
		SanitizeDNSConfig(cfg)
		return cfg, nil
	}
	// Orch-nil: fakeip is not available without the orchestrator.
	return NewEmptyConfig(), nil
}

// persistFakeIPConfig materializes, validates and saves a fakeip RouterConfig
// directly to the active path (20-fakeip.json) via Orch.Save. It mirrors
// persistConfigDirect but targets SlotFakeIP instead of SlotRouting.
// Byte-equal short-circuit: if the serialized bytes match what is already on
// disk we skip the write (and the debounced reload it would trigger).
func (s *ServiceImpl) persistFakeIPConfig(ctx context.Context, cfg *RouterConfig) error {
	if s.deps.Orch == nil {
		// Orch-nil: test-only, nothing to persist.
		return nil
	}
	return s.persistSlotDirect(orchestrator.SlotFakeIP, cfg, true)
}

// guardFakeIPRuleSetRefs не даёт fakeip-слоту сослаться на набор, который ещё
// НЕ ПРИМЕНЁН в общем слоте.
//
// Наборы живут в общем слоте и правятся через staging, а DNS fakeip пишется
// напрямую в active (persistSlotDirect + reload). Списки же читаются
// pending-first, поэтому в пикере DNS-правила виден и черновичный набор: без
// этой проверки пользователь сохранил бы DNS-правило со ссылкой, которой в
// applied-конфиге нет, — merged-конфиг стал бы битым, а reload sing-box упал бы
// на неизвестном rule_set. Кросс-слотовая валидация оркестратора этот путь не
// прикрывает: она работает на применении черновика, которого здесь нет.
//
// Проверка диффная: ссылки, уже висевшие в before, не блокируют правку (иначе
// однажды сломанный слот нельзя было бы починить), отвергаются только НОВЫЕ.
func (s *ServiceImpl) guardFakeIPRuleSetRefs(before, after *RouterConfig) error {
	if s.deps.Orch == nil {
		return nil // legacy-обвязка тестов: слотов нет, проверять не по чему
	}
	refs := dnsRuleSetRefs(after)
	if len(refs) == 0 {
		return nil
	}
	appliedCfg, err := s.loadAppliedRouterConfig()
	if err != nil {
		return err
	}
	// Теги — в том же виде, что отдаёт ListRuleSets (после restoreConfig),
	// иначе материализованные inline-наборы не совпали бы по имени.
	known := make(map[string]bool)
	for _, rs := range s.ruleSetMaterializer().restoreConfig(appliedCfg).Route.RuleSet {
		known[rs.Tag] = true
	}
	for _, rs := range after.Route.RuleSet { // собственные наборы слота, если появятся
		known[rs.Tag] = true
	}
	wasDangling := make(map[string]bool)
	for _, tag := range dnsRuleSetRefs(before) {
		if !known[tag] {
			wasDangling[tag] = true
		}
	}
	for _, tag := range refs {
		if !known[tag] && !wasDangling[tag] {
			return fmt.Errorf("%w: %q", ErrRuleSetNotApplied, tag)
		}
	}
	return nil
}

// dnsRuleSetRefs собирает теги наборов, на которые ссылаются DNS-правила cfg.
func dnsRuleSetRefs(cfg *RouterConfig) []string {
	refs := make([]string, 0, 4)
	for _, r := range cfg.DNS.Rules {
		refs = append(refs, r.RuleSet...)
	}
	return refs
}

// captureFakeIPRealServerEdit persists a user edit of the "real" DNS server's
// upstream address into settings.SingboxRouter.FakeIPRealServer BEFORE the
// overlay runs, so ensureFakeIPOverlayFromState re-asserts the user's new
// upstream instead of clobbering it back to the previous one (issue #487: the
// update API returned success but the edit silently vanished from the config
// and the UI). The upstream must be a plain IP — the fakeip topology resolves
// every domain through "real" itself, so a domain upstream could never
// bootstrap.
//
// NB: settings are saved BEFORE persistFakeIPConfig runs (the overlay needs
// the new value). If the subsequent persist fails, the captured upstream
// stays in settings and takes effect on the next successful persist/enable —
// a deliberate trade-off: rolling settings back on persist failure would add
// its own desync window for no practical gain.
func (s *ServiceImpl) captureFakeIPRealServerEdit(before, after *RouterConfig) error {
	b := findDNSServerByTag(before, "real")
	a := findDNSServerByTag(after, "real")
	if b == nil || a == nil || a.Server == b.Server {
		return nil
	}
	addr, err := netip.ParseAddr(a.Server)
	if err != nil || addr.Zone() != "" {
		return fmt.Errorf("%w: got %q", ErrFakeIPRealServerInvalid, a.Server)
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return fmt.Errorf("fakeip real server: load settings: %w", err)
	}
	norm := addr.String()
	if settings.SingboxRouter.FakeIPRealServer == norm {
		return nil
	}
	settings.SingboxRouter.FakeIPRealServer = norm
	if err := s.deps.Settings.Save(settings); err != nil {
		return fmt.Errorf("fakeip real server: save settings: %w", err)
	}
	s.appLog.Info("fakeip-dns", "real", "upstream resolver changed to "+norm)
	return nil
}

// ensureFakeIPOverlayFromState loads settings and re-asserts all engine-locked
// bits into cfg via ensureFakeIPOverlay. Called on every persist so the overlay
// always wins over a user edit that touched a locked field.
func (s *ServiceImpl) ensureFakeIPOverlayFromState(cfg *RouterConfig) error {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return fmt.Errorf("fakeip overlay: load settings: %w", err)
	}
	if settings == nil || settings.FakeIP == nil {
		return fmt.Errorf("fakeip overlay: FakeIPState not provisioned (nil)")
	}
	p := resolveFakeIPParams(s.deps.FakeIPTun, settings.SingboxRouter)
	spec := FakeIPTunSpec{
		Iface:      fakeIPIfaceName(settings.FakeIP.Index),
		TunAddr4:   p.TunAddr4,
		TunAddr6:   p.TunAddr6,
		MTU:        p.MTU,
		Inet4Range: p.Inet4Range,
		Inet6Range: p.Inet6Range,
		CachePath:  p.CachePath,
		RealServer: p.RealServer,
		Stack:      settings.SingboxRouter.FakeIPStack,
		UDPTimeout: settings.SingboxRouter.UDPTimeout,
	}
	ensureFakeIPOverlay(cfg, spec)
	return nil
}

// fakeipWithConfig is the isolated load→restore→clone→mutate→guard→overlay→persist→emit
// skeleton for the fakeip-tun config slot. It mirrors withConfig but:
//   - loads/persists SlotFakeIP (not SlotRouting),
//   - snapshots `before` (deep copy) after restore so guardFakeIPLocked can
//     diff the pre-mutation state against the user's edit,
//   - rejects edits that clobber engine-locked bits via guardFakeIPLocked,
//   - inserts ensureFakeIPOverlayFromState after the guard so locked bits
//     always win on every write.
func (s *ServiceImpl) fakeipWithConfig(ctx context.Context, event string, fn func(*RouterConfig) error) error {
	cfg, err := s.loadFakeIPConfig()
	if err != nil {
		return err
	}
	cfg = s.ruleSetMaterializer().restoreConfig(cfg)
	before, err := cloneRouterConfig(cfg) // deep copy via json round-trip
	if err != nil {
		return err
	}
	if err := fn(cfg); err != nil {
		return err
	}
	if err := guardFakeIPLocked(before, cfg); err != nil {
		return err
	}
	if err := s.guardFakeIPRuleSetRefs(before, cfg); err != nil {
		return err
	}
	if err := s.captureFakeIPRealServerEdit(before, cfg); err != nil {
		return err
	}
	if err := s.ensureFakeIPOverlayFromState(cfg); err != nil {
		return err
	}
	if err := s.persistFakeIPConfig(ctx, cfg); err != nil {
		return err
	}
	// Специфичные CIDR-маршруты в tun здесь больше не считаются: они выводятся
	// из route-правил и наборов, а те переехали в общий слот. Их синк живёт на
	// применении черновика общего слота (ApplyStaging).
	s.emitCfgEvent(event, cfg)
	return nil
}
