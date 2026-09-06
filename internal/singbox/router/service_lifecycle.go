package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox/heavyop"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// xtDscpUsable reports whether iptables `-m dscp` matching is usable,
// honouring the test seam (Deps.XtDscpProbe) when set. Availability changes
// are logged on TRANSITIONS only (available↔unavailable), never per call —
// the reconcile loop hits this every tick and a missing optional module must
// not spam two Warn lines per tick forever (negative probe results are
// additionally TTL-cached inside IsXtDscpAvailable).
func (s *ServiceImpl) xtDscpUsable(ctx context.Context) bool {
	ok := false
	if s.deps.XtDscpProbe != nil {
		ok = s.deps.XtDscpProbe(ctx)
	} else {
		ok = IsXtDscpAvailable(ctx)
	}
	state := int32(2)
	if ok {
		state = 1
	}
	if prev := s.xtDscpState.Swap(state); prev != state {
		switch {
		case !ok:
			s.appLog.Warn("qos-dscp", "", "xt_dscp недоступен — классы QoS пропущены (см. статус xtDscpAvailable)")
		case prev == 2:
			s.appLog.Info("qos-dscp", "", "xt_dscp снова доступен — классы QoS будут применены")
		}
	}
	return ok
}

// prepareNetfilter runs the common netfilter preflight: xt_TPROXY module
// load and TPROXY target availability check. It is shared by Enable and
// reconcileInstalled so both paths run identical validation. Tests can
// override it via deps.NetfilterPreflight to avoid real syscalls.
func (s *ServiceImpl) prepareNetfilter(ctx context.Context) error {
	if s.deps.NetfilterPreflight != nil {
		return s.deps.NetfilterPreflight(ctx)
	}

	if err := EnsureTProxyModule(ctx); err != nil {
		return err
	}

	if !tproxyTargetProbe(ctx) {
		return fmt.Errorf("iptables TPROXY target unavailable — kernel module loaded but iptables extension missing")
	}

	// Best-effort preload of all remaining router netfilter modules.
	// TPROXY is already handled above as fatal; the rest are soft.
	// This mirrors the full matcher/target set bisect-combo.sh warms up:
	// xt_comment, xt_mark, xt_connmark, xt_conntrack, xt_pkttype.
	if errs := EnsureRouterNetfilterModules(ctx); len(errs) > 0 {
		for _, err := range errs {
			s.appLog.Warn("ensure-netfilter", "", err.Error())
		}
	}

	return nil
}

// waitForSingbox polls until sing-box is BOTH process-alive and actually
// listening on the router inbound sockets (TCP RedirectPort + UDP
// TPROXYPort), or the deadline expires. Used by Enable after SetEnabled
// triggers the orchestrator's debounced cold-start so iptables redirects
// don't land on a TPROXY port that nothing is listening on yet.
//
// PID-alive alone is not enough (issue #354): the router config reaches
// sing-box via the orchestrator's debounced (250ms) reload, so an
// already-running process keeps serving the OLD inbound set for a moment,
// and a freshly started one binds inbounds only at the end of startup
// (after config parse + rule-set load — seconds on mipsel). Gating on the
// same socket probe GetStatus uses means the status emitted at the end of
// Enable reflects a truly active interception path instead of a transient
// «СБОЙ».
//
// Returns ctx.Err on cancellation, or a timeout error after the deadline;
// callers can treat the timeout as soft (proceed with iptables and accept
// the brief race) or hard at their discretion.
// tunIfaceName builds the KERNEL interface name for a tun-mode OpkgTun
// (fakeip-tun и policy-tun, а также реап) from its allocated index (e.g. index
// 3 → "opkgtun3"). Use this ONLY where the
// kernel sees the iface: the sing-box tun inbound interface_name, the
// "ip addr flush dev <iface>" exec, /sys/class/net/<iface>/carrier, the
// /proc/net/route iface match, and the /sys index scan. For NDMS RCI calls use
// tunNDMSName instead — NDMS rejects the lowercase kernel name.
func tunIfaceName(index int) string {
	return tunnel.NewNames("awg" + strconv.Itoa(index)).IfaceName
}

// tunNDMSName builds the NDMS RCI interface name for a tun-mode OpkgTun
// (fakeip-tun и policy-tun, а также реап) from its allocated index (e.g. index 3 → "OpkgTun3"). This mirrors
// tunnel.Names.NDMSName (CamelCase "OpkgTun%s"); the kernel name is its lowercase
// (strings.ToLower → tunIfaceName). NDMS REQUIRES this CamelCase form for every
// RCI interface op (create/delete, address/mtu, up/down) and StaticRouteSpec
// Interface — passing the lowercase kernel name yields
// "unsupported interface type: \"opkgtun\"" (stand-verified). Use tunIfaceName
// only for the kernel-facing sites (sing-box config, ip exec, /sys, /proc).
func tunNDMSName(index int) string {
	return tunnel.NewNames("awg" + strconv.Itoa(index)).NDMSName
}

// ReapOrphanedFakeIPTun removes a tun-mode OpkgTun left provisioned by a crash
// or incomplete teardown when the router is no longer in that mode — fakeip-tun
// (исторически, отсюда имя) и policy-tun: у каждого свой персист и своё
// NDMS-описание, а активный режим владеет ТОЛЬКО своим интерфейсом. It runs
// at startup (wired in cmd/awg-manager) and on every Reconcile tick — so a
// runtime orphan (failed disable delete) heals within a tick instead of waiting
// for a reboot. Safe on a tick: Reconcile holds transitionMu (excludes a live
// mode-switch mid-flip), and this function takes s.mu (excludes a concurrent
// Enable creating the iface it is about to judge). Idempotent and best-effort:
// reaps by persisted state (Index), plus a description-based scan
// (reapFakeIPOrphansByDescription) that catches OpkgTuns whose persist was
// lost — a persist-less orphan is exactly the state that triggers the ndm
// nginx-reload loop (see teardownOpkgTun), so it must not survive.
//
// It ALSO sweeps a stale v4 drain reject route for the configured pool in
// non-fakeip mode — the safety net for a disable drain interrupted by a
// restart (the async drain goroutine does not survive one) or an async-remove
// that didn't match the route (Fix 1).
//
// INVARIANT (relied on by this reap): Enable(fakeip-tun) MUST persist the index
// via SetOpkgTunState BEFORE CreateOpkgTun (and roll back its own partial work on
// failure), so persisted state is a reliable superset of live ifaces. A crash
// mid-Enable that still slips a persist-less orphan through is caught by the
// description-scan fallback.
func (s *ServiceImpl) ReapOrphanedFakeIPTun(ctx context.Context) error {
	// s.mu serialises the reap against Enable/Disable: without it the scan can
	// list a freshly created OpkgTun of a concurrent Enable while its `owned`
	// snapshot predates that Enable's persist — and delete the live iface.
	s.mu.Lock()
	defer s.mu.Unlock()

	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	sr, _ := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	// Пул берём ДЕЙСТВУЮЩИЙ, а не проводной дефолт: при кастомном пуле реап
	// иначе адресовал бы 198.18.0.0/15 и оставлял протухший reject-маршрут
	// висеть на настроенном префиксе до ручного удаления (F11).
	cfgPool4 := s.resolveFakeIPParams(sr).Inet4Range
	// Владелец — ОДНО чтение ОДНОЙ записи. owned/ownedPolicy нужны только для
	// exclusion в description-сканах своего режима: у fakeip владение гейтится
	// на Provisioned, у policy-tun НЕТ — выключение интерфейс не удаляет, а
	// удерживает вместе с индексом (holdOpkgTun), и удержанный интерфейс наш.
	// Владение отменяет смена режима (персист-реап ниже).
	st := settings.OpkgTun
	owned, ownedPolicy := "", ""
	if st != nil {
		switch st.Mode {
		case storage.OpkgTunModeFakeIP:
			if st.Provisioned {
				owned = tunNDMSName(st.Index)
			}
		case storage.OpkgTunModePolicyTun:
			ownedPolicy = tunNDMSName(st.Index)
		}
	}

	// Description-scan fallback: remove persist-less fakeip orphans in EVERY
	// mode. The currently-persisted iface is excluded — in fakeip-tun mode the
	// active Enable/Reconcile own it, in other modes the persist-based reap
	// below handles it with its persist-clearing semantics. Best-effort.
	s.reapOrphansByDescription(ctx, fakeIPTunDescription, owned, "fakeip-reap",
		func(ctx context.Context, id string) {
			// pre-delete: пуловый (возможно reject-drain) маршрут переживает
			// удаление интерфейса — снять, пока имя сироты его адресует.
			// Best-effort с НАСТРОЕННЫМ пулом: свой пул сироты без персиста
			// неизвестен.
			if s.deps.StaticRoutes == nil {
				return
			}
			if poolNet, poolMask, derr := poolV4NetMask(cfgPool4); derr == nil {
				if err := s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
					Network: poolNet, Mask: poolMask, Interface: id, Comment: fakeIPDrainComment,
				}); err != nil {
					s.appLog.Warn("fakeip-reap", id, "remove pool route: "+err.Error())
				}
			}
		})
	s.reapOrphansByDescription(ctx, policyTunDescription, ownedPolicy, "policy-tun-reap", nil)

	// Миграционный артефакт: policy-payload на записи ЧУЖОГО режима (v34
	// перенесла NAT-записи проигравшей записи). Вне policy-tun восстановить и
	// снять payload; сама запись не трогается — её судьбу решает персист-реап
	// ниже или её собственный режим.
	if st != nil && st.Mode != storage.OpkgTunModePolicyTun && len(natSegmentsOf(st)) > 0 &&
		sr.RoutingMode != statePolicyTun {
		if err := s.restorePolicyTunNAT(ctx, natSegmentsOf(st)); err != nil {
			s.appLog.Warn("policy-tun-reap", "", "restore segment NAT (migrated payload): "+err.Error())
		} else if err := s.deps.Settings.SetOpkgTunNATSegments(nil); err != nil {
			s.appLog.Warn("policy-tun-reap", "", "clear migrated NAT payload: "+err.Error())
		}
	}

	// Персист-реап policy-tun. Идёт ДО fakeip-возврата ниже: активный режим
	// владеет ТОЛЬКО своим интерфейсом, поэтому в fakeip-tun (и в tproxy)
	// сирота policy-tun обязана сноситься. Гарды кросс-персистных коллизий
	// сняты: с единой записью «две записи на один индекс» невыразимы по
	// построению.
	if sr.RoutingMode != statePolicyTun && ownedPolicy != "" && s.deps.OpkgTun != nil {
		// releaseForeignOpkgTun: записанный NAT сегментов восстанавливается ДО
		// сноса интерфейса — тот же первый шаг, что в disablePolicyTun (записи
		// живут в персисте, который очищается только при успешном delete).
		removed, err := s.releaseForeignOpkgTun(ctx, st, "policy-tun-reap")
		if err != nil {
			// Персист остаётся — следующий тик/бут повторит.
			s.appLog.Warn("policy-tun-reap", ownedPolicy, "reap opkgtun: "+err.Error())
		} else {
			// Info — только на реальном сносе: на пропуске чужого интерфейса
			// сносить было нечего, а запись всё равно снимается (скан успешен,
			// нашего описания на имени нет → наш интерфейс исчез, запись
			// протухла). Та же форма, что в fakeip-реапе ниже.
			if removed {
				s.appLog.Info("policy-tun-reap", ownedPolicy, "removed orphaned policy-tun OpkgTun (mode != policy-tun)")
			}
			if err := s.deps.Settings.SetOpkgTunState(nil); err != nil {
				s.appLog.Warn("policy-tun-reap", ownedPolicy, "clear policy-tun persist: "+err.Error())
			}
		}
	}

	if sr.RoutingMode == "fakeip-tun" {
		return nil // active mode owns the iface; Enable/Reconcile manage it
	}

	// Safety net для ingress-заворота (issue #678): краш демона при живом
	// fakeip оставил бы DNAT DNS на адрес исчезнувшего tun — у клиентов
	// ingress-серверов DNS был бы наглухо сломан. Снимаем в НЕ-fakeip режиме;
	// EnsureFakeIPIngress с пустым spec трогает правила только если они есть.
	//
	// В policy-tun полный свип НЕДОПУСТИМ: там заворот СВОЙ (ip rule iif +
	// таблица 700), а реап идёт в Reconcile первым — свип сносил бы его на
	// каждом тике, и enable/reconcile ставили бы заново (churn) или не ставили
	// вовсе (enable no-op'ится на provisioned+live). Снимаем только ЧУЖОЙ,
	// fakeip-тег: протухший от fakeip DNAT ломал бы DNS клиентов, а правила
	// перехвата policy-tun (PolicyTunDNSTag) ставит и чинит ensure этого же
	// режима — снос их здесь дал бы churn каждые 30 секунд с окном резолвинга
	// мимо туннеля внутри каждого тика.
	if sr.RoutingMode == statePolicyTun {
		s.removeFakeIPIngressDNAT(ctx, FakeIPIngressTag)
	} else {
		s.ensureFakeIPIngress(ctx, FakeIPIngressSpec{})
	}

	// Safety net for the disable drain (Fix 1): the async drain goroutine that
	// removes the v4 reject route does NOT survive a daemon restart (no
	// persisted pending-drain). So in NON-fakeip mode best-effort remove a
	// stale drain reject route for the PERSISTED pool. Derive net/mask exactly
	// as disableFakeIPTun does (Masked → splitCIDR). NDMS no:true on a
	// non-existent route is idempotent. The reject route is a kill-switch FLAG
	// on the pool→OpkgTun route (stand-verified), so its NDMS form is
	// interface-bound and only addressable via the persisted name; persist-less
	// orphans get their route swept inside the description scan instead.
	if s.deps.StaticRoutes != nil && owned != "" {
		// Пул — из ПЕРСИСТА (как в disableFakeIPTun): drain ставился теми
		// значениями, что были на момент выключения. Фолбэк — действующие
		// настройки, когда payload пуст.
		pool4 := cfgPool4
		if st != nil && st.FakeIP != nil && st.FakeIP.Inet4Range != "" {
			pool4 = st.FakeIP.Inet4Range
		}
		if poolNet, poolMask, derr := poolV4NetMask(pool4); derr == nil {
			if err := s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
				Network: poolNet, Mask: poolMask, Interface: owned, Comment: fakeIPDrainComment,
			}); err != nil {
				s.appLog.Warn("fakeip-reap", owned, "sweep stale drain reject route: "+err.Error())
			}
		}
	}

	if owned == "" {
		return nil // nothing persisted to reap
	}
	if s.deps.OpkgTun == nil {
		// No provisioner (degraded/test): we can't confirm the iface is gone, so
		// KEEP the persist — clearing it would convert a tracked orphan into an
		// un-reapable persist-less one. The index isn't leaked: the allocator is
		// live-sourced (reads /sys + NDMS), so a still-live iface stays occupied.
		// A future boot with a real provisioner reaps it.
		return nil
	}
	// Доказанно чужой интерфейс на нашем индексе не сносим (нашего там нет);
	// запись при этом снимается как отработанная — гоняться за чужим индексом
	// каждый тик значило бы churn без шанса на успех.
	if !s.skipForeignTeardown(ctx, owned, fakeIPTunDescription, "fakeip-reap") {
		if err := s.teardownOpkgTun(ctx, owned, "fakeip-reap"); err != nil {
			// Keep the persist on failure: the next tick/boot retries the reap
			// rather than leaking the orphan forever. teardownOpkgTun has already
			// cleared the addresses, so the leftover cannot loop ndm's nginx.
			return fmt.Errorf("reap opkgtun %s: %w", owned, err)
		}
		s.appLog.Info("fakeip-reap", owned, "removed orphaned fakeip OpkgTun (mode != fakeip-tun)")
	}
	// Clear the record ONLY after a confirmed delete success (NDMS returns nil
	// even when the iface was already gone, i.e. idempotent), so the index frees.
	return s.deps.Settings.SetOpkgTunState(nil)
}

