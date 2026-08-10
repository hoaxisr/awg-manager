package router

import (
	"context"
	"slices"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// issuePolicyTunUnbound — режим поднят, но интерфейс не разрешён ни в одной
// политике доступа NDMS: технически всё работает, а трафик клиентов в tun не
// заходит. Настройка политики — ручной шаг пользователя, поэтому warning.
const issuePolicyTunUnbound = "policy-tun-unbound"

// policyTunDefaultRoutePresent сообщает, припаркован ли NDMS-дефолт (v4/v6) на
// ndmsName по строкам /show/running-config.
//
// Сравнение ТОКЕНАМИ, а не подстрокой: `strings.Contains(line, "OpkgTun0")`
// матчил бы и "OpkgTun01". Хвостовые токены (NDMS печатает метрику/auto) матч
// не ломают — сверяются первые четыре поля.
func policyTunDefaultRoutePresent(lines []string, ndmsName string) (v4, v6 bool) {
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 4 || f[1] != "route" || f[2] != "default" || f[3] != ndmsName {
			continue
		}
		switch f[0] {
		case "ip":
			v4 = true
		case "ipv6":
			v6 = true
		}
	}
	return v4, v6
}

// policyTunPermitted сообщает, разрешён ли интерфейс хотя бы в одной политике
// доступа (`permit global <name>` внутри блока `ip policy …`). Блок политики не
// разбирается: достаточно факта разрешения где угодно — привязка устройств к
// конкретной политике вне зоны детекта v1.
func policyTunPermitted(lines []string, ndmsName string) bool {
	for _, line := range lines {
		f := strings.Fields(line)
		for i := 0; i+2 < len(f); i++ {
			if f[i] == "permit" && f[i+1] == "global" && f[i+2] == ndmsName {
				return true
			}
		}
	}
	return false
}

// policyTunIPGlobalPresent ищет `ip global` ВНУТРИ блока своего интерфейса:
// running-config блочный (заголовок без отступа, тело с отступом, `!` — конец),
// и та же строка под чужим интерфейсом нашей не является. Без `ip global`
// интерфейс пропадает из списка выходов политики, то есть режим тихо мёртв.
func policyTunIPGlobalPresent(lines []string, ndmsName string) bool {
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if line == trimmed { // строка без отступа — заголовок блока или его конец
			f := strings.Fields(trimmed)
			inBlock = len(f) == 2 && f[0] == "interface" && f[1] == ndmsName
			continue
		}
		if inBlock && (trimmed == "ip global" || strings.HasPrefix(trimmed, "ip global ")) {
			return true
		}
	}
	return false
}

