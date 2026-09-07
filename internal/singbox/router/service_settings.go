package router

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/router/bypassset"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func (s *ServiceImpl) ListPresets() ([]Preset, error) {
	return listRouterPresets(s.deps.PresetCatalog)
}

func (s *ServiceImpl) ApplyPreset(ctx context.Context, presetID, outboundTag string) error {
	p, err := findRouterPreset(s.deps.PresetCatalog, presetID)
	if err != nil {
		return err
	}
	return s.withConfig(ctx, "status", func(c *RouterConfig) error {
		return ApplyPresetToConfig(c, p, outboundTag)
	})
}

func (s *ServiceImpl) GetSettings(ctx context.Context) (storage.SingboxRouterSettings, error) {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return storage.SingboxRouterSettings{}, err
	}
	return NormalizeSingboxRouterSettings(settings.SingboxRouter)
}

func (s *ServiceImpl) UpdateSettings(ctx context.Context, sr storage.SingboxRouterSettings) error {
	normalized, err := NormalizeSingboxRouterSettings(sr)
	if err != nil {
		return err
	}
	// QoS classes must route to outbounds that actually exist — an unknown
	// tag would either be skipped at emit time (class silently inert) or,
	// unguarded, take the whole merged config down at sing-box load. Checked
	// here (not in the pure Normalize) because outbound catalogs are deps.
	if err := s.validateQoSClassOutbounds(ctx, normalized.QoSClasses); err != nil {
		return err
	}
	if err := s.validateBypassGeoIPTags(normalized); err != nil {
		return err
	}
	// Персист-окно под transitionMu: см. ErrTransitionInProgress. Reconcile
	// ниже остаётся ВНЕ окна — он сам берёт transitionMu через TryLock и под
	// нашим локом молча съел бы тик (мьютекс нерекурсивный).
	settings, err := func() (*storage.Settings, error) {
		if !s.transitionMu.TryLock() {
			return nil, ErrTransitionInProgress
		}
		defer s.transitionMu.Unlock()
		// Проба бинаря — ДО лока стора: на промахе кэша она синхронно щупает
		// кандидатов, и держать на этом лок стора нельзя. Щупаем ровно там,
		// где щупал прежний код: только если переход «пусто → непусто»
		// возможен. Авторитетную проверку перехода делает уже мутатор — по
		// той самой записи, что уйдёт на диск.
		ipsetOK := true
		if len(normalized.BypassGeoIPTags) > 0 {
			cur, err := s.deps.Settings.Get()
			if err != nil {
				return nil, err
			}
			if len(cur.SingboxRouter.BypassGeoIPTags) == 0 {
				ipsetOK = bypassset.IsIPSetAvailable()
			}
		}
		// Место хранения cache.db живёт в 00-base.json — применяем через
		// оператор ДО персиста и ДО overlay (issue #842): при отказе стор не
		// говорит «tmp», пока база на флеше, а overlay ниже берёт у оператора
		// уже эффективный путь. На КАЖДОМ PUT, без гейта по прежнему значению:
		// применение без правки не пишет и не взводит reload, зато любой PUT
		// долечивает базу, разъехавшуюся с настройкой (отказ персиста ниже
		// после успешного применения, гонка с Enable), а не только бут.
		// Отказ персиста ниже откатывает применение к прежнему месту.
		prevLoc := ""
		if cur, err := s.deps.Settings.Get(); err == nil {
			prevLoc = cur.SingboxRouter.CacheFileLocation
		}
		if s.deps.ApplyCacheFileLocation != nil {
			if err := s.deps.ApplyCacheFileLocation(normalized.CacheFileLocation); err != nil {
				return nil, err
			}
		}
		if err := s.deps.Settings.Update(func(cur *storage.Settings) error {
			// Переход «пусто → непусто» требует живого ipset-бинаря. Только на
			// переходе: при уже выбранных тегах и сломанном ipset прочие правки
			// настроек остаются проходимыми.
			if len(normalized.BypassGeoIPTags) > 0 && len(cur.SingboxRouter.BypassGeoIPTags) == 0 && !ipsetOK {
				return bypassset.ErrIPSetNotAvailable
			}
			// Port-slot stability: the UI contract carries no slot field, so incoming
			// classes are re-associated with their persisted slots by DSCP before the
			// save — otherwise every PUT would re-deal ports positionally and RST
			// untouched classes' flows.
			normalized.QoSClasses = reassociateQoSSlots(normalized.QoSClasses, cur.SingboxRouter.QoSClasses)

			// Режим и Enabled через PUT настроек НЕ меняются: путь Save→Reconcile
			// диспатчится по НОВОМУ режиму и не гасит ресурсы старого (см. шапку
			// fakeip_transition.go) — принятый отсюда чужой режим оставил бы,
			// например, tproxy-перехват жить под fakeip-tun навсегда. Смена
			// режима — только SwitchRoutingMode (POST /mode). Поля из тела молча
			// игнорируются (patch-семантика, как ApiKey в /settings/update).
			normalized.RoutingMode = cur.SingboxRouter.RoutingMode
			if normalized.RoutingMode == "" {
				normalized.RoutingMode = stateTProxy // legacy-дефолт, как в currentState
			}
			normalized.Enabled = cur.SingboxRouter.Enabled

			cur.SingboxRouter = normalized
			return nil
		}); err != nil {
			// Персист не прошёл, а база уже переписана — вернуть её к прежнему
			// месту, иначе стор и 00-base.json расходятся до следующего PUT/бута.
			// Снесённый при переезде cache.db откат не воскрешает (это кэш).
			if s.deps.ApplyCacheFileLocation != nil {
				if err := s.deps.ApplyCacheFileLocation(prevLoc); err != nil {
					s.appLog.Warn("settings", "cache-location", "откат места cache.db к "+prevLoc+" не удался: "+err.Error())
				}
			}
			return nil, err
		}
		return s.deps.Settings.Get()
	}()
	if err != nil {
		return err
	}
	// Оverlay-поля fakeip (real-server, стек, UDP-таймаут) живут внутри
	// 21-fakeip.json — Reconcile их не перегенерирует (drift-heal чинит
	// слоты/маршруты, не содержимое overlay). Без reapply изменение
	// применялось бы только при следующем выключении/включении режима
	// (стенд-находка 2026-07-16). Пере-прогоняем overlay изолированным
	// CRUD-пайплайном (no-op мутатор): persist слота взводит reload, а при
	// байт-идентичном результате short-circuit'ится (persistSlotDirect).
	if err := s.reapplyFakeIPOverlay(ctx, settings); err != nil {
		return err
	}
	// Пресет keendns применяем здесь, а не только внутри Reconcile: тот
	// пропускает тик целиком, если transitionMu занят сменой режима, и
	// снятие пресета молча не доехало бы. Повторный вызов из Reconcile —
	// no-op (набор уже совпадает).
	s.syncKeenDNSPreset(ctx, normalized)
	return s.Reconcile(ctx)
}