// reapOrphansByDescription удаляет persist-less OpkgTun-сироты по точному
// NDMS-описанию (см. fakeIPTunDescription/policyTunDescription — LOAD-BEARING,
// два штампа остаются двумя). owned — NDMS-имя из единой записи владения,
// исключается: им занимается активный режим либо персист-реап. preDelete —
// режимный pre-delete-хук (fakeip: снять пуловый drain-маршрут, пока имя
// сироты ещё адресует его; policy-tun: нечего снимать — nil). Best-effort;
// провалившийся teardown повторится на следующем тике/буте.
func (s *ServiceImpl) reapOrphansByDescription(ctx context.Context, description, owned, scope string, preDelete func(ctx context.Context, id string)) {
	if s.deps.OpkgTunScan == nil || s.deps.OpkgTun == nil {
		return
	}
	ids, err := s.deps.OpkgTunScan(ctx, description)
	if err != nil {
		s.appLog.Warn(scope, "", "scan opkgtuns by description: "+err.Error())
		return
	}
	for _, id := range ids {
		if id == owned {
			continue
		}
		if preDelete != nil {
			preDelete(ctx, id)
		}
		if err := s.teardownOpkgTun(ctx, id, scope); err != nil {
			continue // залогировано в teardownOpkgTun; повтор на следующем тике/буте
		}
		s.appLog.Info(scope, id, "removed persist-less orphaned OpkgTun")
	}
}

// fakeIPReadyInputs derives the inputs the fakeip-tun readiness probes need
// from loaded settings + the static FakeIPTun params: the tun iface name (from
// the allocated OpkgTun index), the tun-side DNS address (the other /30 host,
// where sing-box's DNS server listens), and the fakeip v4 pool prefix. ok is
// false when fakeip is not provisioned or any field is unparseable, so callers
// can fail-closed without a fakeip branch firing on tproxy state.
func (s *ServiceImpl) fakeIPReadyInputs() (iface, dnsAddr string, fakeipNet netip.Prefix, ok bool) {
	if s.deps.Settings == nil {
		return "", "", netip.Prefix{}, false
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return "", "", netip.Prefix{}, false
	}
	st, ok := opkgTunOwned(settings, stateFakeIPTun)
	if !ok || !st.Provisioned {
		return "", "", netip.Prefix{}, false
	}
	iface = tunIfaceName(st.Index)
	dnsAddr, err = DeriveTunDNS(s.deps.FakeIPTun.TunAddr4)
	if err != nil {
		return "", "", netip.Prefix{}, false
	}
	// Пул — из ПЕРСИСТА (им поднят живой tun), фолбэк — действующие настройки.
	// Проводной дефолт сверял бы статус с чужим префиксом (F11).
	pool4 := ""
	if st.FakeIP != nil {
		pool4 = st.FakeIP.Inet4Range
	}
	if pool4 == "" {
		sr, _ := NormalizeSingboxRouterSettings(settings.SingboxRouter)
		pool4 = s.resolveFakeIPParams(sr).Inet4Range
	}
	fakeipNet, err = netip.ParsePrefix(pool4)
	if err != nil || !fakeipNet.Addr().Is4() {
		return "", "", netip.Prefix{}, false
	}
	return iface, dnsAddr, fakeipNet, true
}

// healDetachedTunAttempts — на каких по счёту тиках подряд с нулевым carrier
// перезапускать движок. Считаем ТИКАМИ реконсиляции, а не часами: тик и так
// периодический, поэтому отдельный таймер и арифметика зазоров тут лишние.
//
// Первый такт пропущен намеренно: после любого рестарта движка (watchdog
// поднял упавший, оркестратор применил конфиг) есть окно в секунды, когда
// процесс уже жив, а стек к устройству ещё не привязан — тик, попавший в это
// окно, дал бы ЛИШНИЙ Stop+Start, ровно тот отказ, который мы чиним.
//
// После последней попытки молчим до тех пор, пока carrier не поднимется сам:
// если три перезапуска подряд не привязали стек, причина не в движке, и
// четвёртый рестарт только оборвёт соединения остальных слотов. Ждать вечно,
// но реже — не лечение, а тот же вечный цикл с растянутым шагом.
var healDetachedTunAttempts = [...]int{2, 4, 8}

// healDetachedTun чинит состояние «процесс жив, tun не прицеплен»: sing-box
// работает и отвечает, но carrier на tun нулевой — значит его стек к
// устройству не привязан. Для клиентов это худший вид отказа: дефолт уже
// припаркован на интерфейс, трафик уходит в никуда, а режим числится
// включённым. Раньше это не лечил НИКТО — Operator.Reconcile реагирует только
// на мёртвый процесс, а drift-heal режима смотрит на NDMS-ресурсы, не на
// привязку стека (стенд 2026-08-20: помогал только ручной перезапуск движка).
// Связь carrier с привязкой стека проверена на железе (стенд 2026-08-24):
// NDMS создал интерфейс — carrier 0; sing-box привязался — 1; движок убит —
// снова 0, а устройство осталось.
//
// Лечение — Reload движка: при живом tun он выполняется как Stop+Start
// (см. process.go) и пересоздаёт привязку. Через оркестратор идти нельзя —
// его skip-gate по хешу увидит неизменный конфиг и не сделает ничего.
//
// Вызывается из reconcile-тика, сериализованного transitionMu, — им же
// защищено поле tunDownStrikes.
func (s *ServiceImpl) healDetachedTun(iface, scope string, slot orchestrator.Slot) {
	if s.deps.Singbox == nil || iface == "" {
		return
	}
	// Запаркованный слот — нулевой carrier ЗАКОНОМЕРЕН: в merged-конфиге нет
	// tun-инбаунда, привязываться нечем. Перезапуск здесь ничего не чинит, а
	// возврат слота — забота вызывающего (и он идёт выше по тику). Гейт кодом,
	// а не порядком вызова: возврат слота может и провалиться.
	if s.deps.Orch != nil {
		if st, ok := s.slotSnapshot(slot); !ok || !st.Enabled {
			s.tunDownStrikes = 0
			return
		}
	}
	if running, _ := s.deps.Singbox.IsRunning(); !running {
		s.tunDownStrikes = 0 // мёртвый процесс — забота watchdog'а, не наша
		return
	}
	if tunReadyProbe(iface) {
		s.tunDownStrikes = 0
		return
	}
	s.tunDownStrikes++
	if !slices.Contains(healDetachedTunAttempts[:], s.tunDownStrikes) {
		return
	}
	// Гейт памяти heavyop: Stop+Start tun'а не должен идти параллельно с Reload
	// оркестратора (reload.go держит heavyop → o.mu). Оркестраторский `sing-box
	// check` под этим гейтом НЕ ходит (F85, замер на стенде OOM не показал —
	// принято); гейт здесь — про Reload, не про check.
	if !heavyop.Default.TryLock() {
		s.tunDownStrikes--
		return
	}
	defer heavyop.Default.Unlock()

	// Свежая проверка намерения — КАК МОЖНО ПОЗЖЕ, уже под гейтом памяти:
	// режим мог выключиться, пока мы копили такты и ждали гейт. Поднять
	// движок в выключенном режиме хуже, чем пропустить такт: лечение
	// повторится, а воскрешение придётся отменять пользователю.
	//
	// NB: прежняя редакция обосновывала эту проверку тем, что «Disable ходит
	// мимо transitionMu (признано в service.go)» — это было НЕВЕРНО и в обе
	// стороны: все вызовы Disable идут под transitionMu, а service.go прямо
	// говорит, что третий путь мимо него вернул бы гонку и потому удалён.
	// Сама проверка полезна (мы ждали гейт памяти), обоснование было ложным.
	if s.deps.Settings != nil {
		if settings, err := s.deps.Settings.Load(); err != nil || settings == nil || !settings.SingboxRouter.Enabled {
			s.tunDownStrikes = 0
			return
		}
	}

	last := s.tunDownStrikes == healDetachedTunAttempts[len(healDetachedTunAttempts)-1]
	msg := "движок жив, но tun не прицеплен (carrier=0) — перезапускаю движок"
	if last {
		msg += " (последняя попытка: дальше жду, пока carrier поднимется сам)"
	}
	s.appLog.Warn(scope, iface, msg)
	if err := s.deps.Singbox.Reload(); err != nil {
		s.appLog.Warn(scope, iface, "перезапуск движка не удался: "+err.Error())
	}
}

