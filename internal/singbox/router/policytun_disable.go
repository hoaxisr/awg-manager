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
	if st == nil || !st.Provisioned {
		if s.deps.Orch != nil && s.routerSlotEnabled() {
			if err := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
				s.appLog.Warn("policy-tun-disable", "", "disable slot: "+err.Error())
			} else {
				s.notifyRoutingSlotsChanged()
			}
		}
		// Мутируем локальную копию: `settings` — алиас живого кэша стора,
		// который параллельно читают другие горутины без лока.
		cp := *settings
		settings = &cp
		settings.SingboxRouter.Enabled = false
		if err := s.deps.Settings.Save(settings); err != nil {
			return err
		}
		s.emitStatus(ctx)
		return nil
	}

	iface := fakeIPIfaceName(st.Index)   // kernel name: только метки в логах
	ndmsName := fakeIPNDMSName(st.Index) // NDMS RCI name: маршруты + удаление

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
	if s.deps.DefaultRoute != nil {
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
		if err := s.deps.IPTables.Uninstall(ctx); err != nil {
			s.appLog.Warn("policy-tun-disable", iface, "iptables uninstall: "+err.Error())
		}
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
	if err := s.holdOpkgTun(ctx, ndmsName, "policy-tun-disable"); err == nil {
		held := &storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: st.Index}
		if !natRestored {
			held.PolicyTun = &storage.OpkgTunPolicyData{NATSegments: natSegmentsOf(st)}
		}
		if err := s.deps.Settings.SetOpkgTunState(held); err != nil {
			s.appLog.Warn("policy-tun-disable", iface, "hold policy-tun persist: "+err.Error())
		}
	}

	// (6) Persist disabled — ОБЯЗАТЕЛЕН.
	// Мутируем локальную копию: `settings` всё ещё алиас живого кэша стора,
	// который параллельно читают другие горутины без лока. Копия именно
	// здесь, а не сразу после Load: так в неё попадает всё, что записали в
	// кэш узкие мутаторы выше, — ровно то, что сохранил бы прежний Save.
	cp := *settings
	settings = &cp
	settings.SingboxRouter.Enabled = false
	if err := s.deps.Settings.Save(settings); err != nil {
		return err
	}

	s.emitStatus(ctx)
	return nil
}