// policyTunIngressSpec собирает желаемое состояние перехвата и заворота для
// policy-tun. Второе значение false означает «пропустить тик»: набор марок
// прочитать не удалось, и трактовать это как «политик нет» нельзя — снятие
// правил на каждой икоте RCI открывало бы окно резолвинга мимо туннеля.
//
// Единый источник для enable и reconcile.
func (s *ServiceImpl) policyTunIngressSpec(ctx context.Context, iface, ndmsName string, sr storage.SingboxRouterSettings) (FakeIPIngressSpec, bool) {
	if iface == "" {
		return FakeIPIngressSpec{}, true
	}
	spec := FakeIPIngressSpec{
		TunIface: iface,
		Tag:      PolicyTunDNSTag,
		Ifaces:   s.resolveIngressInterfaces(ctx, sr.IngressInterfaces),
	}

	// Аварийный выход: явно исключённый порт 53 отключает перехват целиком.
	// Bypass задаётся диапазонами, поэтому проверяется вхождение, а не
	// равенство.
	//
	// В tproxy bypass-порты пер-протокольные (RETURN'ы ставятся раздельно: в
	// AWGM-TPROXY по UDP, в AWGM-REDIRECT по TCP). Здесь выключатель
	// СОЗНАТЕЛЬНО грубее: 53 в любом из двух списков гасит перехват ОБОИХ
	// протоколов. Причина — сам перехват 53-го неделим: на усечённый ответ
	// клиент переспрашивает по TCP, и половинчатый перехват дал бы резолвинг,
	// зависящий от размера ответа (часть имён через туннель, часть мимо, и
	// ответы могут расходиться).
	//
	// NoDNAT ОБЯЗАТЕЛЕН во всех ветках без TunDNS: без него active() читает
	// такой спек как неактивный и EnsureFakeIPIngress сносит заворот ЦЕЛИКОМ,
	// вместе с маршрутной половиной, которая от DNS не зависит.
	if bypassUDP, bypassTCP, err := resolveBypassPorts(sr.BypassPresets, sr.BypassExtraPorts); err == nil &&
		(portRangesContain(bypassUDP, 53) || portRangesContain(bypassTCP, 53)) {
		spec.NoDNAT = true
		return spec, true
	}

	tunDNS, err := DeriveTunDNS(resolveFakeIPParams(s.deps.FakeIPTun, sr).TunAddr4)
	if err != nil {
		// Маршрутную половину заворота сохраняем: она от DNS не зависит.
		s.appLog.Warn("policy-tun-ingress", iface, "derive tun DNS: "+err.Error())
		spec.NoDNAT = true
		return spec, true
	}
	if s.deps.Policies == nil {
		spec.NoDNAT = true
		return spec, true
	}
	exits, err := s.deps.Policies.ListPolicyExits(ctx, ndmsName)
	if err != nil {
		s.appLog.Warn("policy-tun-ingress", iface, "список политик: "+err.Error())
		return FakeIPIngressSpec{}, false
	}
	spec.TunDNS = tunDNS
	for _, e := range exits {
		spec.Marks = append(spec.Marks, e.Mark)
	}
	return spec, true
}

// portRangesContain — вхождение порта в набор диапазонов bypass.
func portRangesContain(ranges []PortRange, port int) bool {
	for _, pr := range ranges {
		if port >= pr.From && port <= pr.To {
			return true
		}
	}
	return false
}

// reconcilePolicyTun — арм policy-tun в Reconcile (диспатч в начале Reconcile).
// Как и fakeip-tun, режим не ставит основных iptables, поэтому installed-проверка
// tproxy здесь бессмысленна: состояние ведётся по собственным сигналам.
//
// Дерево решений — зеркало reconcileFakeIPTun:
//   - !Enabled                    → Disable (диспатчится в disablePolicyTun);
//   - Enabled, не провижинен/исчез → Enable (гард идемпотентности внутри);
//   - Enabled, провижинен и жив    → DRIFT-HEAL: best-effort, шаг за шагом, с
//     логом и продолжением при сбое отдельного шага.
//
// Рестарт мёртвого движка сюда НЕ входит: единственный рестарт-авторитет —
// watchdog (Operator.Reconcile). Fail-closed режима врождённый: дефолт
// припаркован на tun, чей читатель мёртв, — трафик политики дропается, а не
// течёт в WAN.
func (s *ServiceImpl) reconcilePolicyTun(ctx context.Context, sr storage.SingboxRouterSettings) error {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	st := settings.PolicyTun

	if !sr.Enabled {
		// Teardown только когда что-то реально поднято — иначе каждый
		// boot-reconcile писал бы ложное «выключение движка» в журнал.
		provisioned := st != nil && st.Provisioned
		slotActive := s.deps.Orch != nil && s.routerSlotEnabled()
		if !provisioned && !slotActive {
			return nil
		}
		return s.Disable(ctx)
	}

	// Транзиентная ошибка пробы (глюк NDMS на буте) НЕ равна «интерфейс исчез»:
	// иначе каждый сбойный тик уходил бы в полный re-provision (та же ловушка,
	// что в fakeip).
	var live map[int]bool
	var probeErr error
	if s.deps.OpkgTunIndices != nil {
		live, probeErr = s.deps.OpkgTunIndices.LiveOpkgTunIndices(ctx)
	}
	if st == nil || !st.Provisioned || (probeErr == nil && !live[st.Index]) {
		// Drift-heal, НЕ действие пользователя: sticky master-Stop не сбрасываем.
		return s.enableLocked(ctx, false)
	}

	iface := fakeIPIfaceName(st.Index)   // kernel: метки логов, carrier, ingress
	ndmsName := fakeIPNDMSName(st.Index) // NDMS RCI: маршруты, ip global, permit

	// Запаркованный слот 20 — дрейф независимо от жизни процесса: enable
	// no-op'ится на provisioned+live и слот бы уже не вернул, а без него в
	// merged-конфиге нет tun-инбаунда.
	if s.deps.Orch != nil {
		if slot, ok := s.slotSnapshot(orchestrator.SlotRouter); !ok || !slot.Enabled {
			if e := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, true); e != nil {
				s.appLog.Warn("policy-tun-reconcile", iface, "enable slot: "+e.Error())
			} else {
				s.appLog.Info("policy-tun-reconcile", iface,
					"слот 20-router был запаркован — возвращён в конфиг (drift-heal)")
				s.notifyRoutingSlotsChanged()
			}
		}
	}

	// One-shot (до первого УСПЕХА) ассерт permit-ACL: покрывает апгрейд поверх
	// уже включённого режима и удаление списка мимо нас. Гейт probeErr == nil —
	// живость интерфейса подтверждена, иначе bind упал бы и осиротевший список
	// остался бы в конфиге навсегда.
	if !s.policyTunACLAsserted && s.deps.OpkgTun != nil && probeErr == nil {
		if e := s.deps.OpkgTun.SetPermitAllACL(ctx, ndmsName); e != nil {
			s.appLog.Warn("policy-tun-reconcile", iface, "permit acl: "+e.Error())
		} else {
			s.policyTunACLAsserted = true
		}
	}

	s.healPolicyTunNDMS(ctx, sr, iface, ndmsName)

	// Ingress-заворот: и drift-heal после сброса firewall NDMS, и применение
	// смены состава ingress-интерфейсов (UpdateSettings завершается Reconcile'ом).
	// Реап, идущий в Reconcile первым, наш заворот в этом режиме не трогает —
	// см. ReapOrphanedFakeIPTun.
	if spec, ok := s.policyTunIngressSpec(ctx, iface, ndmsName, sr); ok {
		s.ensureFakeIPIngress(ctx, spec)
	}

	s.restoreRevokedPolicyTunNAT(ctx, sr, st, iface)
	s.reconcilePolicyTunNAT(ctx, sr, st, iface)
	s.reconcilePolicyTunQoS(ctx, sr)
	return nil
}

