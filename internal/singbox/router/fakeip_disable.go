package router

import (
	"context"
	"errors"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// fakeIPLinkPresent reports whether the kernel netdev <iface> still exists after
// the NDMS-level DeleteOpkgTun. NDMS normally tears the kernel device down too,
// but a half-removed teardown can leave a DOWN orphan opkgtunN behind that would
// collide with the index allocator on the next Enable. `ip link show dev <iface>`
// exits non-zero when the device is absent → we treat any error as "absent" (no
// delete attempted). Seam var for tests.
var fakeIPLinkPresent = func(ctx context.Context, iface string) bool {
	_, err := sysexec.Run(ctx, ipBinary, "link", "show", "dev", iface)
	return err == nil
}

// fakeIPLinkDelete removes a lingering kernel netdev under the list barrier.
// Запись OpkgTunN к этому моменту уже снята, так что C 0767 тут невозможен;
// под гейтом — ради инварианта «ни одного del/add мимо netdev.Swapper» (N1).
// Seam var for tests.
var fakeIPLinkDelete = func(ctx context.Context, gate *netdev.SwapGate, iface string) error {
	return gate.DeleteLink(ctx, iface)
}

// disableFakeIPTun снимает путь fakeip-tun. Вызывается под s.mu из disable.
//
// Пул снимается сразу, без reject и окна слива: reject на маршруте NDMS уносит
// вместе с интерфейсом, а `unreachable` застревает в ядре (main и таблицы
// маршрутизации NDMS) до перезагрузки, и снять его через NDMS уже нельзя — стенд
// KN-1810, 5.02.B.1, 04.11.2026 (F758). Цена принята владельцем: клиент с
// кэшированным fake-адресом уходит по дефолту до перерезолва, но пулы
// 198.18.0.0/15 и fc00::/7 в интернете не маршрутизируются.
//
// Порядок:
//  1. ничего не поднято → только записать Enabled=false (идемпотентно);
//  2. снять маршруты пула v4 и v6, ingress-заворот, остановить sing-box;
//  3. снять интерфейс и CIDR-маршруты, очистить запись владения;
//  4. записать выключение.
//
// В отличие от Enable (откат по первой ошибке) выключение идёт до конца:
// наполовину снятый fakeip хуже полной попытки, поэтому шаги сноса только
// пишут предупреждение, обязательна лишь запись состояния.
func (s *ServiceImpl) disableFakeIPTun(ctx context.Context, settings *storage.Settings) error {
	st, _ := opkgTunOwned(settings, stateFakeIPTun)

	// Nothing provisioned (or persist already cleared) → idempotent: just persist
	// the disabled flag and emit. No NDMS teardown to do.
	if st == nil || !st.Provisioned {
		if err := s.deps.Settings.Update(func(cur *storage.Settings) error {
			cur.SingboxRouter.Enabled = false
			return nil
		}); err != nil {
			return err
		}
		s.emitStatus(ctx)
		return nil
	}

	iface := tunIfaceName(st.Index)   // kernel name: log labels only here
	ndmsName := tunNDMSName(st.Index) // NDMS RCI name: pool routes + iface delete

	// Derive the v4 pool network + dotted mask (Masked, mirroring Enable). If the
	// persisted range is malformed we cannot address the v4 route; log and skip it
	// (the rest of the teardown — stop sing-box, delete iface, clear persist — still
	// runs).
	var inet4Range, inet6Range string
	if st.FakeIP != nil {
		inet4Range, inet6Range = st.FakeIP.Inet4Range, st.FakeIP.Inet6Range
	}
	var poolNet4, poolMask4 string
	if inet4Range != "" {
		if n, m, derr := poolV4NetMask(inet4Range); derr == nil {
			poolNet4, poolMask4 = n, m
		} else {
			s.appLog.Warn("fakeip-disable", iface, "derive pool v4 net/mask: "+derr.Error())
		}
	}
	haveV4 := poolNet4 != "" && poolMask4 != ""
	haveV6 := inet6Range != ""

	// (2) Снять маршруты пула, пока интерфейс жив и адресует их. Сбой не критичен:
	// удаление интерфейса уносит маршрут, остаток добирает реап на старте.
	if haveV4 {
		if err := s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
			Network: poolNet4, Mask: poolMask4, Interface: ndmsName,
		}); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "remove pool route: "+err.Error())
		}
	}
	if haveV6 {
		if err := s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
			V6: true, Network: inet6Range, Interface: ndmsName,
		}); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "remove pool route v6: "+err.Error())
		}
	}

	// (2b) Снять ingress-заворот (issue #678) ДО остановки движка и удаления
	// интерфейса: иначе клиенты ingress-серверов остались бы с default в
	// мёртвый tun и перехватом DNS на несуществующий адрес. Идемпотентно.
	if s.deps.IPTables != nil {
		s.deps.IPTables.RemoveFakeIPIngress(ctx)
	}

	// (2c) Stop sing-box (move 21-fakeip.json under disabled/). Legacy (no orch):
	// skip — there is no in-place inbound to strip for fakeip-tun. Best-effort.
	if s.deps.Orch != nil {
		if err := s.deps.Orch.SetEnabled(orchestrator.SlotFakeIP, false); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "disable slot: "+err.Error())
		}
		// Композиты слота 21 пропали из merged-конфига — device-proxy
		// перегенерирует слот 30 до ближайшего reload (issue #465).
		s.notifyRoutingSlotsChanged()
	}
	s.releaseTunNow("fakeip-disable", iface)

	// (3) Tear the iface down (delete; on delete failure — clear the configured
	// addresses, then down, see teardownOpkgTun) — NDMS name. Best-effort: a failed
	// delete is retried by the periodic reap scan.
	//
	// Гейт общий с (3a): доказанно чужой интерфейс на нашем индексе не сносим
	// НИ на уровне NDMS, ни добивающим `ip link delete` — пропустив первое и
	// выполнив второе, мы убили бы посторонний туннель наполовину. Цена гейта
	// честная: kernel-сироту без NDMS-объекта скан тоже не видит, поэтому её
	// уборка здесь пропускается (индекс не течёт — аллокатор live-sourced).
	// Скан упал — тоже пропуск (F493): запись всё равно снимается на шаге (5),
	// а интерфейс с нашим описанием добирает reapOrphansByDescription, когда
	// скан заработает. Ошибку гейта не возвращаем: выключение обязано дойти
	// до персиста.
	if proceed, gateErr := s.teardownGate(ctx, ndmsName, "fakeip-disable", fakeIPTunDescription); proceed {
		// (3a) Уборку осиротевшего kernel-netdev делает сам teardownOpkgTun —
		// он же нужен откатам и реап-ретраям, которые ходят туда напрямую.
		_ = s.teardownOpkgTun(ctx, ndmsName, "fakeip-disable")
	} else if gateErr == nil {
		s.warnPermitIndexChange("fakeip-disable", ndmsName)
	}

	// Remove specific CIDR routes on disable. After fakeip is off these
	// destinations correctly fall back to the normal WAN exit (direct).
	// Best-effort, logged. Must run while the config is still loadable.
	if cfg, cerr := s.loadFakeIPConfig(); cerr == nil {
		cfg = s.ruleSetMaterializer().restoreConfig(cfg)
		dV4, dV6 := desiredTunCIDRs(cfg)
		// Одно подтверждение на оба цикла (F546); интерфейса нет — снимать нечего.
		if len(dV4)+len(dV6) > 0 {
			rt, e := s.cidrRoutes(ctx, ndmsName)()
			switch {
			case errors.Is(e, ErrIfaceAbsent):
			case e != nil:
				s.appLog.Warn("fakeip-disable", iface, "remove cidr routes: "+e.Error())
			default:
				for _, c := range dV4 {
					if e := s.removeCIDRRoute(ctx, rt, ndmsName, c, false); e != nil {
						s.appLog.Warn("fakeip-disable", iface, "remove cidr route "+c+": "+e.Error())
					}
				}
				for _, c := range dV6 {
					if e := s.removeCIDRRoute(ctx, rt, ndmsName, c, true); e != nil {
						s.appLog.Warn("fakeip-disable", iface, "remove cidr route v6 "+c+": "+e.Error())
					}
				}
			}
		}
	}

	// (3b) Clear the ownership record — MANDATORY (push through even if a step
	// above errored). A stale record would make the startup reap chase a gone
	// iface. Миграционный policy-payload на записи (артефакт v34) сперва
	// восстанавливаем best-effort — очистка обязательна в любом случае, паритет
	// с реапом.
	if segs := natSegmentsOf(st); len(segs) > 0 {
		if err := s.restorePolicyTunNAT(ctx, segs); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "restore segment NAT (migrated payload): "+err.Error())
		}
	}
	if err := s.deps.Settings.SetOpkgTunState(nil); err != nil {
		s.appLog.Warn("fakeip-disable", iface, "clear opkgtun persist: "+err.Error())
	}

	// (4) Persist disabled — MANDATORY. This is the durable on/off truth.
	if err := s.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter.Enabled = false
		return nil
	}); err != nil {
		return err
	}

	s.emitStatus(ctx)
	return nil
}

