package router

import (
	"context"
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

// policyTunPermitted сообщает, разрешён ли интерфейс выходом ЦЕЛЕВОЙ политики
// доступа: `permit global <ndmsName>` внутри блока `ip policy <policyName>`.
// Разрешение в чужой политике таковым не является — устройства сидят в целевой,
// и выхода у неё нет. Блок разбирается той же формой, что в
// policyTunIPGlobalPresent: заголовок без отступа, тело с отступом.
//
// Пустой policyName — «политика не выбрана»: годится разрешение в любой, сказать
// в какой именно оно обязано стоять нам нечего.
func policyTunPermitted(lines []string, ndmsName, policyName string) bool {
	inPolicy := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if line == trimmed { // строка без отступа — заголовок блока или его конец
			f := strings.Fields(trimmed)
			inPolicy = len(f) == 3 && f[0] == "ip" && f[1] == "policy" &&
				(policyName == "" || f[2] == policyName)
			continue
		}
		if !inPolicy {
			continue
		}
		// Матч с начала строки, а не скользящим окном: `permit` — первый токен
		// правила, а окно ловило бы те же слова в description.
		f := strings.Fields(trimmed)
		if len(f) >= 3 && f[0] == "permit" && f[1] == "global" && f[2] == ndmsName {
			return true
		}
	}
	return false
}