func (s *ServiceImpl) waitForSingbox(ctx context.Context, timeout time.Duration) error {
	if s.deps.Singbox == nil {
		return nil
	}

	// Mode-aware readiness: read the mode INTERNALLY (the signature has test
	// callers and must not change). fakeip-tun has no inbound sockets, so the
	// tproxy socket probe never turns true for it — gate instead on process +
	// tun carrier (carrier=1 = sing-box attached the gvisor tun stack, the
	// structural "config is live" signal). The live .2→fakeip DNS answer is NO
	// longer in this gate (it tripped on resolv.conf attempts:1, stand-verified
	// 2026-06-15) — it is now a best-effort confirm after readiness in
	// enableFakeIPTun. See singboxReady for the full rationale.
	tunMode := false
	if s.deps.Settings != nil {
		if settings, err := s.deps.Settings.Load(); err == nil && settings != nil {
			tunMode = usesTunInbound(settings.SingboxRouter.RoutingMode)
		}
	}

	deadline := time.Now().Add(timeout)
	start := time.Now()
	lastHeartbeat := time.Time{}
	const pollInterval = 100 * time.Millisecond
	for {
		if s.singboxReady(ctx, tunMode) {
			return nil
		}
		if fn := s.transitionReadinessProgress; fn != nil && time.Since(lastHeartbeat) >= 2*time.Second {
			elapsed := time.Since(start).Round(time.Second)
			running, _ := s.deps.Singbox.IsRunning()
			msg := fmt.Sprintf("запуск sing-box… %s", elapsed)
			if running {
				msg = fmt.Sprintf("sing-box работает, ожидаем inbounds… %s", elapsed)
			}
			fn(msg)
			lastHeartbeat = time.Now()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sing-box did not come up within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// singboxReady reports whether sing-box is up for the active mode. tproxy:
// process + both inbound sockets bound. fakeip-tun: process + tun carrier.
//
// For fakeip-tun, carrier=1 IS the structural readiness signal: it means
// sing-box created and attached the gvisor tun stack from the fakeip config —
// the analog of "inbound socket bound" for tproxy, and it is fast and reliable.
//
// The live .2→fakeip DNS probe was DEMOTED out of this hard gate (stand-verified
// 2026-06-15): the Go resolver (net.Resolver{PreferGo:true}) HONORS the router's
// /etc/resolv.conf `options timeout:1 attempts:1`, so it does a single ~1s-bounded
// attempt with no retry. In the first seconds after sing-box starts the fakeip
// round-trip to .2 is occasionally slower than that, so the probe returned false
// on every poll and waitForSingbox timed out at 60s — falsely failing Enable even
// though sing-box was fully up (carrier=1) and fakeip worked. The DNS check now
// runs ONCE as a best-effort, logged confirmation AFTER readiness (see
// enableFakeIPTun), never as a flaky gate. ctx is unused now that the live DNS
// probe is out of the gate; kept on the signature for the tproxy/test callers.
func (s *ServiceImpl) singboxReady(_ context.Context, tunMode bool) bool {
	running, _ := s.deps.Singbox.IsRunning()
	if !running {
		return false
	}
	if !tunMode {
		// HARD gate (issue #221): only the procfs socket probe proves the
		// router-slot TPROXY/REDIRECT inbounds actually bound. A healthy
		// Clash API is NOT equivalent — the process can be up and serving
		// Clash while the router inbounds failed to bind (port taken,
		// rejected hot-reload), and installing iptables in that state
		// blackholes all policy traffic including DNS:53.
		return singboxListeningProbe()
	}
	// Only iface is needed for the carrier gate; dnsAddr/fakeipNet (which the
	// demoted DNS probe used) are derived later in enableFakeIPTun for the
	// best-effort confirm.
	iface, ok := s.tunModeIface()
	if !ok {
		return false
	}
	return tunReadyProbe(iface)
}

// tunModeIface returns the kernel tun iface of the active tun-inbound mode:
// fakeip-tun reads FakeIPState (via fakeIPReadyInputs, which also validates the
// DNS/pool inputs), policy-tun reads PolicyTunState — it has no DNS inputs at
// all, so its carrier gate needs the iface alone. ok=false while the mode is not
// provisioned, so the gate fails closed instead of probing an empty name.
func (s *ServiceImpl) tunModeIface() (string, bool) {
	if s.deps.Settings == nil {
		return "", false
	}
	settings, err := s.deps.Settings.Load()
	if err != nil || settings == nil {
		return "", false
	}
	if settings.SingboxRouter.RoutingMode == statePolicyTun {
		st, ok := opkgTunOwned(settings, statePolicyTun)
		if !ok || !st.Provisioned {
			return "", false
		}
		return tunIfaceName(st.Index), true
	}
	iface, _, _, ok := s.fakeIPReadyInputs()
	return iface, ok
}

func cleanValidateError(err error) string {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "\x1b[31m", "")
	msg = strings.ReplaceAll(msg, "\x1b[0m", "")
	if idx := strings.Index(msg, "FATAL"); idx >= 0 {
		msg = msg[idx+len("FATAL"):]
	}
	msg = strings.TrimSpace(msg)
	if idx := strings.Index(msg, ": exit status"); idx > 0 {
		msg = msg[:idx]
	}
	if idx := strings.Index(msg, "decode config at "); idx >= 0 {
		tail := msg[idx+len("decode config at "):]
		if j := strings.Index(tail, ": "); j > 0 {
			tail = tail[j+2:]
		}
		msg = "конфиг недопустим: " + tail
	}
	return strings.TrimSpace(msg)
}

// Enable is the USER-INITIATED router enable (HTTP handler + SwitchRoutingMode).
// It clears the sticky master-stop intent — an explicit enable is an explicit
// intent to run sing-box, which must override a prior master-Stop — then runs
// the idempotent provisioning. Drift-heal (Reconcile / reconcileFakeIPTun) must
// NOT clear the intent (the watchdog must respect a user's manual stop and not
// resurrect the daemon on drift), so it calls enableLocked(ctx, false) instead.
func (s *ServiceImpl) Enable(ctx context.Context) error {
	return s.enableLocked(ctx, true)
}

// enableLocked provisions the router under s.mu. clearManualStop gates the
// sticky-intent clear: true for user-initiated Enable, false for drift-heal
// reuse (Reconcile / reconcileFakeIPTun) which must honour a prior master-Stop.
func (s *ServiceImpl) enableLocked(ctx context.Context, clearManualStop bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Hold на всю транзакцию: провижининг пишет слоты по нескольку раз и
	// дольше окна debounce, и без него чужой продюсер (подписки, device-proxy)
	// выстреливает reload'ом посреди — при живом tun это полный Stop+Start.
	// SwitchRoutingMode держит свой hold снаружи; счётчик вложенность терпит.
	if s.deps.Orch != nil {
		defer s.deps.Orch.HoldReloads()()
	}

	// Validate settings first — fail fast with a meaningful error before
	// attempting any kernel / iptables operations.
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	sr, err := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	if err != nil {
		return fmt.Errorf("router settings: %w", err)
	}

	// Explicit enable = explicit intent to run sing-box. Clear any sticky
	// manual-stop so the orchestrator cold-start (triggered by SetEnabled below)
	// isn't suppressed by shouldRun()=!IsManuallyStopped — otherwise Enable waits
	// the full boot window and fails with a misleading readiness timeout
	// (stand-found 2026-06-15, applies to BOTH tproxy and fakeip-tun). Gated to
	// user-initiated Enable: a drift-heal reconcile (clearManualStop=false) must
	// NOT wipe a user's master-Stop. The nil guard keeps test wirings that omit
	// Singbox working.
	if clearManualStop && s.deps.Singbox != nil {
		if err := s.deps.Singbox.ClearManualStop(); err != nil {
			return fmt.Errorf("clear manual-stop intent: %w", err)
		}
	}

	// fakeip-tun has an entirely separate provisioning path (OpkgTun + tun +
	// fakeip DNS + pool/CIDR routes) with its own rollback. The tproxy body
	// below stays byte-for-byte unchanged for RoutingMode=="tproxy".
	if sr.RoutingMode == "fakeip-tun" {
		sr.Enabled = true
		return s.enableFakeIPTun(ctx, settings, sr)
	}
	// policy-tun: тот же слот 20, но ingress — tun-инбаунд, а заворот делает
	// NDMS-политика; отдельная последовательность со своим rollback.
	if sr.RoutingMode == statePolicyTun {
		sr.Enabled = true
		return s.enablePolicyTun(ctx, settings, sr)
	}

	policyMode := sr.DeviceMode == "" || sr.DeviceMode == "policy"
	mark := ""
	if policyMode {
		if sr.PolicyName == "" {
			return ErrPolicyNotConfigured
		}
		mark, err = s.deps.Policies.GetPolicyMark(ctx, sr.PolicyName)
		if err != nil && !errors.Is(err, query.ErrPolicyMarkNotFound) {
			// Транзиентная ошибка чтения (RCI недоступен) — не «политика
			// отсутствует»: наверх уходит настоящая причина, а не ложный
			// ErrPolicyMissing (ревью #523).
			return fmt.Errorf("policy %q mark: %w", sr.PolicyName, err)
		}
		if err != nil || mark == "" {
			return fmt.Errorf("policy %q: %w", sr.PolicyName, ErrPolicyMissing)
		}
	}

	if err := s.prepareNetfilter(ctx); err != nil {
		return err
	}

	sr.Enabled = true

	cfg, err := s.loadAppliedRouterConfig()
	if err != nil {
		return err
	}
	cfg.Inbounds = ensureTProxyInbound(cfg.Inbounds, sr.UDPTimeout, sr.UDPNATMax)
	cfg.Outbounds = stripAutoManagedDirect(cfg.Outbounds)
	cfg.EnsureSystemRules(sr.SnifferEnabled)
	// Neutralize sing-box's short per-protocol UDP timeouts (QUIC/DTLS 30s,
	// STUN/DNS 10s) applied on sniff/port inference — they ignore the inbound
	// udp_timeout and drop games/VoIP early. Raise them to the effective inbound
	// value via a route-options rule. Placed after the system prefix, before user
	// rules, so it runs ahead of any final `route` action.
	cfg.EnsureUDPTimeoutRule(resolveUDPTimeout(sr.UDPTimeout))
	// QoS-by-DSCP (issue #371): per-class inbound pairs, derived from the
	// same settings snapshot the iptables spec below uses so ports/classes
	// cannot drift between the two. The managed route rules live in their
	// own slot (18-qos-routes.json) and are synced after the config write
	// below — see qos_routes.go for why they must not live in 20-router.json.
	qosClasses := activeQoSClasses(sr.QoSClasses)
	cfg.Inbounds, _ = ensureQoSInbounds(cfg.Inbounds, qosClasses, sr.UDPTimeout, sr.UDPNATMax)
	// Settings was already loaded above; revalidate here in case the
	// store is corrupted or hand-edited around a schema migration. We
	// fail Enable rather than apply a half-broken config — the user
	// sees a clean error in the UI and can fix it.
	if err := ValidateSingboxRouterSettings(sr); err != nil {
		return fmt.Errorf("router settings: %w", err)
	}
	cfg.EnsureRouteWAN(sr.WANAutoDetect, sr.WANInterface)

	// Promote SlotRouter to active FIRST so persistConfigDirect's
	// orch.Save targets the active path (it keys on the slot's enabled
	// flag). SetEnabled also triggers the orchestrator's debounced cold-
	// start — sing-box will read the active config we are about to write.
	// Legacy fallback (tests) keeps the explicit Start call.
	if s.deps.Orch != nil {
		if err := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
			return fmt.Errorf("orchestrator enable router: %w", err)
		}
	} else {
		if running, _ := s.deps.Singbox.IsRunning(); !running {
			if err := s.deps.Singbox.Start(); err != nil {
				return fmt.Errorf("sing-box start: %w", err)
			}
		}
	}
	// Direct write — no staging. Byte-equal short-circuit makes boot
	// recovery (Reconcile→Enable with iptables gone but active config
	// already on disk) a no-op write, which is what kills the phantom
	// "Несохранённые правки" banner that used to follow every reboot.
	if err := s.persistConfigDirect(ctx, cfg); err != nil {
		return err
	}
	// Managed QoS route rules → 18-qos-routes.json. Runs AFTER the router
	// slot write so outbound resolution sees the applied config; the
	// orchestratorApplyNow below covers both slots in one reload.
	if _, err := s.syncQoSRoutesSlot(ctx, qosClasses, sr); err != nil {
		return fmt.Errorf("sync qos routes slot: %w", err)
	}
	// Слот 20 снова активен — зависимые продюсеры (device-proxy) должны
	// перегенерировать свои слоты (вернуть ссылки на композиты) ДО reload.
	s.notifyRoutingSlotsChanged()
	if err := s.orchestratorApplyNow(); err != nil {
		return fmt.Errorf("orchestrator reload after enable: %w", err)
	}

	// Wait for sing-box to be listening before iptables start redirecting
	// traffic to its TPROXY/REDIRECT ports. HARD fail (issue #221): if
	// sing-box never comes up — most commonly because a slot config is
	// rejected by `sing-box check` at load time — installing the AWGM-TPROXY
	// rule still redirects DNS:53 to 127.0.0.1:<proxy_port>, where nothing
	// is listening, and the router loses DNS until the user manually stops
	// awg-manager. The earlier "brief packet-drop blip vs no routing"
	// trade-off is wrong: a failed sing-box start turns the blip into a
	// permanent outage.
	//
	// Same env-var contract as singbox.maxSingboxBootWait — clamped to a
	// 60s floor (bootWaitWithFloor). Import-cycle (integration_test in parent
	// already pulls router) blocks reusing the parent helper directly.
	bootWait := bootWaitWithFloor()
	if err := s.waitForSingbox(ctx, bootWait); err != nil {
		return fmt.Errorf("%w: waited %s (%v)", ErrSingboxNotReady, bootWait, err)
	}

	// Collect WAN IPs BEFORE Install: the router's own public-IP
	// addresses on default-route interfaces become RETURN rules in
	// AWGM-TPROXY/AWGM-REDIRECT, preventing LAN-to-router-WAN-IP
	// traffic from looping back into sing-box. A collector failure
	// is fatal — installing without the exclusions would silently
	// expose the loop edge case to users.
	wanIPs, err := s.deps.WANIPCollector.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect WAN IPs: %w", err)
	}

	// QoS iptables dispatch — graceful degradation: when xt_dscp support is
	// missing the DSCP rules are skipped (feature-off) with a warning, NEVER
	// failing Enable — otherwise a missing optional module would take down
	// the whole interception path at iptables-restore COMMIT (XKeen shipped
	// the same policy). EnsureXtDscpModule first: prepareNetfilter already
	// tried, this is the belt-and-suspenders retry closest to Install.
	qosSpecs := qosIPTablesSpecs(qosClasses)
	if len(qosSpecs) > 0 {
		if err := EnsureXtDscpModule(ctx); err != nil {
			s.appLog.Warn("ensure-xt-dscp", "", err.Error())
		}
		if !s.xtDscpUsable(ctx) {
			s.appLog.Warn("qos-dscp", "", "xt_dscp недоступен — классы QoS пропущены (см. статус xtDscpAvailable)")
			qosSpecs = nil
		}
	}

	// Набор geoip-обхода должен существовать ДО установки правил: правило
	// `-m set --match-set AWGM-BYPASS` на отсутствующий набор роняет весь
	// iptables-restore. Наполняется он асинхронно ниже.
	if len(sr.BypassGeoIPTags) > 0 {
		s.ensureBypassSetExists(ctx)
	}

	spec := s.buildTproxySpec(ctx, sr, mark, policyMode, wanIPs, qosSpecs)
	if policyMode && len(spec.LANBridges) == 0 {
		s.appLog.Warn("discover-lan-bridges", "", "no NDMS hotspot LAN bridges, DNS fallback skipped")
	}
	if err := s.deps.IPTables.Install(ctx, spec); err != nil {
		// См. F20: restore коммитит по таблицам — часть могла примениться,
		// снимок больше не соответствует железу. appliedSpec не обнуляем.
		s.netfilterStateKnown = false
		// Stop sing-box from listening on the now-orphan TPROXY port,
		// but DO NOT corrupt the persisted user config. With orchestrator
		// wired we just park the slot back under disabled/ — sing-box
		// stops seeing it on next reload, the file's content (including
		// tproxy-in) is preserved verbatim. Without the orchestrator
		// (legacy fallback) the only recourse is to strip the inbound.
		if s.deps.Orch != nil {
			_ = s.deps.Orch.SetEnabled(orchestrator.SlotRouter, false)
			// The QoS overlay references qos-* inbounds that just got
			// parked with the router slot — park it too.
			_ = s.disableQoSRoutesSlot()
			// Слот 20 снова выключен — device-proxy должен деградировать
			// ссылки на композиты до дефолт-членов до ближайшего reload.
			s.notifyRoutingSlotsChanged()
		} else {
			cfg.Inbounds = filterTProxyInbound(cfg.Inbounds)
			_ = s.persistConfigDirect(ctx, cfg)
		}
		return fmt.Errorf("iptables install: %w", err)
	}
	s.appliedSpec = &spec
	s.currentBypassGeoIPTags = sr.BypassGeoIPTags
	s.netfilterStateKnown = true
	// Правила переустановлены и на AWGM-SELECTIVE больше не ссылаются —
	// теперь набор выпиленного селектива можно снести (однократно).
	s.destroyLegacySelectiveSetOnce(ctx)

	if err := s.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter = sr
		return nil
	}); err != nil {
		return err
	}

	// После сохранения настроек: триггер читает теги из стора.
	s.TriggerBypassSetPopulate()

	s.emitStatus(ctx)
	return nil
}