// healPolicyTunNDMS сверяет NDMS-состояние режима с running-config и чинит
// расхождения: `ip global` (без него интерфейс исчезает из выходов политики) и
// припаркованный дефолт (v4/v6 раздельно — re-add только отсутствующего).
//
// Кэш running-config (TTL 60 мин) инвалидируется ТОЛЬКО когда по кэшу состояние
// нездорово: RCI на роутере медленный, а сброс кэша на каждом тике превратил бы
// проверку в полноценный запрос конфига раз в 30 секунд. Нездоровым считается и
// отсутствие permit'а — тогда сброс кэша заодно гасит issue policy-tun-unbound
// в пределах тика после того, как пользователь настроил политику (иначе он
// висел бы до часа).
func (s *ServiceImpl) healPolicyTunNDMS(ctx context.Context, sr storage.SingboxRouterSettings, iface, ndmsName string) {
	if s.deps.RunningConfig == nil {
		return
	}
	lines, err := s.deps.RunningConfig.Lines(ctx)
	if err != nil {
		s.appLog.Warn("policy-tun-reconcile", iface, "running-config: "+err.Error())
		return
	}
	wantV6 := resolveFakeIPParams(s.deps.FakeIPTun, sr).TunAddr6 != ""

	v4, v6 := policyTunDefaultRoutePresent(lines, ndmsName)
	global := policyTunIPGlobalPresent(lines, ndmsName)
	permitted := policyTunPermitted(lines, ndmsName)
	healthy := v4 && global && permitted && (!wantV6 || v6)
	if !healthy {
		// По кэшу всё плохо — перечитываем и решаем по свежим данным: иначе
		// починка мимо нас (пользователь в веб-морде) выглядела бы дрейфом и
		// каждый тик переставляла бы уже стоящие маршруты.
		s.deps.RunningConfig.InvalidateAll()
		if fresh, ferr := s.deps.RunningConfig.Lines(ctx); ferr == nil {
			v4, v6 = policyTunDefaultRoutePresent(fresh, ndmsName)
			global = policyTunIPGlobalPresent(fresh, ndmsName)
			permitted = policyTunPermitted(fresh, ndmsName)
		}
	}

	if !global && s.deps.OpkgTun != nil {
		if e := s.deps.OpkgTun.SetIPGlobal(ctx, ndmsName); e != nil {
			s.appLog.Warn("policy-tun-reconcile", iface, "re-assert ip global: "+e.Error())
		} else {
			s.appLog.Warn("policy-tun-reconcile", iface,
				"ip global пропал у "+ndmsName+" — интерфейс не был виден в политиках, восстановлен (drift-heal)")
		}
	}
	// Permit-ассерт: включение доставить его повторно не может — оно no-op'ится
	// по гарду provisioned+live, поэтому отказ RCI при включении и смена
	// PolicyName на работающем режиме чинятся только здесь.
	s.ensurePolicyTunPermit(ctx, sr, iface, ndmsName, permitted)
	if s.deps.DefaultRoute == nil {
		return
	}
	if !v4 {
		if e := s.deps.DefaultRoute.SetDefaultRoute(ctx, ndmsName); e != nil {
			s.appLog.Warn("policy-tun-reconcile", iface, "re-add default route: "+e.Error())
		} else {
			s.appLog.Info("policy-tun-reconcile", iface, "дефолт-маршрут пропал — переустановлен (drift-heal)")
		}
	}
	if wantV6 && !v6 {
		if e := s.deps.DefaultRoute.SetIPv6DefaultRoute(ctx, ndmsName); e != nil {
			s.appLog.Warn("policy-tun-reconcile", iface, "re-add ipv6 default route: "+e.Error())
		} else {
			s.appLog.Info("policy-tun-reconcile", iface, "v6-дефолт пропал — переустановлен (drift-heal)")
		}
	}
}