// reapplyFakeIPOverlay перегенерирует fakeip-overlay на ВКЛЮЧЁННОМ и
// provisioned fakeip-tun. Безусловно (без diff prev/cur): no-op бесплатен,
// а ретрай PUT после транзиентного сбоя reapply самоисцеляет overlay —
// diff-гейт после уже сделанного Save маскировал бы такой сбой навсегда.
// В окне провижининга (SwitchRoutingMode ставит Enabled до Enable, FakeIP
// state ещё nil) — пропуск: overlay построит сам Enable из уже сохранённых
// настроек. Пулы/MTU так не применяются намеренно — они требуют
// пересоздания NDMS-интерфейса и маршрутов (выключение/включение режима).
func (s *ServiceImpl) reapplyFakeIPOverlay(ctx context.Context, settings *storage.Settings) error {
	sr := settings.SingboxRouter
	if sr.RoutingMode != "fakeip-tun" || !sr.Enabled {
		return nil
	}
	if st, ok := opkgTunOwned(settings, stateFakeIPTun); !ok || !st.Provisioned {
		return nil
	}
	return s.fakeipWithConfig(ctx, "settings", func(*RouterConfig) error { return nil })
}

// validateQoSClassOutbounds rejects ENABLED QoS classes whose Outbound does
// not resolve against any known catalog (router composites, subscription
// composites, AWG tags, sing-box tunnel tags, built-ins). Disabled classes
// are deliberately exempt: outbound force-delete keeps the class around in a
// disabled state (see disableQoSClassesForOutbound), and a later settings
// PUT carrying that class verbatim must not 400. Wraps ErrQoSClassesInvalid
// so the API maps it to 400 QOS_CLASSES_INVALID.
func (s *ServiceImpl) validateQoSClassOutbounds(ctx context.Context, classes []storage.SingboxQoSClass) error {
	if len(classes) == 0 {
		return nil
	}
	cfg, err := s.loadRouterConfig()
	if err != nil {
		cfg = NewEmptyConfig()
	}
	for i, c := range classes {
		if !c.Enabled {
			continue
		}
		if !s.isKnownOutboundTag(ctx, c.Outbound, cfg) {
			return fmt.Errorf("%w: qosClasses[%d]: неизвестный outbound %q", ErrQoSClassesInvalid, i, c.Outbound)
		}
	}
	return nil
}