func filterTProxyInbound(in []Inbound) []Inbound {
	out := make([]Inbound, 0, len(in))
	for _, i := range in {
		if i.Tag != "tproxy-in" {
			out = append(out, i)
		}
	}
	return out
}

// healTProxyInbound checks the persisted router config and brings the two
// UDP-timeout carriers to spec: the tproxy-in inbound (re-added if missing,
// udp_timeout re-applied on drift) AND the system route-options rule that
// raises sing-box's short sniff timeouts (#469). Both drift the same way —
// the user changes the setting while the engine is running (UpdateSettings →
// Reconcile lands here). The rule used to be regenerated only by Enable, so
// a changed timeout stayed stale in the config until the engine was toggled
// off/on (#554). Idempotent.
func (s *ServiceImpl) healTProxyInbound(ctx context.Context, udpTimeout string, udpNATMax int) error {
	// APPLIED config, not the effective (pending-first) view: heal writes to
	// active/, so reading a user's staged draft here would materialize the
	// draft into the live config BYPASSING ApplyDraft validation (and leave
	// the stale pending banner hanging over an already-applied config).
	cfg, err := s.loadAppliedRouterConfig()
	if err != nil {
		return err
	}
	// Cheap steady-state guard: both carriers already at the desired timeout →
	// skip the marshal/write entirely (this runs on every reconcile tick).
	// Listen тоже в guard'е (#689): после обновления рестарт демона не трогает
	// ни sing-box, ни iptables, и это ЕДИНСТВЕННЫЙ путь, который доведёт
	// listen 0.0.0.0 → 127.0.0.1 на живом конфиге без ручного передёргивания.
	effective := resolveUDPTimeout(udpTimeout)
	inboundOK := false
	for _, in := range cfg.Inbounds {
		if in.Tag == "tproxy-in" {
			// UDPNATMax тоже в guard'е: смена только udpNatMax в настройках должна
			// доехать до живого движка через этот же путь, без Disable/Enable.
			inboundOK = in.UDPTimeout == effective && in.Listen == tproxyListen && in.UDPNATMax == udpNATMax
			break
		}
	}
	ruleOK := false
	for _, r := range cfg.Route.Rules {
		if isSystemUDPTimeoutRule(r) {
			ruleOK = r.UDPTimeout == effective
			break
		}
	}
	if inboundOK && ruleOK {
		return nil
	}
	cfg.Inbounds = ensureTProxyInbound(cfg.Inbounds, udpTimeout, udpNATMax)
	cfg.EnsureUDPTimeoutRule(effective)
	// System self-heal — direct write, no staging UI.
	return s.persistConfigDirect(ctx, cfg)
}