// holdOpkgTun — выключение БЕЗ удаления интерфейса: индекс закреплён за
// режимом, потому что permit в политике доступа привязан к имени OpkgTun<N>, и
// удаление заставило бы следующее включение взять другой номер, оставив
// разрешение висеть на несуществующем.
//
// Адреса снимаются обязательно: стенд 2026-07-15 показал, что nginx-цикл ndm
// вызывает именно СКОНФИГУРИРОВАННЫЙ `ip address` без kernel-адреса, а не сам
// интерфейс — снятие адреса убивало цикл мгновенно.
//
// ACL продукта (`_WEBADMIN_<name>`) снимаем сами: каскада от delete здесь больше
// нет. К permit пользователя в политике этот ACL отношения не имеет.
//
// Ошибку возвращает ТОЛЬКО снятие v4-адреса: он ставится при каждом успешном
// включении, поэтому отказ — настоящая поломка, и выключение обязано
// повториться. Отказ на v6 провалом не считается: v6-адреса могло не быть
// вовсе, а вечный ретрай выключения хуже незакрытого v6.
func (s *ServiceImpl) holdOpkgTun(ctx context.Context, ndmsName, scope string) error {
	// v4 и v6 — по одному running-config; отсутствующей привязки/списка команда
	// не шлёт (F606), так что ошибка здесь — настоящий отказ NDMS. Debug: hold
	// best-effort, разрешение снимется при следующем выключении.
	if err := s.deps.OpkgTun.RemovePermitAllACLs(ctx, ndmsName); err != nil {
		s.appLog.Debug(scope, ndmsName, "remove permit acl: "+err.Error())
	}
	// Гейт существования — обязателен: дальше идут down/clear, а NDMS создаёт
	// интерфейс по ЛЮБОЙ мутации его имени (см. teardownOpkgTun). Здесь, в
	// отличие от teardown, за ними НЕТ delete, который бы такую пустышку
	// подобрал: hold интерфейс намеренно сохраняет. Пустышка же не несёт
	// нашего описания, реап по описанию её не видит, и индекс занят навсегда.
	// Вызывающий зовёт hold при вердикте «наш» либо «скана нет» (обвязка без
	// NDMS): во втором случае интерфейса может и не быть.
	if !fakeIPLinkPresent(ctx, strings.ToLower(ndmsName)) {
		s.appLog.Debug(scope, ndmsName, "hold: интерфейса нет, мутации пропущены")
		return nil
	}
	err := s.deps.OpkgTun.ClearAddress(ctx, ndmsName)
	if err != nil {
		s.appLog.Warn(scope, ndmsName, "clear address: "+err.Error())
	}
	// Снимаем в любом случае: провал v4 не повод оставить v6 висеть.
	if e := s.deps.OpkgTun.ClearIPv6Address(ctx, ndmsName); e != nil {
		s.appLog.Debug(scope, ndmsName, "clear ipv6 address: "+e.Error())
	}
	// Down — ПОСЛЕ снятия адресов: down интерфейса со статическим IPv6 NDMS
	// отвечает двумя W «reinstall address» (F675, проба 17.10 голым RCI).
	if e := s.deps.OpkgTun.InterfaceDown(ctx, ndmsName); e != nil {
		s.appLog.Warn(scope, ndmsName, "iface down: "+e.Error())
	}
	return err
}