// ValidateSingboxRouterSettings enforces the WAN-binding discriminator:
//   - WANAutoDetect=true   && WANInterface==""    → OK
//   - WANAutoDetect=false  && WANInterface!=""    → OK
//   - WANAutoDetect=true   && WANInterface!=""    → error (contradictory)
//   - WANAutoDetect=false  && WANInterface==""    → error (no target)
//
// This guards both the storage layer (UpdateSettings) and the apply
// path (Enable → EnsureRouteWAN) so an invalid state cannot reach
// sing-box config either through the API or through a hand-edited
// settings.json.
func NormalizeSingboxRouterSettings(sr storage.SingboxRouterSettings) (storage.SingboxRouterSettings, error) {
	if sr.DeviceMode == "" {
		sr.DeviceMode = "policy"
	}
	switch sr.DeviceMode {
	case "policy", "all":
	default:
		return sr, fmt.Errorf("deviceMode must be %q or %q, got %q", "policy", "all", sr.DeviceMode)
	}
	if sr.RoutingMode == "" {
		sr.RoutingMode = "tproxy"
	}
	if sr.RoutingMode != "tproxy" && sr.RoutingMode != "fakeip-tun" && sr.RoutingMode != "policy-tun" {
		return sr, fmt.Errorf("invalid routingMode %q (want tproxy|fakeip-tun|policy-tun)", sr.RoutingMode)
	}
	if sr.WANAutoDetect && sr.WANInterface != "" {
		return sr, fmt.Errorf("wanAutoDetect=true requires wanInterface to be empty (got %q)", sr.WANInterface)
	}
	if !sr.WANAutoDetect && sr.WANInterface == "" {
		return sr, fmt.Errorf("wanAutoDetect=false requires wanInterface to be set to a kernel interface name")
	}
	for _, name := range sr.BypassPresets {
		if _, ok := knownPresets[name]; !ok {
			return sr, fmt.Errorf("unknown bypass preset %q", name)
		}
	}
	if _, _, err := parseExtraPorts(sr.BypassExtraPorts); err != nil {
		return sr, fmt.Errorf("bypassExtraPorts: %w", err)
	}
	if _, err := resolveBypassSubnets(sr.BypassExtraSubnets); err != nil {
		return sr, fmt.Errorf("bypassExtraSubnets: %w", err)
	}
	if err := validateIngressRefs(sr.IngressInterfaces); err != nil {
		return sr, err
	}
	if err := normalizeFakeIPSettings(&sr); err != nil {
		return sr, err
	}
	if sr.UDPTimeout != "" {
		if _, err := time.ParseDuration(sr.UDPTimeout); err != nil {
			return sr, fmt.Errorf("udpTimeout: invalid duration %q: %w", sr.UDPTimeout, err)
		}
	}
	if sr.UDPNATMax < 0 || sr.UDPNATMax > 65536 {
		return sr, fmt.Errorf("udpNatMax: must be 0..65536, got %d", sr.UDPNATMax)
	}
	// source-preserve без списка сегментов — включённая опция, которая ничего не
	// делает; пустой список при выключенной опции чистим, чтобы персист не тянул
	// протухший выбор до следующего включения.
	if sr.PolicyTunSourcePreserve {
		if len(sr.PolicyTunNATSegments) == 0 {
			return sr, fmt.Errorf("policyTunSourcePreserve=true requires a non-empty policyTunNatSegments list")
		}
	} else {
		sr.PolicyTunNATSegments = nil
	}
	if err := validateQoSClasses(sr.QoSClasses); err != nil {
		return sr, err
	}
	sr.QoSClasses = normalizeQoSClasses(sr.QoSClasses)
	switch sr.CacheFileLocation {
	case "", storage.CacheFileLocationFlash, storage.CacheFileLocationTmp:
		// valid
	default:
		return sr, fmt.Errorf("cacheFileLocation: invalid value %q (must be \"flash\" or \"tmp\")", sr.CacheFileLocation)
	}
	return sr, nil
}