// heal1140SlotMigration re-persists the applied config of the given slot
// unchanged, so materializeConfig's byte-for-byte round trip repairs a slot
// written before the sing-box 1.14 migration (download_detour, gso,
// endpoint_independent_nat) without needing a version marker. Best-effort — a
// load or persist failure here must not abort the rest of the caller's
// reconcile. Three call sites, two slots: reconcileInstalled (tproxy) and
// reconcilePolicyTun target SlotRouter (20-router.json); reconcileFakeIPTun
// targets SlotFakeIP (21-fakeip.json).
//
// Steady state is ONE file read and a JSON unmarshal into a two-field shadow
// struct, not a load+persist: persistSlotDirect's byte-compare guard runs
// AFTER materializeConfig, and materializeRuleSet has no unchanged-guard of
// its own — every inline rule set would fork `sing-box rule-set compile` and
// rename a fresh .json/.srs into config.d/rule-sets/inline/ on EVERY
// reconcile tick (30s) forever, even though the byte-compare then finds the
// slot unchanged and skips the write+SIGHUP. Materialization (and the
// persist that follows it) runs only the first tick after upgrade, while the
// slot has no route.default_http_client yet — applyHTTPClients sets that
// field on every materialize, so its presence in the ALREADY MATERIALIZED
// on-disk bytes is the migration marker. Cannot gate on the config returned
// by loadAppliedRouterConfig/restoreConfig instead: restoreConfig clears
// DefaultHTTPClient as part of projecting back to the stored form, so that
// signal is invisible past the raw bytes.
//
// Load/persist are per-slot: SlotRouter goes through loadAppliedRouterConfig
// + persistConfigDirect. SlotFakeIP deliberately does NOT use loadFakeIPConfig
// (it wraps Orch.LoadEffective, which prefers pending/ over active/) — this
// runs from reconcile self-heal, an enforcement path that LoadApplied's own
// doc comment says must never act on a draft the user has not applied yet.
// So SlotFakeIP reads Orch.LoadApplied directly (mirrors the existing
// active/disabled-only read in referencedRuleSetArtifactBases) and persists
// via persistFakeIPConfig.
func (s *ServiceImpl) heal1140SlotMigration(ctx context.Context, slot orchestrator.Slot) {
	if s.deps.Orch != nil {
		activePath, err := s.deps.Orch.ActivePath(slot)
		if err != nil {
			s.appLog.Warn("heal-1140-slot", "", err.Error())
			return
		}
		raw, err := os.ReadFile(activePath)
		if err != nil {
			if os.IsNotExist(err) {
				// Слот запаркован (или движок мёртв и его ещё не поднимали) —
				// мигрировать нечего.
				return
			}
			s.appLog.Warn("heal-1140-slot", "", err.Error())
			return
		}
		var shadow struct {
			Route struct {
				DefaultHTTPClient string `json:"default_http_client"`
				RuleSet           []struct {
					HTTPClient json.RawMessage `json:"http_client"`
				} `json:"rule_set"`
			} `json:"route"`
			HTTPClients []struct {
				Detour string `json:"detour"`
			} `json:"http_clients"`
		}
		if err := json.Unmarshal(raw, &shadow); err != nil {
			s.appLog.Warn("heal-1140-slot", "", err.Error())
			return
		}
		// Старый (неверный) applyHTTPClients писал явный detour на пустой
		// direct-outbound ("direct" — базовый тег, без загрузки outbound'ов),
		// который sing-box 1.14 отвергает при старте: "detour to an empty
		// direct outbound makes no sense" (стенд-находка). Такой слот уже
		// несёт default_http_client и гейтом выше был бы принят за healthy —
		// проверяем этот признак отдельно, чтобы всё равно перематериализовать.
		brokenEmptyDirectDetour := false
		for _, hc := range shadow.HTTPClients {
			if hc.Detour == "direct" {
				brokenEmptyDirectDetour = true
				break
			}
		}
		if !brokenEmptyDirectDetour {
			for _, rs := range shadow.Route.RuleSet {
				var obj struct {
					Detour string `json:"detour"`
				}
				if err := json.Unmarshal(rs.HTTPClient, &obj); err == nil && obj.Detour == "direct" {
					brokenEmptyDirectDetour = true
					break
				}
			}
		}
		if shadow.Route.DefaultHTTPClient != "" && !brokenEmptyDirectDetour {
			// Уже в форме 1.14, без бага пустого direct — материализация и
			// запись не нужны.
			return
		}
	}

	var (
		cfg *RouterConfig
		err error
	)
	switch slot {
	case orchestrator.SlotFakeIP:
		var data []byte
		if s.deps.Orch != nil {
			data, err = s.deps.Orch.LoadApplied(orchestrator.SlotFakeIP)
		}
		if err == nil {
			cfg, err = parseRouterConfigBytes(data)
		}
	default:
		cfg, err = s.loadAppliedRouterConfig()
	}
	if err != nil {
		s.appLog.Warn("heal-1140-slot", "", err.Error())
		return
	}

	// Проецируем уже материализованные http_clients/http_client обратно в
	// download_detour, прежде чем повторно материализовать: иначе для слота
	// с brokenEmptyDirectDetour applyHTTPClients увидел бы пустой
	// DownloadDetour и оставил бы битый rs.HTTPClient как есть. На чистом
	// pre-1.14 слоте (DownloadDetour уже стоит, HTTPClient пуст) — no-op.
	restoreHTTPClients(cfg)

	switch slot {
	case orchestrator.SlotFakeIP:
		err = s.persistFakeIPConfig(ctx, cfg)
	default:
		err = s.persistConfigDirect(ctx, cfg)
	}
	if err != nil {
		s.appLog.Warn("heal-1140-slot", "", err.Error())
	}
}

// ensureTProxyInbound enforces the SKeen-style split: tproxy-in
// handles UDP only, redirect-in handles TCP. TPROXY for TCP relies on
// `-m socket --transparent` to deliver established-connection packets
// to sing-box's accept()ed transparent socket, but that match
// evaluates to 0 on Keenetic 4.9-ndm-5 — established TCP packets fall
// through to the listener and get RST. NAT REDIRECT sidesteps the
// problem: conntrack records the DNAT for SYN, established packets
// are auto-translated.
//
// The two inbounds bind to DIFFERENT addresses (issue #689):
//
// tproxy-in → 127.0.0.1. The TPROXY target diverts packets to the
// socket bound at --on-ip 127.0.0.1 (iptables.go), so a loopback
// listener receives every diverted packet. A 0.0.0.0 bind additionally
// accepted *normally delivered* UDP — any datagram addressed to a
// router IP on TPROXYPort landed on the wildcard socket, sing-box
// relayed it to its "destination" (the router itself) via direct, and
// the relay re-entered the same socket: a self-sustaining flow loop
// (thousands of UDP flows, CPU pegged in softirq).
//
// redirect-in → 0.0.0.0. NAT REDIRECT rewrites the packet destination
// to the *primary IP of the inbound interface* (e.g. 10.10.10.1 on
// br0), NOT to 127.0.0.1. A listener on 127.0.0.1 would never see
// redirected packets — kernel emits RST (96a61c77).
const (
	tproxyListen   = "127.0.0.1"
	redirectListen = "0.0.0.0"
)

// DefaultUDPTimeout is the fallback UDP session timeout when the user has not
// configured a custom value. It matches sing-box's built-in C.UDPTimeout (5m):
// fakeip's tun-in previously carried no udp_timeout and thus ran at the engine's
// 5m, so defaulting to 5m here keeps unconfigured sessions no shorter than
// before while still letting the user raise it. Shorter values dropped games /
// VoIP that go quiet mid-session.
const DefaultUDPTimeout = "5m0s"

// resolveUDPTimeout returns the effective UDP timeout string: the user value
// when non-empty, otherwise DefaultUDPTimeout.
func resolveUDPTimeout(configured string) string {
	if configured != "" {
		return configured
	}
	return DefaultUDPTimeout
}

// ensureTProxyInbound converges tproxy-in/redirect-in to the canonical shapes
// described above, applying the effective udp_timeout and udp_nat_max on
// every call (the user may have changed either since the last sync).
func ensureTProxyInbound(in []Inbound, udpTimeout string, udpNATMax int) []Inbound {
	effective := resolveUDPTimeout(udpTimeout)
	hasTProxy := false
	hasRedirect := false
	for i := range in {
		switch in[i].Tag {
		case "tproxy-in":
			hasTProxy = true
			// Force UDP-only on existing entry. Older configs had no
			// `network` field which means TCP+UDP — that's the broken
			// behaviour we're moving away from.
			if in[i].Network != "udp" {
				in[i].Network = "udp"
			}
			if !in[i].UDPFragment {
				in[i].UDPFragment = true
			}
			// Always apply the effective timeout — user may have changed it.
			in[i].UDPTimeout = effective
			in[i].UDPNATMax = udpNATMax
			// tcp_fast_open is meaningless on a UDP-only inbound.
			if in[i].TCPFastOpen {
				in[i].TCPFastOpen = false
			}
			// Strip RoutingMark — see history note below.
			if in[i].RoutingMark != 0 {
				in[i].RoutingMark = 0
			}
			if in[i].Listen != tproxyListen {
				in[i].Listen = tproxyListen
			}
		case "redirect-in":
			hasRedirect = true
			if !in[i].TCPFastOpen {
				in[i].TCPFastOpen = true
			}
			if in[i].Listen != redirectListen {
				in[i].Listen = redirectListen
			}
		}
	}
	out := in
	if !hasTProxy {
		out = append([]Inbound{{
			Type:        "tproxy",
			Tag:         "tproxy-in",
			Listen:      tproxyListen,
			ListenPort:  TPROXYPort,
			Network:     "udp",
			UDPFragment: true,
			UDPTimeout:  effective,
			UDPNATMax:   udpNATMax,
		}}, out...)
	}
	if !hasRedirect {
		out = append([]Inbound{{
			Type:        "redirect",
			Tag:         "redirect-in",
			Listen:      redirectListen,
			ListenPort:  RedirectPort,
			TCPFastOpen: true,
		}}, out...)
	}
	return out
}

func (s *ServiceImpl) emitStatus(ctx context.Context) {
	if s.deps.Events == nil {
		return
	}
	status, _ := s.GetStatus(ctx)
	s.deps.Events.Publish("singbox-router:status", status)
}

func (s *ServiceImpl) emitStagingEvent(reason string) {
	if s.deps.Bus == nil {
		return
	}
	events.PublishInvalidatedTo(s.deps.Bus, events.ResourceSingboxRouterStaging, reason)
}

func (s *ServiceImpl) emitRulesEvent() {
	if s.deps.Bus == nil {
		return
	}
	events.PublishInvalidatedTo(s.deps.Bus, events.ResourceSingboxRouterRules, "")
}