// releaseTunNow применяет конфиг без tun-инбаунда сразу, до того как NDMS
// снесёт или переконфигурирует OpkgTun. Под HoldReloads (SwitchRoutingMode)
// reload иначе копится до конца перехода, и sing-box держит tun открытым,
// пока NDMS удаляет запись: C 0xcffd003b в ndm и «engine wait: connection
// reset by peer» у движка — на каждом переходе с OpkgTun (F447/F638, стенд
// 28.10). ReloadNow работает под hold и снимает накопленное, лишнего reload
// на выходе нет. Провал — Warn: дальше идём, как шли раньше.
func (s *ServiceImpl) releaseTunNow(scope, iface string) {
	if err := releaseTunApply(s); err != nil {
		s.appLog.Warn(scope, iface, "apply config without tun: "+err.Error())
	}
}

// releaseTunApply — шов над немедленным применением для тестов порядка.
var releaseTunApply = (*ServiceImpl).orchestratorApplyNow

// teardownOpkgTun best-effort сносит NDMS OpkgTun: delete; при провале delete
// снимает сконфигурированные v4/v6 адреса и гасит интерфейс. Единственное место, где живёт
// инвариант: интерфейс с настроенным `ip address`, но без kernel-адреса вгоняет
// ndm в бесконечный nginx-reload цикл (bind fail → регенерация конфига →
// reload → …), подвешивающий весь RCI на секунды (stand-verified 2026-07-15) —
// поэтому провал delete ОБЯЗАН оставлять интерфейс без адресов. Clear'ы идут
// ПОСЛЕ провала delete: на happy-path они были бы лишними RCI-вызовами, а на
// уже исчезнувшем интерфейсе delete идемпотентно успешен и clear'ы (с их
// create-on-reference риском в NDMS) не выполняются вовсе. Возвращает ошибку
// delete; down и clear'ы — warn-and-continue.
func (s *ServiceImpl) teardownOpkgTun(ctx context.Context, ndmsName, scope string) error {
	// Снять permit-all ACL v4 и v6 ДО down/delete: при успешном delete
	// auto-delete каскадит ACL и сам, но при провале delete интерфейс не должен
	// остаться с висящей привязкой. Что снимать — решает свежий running-config
	// (F606): на давно снятом ACL (reap-ретраи, сироты от версий без ACL) не
	// уходит ни одного POST. Debug, не Warn: teardown best-effort, а снос
	// интерфейса ACL всё равно уносит.
	if err := s.deps.OpkgTun.RemovePermitAllACLs(ctx, ndmsName); err != nil {
		s.appLog.Debug(scope, ndmsName, "remove permit acl: "+err.Error())
	}
	// БЕЗ предварительного down: NDMS создаёт интерфейс по ЛЮБОЙ мутации его
	// имени (стенд 2026-08-24: `{"interface":{"OpkgTunN":{"down":true}}}` на
	// отсутствующем отвечает «interface created»), а teardown штатно зовут и на
	// уже снесённом — из откатов и реап-ретраев. Рождённая так пустышка не
	// несёт нашего описания, поэтому reapOrphansByDescription её не видит, и
	// она занимает индекс НАВСЕГДА: прежний пул 0..9 вычерпывался за десяток переходов
	// до «нет свободного OpkgTun-индекса». Удаление в предварительном down не
	// нуждается — стенд-проверено на живом интерфейсе с адресом, — а на
	// отсутствующем `no:true` отвечает «unable to find» и ничего не создаёт.
	err := s.deps.OpkgTun.DeleteOpkgTun(ctx, ndmsName)
	if err == nil {
		// NDMS-запись снята — добить kernel-устройство, если оно пережило снос.
		// Устройство persist и остаётся, когда на момент удаления его держал
		// открытым sing-box (обычный порядок: движок останавливают уже после
		// сноса интерфейса). NDMS про него больше не знает, а /sys — знает, и
		// аллокатор индексов (union kernel+NDMS) считает номер занятым НАВСЕГДА:
		// стенд 2026-08-24, прежний пул 0..9 вычерпан за десяток переходов до «нет
		// свободного OpkgTun-индекса». disableFakeIPTun это уже делал у себя —
		// здесь тот же приём для откатов и реап-ретраев, которые ходят сюда.
		iface := strings.ToLower(ndmsName)
		if fakeIPLinkPresent(ctx, iface) {
			if e := fakeIPLinkDelete(ctx, s.deps.SwapGate, iface); e != nil {
				s.appLog.Warn(scope, ndmsName, "delete kernel netdev: "+e.Error())
			}
		}
		return nil
	}
	s.appLog.Warn(scope, ndmsName, "delete opkgtun: "+err.Error())
	// Down — только здесь: delete провалился, значит интерфейс СУЩЕСТВУЕТ и
	// create-on-reference не грозит; уцелевший интерфейс не должен остаться
	// поднятым. Гасим ПОСЛЕ снятия адресов — см. holdOpkgTun (F675).
	if e := s.deps.OpkgTun.ClearAddress(ctx, ndmsName); e != nil {
		s.appLog.Warn(scope, ndmsName, "clear address: "+e.Error())
	}
	if e := s.deps.OpkgTun.ClearIPv6Address(ctx, ndmsName); e != nil {
		s.appLog.Warn(scope, ndmsName, "clear ipv6 address: "+e.Error())
	}
	if e := s.deps.OpkgTun.InterfaceDown(ctx, ndmsName); e != nil {
		s.appLog.Warn(scope, ndmsName, "iface down: "+e.Error())
	}
	return err
}