// fakeIPMTUMin / fakeIPMTUMax bound the user-editable tun MTU. 576 is the IPv4
// minimum-reassembly floor; 9000 is jumbo-frame ceiling. Outside this range
// sing-tun behaves unpredictably, so reject early.
const (
	fakeIPMTUMin = 576
	fakeIPMTUMax = 9000
)

// normalizeFakeIPSettings defaults the user-editable fakeip engine fields from
// DefaultFakeIPTunParams (single source of truth) when empty/zero, then
// validates them. FakeIPPool6 — ИСКЛЮЧЕНИЕ: пустое значение ЗНАЧИМО («v6
// выключен», обещание UI/DTO) и НЕ дефолтится; дефолт свежей установки сеет
// defaultSettings, хранимое "" старых установок материализовала migrateToV35.
// «Не прислано» ≠ «прислано пустым» различает PUT-handler (PutSettings,
// теневой указатель): absent сохраняет текущее значение — сюда пустое приходит
// только как осознанное выключение. Idempotent: re-running on a normalized
// struct is a fixed point.
func normalizeFakeIPSettings(sr *storage.SingboxRouterSettings) error {
	def := DefaultFakeIPTunParams()
	if sr.FakeIPStack == "" {
		sr.FakeIPStack = "gvisor"
	}
	if sr.FakeIPStack != "gvisor" && sr.FakeIPStack != "system" {
		return fmt.Errorf("fakeipStack must be %q or %q, got %q", "gvisor", "system", sr.FakeIPStack)
	}
	if sr.FakeIPPool4 == "" {
		sr.FakeIPPool4 = def.Inet4Range
	}
	if sr.FakeIPMTU == 0 {
		sr.FakeIPMTU = def.MTU
	}
	// v4 pool must parse as an IPv4 prefix.
	if p, err := netip.ParsePrefix(sr.FakeIPPool4); err != nil {
		return fmt.Errorf("fakeipPool4: invalid CIDR %q: %w", sr.FakeIPPool4, err)
	} else if !p.Addr().Is4() {
		return fmt.Errorf("fakeipPool4: %q is not IPv4", sr.FakeIPPool4)
	}
	// v6 pool: empty disables v6; otherwise it must parse as an IPv6 prefix.
	if sr.FakeIPPool6 != "" {
		if p, err := netip.ParsePrefix(sr.FakeIPPool6); err != nil {
			return fmt.Errorf("fakeipPool6: invalid CIDR %q: %w", sr.FakeIPPool6, err)
		} else if p.Addr().Is4() {
			return fmt.Errorf("fakeipPool6: %q is not IPv6", sr.FakeIPPool6)
		}
	}
	if sr.FakeIPMTU < fakeIPMTUMin || sr.FakeIPMTU > fakeIPMTUMax {
		return fmt.Errorf("fakeipMtu %d out of range [%d, %d]", sr.FakeIPMTU, fakeIPMTUMin, fakeIPMTUMax)
	}
	// FakeIPRealServer сознательно НЕ дефолтится здесь. Раньше пустое поле
	// штамповалось константой 1.1.1.1 — после этого «значения не было» и
	// «пользователь выбрал 1.1.1.1» становились неразличимы, и общий
	// Bootstrap-DNS (issue #770) не мог подставиться никогда. Пусто =
	// «значения нет», эффективный адрес выбирает resolveFakeIPParamsWith:
	// Bootstrap-DNS, а без него исторический 1.1.1.1. Уже сохранённое
	// значение — чьё бы оно ни было — не трогаем.
	if sr.FakeIPRealServer != "" {
		// Real upstream must be a plain IP: the fakeip topology resolves every
		// domain through the "real" server itself, so a domain upstream could
		// never bootstrap. Zoned addresses (fe80::1%eth0) make no sense for a
		// DNS upstream either.
		if addr, err := netip.ParseAddr(sr.FakeIPRealServer); err != nil {
			return fmt.Errorf("fakeipRealServer: invalid IP address %q: %w", sr.FakeIPRealServer, err)
		} else if addr.Zone() != "" {
			return fmt.Errorf("fakeipRealServer: zoned address %q is not allowed", sr.FakeIPRealServer)
		}
	}
	return nil
}

