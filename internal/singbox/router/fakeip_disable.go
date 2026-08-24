package router

import (
	"context"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// fakeIPLinkPresent reports whether the kernel netdev <iface> still exists after
// the NDMS-level DeleteOpkgTun. NDMS normally tears the kernel device down too,
// but a half-removed teardown can leave a DOWN orphan opkgtunN behind that would
// collide with the index allocator on the next Enable. `ip link show dev <iface>`
// exits non-zero when the device is absent → we treat any error as "absent" (no
// delete attempted). Seam var for tests. Mirrors fakeIPAddrFlush's sysexec seam.
var fakeIPLinkPresent = func(ctx context.Context, iface string) bool {
	_, err := sysexec.Run(ctx, ipBinary, "link", "show", "dev", iface)
	return err == nil
}

// fakeIPLinkDelete removes a lingering kernel netdev (`ip link delete <iface>`).
// Seam var for tests. Mirrors fakeIPAddrFlush's sysexec seam.
var fakeIPLinkDelete = func(ctx context.Context, iface string) error {
	_, err := sysexec.Run(ctx, ipBinary, "link", "delete", iface)
	return err
}

// fakeIPDrainComment labels the temporary fail-closed reject route installed
// during fakeip-tun teardown so it is recognizable in NDMS running-config and
// can be removed by the async drain.
const fakeIPDrainComment = "awgm fakeip drain"

// fakeIPDrainWindow is how long the v4 reject route stays up after the auto-route
// is removed. During this window any client still holding a fakeip address is
// DROPPED, not routed to WAN (spec §5 leak). Removed off the lock (Disable holds
// s.mu; a blocking sleep there would stall everything).
//
// This is a COARSE drain window (NOT lease-sized): a client still caching a
// fakeip address (minted off tunDNS) after the window would leak once the reject
// route is removed. The proper fix is to force a DHCP renew on disable so clients
// re-resolve off the router's default DNS — roadmap. Until then 120s is a
// conservative best-effort. Kept a package var so tests stay override-able.
var fakeIPDrainWindow = 120 * time.Second

// fakeIPScheduleDrain runs removeReject after the drain window, OFF the s.mu lock.
// Seam var so tests can capture/run the closure synchronously without sleeping.
var fakeIPScheduleDrain = func(removeReject func()) {
	go func() {
		time.Sleep(fakeIPDrainWindow)
		removeReject()
	}()
}

// disableFakeIPTun tears down the fakeip-tun path with leak-safe ordering and a
// fail-closed drain. Called with s.mu held by Disable.
//
// Drain model (stand-verified, Fix 2): the reject route is NOT a separate
// interface-less blackhole — that form is rejected by NDMS ("no input"). It is a
// reject FLAG renewed ONTO the existing pool→OpkgTun route (same
// network+mask+interface, reject:true), which NDMS UPDATES in place. Semantics
// (stand-verified): reject is UNCONDITIONAL once set — the route shows
// rejecting:true / flags:'!' and drops pool traffic regardless of iface up/down
// (NOT a "reject-only-when-down" kill-switch). That's exactly why we add it ONLY
// at teardown (the live pool route is plain auto, no reject): from the renew
// onward, any client still holding a cached fakeip address is REJECTED, not
// leaked to WAN. There is only ONE route for that prefix+iface, so we do NOT
// remove an auto-route separately — we renew it to reject (fail-closed), delete
// the iface, and the async drain removes the (lingering, still-rejecting) route
// LAST, after the window. Stand-verified: DeleteOpkgTun does NOT cascade-remove
// the reject route — it survives the iface deletion still rejecting, which keeps
// the pool fail-closed for the whole drain window.
//
// Safe ordering (each step is leak-conscious; the inline (N) markers below match
// these numbers):
//  1. nothing provisioned → just persist Enabled=false (idempotent).
//  2. RENEW the v4 pool route with reject:true ON the OpkgTun interface — this
//     upgrades the existing route to a fail-closed kill-switch (no separate
//     auto-route removal; the route always exists until step 7).
//  3. v6 drain (remove the pool v6 route), 4. stop sing-box, then delete the
//     iface (the reject route now fail-closes the pool), 5. clear persist,
//  6. persist disabled.
//  7. schedule the reject-route removal AFTER the drain window, off-lock.
//
// Asymmetry vs Enable (which rolls back on the first error): Disable PUSHES
// THROUGH on best-effort step errors (log + continue). A half-removed fakeip is
// worse than a fully-attempted teardown, so the teardown steps never abort; only
// the persist and the drain schedule are mandatory.
//
// v6 asymmetry (FAIL-OPEN, honest): the v6 route form (StaticRouteSpec.V6)
// carries no reject flag, so v6 gets NO explicit reject route — its drain is the
// pool-route removal alone. On a dual-stack router that has a v6 WAN default
// route (::/0), removing the pool's more-specific v6 route does NOT drop
// fakeip-v6 packets — they fall through to ::/0 via WAN and LEAK. The v6 drain is
// therefore currently fail-open. Closing it needs a v6 reject/blackhole route,
// which requires extending the v6 route form to support reject — see the
// TODO(fakeip-v6-drain) marker below. Only v4 gets a real fail-closed reject
// route today.
func (s *ServiceImpl) disableFakeIPTun(ctx context.Context, settings *storage.Settings) error {
	st, _ := opkgTunOwned(settings, stateFakeIPTun)

	// Nothing provisioned (or persist already cleared) → idempotent: just persist
	// the disabled flag and emit. No NDMS teardown to do.
	if st == nil || !st.Provisioned {
		// Mutate a private copy: `settings` aliases the store's live cache,
		// which other goroutines read without a lock.
		cp := *settings
		settings = &cp
		settings.SingboxRouter.Enabled = false
		if err := s.deps.Settings.Save(settings); err != nil {
			return err
		}
		s.emitStatus(ctx)
		return nil
	}

	iface := tunIfaceName(st.Index)   // kernel name: log labels only here
	ndmsName := tunNDMSName(st.Index) // NDMS RCI name: reject-renew + iface delete

	// Derive the v4 pool network + dotted mask (Masked, mirroring Enable) for both
	// the reject route and the auto-route removal. If the persisted range is
	// malformed we cannot build the v4 routes; log and skip them (the rest of the
	// teardown — stop sing-box, delete iface, clear persist — still runs).
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

	// (2) RENEW the v4 pool route with reject:true ON the OpkgTun interface (Fix 2,
	// stand-verified). NDMS renews the existing pool→OpkgTun route in place, adding
	// the reject flag — this turns the pool route into a fail-closed kill-switch:
	// while the iface is up it routes, once we delete the iface below it REJECTS.
	// Same network+mask+interface as the Enable pool route, so this UPDATES it
	// rather than adding a second route (NDMS rejects two routes for one
	// prefix+iface). We do NOT remove the auto-route — there is only ONE route for
	// this prefix+iface and the async drain (step 7) removes it LAST.
	//
	// rejectRenewed gates the async drain schedule (step 7): if the renew FAILS the
	// route stays a plain (non-reject) pool route — still present, so the pool is
	// not leaked between here and iface delete (packets dead-end at the about-to-be-
	// deleted tun). We then leave it for the startup sweep / a later reconcile
	// rather than removing it here.
	rejectRenewed := false
	if haveV4 {
		if err := s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
			Network:   poolNet4,
			Mask:      poolMask4,
			Interface: ndmsName,
			Reject:    true,
			Comment:   fakeIPDrainComment,
		}); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "renew pool route as reject kill-switch FAILED — pool NOT fail-closed (plain route still present, no WAN leak): "+err.Error())
		} else {
			rejectRenewed = true
		}
	}

	// (3) v6 drain: remove the pool v6 route (see the FAIL-OPEN note above — on a
	// dual-stack router with a v6 default it does NOT drop, it leaks). Best-effort.
	// v4 needs NO auto-route removal here — step 2 renewed the single pool route in
	// place; the async drain (step 7) removes it after the window.
	// TODO(fakeip-v6-drain): v6 is fail-open on dual-stack routers with a v6 default
	// route (no reject equivalent). Closing it needs the v6 route form to support a
	// reject/blackhole route (ndms work + stand verification) — not done in v1.
	if haveV6 {
		if err := s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
			V6: true, Network: inet6Range, Interface: ndmsName,
		}); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "remove pool route v6: "+err.Error())
		}
	}

	// (3b) Снять ingress-заворот (issue #678) ДО остановки движка и удаления
	// интерфейса: иначе клиенты ingress-серверов остались бы с default в
	// мёртвый tun и перехватом DNS на несуществующий адрес. Идемпотентно.
	if s.deps.IPTables != nil {
		s.deps.IPTables.RemoveFakeIPIngress(ctx)
	}

	// (4) Stop sing-box (move 21-fakeip.json under disabled/). Legacy (no orch):
	// skip — there is no in-place inbound to strip for fakeip-tun. Best-effort.
	if s.deps.Orch != nil {
		if err := s.deps.Orch.SetEnabled(orchestrator.SlotFakeIP, false); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "disable slot: "+err.Error())
		}
		// Композиты слота 21 пропали из merged-конфига — device-proxy
		// перегенерирует слот 30 до ближайшего reload (issue #465).
		s.notifyRoutingSlotsChanged()
	}

	// (4b) Tear the iface down (down → delete; on delete failure — clear the
	// configured addresses, see teardownOpkgTun) — NDMS name. With the pool
	// route renewed to reject (step 2), deleting the iface fail-closes the
	// pool: the reject flag now drops any client still on a fakeip address.
	// Best-effort: a failed delete is retried by the periodic reap scan.
	//
	// Гейт общий с (4c): доказанно чужой интерфейс на нашем индексе не сносим
	// НИ на уровне NDMS, ни добивающим `ip link delete` — пропустив первое и
	// выполнив второе, мы убили бы посторонний туннель наполовину. Цена гейта
	// честная: kernel-сироту без NDMS-объекта скан тоже не видит, поэтому её
	// уборка здесь пропускается (индекс не течёт — аллокатор live-sourced).
	if !s.skipForeignTeardown(ctx, ndmsName, fakeIPTunDescription, "fakeip-disable") {
		// (4c) Уборку осиротевшего kernel-netdev делает сам teardownOpkgTun —
		// он же нужен откатам и реап-ретраям, которые ходят туда напрямую.
		_ = s.teardownOpkgTun(ctx, ndmsName, "fakeip-disable")
	}

	// Remove specific CIDR routes on disable. After fakeip is off these
	// destinations correctly fall back to the normal WAN exit (direct); unlike
	// the synthetic pool they need no reject. Explicit per-CIDR removal — the
	// async pool-drain removes only the pool prefix by net/mask, never these.
	// Best-effort, logged. Must run while the config is still loadable.
	if cfg, cerr := s.loadFakeIPConfig(); cerr == nil {
		cfg = s.ruleSetMaterializer().restoreConfig(cfg)
		dV4, dV6 := desiredTunCIDRs(cfg)
		for _, c := range dV4 {
			if e := s.removeCIDRRoute(ctx, ndmsName, c, false); e != nil {
				s.appLog.Warn("fakeip-disable", iface, "remove cidr route "+c+": "+e.Error())
			}
		}
		for _, c := range dV6 {
			if e := s.removeCIDRRoute(ctx, ndmsName, c, true); e != nil {
				s.appLog.Warn("fakeip-disable", iface, "remove cidr route v6 "+c+": "+e.Error())
			}
		}
	}

	// (5) Clear the ownership record — MANDATORY (push through even if a step
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

	// (6) Persist disabled — MANDATORY. This is the durable on/off truth.
	// Mutate a private copy: `settings` still aliases the store's live cache,
	// which other goroutines read without a lock. Copying here (and not right
	// after Load) keeps what the narrow mutators wrote into the cache above —
	// the copy is exactly what the old aliased Save would have persisted.
	cp := *settings
	settings = &cp
	settings.SingboxRouter.Enabled = false
	if err := s.deps.Settings.Save(settings); err != nil {
		return err
	}

	// (7) Schedule removal of the (now reject) pool route AFTER the drain window,
	// OFF the lock (Disable holds s.mu; a blocking sleep here would stall
	// everything). This is the LAST removal — the route fail-closes the pool until
	// then. Use a background context (the request ctx may be cancelled when Disable
	// returns). The closure touches NO s.mu-protected state: it only calls NDMS, so
	// it cannot deadlock on the lock the parent still holds. Only scheduled when the
	// reject renew SUCCEEDED (rejectRenewed) — a failed renew left a plain pool
	// route, and the startup sweep (ReapOrphanedFakeIPTun) is the safety net for any
	// stale route that does linger.
	if haveV4 && rejectRenewed {
		s.scheduleFakeIPDrain(poolNet4, poolMask4, ndmsName)
	}

	s.emitStatus(ctx)
	return nil
}

