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
	// Raw-половина сервера — отдельный OpkgTun: свой description (по нему реап
	// находит бесхозные) и свой MTU (1300 у wdtt-server, rawMTU форка).
	wdttRawOpkgDescription = "AWGM WDTT Raw"
	wdttRawOpkgMTU         = 1300
)

// OpkgTunIndexLister reports occupied OpkgTun indices from kernel /sys.
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

func (cfg ServerConfig) ndmsRawIface() string {
	return strings.TrimSpace(cfg.RawNdmsIface)
}

func (cfg ServerConfig) usesNDMSRawOpkgTun() bool {
	_, ok := parseOpkgTunIndex(cfg.RawNdmsIface)
	return ok && strings.TrimSpace(cfg.RawIface) != "" && cfg.RawIface != DefaultRawServerIface
}

// serverOpkgSecurityLevel — тумблер «использовать в политиках доступа».
// private (по умолчанию) — сервер остаётся внутренним интерфейсом; public
// вместе с `ip global` делает его подключением, видимым в приоритетах и
// политиках роутера, и КАНДИДАТОМ В DEFAULT ROUTE со всеми последствиями.
func (cfg ServerConfig) serverOpkgSecurityLevel() string {
	if cfg.ExposeToPolicies {
		return "public"
	}
	return "private"
}

func allocateWdttOpkgIndex(live map[int]bool) (int, error) {
	return allocateWdttOpkgIndexFrom(live)
}

func isLegacyWdttOpkgIndex(idx int) bool {
	return idx >= wdttLegacyIndexMin
}

// serverHelpText — кэш вывода `wdtt-server -h`. Один проб на все флаги: их
// стало два (-wg-iface, -raw-iface), а запускать бинарь на каждый вопрос
// незачем.
func (s *Service) serverHelpText(ctx context.Context) (string, bool) {
	s.wgIfaceMu.Lock()
	defer s.wgIfaceMu.Unlock()
	if s.serverHelpKnown {
		return s.serverHelp, true
	}
	if strings.TrimSpace(s.serverBin) == "" {
		return "", false
	}
	out, err := probeBinaryHelp(ctx, s.serverBin)
	if err != nil {
		return "", false
	}
	// Кэшируем только удачный проб: иначе первая попытка до установки бинаря
	// намертво зафиксировала бы «флага нет» до рестарта демона.
	s.serverHelpKnown = true
	s.serverHelp = out
	return out, true
}

func (s *Service) serverSupportsWgIface(ctx context.Context) bool {
	out, ok := s.serverHelpText(ctx)
	return ok && strings.Contains(out, "-wg-iface")
}

// serverSupportsRawIface — без этого флага raw-интерфейс остаётся wdttraw0, а
// имя не-OpkgTun формата NDMS зарегистрировать не может (тип интерфейса он
// выводит из имени).
func (s *Service) serverSupportsRawIface(ctx context.Context) bool {
	out, ok := s.serverHelpText(ctx)
	return ok && strings.Contains(out, "-raw-iface")
}