// ensurePolicyTunPermit разрешает наш интерфейс выходом целевой политики
// (sr.PolicyName) — без этого режим поднят, а трафик членов политики в туннель
// не заходит.
//
// permitted — ответ «уже разрешён?» из running-config: идемпотентность держится
// на чтении перед записью, а не на поведении NDMS (повторный order=0 в непустой
// политике переставляет список выходов). Цена — permit, стоящий не первым, мы
// не двигаем: расстановку пользователя не перебиваем.
//
// Best-effort: отказ RCI логируется и НЕ валит подъём режима — permit доставит
// drift-heal на следующем тике.
func (s *ServiceImpl) ensurePolicyTunPermit(ctx context.Context, sr storage.SingboxRouterSettings, iface, ndmsName string, permitted bool) {
	if permitted || sr.PolicyName == "" || s.deps.Policies == nil {
		return
	}
	if e := s.deps.Policies.PermitInterface(ctx, sr.PolicyName, ndmsName, 0); e != nil {
		s.appLog.Warn("policy-tun", iface, "permit "+ndmsName+" в политике "+sr.PolicyName+": "+e.Error())
		return
	}
	s.appLog.Info("policy-tun", iface, ndmsName+" разрешён выходом политики "+sr.PolicyName+" (order 0)")
}