// globalEgressInterfaces собирает интерфейсы, которые МОГУТ быть выходом
// наружу: те, у кого в блоке есть `ip global`. Порядок — как в конфиге.
//
// Почему не выходы политики: `ip static <Seg> <iface>` — общероутерная
// настройка, а не свойство политики. Правило SNAT вешается на выходной
// интерфейс и срабатывает для любого трафика сегмента, ушедшего в него, — хоть
// по политике, хоть мимо неё. Привязка целей к составу политики оставила бы
// без SNAT ровно те пути, которыми ходят устройства вне её.
//
// Собственные OpkgTun отсеивает вызывающий: они выход, но SNAT в них — тот
// самый маскарад, от которого опция и спасает.
func globalEgressInterfaces(lines []string) []string {
	var out []string
	current := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if line == trimmed { // без отступа — заголовок блока или его конец
			f := strings.Fields(trimmed)
			current = ""
			if len(f) == 2 && f[0] == "interface" {
				current = f[1]
			}
			continue
		}
		if current == "" {
			continue
		}
		if trimmed == "ip global" || strings.HasPrefix(trimmed, "ip global ") {
			out = append(out, current)
			current = "" // одного признака на блок достаточно
		}
	}
	return out
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

	tunDNS, err := DeriveTunDNS(s.resolveFakeIPParams(sr).TunAddr4)
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
	// Копию снимает сам opkgTunOwned (F24) — здесь она уже наша.
	st, _ := opkgTunOwned(settings, statePolicyTun)

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
	if s.needsReprovision(ctx, st, live, probeErr, policyTunDescription) {
		// Drift-heal, НЕ действие пользователя: sticky master-Stop не сбрасываем.
		return s.enableLocked(ctx, false)
	}

	iface := tunIfaceName(st.Index)   // kernel: метки логов, carrier, ingress
	ndmsName := tunNDMSName(st.Index) // NDMS RCI: маршруты, ip global, permit

	// Провижинен и жив, но tun-инбаунда в слоте нет — состояние НЕДОДЕЛАНО, и
	// само оно не заживёт. Так выглядит краш между удержанием интерфейса и
	// записью персиста: выключение успело вырезать инбаунд (шаг 4), персист
	// остался Provisioned=true, а NDMS-объект пережил и down, и снятие адресов —
	// значит live истинен, полного re-provision никто не запустит, и heal ниже
	// вернёт только маршруты с permit. Адрес, up и инбаунд не вернёт НИКТО.
	//
	// Признак взят из НАШЕГО файла (applied-конфиг слота), а не из
	// running-config: его форму мы задаём сами, и она не зависит от NDMS.
	// Гасим гард идемпотентности через персист и переустанавливаем режим целиком.
	if !s.policyTunInboundPresent() {
		s.appLog.Warn("policy-tun-reconcile", iface,
			"режим включён, но tun-инбаунд пропал из слота — переустановка (недоделанное выключение)")
		if e := s.deps.Settings.SetOpkgTunState(&storage.OpkgTunState{
			Mode: storage.OpkgTunModePolicyTun, Index: st.Index, PolicyTun: st.PolicyTun,
		}); e != nil {
			s.appLog.Warn("policy-tun-reconcile", iface, "reset policy-tun persist: "+e.Error())
		}
		// Drift-heal, НЕ действие пользователя: sticky master-Stop не сбрасываем.
		return s.enableLocked(ctx, false)
	}

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

	// Интерфейс наш и на месте — но стек мог отцепиться от tun. Это состояние
	// не ловит ни один другой heal, см. healDetachedTun. Слот он проверяет сам.
	s.healDetachedTun(iface, "policy-tun-reconcile", orchestrator.SlotRouter)

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
	// v6-разрешение — отдельной сущностью и со своим флагом: тот же апгрейд-путь
	// (режим, поднятый версией без v6-ACL, его не имеет), но успех v4 не должен
	// гасить ретрай упавшего v6. Гейт по адресу: на интерфейсе без v6 разрешать
	// нечего.
	if !s.policyTunACLv6Asserted && s.deps.OpkgTun != nil && probeErr == nil &&
		s.resolveFakeIPParams(sr).TunAddr6 != "" {
		if e := s.deps.OpkgTun.SetPermitAllACLv6(ctx, ndmsName); e != nil {
			s.appLog.Warn("policy-tun-reconcile", iface, "permit acl v6: "+e.Error())
		} else {
			s.policyTunACLv6Asserted = true
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
	wantV6 := s.resolveFakeIPParams(sr).TunAddr6 != ""

	v4, v6 := policyTunDefaultRoutePresent(lines, ndmsName)
	global := policyTunIPGlobalPresent(lines, ndmsName)
	permitted := policyTunPermitted(lines, ndmsName, sr.PolicyName)
	healthy := v4 && global && permitted && (!wantV6 || v6)
	if !healthy {
		// По кэшу всё плохо — перечитываем и решаем по свежим данным: иначе
		// починка мимо нас (пользователь в веб-морде) выглядела бы дрейфом и
		// каждый тик переставляла бы уже стоящие маршруты.
		s.deps.RunningConfig.InvalidateAll()
		if fresh, ferr := s.deps.RunningConfig.Lines(ctx); ferr == nil {
			v4, v6 = policyTunDefaultRoutePresent(fresh, ndmsName)
			global = policyTunIPGlobalPresent(fresh, ndmsName)
			permitted = policyTunPermitted(fresh, ndmsName, sr.PolicyName)
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

// policyTunInboundPresent сообщает, есть ли tun-инбаунд в applied-конфиге
// слота 20. Ошибка чтения трактуется как «есть»: «не знаем» ≠ «пропал», а
// цена ложного срабатывания — полный re-provision живого режима.
func (s *ServiceImpl) policyTunInboundPresent() bool {
	cfg, err := s.loadAppliedRouterConfig()
	if err != nil || cfg == nil {
		return true
	}
	return len(filterPolicyTunInbound(cfg.Inbounds)) != len(cfg.Inbounds)
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
//     применённым (s.appliedSpec) — не только по составу классов, но и по
//     WAN-адресам и bypass; Probe ловит пропажу цепочек, но не их содержимое;
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

	// Желаемый спек ЦЕЛИКОМ, а не одни классы: WAN-адрес роутера и bypass —
	// такие же входы правил, и их смена обязана переустанавливать цепочки.
	// nil = «netfilter в этом режиме не нужен» (активных классов нет).
	var want *RestoreInputSpec
	if len(qosSpecs) > 0 {
		if s.deps.WANIPCollector == nil {
			return
		}
		// WAN-IP исключения обязательны: без них DSCP-меченный трафик на
		// собственный WAN-адрес роутера ушёл бы в sing-box петлёй.
		wanIPs, err := s.deps.WANIPCollector.Collect(ctx)
		if err != nil {
			s.appLog.Warn("policy-tun-reconcile", "qos", "collect WAN IPs: "+err.Error())
			return
		}
		spec := s.buildPolicyTunSpec(sr, wanIPs, qosSpecs)
		want = &spec
	}

	s.mu.Lock()
	force := !s.netfilterStateKnown
	changed := !equalInstalledSpec(s.appliedSpec, want)
	s.mu.Unlock()
	if !force && !changed {
		if want == nil {
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

	if want == nil {
		s.deps.IPTables.Uninstall(ctx)
		// Тот же выход из режима, что и в policy-tun-Disable, — и тот же снос
		// набора: ресурс сносит тот, кто перестал в нём нуждаться, в КАЖДОМ
		// пути выхода. Порядок обязателен: после Uninstall, иначе «set is in
		// use».
		s.teardownBypassSet(ctx)
		s.mu.Lock()
		s.appliedSpec = nil
		// Uninstall снимает и blackhole прежнего режима — снимок обнуляем.
		s.appliedBlackhole = nil
		s.netfilterStateKnown = true
		// Четвёртый член той же группы (см. policytun_disable.go): без него
		// следующее включение не увидело бы изменения состава тегов и не
		// пересобрало набор AWGM-BYPASS. Сегодня недостижимо как баг — теги к
		// этому моменту уже обнулены, — но дисциплина «четыре поля сбрасываются
		// вместе» была нарушена ровно здесь (F21).
		s.currentBypassGeoIPTags = nil
		s.mu.Unlock()
		return
	}

	if err := s.prepareNetfilter(ctx); err != nil {
		s.appLog.Warn("qos-dscp", "", "netfilter TPROXY недоступен — классы QoS пропущены: "+err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.deps.IPTables.Install(ctx, *want); err != nil {
		// restore коммитит ПО ТАБЛИЦАМ: часть правил могла примениться, поэтому
		// снимок больше не соответствует железу. Помечаем состояние неизвестным
		// — следующий тик переустановит детерминированно (F20). appliedSpec НЕ
		// обнуляем: nil значит «ничего нашего не установлено», а после
		// частичного провала это было бы враньём.
		s.netfilterStateKnown = false
		s.appLog.Warn("policy-tun-reconcile", "qos", "iptables install: "+err.Error())
		return
	}
	s.appliedSpec = want
	s.netfilterStateKnown = true
}