func (s *ServiceImpl) GetStatus(ctx context.Context) (Status, error) {
	settings, _ := s.deps.Settings.Load()
	sr := storage.SingboxRouterSettings{}
	if settings != nil {
		sr, _ = NormalizeSingboxRouterSettings(settings.SingboxRouter)
	}
	cfg, _ := s.loadRouterConfigForMode(sr.RoutingMode)
	if cfg == nil {
		cfg = NewEmptyConfig()
	}
	awgCount := 0
	compCount := len(cfg.CompositeOutbounds())

	policyExists := false
	policyMark := ""
	deviceCount := 0
	if sr.PolicyName != "" && s.deps.Policies != nil {
		if mark, err := s.deps.Policies.GetPolicyMark(ctx, sr.PolicyName); err == nil && mark != "" {
			policyExists = true
			policyMark = mark
		}
		if devices, err := s.deps.Policies.ListDevicesForPolicy(ctx, sr.PolicyName); err == nil {
			for _, d := range devices {
				if d.Bound {
					deviceCount++
				}
			}
		}
	}

	// policy-tun: активность и issue считаются по running-config NDMS (spec §8),
	// не по /proc/net/route — дефолт паркует NDMS, и его правда живёт там.
	// Читаем строки ОДИН раз на статус (кэш TTL, сброс — забота reconcile).
	var policyTunLines []string
	policyTunNDMSName := ""
	policyTunIfaceName := ""
	if policyTunSt, ok := opkgTunOwned(settings, statePolicyTun); sr.RoutingMode == statePolicyTun &&
		ok && policyTunSt.Provisioned {
		policyTunNDMSName = tunNDMSName(policyTunSt.Index)
		policyTunIfaceName = tunIfaceName(policyTunSt.Index)
		if s.deps.RunningConfig != nil {
			policyTunLines, _ = s.deps.RunningConfig.Lines(ctx)
		}
	}

	// One -S probe per table yields both chain existence and jump presence.
	// A probe error is treated as "unknown" — installed/jumps stay false but
	// the badge self-corrects on the next status read (no side effect here,
	// unlike the reconcile path which must not reinstall on a transient error).
	installed, jumps, _ := s.deps.IPTables.Probe(ctx)
	// Active = interception path truly live, computed per routing mode.
	var active bool
	if sr.RoutingMode == "fakeip-tun" {
		// fakeip-tun has no iptables jumps and no inbound sockets. Steady-state
		// liveness = process up + tun carrier + the fakeip pool auto-route
		// present (the honest structural check — the fakeip equivalent of
		// "TPROXY jumps present"). No live DNS query here: that would add
		// per-poll latency, and the route-presence check is enough once Enable
		// has finished wiring routes (waitForSingbox already gated on live DNS).
		running, _ := s.deps.Singbox.IsRunning()
		if iface, _, fakeipNet, ok := s.fakeIPReadyInputs(); ok {
			active = running && tunReadyProbe(iface) && fakeIPPoolRoutePresent(iface, fakeipNet)
		}
	} else if sr.RoutingMode == statePolicyTun {
		// policy-tun: перехвата netfilter нет, «работает» = процесс жив +
		// carrier tun + дефолт NDMS припаркован на нём (структурный эквивалент
		// «джампы на месте»). Installed = интерфейс провижинен: цепочки AWGM в
		// этом режиме появляются только под классы QoS и о режиме не говорят.
		installed = policyTunNDMSName != ""
		if policyTunNDMSName != "" {
			running, _ := s.deps.Singbox.IsRunning()
			if running && tunReadyProbe(policyTunIfaceName) {
				active, _ = policyTunDefaultRoutePresent(policyTunLines, policyTunNDMSName)
			}
		}
	} else {
		// tproxy: chains + PREROUTING jumps + sing-box listening on both inbound sockets.
		active = jumps && singboxListeningProbe()
	}
	// Surface the captured sing-box fatal reason only when the engine is
	// meant to be up but isn't (СБОЙ). lastError is cleared by the operator
	// on a successful (re)start, so a healthy engine reports empty.
	lastError := ""
	if sr.Enabled && !active && s.deps.Singbox != nil {
		lastError = s.deps.Singbox.LastError()
	}
	// Crash observability (#456): счётчик недавних падений, причина
	// последнего и пауза авто-перезапуска. Заполняется всегда (omitempty
	// прячет нули) — UI показывает блок и после восстановления, пока
	// падения не выйдут из окна.
	crashCount := 0
	lastCrashReason := ""
	restartSuppressedUntil := ""
	var suppressedUntil time.Time
	if s.deps.Singbox != nil {
		n, reason, until := s.deps.Singbox.CrashStats()
		crashCount = n
		lastCrashReason = reason
		if !until.IsZero() {
			suppressedUntil = until
			restartSuppressedUntil = until.Format(time.RFC3339)
		}
	}
	issues := s.computeIssues(cfg)
	// Мёртвый движок при живом перехвате (#456 FIX-B): PREROUTING-джампы
	// стоят, а процесс не работает — весь policy-трафик (включая hijacked
	// DNS:53) уходит в мёртвый порт до конца backoff-паузы. computeIssues
	// видит только конфиг, поэтому этот runtime-issue собирается здесь, где
	// уже есть probe и crash-статистика. Только tproxy: у fakeip-tun нет
	// iptables-перехвата.
	if sr.Enabled && sr.RoutingMode != "fakeip-tun" && jumps && s.deps.Singbox != nil {
		if running, _ := s.deps.Singbox.IsRunning(); !running {
			msg := "Движок остановлен, но перехват трафика активен — трафик политик не ходит."
			if !suppressedUntil.IsZero() {
				msg += fmt.Sprintf(" Автоперезапуск приостановлен до %s (падений за 10 мин: %d).",
					suppressedUntil.Local().Format("15:04"), crashCount)
			} else {
				msg += " Автоперезапуск: при следующей проверке (до 30 с)."
			}
			issues = append(issues, Issue{
				Severity: "error",
				Kind:     "engine-dead-interception",
				Message:  msg,
			})
		}
	}
	// Эксперт-редактор (90-user.json): если последний reload пропущен из-за
	// провала кросс-слот валидации по вине пользовательского слота, движок
	// продолжает работать на старом конфиге, а сам файл оркестратор
	// намеренно не чинит (prune пропускает user-слот). computeIssues видит
	// только конфиг роутера, поэтому runtime-issue собирается здесь из
	// orchestrator.LastReloadValidation — по паттерну #456.
	if s.deps.Orch != nil {
		if v := s.deps.Orch.LastReloadValidation(); v != nil {
			for _, ve := range v.Errors {
				if ve.Slot != orchestrator.SlotUser || ve.Severity == orchestrator.SeverityWarning {
					continue
				}
				var msg string
				switch {
				case strings.HasPrefix(ve.Kind, "unknown-") && ve.Tag != "":
					msg = fmt.Sprintf("Пользовательский конфиг (90-user.json) ссылается на несуществующий тег %q — правьте в редакторе конфигурации", ve.Tag)
				case ve.Tag != "":
					msg = fmt.Sprintf("Пользовательский конфиг (90-user.json): %s %q — правьте в редакторе конфигурации", ve.Kind, ve.Tag)
				default:
					msg = fmt.Sprintf("Пользовательский конфиг (90-user.json): %s — правьте в редакторе конфигурации", ve.Message)
				}
				issues = append(issues, Issue{
					Severity: "error",
					Kind:     "user-slot-validation",
					Tag:      ve.Tag,
					Message:  msg,
				})
			}
		}
	}
	// QoS-DSCP support: xtDscpAvailable is always reported (the UI keys the
	// feature's "supported" state on it). When classes are actually
	// configured but the support probe fails, additionally surface an issue
	// that distinguishes the two causes (kernel module vs iptables
	// extension) so diagnostics can tell them apart. The detailed check on
	// the failure path is served from the same TTL-bounded probe cache as
	// the availability flag, so status polling never execs iptables per poll.
	qosActive := activeQoSClasses(sr.QoSClasses)
	// A class whose outbound no longer resolves is skipped at emit time
	// (syncQoSRoutesSlot) — surface WHY the class is inert so the user can
	// re-point or disable it.
	if sr.RoutingMode != "fakeip-tun" {
		for _, c := range qosActive {
			if !s.isKnownOutboundTag(ctx, c.Outbound, cfg) {
				issues = append(issues, Issue{
					Severity: "warning",
					Kind:     "qos-outbound-missing",
					Tag:      c.Outbound,
					Message:  fmt.Sprintf("класс QoS (DSCP %d) ссылается на несуществующий outbound %q — класс не применяется", c.DSCP, c.Outbound),
				})
			}
		}
	}
	xtDscpAvailable := s.xtDscpUsable(ctx)
	if !xtDscpAvailable && sr.RoutingMode != "fakeip-tun" && len(qosActive) > 0 {
		moduleOK, matchOK := cachedXtDscpAvailability(ctx)
		var msg string
		switch {
		case !moduleOK && !matchOK:
			msg = "QoS DSCP: модуль ядра xt_dscp не найден и расширение iptables «dscp» недоступно — классы QoS не будут применены"
		case !moduleOK:
			msg = "QoS DSCP: модуль ядра xt_dscp не найден (/lib/modules) — классы QoS не будут применены"
		default:
			msg = "QoS DSCP: расширение iptables «dscp» недоступно — классы QoS не будут применены"
		}
		issues = append(issues, Issue{Severity: "warning", Kind: "qos-xt-dscp", Message: msg})
	}
	// fakeip-tun active iface: surface the provisioned kernel iface name
	// ("opkgtun<idx>") so the UI can show it in the engine-settings panel. Only
	// when in fakeip-tun mode AND actually provisioned (persisted FakeIPState);
	// empty otherwise.
	var fakeIPIface string
	fakeIPDns := ""
	fakeIPTunAddr := ""
	if fakeIPSt, ok := opkgTunOwned(settings, stateFakeIPTun); sr.RoutingMode == "fakeip-tun" &&
		ok && fakeIPSt.Provisioned {
		fakeIPIface = tunIfaceName(fakeIPSt.Index)
		if d, derr := DeriveTunDNS(s.deps.FakeIPTun.TunAddr4); derr == nil {
			fakeIPDns = d
		}
		if addr, _, aerr := splitCIDRToAddrMask(s.deps.FakeIPTun.TunAddr4); aerr == nil {
			fakeIPTunAddr = addr
		}
	}
	// policy-tun: интерфейс поднят, но не разрешён выходом целевой политики —
	// технически всё живо, а трафик клиентов в tun не заходит. Продукт ставит
	// permit сам, так что issue означает отказ RCI или правку мимо нас.
	// Без строк running-config (dep не подключён / чтение упало) issue не
	// собирается: «не знаем» ≠ «не разрешено».
	if sr.Enabled && policyTunNDMSName != "" && len(policyTunLines) > 0 &&
		!policyTunPermitted(policyTunLines, policyTunNDMSName, sr.PolicyName) {
		where := "ни в одной политике доступа"
		if sr.PolicyName != "" {
			where = "в политике " + sr.PolicyName
		}
		issues = append(issues, Issue{
			Severity: "warning",
			Kind:     issuePolicyTunUnbound,
			Message: fmt.Sprintf("%s не разрешён %s — трафик клиентов не направляется; "+
				"разрешение ставится автоматически, проверьте политику в NDMS", policyTunNDMSName, where),
		})
	}
	// policy-tun: имена интерфейса нужны пользователю ДО того, как режим станет
	// active (по ним он ищет выход в политике), поэтому гейт — Enabled+
	// Provisioned, а не Active. При Enabled=false не светим ничего.
	policyTunIface := ""
	policyTunNDMS := ""
	var policyTunSourcePreserve *bool
	if sr.RoutingMode == statePolicyTun && sr.Enabled {
		if st, ok := opkgTunOwned(settings, statePolicyTun); ok && st.Provisioned {
			policyTunIface = tunIfaceName(st.Index)
			policyTunNDMS = tunNDMSName(st.Index)
		}
		// ПРИМЕНЁННОЕ, а не желаемое: записи персиста — единственный след того,
		// что static-NAT реально доехал до роутера. Сравнение ПОСЕГМЕНТНОЕ:
		// длина не видит, что к уже применённому набору дописали сегмент, и
		// расхождение оставалось бы без подсказки до перезапуска режима.
		sp := false
		if st, ok := opkgTunOwned(settings, statePolicyTun); ok {
			sp = policyTunNATApplied(sr.PolicyTunNATSegments, natSegmentsOf(st))
		}
		policyTunSourcePreserve = &sp
	}
	cacheDBPath := ""
	if s.deps.CacheDBPath != nil {
		cacheDBPath = s.deps.CacheDBPath()
	}
	return Status{
		Enabled:                 sr.Enabled,
		Installed:               installed,
		Active:                  active,
		NetfilterAvailable:      IsNetfilterAvailable(),
		NetfilterComponentName:  "Модули ядра подсистемы сетевой фильтрации",
		TProxyTargetAvailable:   tproxyTargetProbe(ctx),
		XtDscpAvailable:         xtDscpAvailable,
		PolicyName:              sr.PolicyName,
		PolicyMark:              policyMark,
		PolicyExists:            policyExists,
		DeviceMode:              sr.DeviceMode,
		SnifferEnabled:          sr.SnifferEnabled,
		DeviceCount:             deviceCount,
		RuleCount:               len(cfg.Route.Rules),
		RuleSetCount:            len(cfg.Route.RuleSet),
		OutboundAWGCount:        awgCount,
		OutboundCompositeCount:  compCount,
		Final:                   cfg.Route.Final,
		FakeIPIface:             fakeIPIface,
		FakeIPDns:               fakeIPDns,
		FakeIPTunAddr:           fakeIPTunAddr,
		CacheDBPath:             cacheDBPath,
		PolicyTunIface:          policyTunIface,
		PolicyTunNDMSName:       policyTunNDMS,
		PolicyTunSourcePreserve: policyTunSourcePreserve,
		Issues:                  issues,
		LastError:               lastError,
		CrashCount:              crashCount,
		LastCrashReason:         lastCrashReason,
		RestartSuppressedUntil:  restartSuppressedUntil,
	}, nil
}