// reconcilePolicyTunQoS применяет runtime-изменения классов QoS — единственный
// netfilter этого режима. Другого пути применения нет: UpdateSettings
// завершается Reconcile'ом.
//
//   - sing-box-сторона (qos-инбаунды слота 20 + оверлей 18-qos-routes) сходится
//     общим healQoSConfig: при нуле классов он вычищает инбаунды и паркует слот;
//   - netfilter переустанавливается только при РАСХОЖДЕНИИ желаемого спека с
//     применённым (s.currentQoSClasses) — Probe ловит пропажу цепочек, но не их
//     содержимое;
//   - ноль классов при расхождении → безусловный (идемпотентный) Uninstall:
//     иначе устаревшие `-m dscp → TPROXY` вечно реассертятся netfilter.d-хуком и
//     блэкхолят DSCP-трафик в порт без слушателя.
//
// force на первом тике процесса (netfilterStateKnown=false — тот же признак,
// что у tproxy-ветки) закрывает случай «демон рестартовал, чужие цепочки от
// прежнего режима/версии живы, а желаемое состояние с ними случайно совпало по
// пустому слайсу». Uninstall заодно снимает fail-closed blackhole, оставшийся от
// tproxy: в policy-tun он не применяется.
func (s *ServiceImpl) reconcilePolicyTunQoS(ctx context.Context, sr storage.SingboxRouterSettings) {
	qosClasses := activeQoSClasses(sr.QoSClasses)
	qosSpecs := qosIPTablesSpecs(qosClasses)
	if len(qosSpecs) > 0 {
		if e := EnsureXtDscpModule(ctx); e != nil {
			s.appLog.Warn("ensure-xt-dscp", "", e.Error())
		}
		// Деградация ДО сравнения: гейт после него оставил бы
		// желаемое≠применённое навсегда и переустанавливал бы каждый тик.
		if !s.xtDscpUsable(ctx) {
			qosSpecs = nil
		}
	}
	if _, err := s.healQoSConfig(ctx, sr); err != nil {
		s.appLog.Warn("policy-tun-reconcile", "qos", err.Error())
	}
	if s.deps.IPTables == nil {
		return
	}

	s.mu.Lock()
	force := !s.netfilterStateKnown
	changed := !slices.Equal(s.currentQoSClasses, qosSpecs)
	s.mu.Unlock()
	if !force && !changed {
		if len(qosSpecs) == 0 {
			return
		}
		// Классы не менялись, но цепочки/джампы могли пропасть мимо NDMS (ручной
		// iptables -F, сбой netfilter.d-хука) — тогда классы QoS молча мертвы до
		// рестарта демона. Probe — единственный сигнал такого дрейфа. Ошибка
		// пробы = «неизвестно»: НЕ переустанавливаем (транзиентный сбой `-S` во
		// время reload NDMS не повод пересобирать перехват), как в tproxy-ветке.
		chains, jumps, perr := s.deps.IPTables.Probe(ctx)
		if perr != nil || (chains && jumps) {
			return
		}
		s.appLog.Warn("policy-tun-reconcile", "qos",
			"цепочки DSCP-диспатча пропали при неизменных классах — переустанавливаем")
	}

	if len(qosSpecs) == 0 {
		if err := s.deps.IPTables.Uninstall(ctx); err != nil {
			s.appLog.Warn("policy-tun-reconcile", "qos", "iptables uninstall: "+err.Error())
			return
		}
		s.mu.Lock()
		s.currentQoSClasses = nil
		s.blackholeActive = false
		s.netfilterStateKnown = true
		s.mu.Unlock()
		return
	}

	if s.deps.WANIPCollector == nil {
		return
	}
	if err := s.prepareNetfilter(ctx); err != nil {
		s.appLog.Warn("qos-dscp", "", "netfilter TPROXY недоступен — классы QoS пропущены: "+err.Error())
		return
	}
	// WAN-IP исключения обязательны: без них DSCP-меченный трафик на собственный
	// WAN-адрес роутера ушёл бы в sing-box петлёй.
	wanIPs, err := s.deps.WANIPCollector.Collect(ctx)
	if err != nil {
		s.appLog.Warn("policy-tun-reconcile", "qos", "collect WAN IPs: "+err.Error())
		return
	}
	bypassUDP, bypassTCP, _ := resolveBypassPorts(sr.BypassPresets, sr.BypassExtraPorts)
	bypassSubnets, _ := resolveBypassCIDRs(sr.BypassPresets, sr.BypassExtraSubnets)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.deps.IPTables.Install(ctx, RestoreInputSpec{
		DSCPOnly:       true,
		MatchAll:       true,
		WANIPs:         wanIPs,
		BypassUDPPorts: bypassUDP,
		BypassTCPPorts: bypassTCP,
		BypassCIDRs:    bypassSubnets,
		QoSClasses:     qosSpecs,
	}); err != nil {
		s.appLog.Warn("policy-tun-reconcile", "qos", "iptables install: "+err.Error())
		return
	}
	s.currentQoSClasses = qosSpecs
	s.netfilterStateKnown = true
}