func validateIngressRefs(refs []string) error {
	for _, ref := range refs {
		if !strings.HasPrefix(ref, "managed:") && !strings.HasPrefix(ref, "iface:") {
			return fmt.Errorf("ingress interface ref %q must be prefixed managed: or iface:", ref)
		}
		if strings.TrimSpace(strings.SplitN(ref, ":", 2)[1]) == "" {
			return fmt.Errorf("ingress interface ref %q has empty target", ref)
		}
	}
	return nil
}

func ValidateSingboxRouterSettings(sr storage.SingboxRouterSettings) error {
	_, err := NormalizeSingboxRouterSettings(sr)
	return err
}

func (s *ServiceImpl) ListWANInterfaces(ctx context.Context) ([]WANInterfaceInfo, error) {
	if s.deps.WANInterfaces == nil {
		return []WANInterfaceInfo{}, nil
	}
	return s.deps.WANInterfaces.ListWAN(ctx)
}

// ListBindableInterfaces returns interfaces a user can bind a direct
// outbound to. Empty when no lister is wired.
func (s *ServiceImpl) ListBindableInterfaces(ctx context.Context) ([]WANInterfaceInfo, error) {
	if s.deps.BindableInterfaces == nil {
		return []WANInterfaceInfo{}, nil
	}
	return s.deps.BindableInterfaces.ListBindable(ctx)
}

// ListAllBindableInterfaces returns every bindable interface, including those
// already used by a direct outbound. Subscriptions and manual tunnels may
// share an interface with a direct outbound, so their picker must offer the
// same set the bind validator accepts (#709).
func (s *ServiceImpl) ListAllBindableInterfaces(ctx context.Context) ([]WANInterfaceInfo, error) {
	if s.deps.BindableInterfaces == nil {
		return []WANInterfaceInfo{}, nil
	}
	return s.deps.BindableInterfaces.ListAllBindable(ctx)
}

// ListIngressEligibleInterfaces возвращает интерфейсы, пригодные для
// ingress-scope: bindable минус WAN минус LAN-бриджи (по Type). Для UI
// router-страницы (мультиселект).
func (s *ServiceImpl) ListIngressEligibleInterfaces(ctx context.Context) ([]WANInterfaceInfo, error) {
	bindable, err := s.ListBindableInterfaces(ctx)
	if err != nil {
		return nil, err
	}
	wan, _ := s.ListWANInterfaces(ctx)
	wanNames := map[string]bool{}
	for _, w := range wan {
		wanNames[w.Name] = true
	}
	out := make([]WANInterfaceInfo, 0, len(bindable))
	for _, i := range bindable {
		if wanNames[i.Name] || strings.EqualFold(i.Type, "Bridge") {
			continue
		}
		out = append(out, i)
	}
	return out, nil
}

// validateBindInterface ensures name refers to a bindable interface. With
// no lister wired (tests / minimal deployments) it is permissive.
func (s *ServiceImpl) validateBindInterface(ctx context.Context, name string) error {
	if s.deps.BindableInterfaces == nil {
		return nil
	}
	ifaces, err := s.deps.BindableInterfaces.ListBindable(ctx)
	if err != nil {
		return err
	}
	for _, i := range ifaces {
		if i.Name == name {
			return nil
		}
	}
	return fmt.Errorf("bind_interface %q is not a selectable interface", name)
}