func (s *ServiceImpl) Disable(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Hold на всю транзакцию — по тем же причинам и с тем же порядком
	// регистрации, что в enableLocked.
	if s.deps.Orch != nil {
		defer s.deps.Orch.HoldReloads()()
	}

	// Каждый teardown — в журнал: выключение бывает не только по кнопке
	// (fail-safe при удалённой политике, drift-heal), и без записи причину
	// «тумблер сам выключился» не восстановить (issue #523).
	s.appLog.Info("disable", "", "выключение движка маршрутизации")

	// fakeip-tun teardown is an entirely separate path (no iptables; opkgtun +
	// pool/CIDR routes + a fail-closed drain). Dispatch by mode before the
	// tproxy body so the tproxy path below stays byte-for-byte unchanged for
	// RoutingMode=="tproxy".
	dispatchSettings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	// Dispatch on the RAW persisted RoutingMode, NOT the normalized value: a
	// Normalize error (corrupt/hand-edited settings) would otherwise mis-route a
	// fakeip-tun teardown into the tproxy body, orphaning the opkgtun/routes.
	// A raw string compare keeps the fakeip branch independent of normalize.
	if dispatchSettings.SingboxRouter.RoutingMode == "fakeip-tun" {
		return s.disableFakeIPTun(ctx, dispatchSettings)
	}
	// policy-tun: свой teardown (дефолт-маршруты + tun-инбаунд слота 20 +
	// opkgtun), тоже до тела tproxy и тоже по СЫРОМУ значению режима.
	if dispatchSettings.SingboxRouter.RoutingMode == statePolicyTun {
		return s.disablePolicyTun(ctx, dispatchSettings)
	}

	s.deps.IPTables.Uninstall(ctx)
	// Только ПОСЛЕ Uninstall: пока правило `--match-set` в ядре, ipset
	// откажется сносить набор («set is in use»). Безусловно, а не по
	// currentBypassGeoIPTags: после рестарта демона поле пустое, а набор и
	// дамп на диске — нет, и хук воскрешал бы их на каждой перезагрузке.
	s.teardownBypassSet(ctx)
	s.currentBypassGeoIPTags = nil
	s.appliedSpec = nil
	s.netfilterStateKnown = false
	// Uninstall already tore down the fail-closed blackhole (if any); drop its
	// snapshot so a later reconcile doesn't try to remove it again.
	s.appliedBlackhole = nil

	if s.deps.Orch != nil {
		// Move 20-router.json under disabled/ — sing-box's non-recursive
		// -C config.d does not see it after the next reload, so the
		// tproxy inbound, route rules, DNS rules and composite outbounds
		// all disappear from the merged config in one atomic rename.
		if err := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
			s.appLog.Warn("orch-disable", "", err.Error())
		}
		// Park the QoS routes overlay with it: its rules reference qos-*
		// inbound tags that only exist while 20-router.json is active.
		if err := s.disableQoSRoutesSlot(); err != nil {
			s.appLog.Warn("orch-disable", "qos-routes", err.Error())
		}
		// Композиты из 20-router.json только что пропали из merged-конфига.
		// Синхронно даём device-proxy перегенерировать слот 30 (деградация
		// до default-члена композита) — SetEnabled выше взвёл 250ms debounce,
		// и один коалесцированный reload увидит уже корректный файл вместо
		// того, чтобы prune молча вырезал vpn/vpn2 из селекторов (issue #465).
		s.notifyRoutingSlotsChanged()
	} else {
		// Legacy fallback: strip the tproxy inbound in place so
		// the running sing-box stops accepting on the TPROXY port
		// after the persistConfigDirect reload.
		cfg, err := s.loadAppliedRouterConfig()
		if err == nil && cfg != nil {
			filtered := make([]Inbound, 0, len(cfg.Inbounds))
			for _, in := range cfg.Inbounds {
				if in.Tag != "tproxy-in" {
					filtered = append(filtered, in)
				}
			}
			cfg.Inbounds = filtered
			_ = s.persistConfigDirect(ctx, cfg)
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

func (s *ServiceImpl) Reconcile(ctx context.Context) error {
	// A routing-mode switch (SwitchRoutingMode) holds transitionMu across its
	// Disable→persist→Enable sequence, during which the persisted state is
	// transiently half-flipped. Reconcile is a periodic heal — if a switch is in
	// flight, skip this tick rather than act on the in-between state and race the
	// switch's own (possibly rolling-back) Enable/Disable. TryLock: never block the
	// scheduler; just defer the heal one tick.
	if !s.transitionMu.TryLock() {
		return nil
	}
	defer s.transitionMu.Unlock()

	// Однократная зачистка наследия выпиленного селектива (файлы config.d +
	// managed-правила в применённом конфиге). Под transitionMu: пишет тот же
	// слот 20, что и смена режима.
	s.cleanupLegacySelectiveOnce(ctx)

	// Периодический reap fakeip-сирот: runtime-сирота (провал delete при
	// disable) лечится в течение тика, а не ждёт перезагрузки роутера. Дёшево
	// в steady-state: скан читает кэш InterfaceStore, NDMS-вызовы идут только
	// когда есть что реапать. transitionMu уже взят (mode-switch исключён);
	// s.mu берёт сам reap. Ошибка — не повод ронять reconcile.
	if err := s.ReapOrphanedFakeIPTun(ctx); err != nil {
		s.appLog.Warn("fakeip-reap", "", err.Error())
	}

	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	sr, err := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	if err != nil {
		return err
	}
	s.syncKeenDNSPreset(ctx, sr)
	// fakeip-tun installs NO iptables, so the tproxy switch below (keyed on
	// IPTables.IsInstalled/HasAnyInstalled) would always read "not installed"
	// and route every tick to Enable. Dispatch by mode FIRST so the tproxy
	// switch stays byte-for-byte unchanged for RoutingMode=="tproxy".
	if sr.RoutingMode == "fakeip-tun" {
		return s.reconcileFakeIPTun(ctx, sr)
	}
	// policy-tun — по той же причине: основных iptables нет, installed всегда
	// false, и switch ниже гнал бы Enable каждый тик.
	if sr.RoutingMode == statePolicyTun {
		return s.reconcilePolicyTun(ctx, sr)
	}
	installedComplete := s.deps.IPTables.IsInstalled(ctx)
	installedAny := s.deps.IPTables.HasAnyInstalled(ctx)
	// Запаркованный слот 20 при живых цепочках — тоже дрейф (issue #523):
	// rollback провального Enable паркует слот, а netfilter.d-hook
	// восстанавливает перехват из rules-файла. reconcileInstalled видел
	// installed=true, считал engineDown и вечно ждал watchdog, которому
	// нечего чинить — процесс жив, просто в конфиге нет tproxy-in. Лечится
	// полным Enable: перепромоут слота + переустановка iptables.
	//
	// Гейт на живой процесс: при мёртвом sing-box Enable потратил бы до 60с
	// на waitForSingbox и отложил бы fail-closed blackhole — вместо этого
	// идём в reconcileInstalled (DROP сразу), watchdog оживляет процесс, и
	// следующий тик при живом движке перепромоутит слот.
	engineUp := true
	if s.deps.Singbox != nil {
		engineUp, _ = s.deps.Singbox.IsRunning()
	}
	routerSlotParked := s.deps.Orch != nil && !s.routerSlotEnabled()
	switch {
	case sr.Enabled && (!installedComplete || (routerSlotParked && engineUp)):
		// Drift-heal, NOT user-initiated: must honour a prior master-Stop, so
		// do not clear the sticky intent (clearManualStop=false).
		return s.enableLocked(ctx, false)
	case !sr.Enabled && installedAny:
		return s.Disable(ctx)
	case sr.Enabled && installedComplete:
		return s.reconcileInstalled(ctx, sr)
	}
	return nil
}

// routerSlotEnabled reports whether 20-router.json currently lives in
// config.d/ AND the file exists (Present) — «включён» флаг при отсутствующем
// файле даёт тот же тупик (в конфиге нет tproxy-in), а enableLocked его
// лечит, переписав файл. Caller guarantees deps.Orch != nil. Unregistered
// slot reads as parked — Reconcile then routes to Enable, whose SetEnabled
// surfaces the real error.
func (s *ServiceImpl) routerSlotEnabled() bool {
	st, ok := s.slotSnapshot(orchestrator.SlotRouter)
	return ok && st.Enabled && st.Present
}

// slotSnapshot returns the orchestrator state of one slot. Caller guarantees
// deps.Orch != nil.
func (s *ServiceImpl) slotSnapshot(slot orchestrator.Slot) (orchestrator.SlotState, bool) {
	for _, st := range s.deps.Orch.Snapshot() {
		if st.Slot == slot {
			return st, true
		}
	}
	return orchestrator.SlotState{}, false
}

// reconcileInstalled handles the "Enabled && installed" branch:
// detect mark or WAN-IP changes and re-Install. Extracted from Reconcile
// to keep the decision tree testable without stubbing IsInstalled.
func (s *ServiceImpl) reconcileInstalled(ctx context.Context, sr storage.SingboxRouterSettings) error {
	sr, err := NormalizeSingboxRouterSettings(sr)
	if err != nil {
		return err
	}
	// Единственный рестарт-авторитет sing-box — watchdog (Operator.Reconcile,
	// свой независимый 30s-тик). Router-reconcile больше НЕ рестартит движок
	// сам: раньше это был второй независимый авторитет (#456), и вся
	// токен-машинерия backoff'а существовала только чтобы примирить гонку двух
	// рестартёров. Здесь лишь фиксируем факт смерти движка: engineDown ниже
	// (а) включит fail-closed blackhole вместо перехвата в мёртвый порт,
	// (б) погасит heal PREROUTING-джампов. Движок поднимет watchdog своим тиком;
	// следующий reconcile-тик при живом движке восстановит перехват и снимет
	// blackhole. Fail-closed держится blackhole'ом всё время, пока движок мёртв.
	//
	// «Готов» = process + оба inbound-сокета ПРИВЯЗАНЫ (singboxReady, hard-gate
	// #221), а не просто IsRunning. Процесс может быть жив, но inbound не
	// привязался (порт занят, отклонённый hot-reload) — тогда установка iptables
	// REDIRECT/TPROXY в непривязанный сокет заблэкхолила бы весь policy-трафик,
	// включая DNS:53. Поэтому up-but-unbound трактуем как engineDown: ставим
	// fail-closed blackhole и НЕ ставим реальный перехват, пока сокеты не встанут.
	engineReady := true
	if s.deps.Singbox != nil {
		engineReady = s.singboxReady(ctx, false)
	}
	engineDown := !engineReady
	policyMode := sr.DeviceMode == "" || sr.DeviceMode == "policy"
	mark := ""
	if policyMode {
		mark, err = s.deps.Policies.GetPolicyMark(ctx, sr.PolicyName)
		if err != nil && !errors.Is(err, query.ErrPolicyMarkNotFound) {
			// Транзиентная ошибка чтения метки (RCI недоступен/медленный —
			// ранняя загрузка, shutdown-гонка при перезагрузке): это НЕ
			// «политика удалена». Раньше здесь срабатывал fail-safe disable —
			// молча и без авто-восстановления гасил движок (issue #523).
			// Оставляем состояние как есть, ретрай на следующем тике.
			return fmt.Errorf("policy %q mark: %w", sr.PolicyName, err)
		}
		if err != nil || mark == "" {
			// NDMS ответил, а политики/метки нет — политика действительно
			// удалена. Fail-safe disable, no auto-recovery; причина — в журнал.
			s.appLog.Warn("reconcile", sr.PolicyName,
				"политика не найдена в NDMS — движок маршрутизации выключается (fail-safe)")
			return s.Disable(ctx)
		}
	}
	wanIPs, err := s.deps.WANIPCollector.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect WAN IPs: %w", err)
	}

	// Смена состава geoip-тегов меняет и наличие правила `--match-set`
	// (пусто ↔ непусто), и содержимое набора — переустанавливаем правила и
	// пересобираем набор ниже.
	bypassGeoTagsChanged := !slices.Equal(s.currentBypassGeoIPTags, sr.BypassGeoIPTags)

	// QoS-DSCP change detection: only the iptables-relevant projection
	// (DSCP + ports). An outbound-only change does not need an iptables
	// re-Install — the healQoSConfig call below converges the sing-box side.
	// Graceful degradation happens HERE, before change detection: when
	// xt_dscp support is missing the desired dispatch set degrades to empty
	// (feature-off; xtDscpUsable logs availability transitions) — gating
	// later would leave desired≠installed forever and re-Install on every
	// tick. When the module/extension shows up, the (TTL-bounded) probe
	// flips and qosChanged triggers one re-Install with rules.
	qosClasses := activeQoSClasses(sr.QoSClasses)
	qosSpecs := qosIPTablesSpecs(qosClasses)
	if len(qosSpecs) > 0 {
		if err := EnsureXtDscpModule(ctx); err != nil {
			s.appLog.Warn("ensure-xt-dscp", "", err.Error())
		}
		if !s.xtDscpUsable(ctx) {
			qosSpecs = nil
		}
	}
	// Применённые классы читаются прямо из снимка, как и соседние три входа
	// (forceInitialSync, specChanged, bypassGeoTagsChanged): взаимное
	// исключение путей даёт transitionMu, отдельный аксессор с замком и копией
	// создавал бы ложную асимметрию и защищал бы от вызывающего, которого нет.
	var appliedQoS []QoSClassSpec
	if s.appliedSpec != nil {
		appliedQoS = s.appliedSpec.QoSClasses
	}
	qosChanged := !slices.Equal(appliedQoS, qosSpecs)

	want := s.buildTproxySpec(ctx, sr, mark, policyMode, wanIPs, qosSpecs)
	specChanged := !equalInstalledSpec(s.appliedSpec, &want)

	// Self-heal the sing-box side BEFORE any iptables change — same safe
	// order as Enable (config → wait → Install). Installing new per-class
	// dispatch ports first would blackhole class traffic onto ports nothing
	// listens on until the debounced reload lands.
	//
	// healTProxyInbound: a previous Install rollback or upgrade hop may have
	// left 20-router.json without the tproxy-in inbound — re-add it
	// idempotently so sing-box keeps listening on TPROXYPort.
	if err := s.healTProxyInbound(ctx, sr.UDPTimeout, sr.UDPNATMax); err != nil {
		s.appLog.Warn("heal-tproxy", "", err.Error())
	}
	// healQoSConfig: per-class inbound pairs (20-router.json) + managed route
	// rules (18-qos-routes.json). Converges class add/remove/disable and
	// outbound edits applied through UpdateSettings→Reconcile, and cleans
	// stale qos-* artifacts. No-op (no write, no reload) when converged.
	qosHealed := false
	if healed, err := s.healQoSConfig(ctx, sr); err != nil {
		s.appLog.Warn("heal-qos", "", err.Error())
	} else {
		qosHealed = healed
	}
	// The heal rewrote the QoS sing-box config AND the iptables port set is
	// about to change: wait for sing-box to come back up on its inbounds
	// before Install redirects traffic to the new per-class ports. Soft
	// deadline — on timeout we proceed and accept the brief race rather
	// than blocking the reconcile loop behind a dead engine forever.
	if qosHealed && qosChanged {
		if err := s.waitForSingbox(ctx, qosReloadWait); err != nil {
			s.appLog.Warn("qos-dscp", "", fmt.Sprintf("sing-box not ready after QoS config heal: %v — installing anyway", err))
		}
	}
	// Разовая миграция слота на форму sing-box 1.14: download_detour →
	// http_client (см. applyHTTPClients), снятие удалённых полей gso и
	// endpoint_independent_nat. Установки, обновившиеся с более старой
	// версии, не переписывают 20-router.json до первой правки маршрутизации —
	// без этого шага слот годами остаётся в устаревшей форме. persistConfigDirect
	// сравнивает байты с активным файлом, поэтому в устоявшемся состоянии
	// (слот уже переписан) это бесплатно: чтение и маршалинг без записи и SIGHUP.
	// Тот же вызов есть в reconcilePolicyTun — policy-tun пишет тот же слот
	// своим путём реконсиляции.
	s.heal1140SlotMigration(ctx, orchestrator.SlotRouter)

	// After a daemon restart or upgrade the old awg-manager process died
	// with no chance to run Uninstall, so stale AWGM chains, ip rules
	// and ip routes may remain from the old process. netfilterStateKnown
	// starts false on every fresh ServiceImpl, so the very first
	// reconcileInstalled after startup always forces a full re-install
	// regardless of what IsInstalled reports.
	forceInitialSync := !s.netfilterStateKnown
	// Self-heal: chains can survive while PREROUTING jumps get wiped (NDMS
	// rebuilds PREROUTING on reconfig), leaving the engine "installed" but
	// intercepting nothing. The netfilter.d hook restores them immediately on
	// the NDMS reload; this is the slower secondary net. On a probe error treat
	// the state as unknown and DO NOT reinstall — a transient `-S` failure
	// during an NDMS reload must not trigger a needless rebuild.
	_, jumps, blackholeLive, probeErr := s.deps.IPTables.probeAll(ctx)
	jumpsMissing := probeErr == nil && !jumps
	// wantBlackhole: движок мёртв И PREROUTING-джампы снесены (NDMS перестроил
	// firewall). Раньше здесь перехват просто не восстанавливался в мёртвый порт
	// (FIX-B), НО при снесённых джампах это означало fail-OPEN: policy-трафик
	// уходил в обычный роутинг Keenetic → в WAN мимо proxy/AWG. Теперь ставим
	// явный fail-closed blackhole — DROP policy-трафика (с теми же RETURN-
	// исключениями LAN/router/WAN, что и у перехвата), чтобы гарантированно НЕ
	// течь в WAN, пока движок не вернётся. Снимается ниже, когда движок оживёт.
	wantBlackhole := jumpsMissing && engineDown
	if wantBlackhole {
		// Спек блокировки — ТОТ ЖЕ спек перехвата, а не выборка полей из него.
		// Так блокировка дропает ровно то, что уехало бы в sing-box (включая
		// пользовательские исключения), а её PREROUTING-селектор совпадает с
		// селектором перехвата по построению. Ручная проекция здесь была
		// опаснее всего: ошибка в одном поле — например MatchAll вместо
		// want.MatchAll — превращает fail-closed для членов политики в DROP
		// всего форвард-трафика роутера. Лишние для блокировки поля безвредны:
		// buildBlackholeRestoreInput их не читает, а equalBlackholeSpec
		// сравнивает по этому же рендеру.
		blackholeSpec := want
		s.mu.Lock()
		// Переустановка ровно при смене исключений: спек blackhole — проекция
		// тех же входов, что и перехват, и WAN-адрес роутера среди них. Гард
		// «уже стоит — не трогаем» заморозил бы исключения на всё время
		// простоя движка, и смена адреса до правил не доехала бы.
		// Второй рубеж: блокировку могли снести мимо NDMS (ручной `iptables -F
		// -t mangle`, сбой netfilter.d-хука) — снимок тогда врёт, а fail-closed
		// ресурс без наблюдения факта чинился бы только сменой спека или
		// возвращением движка. Живой считается цепочка ВМЕСТЕ с её
		// PREROUTING-джампом: снесённый джамп при целой цепочке — тот же
		// fail-OPEN, что и снесённая цепочка. Обе части берутся из того же
		// дампа mangle, что и джампы перехвата: лишних вызовов iptables ноль.
		firstEngage := s.appliedBlackhole == nil
		needBlackhole := !blackholeLive || !equalBlackholeSpec(s.appliedBlackhole, &blackholeSpec)
		s.mu.Unlock()
		if needBlackhole {
			// Набор обязан существовать, иначе iptables-restore blackhole'а падает
			// целиком и fail-closed не встаёт вовсе (пустой набор безопасен: он
			// просто ничего не исключает из DROP).
			if len(sr.BypassGeoIPTags) > 0 {
				s.ensureBypassSetExists(ctx)
			}
			s.mu.Lock()
			err := s.deps.IPTables.InstallBlackhole(ctx, blackholeSpec)
			if err == nil {
				s.appliedBlackhole = &blackholeSpec
			}
			s.mu.Unlock()
			switch {
			case err != nil:
				s.appLog.Warn("reconcile", "", "не удалось поставить fail-closed blackhole: "+err.Error())
			case firstEngage:
				s.appLog.Warn("reconcile", "", "движок не работает, PREROUTING jumps снесены — включён fail-closed blackhole (policy-трафик дропается, не течёт в WAN)")
			case !blackholeLive:
				s.appLog.Warn("reconcile", "", "fail-closed blackhole пропал из netfilter (цепочка или её PREROUTING-джамп) — переустановлен")
			default:
				s.appLog.Info("reconcile", "", "исключения fail-closed blackhole обновлены (сменились адреса роутера или настройки обхода)")
			}
		}
		// Реальный перехват в мёртвый порт всё равно не восстанавливаем.
		jumpsMissing = false
	}
	needsInstall := forceInitialSync || jumpsMissing || specChanged || bypassGeoTagsChanged

	// Движок не готов интерсептить (мёртв или inbound-сокеты не привязаны) —
	// НЕ ставим iptables ни по какому триггеру (#221): REDIRECT/TPROXY в
	// непривязанный сокет заблэкхолил бы весь policy-трафик, включая DNS:53.
	// Fail-closed уже держит blackhole (при снесённых джампах) или перехват в
	// мёртвый порт (при целых). Установку откладываем до готовности — следующий
	// reconcile-тик поставит iptables, когда сокеты встанут.
	if needsInstall && engineDown {
		s.appLog.Warn("reconcile", "", "движок не готов (inbound-сокеты не привязаны) — откладываем установку iptables до готовности")
		needsInstall = false
	}

	if needsInstall {
		if forceInitialSync {
			s.appLog.Info("reconcile", "", "first after daemon start — reinstalling netfilter rules")
		} else if jumpsMissing {
			s.appLog.Warn("reconcile", "", "PREROUTING jumps missing while chains present — reinstalling to restore interception")
		}

		if err := s.prepareNetfilter(ctx); err != nil {
			return err
		}

		// Набор должен существовать до правила `--match-set` (см. Enable).
		if len(sr.BypassGeoIPTags) > 0 {
			s.ensureBypassSetExists(ctx)
		}
		s.mu.Lock()
		if err := s.deps.IPTables.Install(ctx, want); err != nil {
			// См. F20: часть таблиц могла закоммититься — снимок неизвестен.
			s.netfilterStateKnown = false
			s.mu.Unlock()
			return err
		}
		s.appliedSpec = &want
		s.currentBypassGeoIPTags = sr.BypassGeoIPTags
		s.netfilterStateKnown = true
		s.mu.Unlock()

		// Правила установлены — набор селектива больше никем не занят.
		s.destroyLegacySelectiveSetOnce(ctx)
		// Наполнение — после установки правил (пустой набор = обхода нет,
		// а не сломанный перехват). forceInitialSync покрывает рестарт демона:
		// набор мог не пережить перезагрузку роутера.
		switch {
		case len(sr.BypassGeoIPTags) > 0 && (bypassGeoTagsChanged || forceInitialSync):
			s.TriggerBypassSetPopulate()
		case len(sr.BypassGeoIPTags) == 0 && (bypassGeoTagsChanged || forceInitialSync):
			// Теги сняты: правила уже переустановлены без `--match-set`,
			// набор освободился — сносим его вместе с дампом для хука.
			// forceInitialSync здесь обязателен: после рестарта демона поле
			// currentBypassGeoIPTags пустое, «изменения» не видно, и набор с
			// дампом остались бы сиротами — хук воскрешал бы их вечно.
			s.teardownBypassSet(ctx)
		}
	}

	// Снимаем fail-closed blackhole ТОЛЬКО когда движок жив И probe успешен —
	// тогда реальный перехват гарантированно на месте (steady state с целыми
	// jumps, либо только что переустановлен выше; при ошибке Install был ранний
	// return). Делаем ПОСЛЕ реального Install, чтобы не было окна утечки между
	// снятием blackhole и восстановлением перехвата. Критично: на probe-ОШИБКЕ
	// (jumpsMissing→false из-за !nil err) или мёртвом движке blackhole СОХРАНЯЕМ,
	// иначе транзиентная -S ошибка во время NDMS-reload снесла бы DROP при живой
	// утечке и удалила бы rules-файл, обездвижив и netfilter.d-хук. Идемпотентно.
	if !engineDown && probeErr == nil {
		s.mu.Lock()
		// Наблюдение факта, а не память об установке — симметрично решению
		// СТАВИТЬ. Блокировка переживает рестарт демона: её файл правил
		// реассертит DEAD-ветка netfilter.d-хука, а appliedBlackhole у нового
		// процесса пуст. На одной памяти терминальный DROP остался бы в
		// PREROUTING навсегда — fail-closed без выхода.
		if s.appliedBlackhole != nil || blackholeLive || blackholeRulesFilePresent() {
			s.deps.IPTables.RemoveBlackhole(ctx)
			s.appliedBlackhole = nil
			s.mu.Unlock()
			s.appLog.Info("reconcile", "", "движок восстановлен — fail-closed blackhole снят")
		} else {
			s.mu.Unlock()
		}
	}

	return nil
}