// ensureServerOpkgIndex picks/persists OpkgTun indices: WG (NdmsIface/WgIface)
// и raw (RawNdmsIface/RawIface). NDMS create/address happens later in
// prepareNDMSOpkgTun (после выделения).
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
		}
	}
	// Raw-половина заводится только вместе с WG: без -wg-iface вся обвязка
	// NDMS выключена, и одинокий raw-OpkgTun остался бы без владельца.
	needWG := !cfg.usesNDMSOpkgTun()
	needRaw := !cfg.usesNDMSRawOpkgTun() && s.serverSupportsRawIface(ctx)
	if !needWG && !needRaw {
		return cfg, nil
	}

	live, err := s.opkgIndices.LiveOpkgTunIndices(ctx)
	if err != nil {
		return cfg, fmt.Errorf("list opkgtun indices: %w", err)
	}
	reserved := map[int]bool{}
	if full, loadErr := s.store.Load(); loadErr == nil {
		reserved = configReservedOpkgIndices(full, id, "")
	}
	// Свои уже выделенные индексы — тоже занятые: configReservedOpkgIndices
	// пропускает наш сервер целиком, и без этого raw получил бы индекс WG.
	for _, name := range []string{cfg.NdmsIface, cfg.RawNdmsIface} {
		if idx, ok := parseOpkgTunIndex(name); ok {
			reserved[idx] = true
		}
	}
	// mergeOpkgIndexMaps отдаёт НОВУЮ карту — дальше её можно править, не
	// трогая ту, что вернул поставщик.
	live = mergeOpkgIndexMaps(live, reserved)

	if needWG {
		idx, err := allocateWdttOpkgIndex(live)
		if err != nil {
			return cfg, err
		}
		live[idx] = true
		cfg.NdmsIface = opkgTunNDMSName(idx)
		cfg.WgIface = opkgTunKernelName(idx)
		if s.appLog != nil {
			s.appLog.Info("ndms", id, fmt.Sprintf("выделен %s → %s", cfg.NdmsIface, cfg.WgIface))
		}
	}
	if needRaw {
		idx, err := allocateWdttOpkgIndex(live)
		if err != nil {
			return cfg, err
		}
		live[idx] = true
		cfg.RawNdmsIface = opkgTunNDMSName(idx)
		cfg.RawIface = opkgTunKernelName(idx)
		if s.appLog != nil {
			s.appLog.Info("ndms", id, fmt.Sprintf("выделен raw %s → %s", cfg.RawNdmsIface, cfg.RawIface))
		}
	}
	if err := s.persistServerConfig(id, cfg); err != nil {
		return cfg, err
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
// security-level по умолчанию private, без `ip global`: это VPN-сервер, куда
// ломятся доверенные пиры (паритет с managed.rciConfigureServer), а не аплинк.
// Тумблер ExposeToPolicies меняет уровень на public — см. serverOpkgSecurityLevel.
//
// Интерфейсов у сервера два: WG (opkgtunN) и raw (opkgtunM), и оба заводятся
// здесь.
func (s *Service) prepareNDMSOpkgTun(ctx context.Context, cfg ServerConfig) error {
	if s.ndmsIfaces == nil {
		return nil
	}
	level := cfg.serverOpkgSecurityLevel()
	if cfg.usesNDMSOpkgTun() {
		if err := s.prepareOpkgTunIface(ctx, cfg.ndmsAccessIface(), wdttOpkgDescription, wdttOpkgMTU, level); err != nil {
			return err
		}
	}
	if cfg.usesNDMSRawOpkgTun() {
		if err := s.prepareOpkgTunIface(ctx, cfg.ndmsRawIface(), wdttRawOpkgDescription, wdttRawOpkgMTU, level); err != nil {
			return err
		}
	}
	return nil
}

// prepareOpkgTunIface — create-if-absent + уровень безопасности + MTU.
// SetSecurityLevel зовётся только для уже существующего интерфейса: при
// создании уровень задаётся сразу, лишняя RCI-мутация на каждом старте не
// нужна. Для пережившего интерфейса она обязательна — иначе тумблер «в
// политиках» не доехал бы до него никогда.
func (s *Service) prepareOpkgTunIface(ctx context.Context, ndmsName, description string, mtu int, level string) error {
	if !s.opkgTunExists(ctx, ndmsName) {
		if err := s.ndmsIfaces.CreateOpkgTunWithSecurityLevel(ctx, ndmsName, description, level); err != nil {
			return fmt.Errorf("create %s: %w", ndmsName, err)
		}
	} else if err := s.ndmsIfaces.SetSecurityLevel(ctx, ndmsName, level); err != nil {
		return fmt.Errorf("security-level %s %s: %w", level, ndmsName, err)
	}
	if err := s.ndmsIfaces.SetMTU(ctx, ndmsName, mtu); err != nil {
		return fmt.Errorf("set mtu %s: %w", ndmsName, err)
	}
	return nil
}

// activateNDMSOpkgTun applies ACL/address/up after kernel opkgtunN is live.
func (s *Service) activateNDMSOpkgTun(ctx context.Context, cfg ServerConfig) error {
	if s.ndmsIfaces == nil {
		return nil
	}
	if cfg.usesNDMSOpkgTun() {
		ndmsName := cfg.ndmsAccessIface()
		if err := s.activateOpkgTunIface(ctx, cfg, ndmsName, DefaultWdttServerGatewayAddr, DefaultWdttServerGatewayMask); err != nil {
			return err
		}
		if err := cfg.ensureServerWgClientRoute(ctx); err != nil && s.appLog != nil {
			s.appLog.Warn("ndms", ndmsName, "маршрут WG-клиентов: "+err.Error())
		}
	}
	if cfg.usesNDMSRawOpkgTun() {
		// Адрес свой, а не 10.70.66.1: тот уже висит на интерфейсе от самого
		// wdtt-server. Сеть та же (10.70.0.0/16) — NDMS кроет NAT/policy/ACL
		// только сеть интерфейса (PR #697, F2).
		if err := s.activateOpkgTunIface(ctx, cfg, cfg.ndmsRawIface(), DefaultRawServerGatewayAddr, DefaultRawServerMask); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) activateOpkgTunIface(ctx context.Context, cfg ServerConfig, ndmsName, addr, mask string) error {
	if err := s.ndmsIfaces.SetAddress(ctx, ndmsName, addr, mask); err != nil {
		return fmt.Errorf("set address %s: %w", ndmsName, err)
	}
	if err := s.ndmsIfaces.InterfaceUp(ctx, ndmsName); err != nil {
		return fmt.Errorf("iface up %s: %w", ndmsName, err)
	}
	if cfg.ExposeToPolicies {
		// После address/up — как managed AWG (operator_os5) и raw-клиент, не до
		// адреса. Обратной команды у нас нет: снятый тумблер убирает `ip global`
		// только пересозданием интерфейса, то есть на стоп/старте сервера.
		if err := s.ndmsIfaces.SetIPGlobal(ctx, ndmsName); err != nil {
			return fmt.Errorf("ip global %s: %w", ndmsName, err)
		}
	}
	// Permit-all ACL после address/up: applyServerAccess дублирует вызов, но
	// первый assert здесь — до entware NAT/LAN в том же StartServerInstance.
	if err := s.ndmsIfaces.SetPermitAllACL(ctx, ndmsName); err != nil {
		if s.appLog != nil {
			s.appLog.Warn("ndms", ndmsName, "firewall permit пропущен: "+err.Error())
		}
	}
	return nil
}

// teardownServerOpkgTun снимает ОБА интерфейса сервера. Единая точка намеренно:
// вызовов у неё десяток (все ветки отказа старта, стоп, удаление, реап), и
// «забыть raw» в одной из них — ровно тот класс утечки, которым болел FORWARD.
func (s *Service) teardownServerOpkgTun(ctx context.Context, cfg ServerConfig) error {
	var firstErr error
	if cfg.usesNDMSOpkgTun() {
		firstErr = s.teardownOpkgTunByName(ctx, cfg.ndmsAccessIface(), "ndms")
	}
	if cfg.usesNDMSRawOpkgTun() {
		if err := s.teardownOpkgTunByName(ctx, cfg.ndmsRawIface(), "ndms-raw"); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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
			if srv.Config.usesNDMSRawOpkgTun() {
				live[srv.Config.ndmsRawIface()] = true
			}
			continue
		}
		_ = s.teardownOpkgTunByName(ctx, srv.Config.ndmsAccessIface(), "wdtt-reap")
		if srv.Config.usesNDMSRawOpkgTun() {
			_ = s.teardownOpkgTunByName(ctx, srv.Config.ndmsRawIface(), "wdtt-reap")
		}
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
	for _, desc := range []string{wdttOpkgDescription, wdttRawOpkgDescription, wdttOpkgClientDescription} {
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
