package wdtt

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// NDMSOpkgTunCommands — узкий срез ndmscommand.InterfaceCommands, нужный WDTT.
// Интерфейс, а не конкретный тип: иначе жизненный цикл OpkgTun (порядок
// адрес/up, teardown, reap) нечем покрыть тестами. Паритет с
// singbox/router.OpkgTunCommands.
type NDMSOpkgTunCommands interface {
	CreateOpkgTunWithSecurityLevel(ctx context.Context, name, description, securityLevel string) error
	DeleteOpkgTun(ctx context.Context, name string) error
	SetDescription(ctx context.Context, name, description string) error
	SetSecurityLevel(ctx context.Context, name, level string) error
	SetIPGlobal(ctx context.Context, name string) error
	SetAddress(ctx context.Context, name, address, mask string) error
	ClearAddress(ctx context.Context, name string) error
	SetMTU(ctx context.Context, name string, mtu int) error
	InterfaceUp(ctx context.Context, name string) error
	InterfaceDown(ctx context.Context, name string) error
	SetPermitAllACL(ctx context.Context, name string) error
	RemovePermitAllACL(ctx context.Context, name string) error
}

const (
	// Диапазон 17..49: вне fakeip (0..9) и типичных awg10..16 / managed 100+.
	wdttOpkgIndexMin    = 17
	wdttOpkgIndexMax    = 49
	wdttLegacyIndexMin  = 90
	wdttOpkgDescription = "AWGM WDTT"
	wdttOpkgMTU         = 1280
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
	// Строго: Sscanf принял бы и "OpkgTun17garbage" как 17.
	idx, err := strconv.Atoi(strings.TrimPrefix(ndmsName, "OpkgTun"))
	if err != nil || idx < 0 {
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
	return allocateWdttOpkgIndexFrom(live)
}

func isLegacyWdttOpkgIndex(idx int) bool {
	return idx >= wdttLegacyIndexMin
}

func (s *Service) serverSupportsWgIface(ctx context.Context) bool {
	s.wgIfaceMu.Lock()
	defer s.wgIfaceMu.Unlock()
	if s.wgIfaceFlagKnown {
		return s.wgIfaceFlagOK
	}
	if strings.TrimSpace(s.serverBin) == "" {
		return false
	}
	out, err := probeBinaryHelp(ctx, s.serverBin)
	if err != nil {
		return false
	}
	// Кэшируем только удачный проб: иначе первая попытка до установки бинаря
	// намертво зафиксировала бы «флага нет» до рестарта демона.
	s.wgIfaceFlagKnown = true
	s.wgIfaceFlagOK = strings.Contains(out, "-wg-iface")
	return s.wgIfaceFlagOK
}

// ensureServerOpkgIndex picks/persists OpkgTun index (NdmsIface/WgIface).
// NDMS create/address happens later in prepareNDMSOpkgTun (после выделения).
func (s *Service) ensureServerOpkgIndex(ctx context.Context, id string, cfg ServerConfig) (ServerConfig, error) {
	// Сервер всегда поднимает WG + Raw (как qWDTT); OpkgTun нужен и в raw-режиме для NAT WG-клиентов.
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
	full, loadErr := s.store.Load()
	if loadErr == nil {
		live = mergeOpkgIndexMaps(live, configReservedOpkgIndices(full, id, ""))
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

// opkgTunKnownAbsent — «чекер подключён и говорит, что интерфейса нет».
// Отдельно от opkgTunExists намеренно: тот при неподключённом чекере отвечает
// «нет» (create нужен), а пропускать по этому же ответу teardown нельзя —
// деградировавшая обвязка перестала бы убирать за собой вообще.
//
// Булев ответ чекера не отличает «нет» от «спросить не удалось» (адаптер над
// InterfaceStore отдаёт false и на ошибку bootstrap). Пропуск в этом случае
// безопасен: при лежащем RCI мутации teardown всё равно провалились бы, а
// защёлки тут нет — следующий тик спросит заново.
func (s *Service) opkgTunKnownAbsent(ctx context.Context, ndmsName string) bool {
	return s.opkgExist != nil && !s.opkgExist.OpkgTunExists(ctx, ndmsName)
}

// prepareNDMSOpkgTun registers OpkgTun in NDMS before wdtt-server creates the
// kernel iface. Адрес здесь НЕ выставляется намеренно: интерфейс со
// сконфигурированным `ip address`, но без kernel-адреса вгоняет ndm в
// бесконечный nginx-reload цикл (bind fail → регенерация конфига → reload → …),
// подвешивающий весь RCI на секунды (stand-verified 2026-07-15, PR #544).
// Адрес ставит activateNDMSOpkgTun — уже после появления opkgtunN.
//
// security-level private, без `ip global`: это VPN-сервер, куда ломятся
// доверенные пиры (паритет с managed.rciConfigureServer), а не аплинк.
func (s *Service) prepareNDMSOpkgTun(ctx context.Context, cfg ServerConfig) error {
	if !cfg.usesNDMSOpkgTun() || s.ndmsIfaces == nil {
		return nil
	}
	ndmsName := cfg.ndmsAccessIface()
	if !s.opkgTunExists(ctx, ndmsName) {
		if err := s.ndmsIfaces.CreateOpkgTunWithSecurityLevel(ctx, ndmsName, wdttOpkgDescription, "private"); err != nil {
			return fmt.Errorf("create %s: %w", ndmsName, err)
		}
	}
	if err := s.ndmsIfaces.SetMTU(ctx, ndmsName, wdttOpkgMTU); err != nil {
		return fmt.Errorf("set mtu %s: %w", ndmsName, err)
	}
	return nil
}

// activateNDMSOpkgTun applies ACL/address/up after kernel opkgtunN is live.
func (s *Service) activateNDMSOpkgTun(ctx context.Context, cfg ServerConfig) error {
	if !cfg.usesNDMSOpkgTun() || s.ndmsIfaces == nil {
		return nil
	}
	ndmsName := cfg.ndmsAccessIface()
	if err := s.ndmsIfaces.SetAddress(ctx, ndmsName, DefaultWdttServerGatewayAddr, DefaultWdttServerGatewayMask); err != nil {
		return fmt.Errorf("set address %s: %w", ndmsName, err)
	}
	if err := s.ndmsIfaces.InterfaceUp(ctx, ndmsName); err != nil {
		return fmt.Errorf("iface up %s: %w", ndmsName, err)
	}
	// Permit-all ACL после address/up: applyServerAccess дублирует вызов, но
	// первый assert здесь — до entware NAT/LAN в том же StartServerInstance.
	if err := s.ndmsIfaces.SetPermitAllACL(ctx, ndmsName); err != nil {
		if s.appLog != nil {
			s.appLog.Warn("ndms", ndmsName, "firewall permit пропущен: "+err.Error())
		}
	}
	if err := cfg.ensureServerWgClientRoute(ctx); err != nil && s.appLog != nil {
		s.appLog.Warn("ndms", ndmsName, "маршрут WG-клиентов: "+err.Error())
	}
	return nil
}

func (s *Service) teardownServerOpkgTun(ctx context.Context, cfg ServerConfig) error {
	if !cfg.usesNDMSOpkgTun() {
		return nil
	}
	return s.teardownOpkgTunByName(ctx, cfg.ndmsAccessIface(), "ndms")
}

// teardownOpkgTunByName best-effort сносит OpkgTun: ACL → down → delete; при
// провале delete снимает адрес. Инвариант тот же, что у fakeip teardownOpkgTun:
// интерфейс НИКОГДА не должен остаться с настроенным адресом без kernel-адреса.
func (s *Service) teardownOpkgTunByName(ctx context.Context, ndmsName, scope string) error {
	if s.ndmsIfaces == nil || strings.TrimSpace(ndmsName) == "" {
		return nil
	}
	// Интерфейса в NDMS нет — сносить нечего, и звать RCI нельзя: `interface
	// <name> up false` создаёт интерфейс по обращению (create-on-reference,
	// см. тот же довод у singbox/router.teardownOpkgTun), а следующая команда
	// его сносит — ifdestroyed-хук на пустом месте. Для выключенного сервера
	// реап зовёт teardown каждые 15 с вечно, и без этой проверки цикл
	// «создали-снесли» крутит 4 RCI-мутации, флеш-сейв и полную инвалидацию
	// кэшей NDMS на каждом тике.
	if s.opkgTunKnownAbsent(ctx, ndmsName) {
		return nil
	}
	if err := s.ndmsIfaces.RemovePermitAllACL(ctx, ndmsName); err != nil && s.appLog != nil {
		s.appLog.Debug(scope, ndmsName, "remove permit acl: "+err.Error())
	}
	if err := s.ndmsIfaces.InterfaceDown(ctx, ndmsName); err != nil && s.appLog != nil {
		s.appLog.Warn(scope, ndmsName, "iface down: "+err.Error())
	}
	err := s.ndmsIfaces.DeleteOpkgTun(ctx, ndmsName)
	if err == nil {
		return nil
	}
	if s.appLog != nil {
		s.appLog.Warn(scope, ndmsName, "delete opkgtun: "+err.Error())
	}
	if e := s.ndmsIfaces.ClearAddress(ctx, ndmsName); e != nil && s.appLog != nil {
		s.appLog.Warn(scope, ndmsName, "clear address: "+e.Error())
	}
	return err
}

// reapOrphanOpkgTuns снимает наши OpkgTun'ы, за которыми не стоит живой
// wdtt-server: демон убит SIGKILL, процесс упал, сервер удалён из конфига
// восстановлением бэкапа. Без этого интерфейс с адресом переживает всё и
// крутит nginx-reload вечно. Вызывается из тика NAT-ресинка.
func (s *Service) reapOrphanOpkgTuns(ctx context.Context) {
	if s.ndmsIfaces == nil || s.opkgStartsInFlight() {
		return
	}
	full, err := s.store.Load()
	if err != nil {
		return
	}
	live := map[string]bool{}
	for _, srv := range full.Servers {
		if !srv.Config.usesNDMSOpkgTun() {
			continue
		}
		if s.serverProcs.get(srv.ID).Status().Running {
			live[srv.Config.ndmsAccessIface()] = true
			continue
		}
		_ = s.teardownOpkgTunByName(ctx, srv.Config.ndmsAccessIface(), "wdtt-reap")
	}
	for _, cl := range full.Clients {
		if cl.Config.UsesWireGuard() || !cl.Config.usesNDMSOpkgTun() {
			continue
		}
		if s.clientProcs.get(cl.ID).Status().Running {
			live[cl.Config.ndmsAccessIface()] = true
			continue
		}
		_ = s.teardownClientOpkgTun(ctx, cl.Config)
	}
	if s.opkgScan == nil {
		return
	}
	for _, desc := range []string{wdttOpkgDescription, wdttOpkgClientDescription} {
		ids, scanErr := s.opkgScan(ctx, desc)
		if scanErr != nil {
			continue
		}
		for _, id := range ids {
			if !live[id] {
				_ = s.teardownOpkgTunByName(ctx, id, "wdtt-reap")
			}
		}
	}
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

func (s *Service) SetNDMSInterfaceCommands(c NDMSOpkgTunCommands) {
	s.ndmsIfaces = c
}

func (s *Service) SetOpkgTunIndexLister(l OpkgTunIndexLister) {
	s.opkgIndices = l
}

func (s *Service) SetOpkgTunExistChecker(c OpkgTunExistChecker) {
	s.opkgExist = c
}

// SetOpkgTunScanner wires the NDMS-by-description scan used to reap our
// OpkgTun'ы, потерявшие владельца (сервер удалён, пока демон был мёртв).
func (s *Service) SetOpkgTunScanner(fn func(ctx context.Context, description string) ([]string, error)) {
	s.opkgScan = fn
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