// scheduleFakeIPDrain schedules removal of the v4 fail-closed reject route (the
// renewed pool→OpkgTun route) after the drain window. ndmsName is the OpkgTun NDMS
// interface the route is bound to — required to address it for removal (the kill-
// switch is iface-bound, not an interface-less blackhole). Split out so the
// closure captures only plain strings + the service (NDMS dep) — never lock-held
// state.
func (s *ServiceImpl) scheduleFakeIPDrain(poolNet4, poolMask4, ndmsName string) {
	fakeIPScheduleDrain(func() {
		// Background ctx: the Disable request ctx is likely cancelled by now.
		if err := s.deps.StaticRoutes.RemoveStaticRoute(context.Background(), StaticRouteSpec{
			Network: poolNet4, Mask: poolMask4, Interface: ndmsName, Comment: fakeIPDrainComment,
		}); err != nil {
			s.appLog.Warn("fakeip-disable", ndmsName, "remove drain reject route: "+err.Error())
		}
	})
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
	if err := s.deps.OpkgTun.RemovePermitAllACL(ctx, ndmsName); err != nil {
		s.appLog.Debug(scope, ndmsName, "remove permit acl: "+err.Error())
	}
	// v6-список — отдельная сущность NDMS, каскадом от v4 не снимается. Debug:
	// у интерфейса без v6 его и не было, «not found» тут норма.
	if err := s.deps.OpkgTun.RemovePermitAllACLv6(ctx, ndmsName); err != nil {
		s.appLog.Debug(scope, ndmsName, "remove permit acl v6: "+err.Error())
	}
	// Гейт существования — обязателен: дальше идут down/clear, а NDMS создаёт
	// интерфейс по ЛЮБОЙ мутации его имени (см. teardownOpkgTun). Здесь, в
	// отличие от teardown, за ними НЕТ delete, который бы такую пустышку
	// подобрал: hold интерфейс намеренно сохраняет. Пустышка же не несёт
	// нашего описания, реап по описанию её не видит, и индекс занят навсегда.
	// Вызывающий зовёт hold только при подтверждённом владении, но подтверждение
	// даёт скан по описанию, а он «не знаю» трактует как «наш» (fail-closed для
	// reuse) — то есть на недоступном скане сюда можно прийти и без интерфейса.
	if !fakeIPLinkPresent(ctx, strings.ToLower(ndmsName)) {
		s.appLog.Debug(scope, ndmsName, "hold: интерфейса нет, мутации пропущены")
		return nil
	}
	if err := s.deps.OpkgTun.InterfaceDown(ctx, ndmsName); err != nil {
		s.appLog.Warn(scope, ndmsName, "iface down: "+err.Error())
	}
	err := s.deps.OpkgTun.ClearAddress(ctx, ndmsName)
	if err != nil {
		s.appLog.Warn(scope, ndmsName, "clear address: "+err.Error())
	}
	// Снимаем в любом случае: провал v4 не повод оставить v6 висеть.
	if e := s.deps.OpkgTun.ClearIPv6Address(ctx, ndmsName); e != nil {
		s.appLog.Debug(scope, ndmsName, "clear ipv6 address: "+e.Error())
	}
	return err
}

