package router

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// disablePolicyTun tears down the policy-tun path. Called with s.mu held by
// Disable.
//
// Структурно — близнец disableFakeIPTun, но БЕЗ pool/reject/drain-механики:
// у policy-tun нет синтетического пула адресов, который надо фейл-клоузить на
// время протухания клиентских кэшей. Весь заворот делает NDMS-политика через
// припаркованный на tun дефолт, поэтому «утечка» после teardown = обычный WAN,
// то есть штатное поведение выключенного движка.
//
// Порядок (каждый шаг best-effort, teardown НИКОГДА не прерывается на ошибке —
// полуразобранный policy-tun хуже доведённого до конца):
//  0. снять netfilter.d-хук перехвата DNS — до любых RCI-мутаций И до гарда
//     «нет персиста»: в той ветке снимать его больше некому;
//  1. восстановление записанного NAT сегментов (source-preserve);
//  2. снять NDMS-дефолт (v4+v6) с tun, пока интерфейс ещё жив: клиенты политики
//     сразу уезжают на WAN, а не в дыру;
//  3. снять ingress-заворот (ip rule iif + таблица 700);
//  4. конфиг слота 20: выкинуть tun-инбаунд и запарковать слот (+ оверлей QoS),
//     затем безусловный IPTables.Uninstall;
//  5. удержать OpkgTun (снять ACL, положить, снять адреса) — интерфейс и его
//     индекс переживают выключение: к имени привязан permit в политике;
//  6. persist Enabled=false — обязателен, это durable-истина выключения.
func (s *ServiceImpl) disablePolicyTun(ctx context.Context, settings *storage.Settings) error {
	st, _ := opkgTunOwned(settings, statePolicyTun)

	// (0) Хук перехвата DNS сносим ПЕРВЫМ и ДО гарда «нет персиста». Две
	// причины:
	//   - ниже идут RCI-мутации (возврат NAT сегментов, снятие дефолта) и
	//     teardown интерфейса, каждая способна спровоцировать перестройку
	//     firewall, а живой хук вернул бы правила перехвата;
	//   - в ветке без персиста (откат enable успел записать файл, но снёс
	//     персист) снимать хук больше НЕКОМУ: реап в этом режиме трогает
	//     только чужой, fakeip-тег, — файл жил бы вечно, возвращая DNAT в
	//     снесённый туннель.
	// Идемпотентно: файла нет — no-op, RCI не трогается.
	if s.deps.IPTables != nil {
		s.deps.IPTables.RemovePolicyTunDNSHook()
	}

	// Ничего не провижинилось (или персист уже очищен) → идемпотентно: только
	// флаг выключения. NDMS не трогаем.
	//
	// Слот 20 при этом паркуем: reconcile зовёт Disable, пока слот активен, и
	// без парковки гард возвращал бы «сделано», ничего не сделав, — тик
	// повторял бы Disable вечно. Раньше «не провижинен при живом слоте» было
	// экзотикой, теперь Provisioned=false — штатное состояние выключенного
	// режима (интерфейс удержан).
	//
	// ОСОЗНАННОЕ ИСКЛЮЧЕНИЕ из «снос одинаков во всех выходах»: сноса набора
	// AWGM-BYPASS здесь нет. Этот ранний выход срабатывает ОДИН раз — при
	// активном, но не провижиненном слоте: он паркует слот, и reconcile при
	// !Enabled больше сюда не заходит (гейт !provisioned && !slotActive в
	// policytun_reconcile.go). Остаточный случай (Enable упал до
	// Provisioned=true при живом наборе от прежнего tproxy) закрывает не
	// Enable — там сноса нет ни в одном режиме, — а первый тик
	// reconcilePolicyTunQoS с want == nil при netfilterStateKnown=false: он
	// зовёт teardownBypassSet. Записано в docs/TRACKER.md.
	if st == nil || !st.Provisioned {
		if s.deps.Orch != nil && s.routerSlotEnabled() {
			if err := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
				s.appLog.Warn("policy-tun-disable", "", "disable slot: "+err.Error())
			} else {
				s.notifyRoutingSlotsChanged()
			}
		}
		if err := s.deps.Settings.Update(func(cur *storage.Settings) error {
			cur.SingboxRouter.Enabled = false
			return nil
		}); err != nil {
			return err
		}
		s.emitStatus(ctx)
		return nil
	}

	iface := tunIfaceName(st.Index)   // kernel name: только метки в логах
	ndmsName := tunNDMSName(st.Index) // NDMS RCI name: маршруты + удаление

	// Имя адресует ИНДЕКС из записи владения, а не сам объект: наш интерфейс мог
	// умереть, и номер занял посторонний OpkgTun. Тогда шаги (2) и (5) сняли бы
	// ЕГО дефолт и адреса. Один скан на всё выключение; «недоступный скан ≠
	// чужой» — без скана и на его ошибке разбираем как раньше.
	foreign := s.provenForeignOpkgTun(ctx, ndmsName, policyTunDescription)
	if foreign {
		s.appLog.Warn("policy-tun-disable", ndmsName, "на этом номере нет нашего OpkgTun — интерфейс не трогаем")
	}

	// (1) Вернуть сегментам записанный NAT ПЕРВЫМ шагом: пока дефолт ещё на tun,
	// трафик сегментов сразу уходит через WAN штатным маскарадом. Best-effort —
	// teardown не прерывается (персист чистится только при успешном delete, так
	// что провал повторится реапом).
	natRestored := true
	if segs := natSegmentsOf(st); len(segs) > 0 {
		if err := s.restorePolicyTunNAT(ctx, segs); err != nil {
			s.appLog.Warn("policy-tun-disable", iface, "restore segment NAT: "+err.Error())
			natRestored = false
		}
	}

	// (2) Снять дефолт с tun. v6 снимаем безусловно: персист не хранит,
	// был ли настроен v6-адрес, а remove-форма NDMS (`no:true`) идемпотентна.
	if !foreign && s.deps.DefaultRoute != nil {
		if err := s.deps.DefaultRoute.RemoveDefaultRoute(ctx, ndmsName); err != nil {
			s.appLog.Warn("policy-tun-disable", iface, "remove default route: "+err.Error())
		}
		if err := s.deps.DefaultRoute.RemoveIPv6DefaultRoute(ctx, ndmsName); err != nil {
			s.appLog.Warn("policy-tun-disable", iface, "remove ipv6 default route: "+err.Error())
		}
	}

	// (3) Снять ingress-заворот: пустой спек означает «заворота быть не должно»,
	// правила и таблица 700 снимаются по тегу/приоритету. Идемпотентно — если
	// галок ingress не было, ни одной мутации.
	s.ensureFakeIPIngress(ctx, FakeIPIngressSpec{})

	// (4) Вычистить tun-инбаунд из слота 20 ВСЕГДА, даже если слот уже
	// запаркован: слот общий с tproxy, а ensureTProxyInbound чужие инбаунды не
	// трогает — остаточный tun-in переоткрыл бы удалённый tun при следующем
	// enable. Запись при выключенном слоте уходит в disabled/ (Orch.Save).
	if cfg, cerr := s.loadAppliedRouterConfig(); cerr != nil {
		s.appLog.Warn("policy-tun-disable", iface, "load router config: "+cerr.Error())
	} else {
		cfg.Inbounds = filterPolicyTunInbound(cfg.Inbounds)
		if err := s.persistConfigDirect(ctx, cfg); err != nil {
			s.appLog.Warn("policy-tun-disable", iface, "persist router config: "+err.Error())
		}
	}

	if s.deps.Orch != nil {
		if err := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
			s.appLog.Warn("policy-tun-disable", iface, "disable slot: "+err.Error())
		}
		// Оверлей QoS ссылается на qos-* инбаунды слота 20 — паркуется вместе.
		if err := s.disableQoSRoutesSlot(); err != nil {
			s.appLog.Warn("policy-tun-disable", "qos-routes", err.Error())
		}
		// Композиты слота 20 пропали из merged-конфига — device-proxy
		// перегенерирует слот 30 до ближайшего reload (issue #465).
		s.notifyRoutingSlotsChanged()
	}

	// Uninstall БЕЗУСЛОВНО: он идемпотентен и снимает остатки dscp-правил даже
	// тогда, когда классы QoS только что удалили из настроек (тогда enable их
	// уже не ставил, но живые цепочки с прошлого раза никуда не делись).
	if s.deps.IPTables != nil {
		s.deps.IPTables.Uninstall(ctx)
		// Снос ресурса живёт в том же выходе из режима, что и его создание, и
		// одинаков во всех выходах. Набор AWGM-BYPASS создаёт tproxy-путь, но
		// выйти из режима можно и здесь, а в policy-tun набор не нужен вовсе
		// (bypassSetWanted при этом режиме — false).
		//
		// Как сирота возникает: наполнение набора асинхронное и идёт до
		// десятка минут; runBypassPopulate делает swap+save и лишь ПОТОМ
		// проверяет, нужен ли набор ещё, снося его сам. Краш демона между
		// сохранением дампа и этой самопроверкой оставляет набор и дамп при
		// уже переключённом режиме. Хук `50-awgm-tproxy.sh` раз установленный
		// не удаляется никогда, а блок `ipset create && restore < дамп` в нём
		// гейтится только НАЛИЧИЕМ дампа, — поэтому сирота воскресала на
		// каждой перезагрузке.
		//
		// Только ПОСЛЕ Uninstall: пока в ядре есть правило `--match-set`,
		// ipset отвечает «set is in use».
		s.teardownBypassSet(ctx)
		// Симметрично tproxy-Disable: снесли — забыли. Иначе выключенный режим
		// оставлял бы за собой снимок применённого спека, а netfilterStateKnown
		// сообщал бы следующему тику, что установленное состояние известно.
		s.appliedSpec = nil
		s.appliedBlackhole = nil
		s.netfilterStateKnown = false
		// Состав geoip-тегов — четвёртый член той же группы. Без обнуления
		// «симметрично tproxy-Disable» было неправдой: следующее включение не
		// увидело бы изменения состава и не пересобрало набор AWGM-BYPASS.
		s.currentBypassGeoIPTags = nil
	}

	// (5) Удержать интерфейс: индекс закреплён за режимом, потому что permit в
	// политике доступа привязан к имени. Персист переписываем ТОЛЬКО при успехе
	// — иначе Provisioned остаётся истиной и выключение повторится следующим
	// тиком (адрес на месте = nginx-цикл ndm жив).
	//
	// КРАШ-ОКНО между удержанием и записью персиста: демон умер здесь — на диске
	// Enabled=true, Provisioned=true, а интерфейс положен, без адресов и без
	// tun-инбаунда (его вырезал шаг 4). Сам по себе выход из этого состояния
	// закрылся вместе с удалением интерфейса: NDMS-объект переживает и down, и
	// снятие адресов, поэтому live[Index] истинен и полного re-provision никто
	// не запускает, а drift-heal вернул бы только маршруты и permit.
	//
	// Закрывает окно ассерт в enabled-ветке reconcile: «провижинен и жив, но
	// tun-инбаунда в слоте нет» → переустановка режима целиком
	// (policyTunInboundPresent).
	//
	// Запись о прежнем NAT сегментов переживает выключение, если восстановить
	// его не удалось: она единственный след того, каким он был до нас.
	// Автоматического повтора у неё НЕТ — в выключенном состоянии
	// reconcilePolicyTun выходит рано; запись отработает на следующем включении,
	// смене режима или реапе.
	// Доказанно чужой интерфейс на нашем индексе не удерживаем, а запись СНИМАЕМ:
	// удержание существует ради permit'а пользователя, привязанного к имени, но
	// стенд 2026-08-18 показал, что NDMS стирает запись permit'а вместе с
	// интерфейсом и пересоздание одноимённого её НЕ воскрешает. Наш интерфейс
	// мёртв → permit уже испарился, беречь нечего, а держаться за чужой номер
	// значило бы навсегда запретить себе аллокацию. Индекс не течёт: аллокатор
	// live-sourced. Профиль потерь тот же, что у персист-реапа (там запись тоже
	// снимается на пропуске чужого).
	if foreign {
		if err := s.deps.Settings.SetOpkgTunState(nil); err != nil {
			s.appLog.Warn("policy-tun-disable", iface, "clear policy-tun persist: "+err.Error())
		}
	} else if err := s.holdOpkgTun(ctx, ndmsName, "policy-tun-disable"); err == nil {
		held := &storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: st.Index}
		if !natRestored {
			held.PolicyTun = &storage.OpkgTunPolicyData{NATSegments: natSegmentsOf(st)}
		}
		if err := s.deps.Settings.SetOpkgTunState(held); err != nil {
			s.appLog.Warn("policy-tun-disable", iface, "hold policy-tun persist: "+err.Error())
		}
	}

	// (6) Persist disabled — ОБЯЗАТЕЛЕН.
	if err := s.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter.Enabled = false
		return nil
	}); err != nil {
		return err
	}

	s.emitStatus(ctx)
	return nil
}