// teardownOpkgTun best-effort сносит NDMS OpkgTun: down → delete; при провале
// delete снимает сконфигурированные v4/v6 адреса. Единственное место, где живёт
// инвариант: интерфейс с настроенным `ip address`, но без kernel-адреса вгоняет
// ndm в бесконечный nginx-reload цикл (bind fail → регенерация конфига →
// reload → …), подвешивающий весь RCI на секунды (stand-verified 2026-07-15) —
// поэтому провал delete ОБЯЗАН оставлять интерфейс без адресов. Clear'ы идут
// ПОСЛЕ провала delete: на happy-path они были бы лишними RCI-вызовами, а на
// уже исчезнувшем интерфейсе delete идемпотентно успешен и clear'ы (с их
// create-on-reference риском в NDMS) не выполняются вовсе. Возвращает ошибку
// delete; down и clear'ы — warn-and-continue.
func (s *ServiceImpl) teardownOpkgTun(ctx context.Context, ndmsName, scope string) error {
	// Снять permit-all ACL (unbind + no access-list) ДО down/delete: при
	// успешном delete auto-delete каскадит ACL и сам, но при провале delete
	// интерфейс не должен остаться с висящей привязкой. Debug, не Warn:
	// «not found» на давно снятом ACL — норма для reap-ретраев (каждый тик
	// до успеха delete) и сирот от версий без ACL (ревью).
	if err := s.deps.OpkgTun.RemovePermitAllACL(ctx, ndmsName); err != nil {
		s.appLog.Debug(scope, ndmsName, "remove permit acl: "+err.Error())
	}
	// v6-список — отдельная сущность NDMS, каскадом от v4 не снимается. Debug:
	// у интерфейса без v6 его и не было, «not found» тут норма.
	if err := s.deps.OpkgTun.RemovePermitAllACLv6(ctx, ndmsName); err != nil {
		s.appLog.Debug(scope, ndmsName, "remove permit acl v6: "+err.Error())
	}
	// БЕЗ предварительного down: NDMS создаёт интерфейс по ЛЮБОЙ мутации его
	// имени (стенд 2026-08-24: `{"interface":{"OpkgTunN":{"down":true}}}` на
	// отсутствующем отвечает «interface created»), а teardown штатно зовут и на
	// уже снесённом — из откатов и реап-ретраев. Рождённая так пустышка не
	// несёт нашего описания, поэтому reapOrphansByDescription её не видит, и
	// она занимает индекс НАВСЕГДА: пул 0..9 вычерпывался за десяток переходов
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
		// стенд 2026-08-24, пул 0..9 вычерпан за десяток переходов до «нет
		// свободного OpkgTun-индекса». disableFakeIPTun это уже делал у себя —
		// здесь тот же приём для откатов и реап-ретраев, которые ходят сюда.
		iface := strings.ToLower(ndmsName)
		if fakeIPLinkPresent(ctx, iface) {
			if e := fakeIPLinkDelete(ctx, iface); e != nil {
				s.appLog.Warn(scope, ndmsName, "delete kernel netdev: "+e.Error())
			}
		}
		return nil
	}
	s.appLog.Warn(scope, ndmsName, "delete opkgtun: "+err.Error())
	// Down — только здесь: delete провалился, значит интерфейс СУЩЕСТВУЕТ и
	// create-on-reference не грозит, а погасить его перед снятием адресов надо.
	if e := s.deps.OpkgTun.InterfaceDown(ctx, ndmsName); e != nil {
		s.appLog.Warn(scope, ndmsName, "iface down: "+e.Error())
	}
	if e := s.deps.OpkgTun.ClearAddress(ctx, ndmsName); e != nil {
		s.appLog.Warn(scope, ndmsName, "clear address: "+e.Error())
	}
	if e := s.deps.OpkgTun.ClearIPv6Address(ctx, ndmsName); e != nil {
		s.appLog.Warn(scope, ndmsName, "clear ipv6 address: "+e.Error())
	}
	return err
}
