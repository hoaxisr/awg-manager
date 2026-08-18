package router

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// reconcileFakeIPTun is the fakeip-tun arm of Reconcile (called from the
// dispatch at the top of Reconcile). fakeip-tun installs no iptables, so the
// tproxy switch's installed-check is meaningless here; this routine drives the
// fakeip path by its own liveness signals.
//
// It closes the gap left by enableFakeIPTun's idempotency guard: that guard is
// a pure no-op when already-provisioned + live, so it neither restarts a dead
// sing-box nor heals drifted routes/DNS. This routine does that DRIFT-HEAL.
//
// Decision tree:
//   - !Enabled                       → Disable (dispatches to disableFakeIPTun).
//   - Enabled, not-provisioned/gone   → Enable (re-provision; Enable's guard
//     handles the already-provisioned case, allocateFakeIPIndex reuses a freed
//     index so there is no leak).
//   - Enabled, provisioned + live     → DRIFT-HEAL: best-effort, log + continue
//     per step (mirrors reconcileInstalled). Restart a dead sing-box and re-add
//     the pool routes idempotently. Never re-allocates an index or re-creates the
//     iface, and never hard-fails the reconcile on a single drifted step.
func (s *ServiceImpl) reconcileFakeIPTun(ctx context.Context, sr storage.SingboxRouterSettings) error {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	st, _ := opkgTunOwned(settings, stateFakeIPTun)

	if !sr.Enabled {
		// Teardown нужен только когда что-то реально поднято. Безусловный
		// Disable здесь писал ложное «выключение движка» в журнал на каждом
		// boot-reconcile при выключенном fakeip (ревью #523) — журнал терял
		// диагностическую ценность, ради которой запись добавлялась.
		provisioned := st != nil && st.Provisioned
		slotActive := s.deps.Orch != nil && s.routerSlotEnabled()
		if !provisioned && !slotActive {
			return nil
		}
		return s.Disable(ctx)
	}

	// LiveOpkgTunIndices probes which opkgtun ifaces actually exist on the box.
	// Capture the error (Fix B4): a TRANSIENT probe failure (NDMS glitch mid-reload)
	// must NOT be read as "the iface is gone" — that would trigger a full Enable
	// re-provision on every flaky tick. Mirror tproxy's "probe error → unknown →
	// don't do the heavy thing": only treat the iface as gone when the probe
	// SUCCEEDED and the index is absent. On a probe error we fall through to the
	// idempotent, best-effort drift-heal, which no-ops harmlessly if the iface
	// really is gone (AddStaticRoute/restart just log on failure).
	var live map[int]bool
	var probeErr error
	if s.deps.OpkgTunIndices != nil {
		live, probeErr = s.deps.OpkgTunIndices.LiveOpkgTunIndices(ctx)
	}

	reprovision := st == nil || !st.Provisioned || (probeErr == nil && !live[st.Index])
	if reprovision {
		// Not provisioned, or the iface vanished (crash / manual removal) →
		// (re-)provision. Enable's idempotency guard short-circuits the
		// already-provisioned+live case, so this is safe to call unconditionally.
		// Drift-heal, NOT user-initiated: must honour a prior master-Stop, so do
		// not clear the sticky intent (clearManualStop=false).
		return s.enableLocked(ctx, false)
	}

	// ---- DRIFT-HEAL (provisioned + live) ---------------------------------
	// Best-effort: each step logs + continues so one drifted resource cannot
	// abort the heal of the others. NEVER re-allocate an index or re-create the
	// iface here — that is Enable's job, gated on the liveness check above.
	iface := fakeIPIfaceName(st.Index)   // kernel name: /proc route probe, log labels
	ndmsName := fakeIPNDMSName(st.Index) // NDMS RCI name: static-route Interface

	// Запаркованный слот 21 — дрейф НЕЗАВИСИМО от жизни процесса (ревью #523):
	// раньше слот чинился только при мёртвом sing-box, а при живом (крутит
	// подписки/device-proxy) merged-конфиг оставался без tun-in навсегда —
	// enableFakeIPTun no-op'ится на provisioned+live и слот не трогает.
	// Рестарт мёртвого процесса — по-прежнему только watchdog (Operator.
	// Reconcile); fail-closed при мёртвом движке присущ fakeip: маршруты пула
	// указывают в OpkgTun без читателя, трафик дропается, не утекает.
	// SetEnabled — только при фактически запаркованном слоте, иначе каждый
	// тик взводил бы debounced reload.
	if s.deps.Orch != nil {
		if st, ok := s.slotSnapshot(orchestrator.SlotFakeIP); !ok || !st.Enabled {
			if e := s.deps.Orch.SetEnabled(orchestrator.SlotFakeIP, true); e != nil {
				s.appLog.Warn("fakeip-reconcile", iface, "enable slot: "+e.Error())
			} else {
				s.appLog.Info("fakeip-reconcile", iface,
					"слот 21-fakeip был запаркован — возвращён в конфиг (drift-heal)")
				// Слот вернулся в merged-конфиг — device-proxy должен
				// восстановить композитные ссылки (ветка reprovision покрыта
				// через enableLocked, эта — нет).
				s.notifyRoutingSlotsChanged()
				// По той же причине здесь и примирение base: владелец
				// dns.strategy сменился мимо enableLocked/Disable.
				s.reconcileBaseDNSStrategy()
			}
		}
	}

	// One-shot (до первого УСПЕХА) ассерт permit-ACL: покрывает апгрейд
	// awg-manager поверх уже включённого fakeip (ACL появился в этой версии)
	// и удаление списка до старта демона. Идемпотентно (дубль permit NDMS
	// отклоняет без дублирования); флаг взводится только ПОСЛЕ успеха —
	// провал (медленный RCI на буте) ретраится следующим тиком (ревью).
	// Гейт probeErr == nil: живость интерфейса подтверждена — иначе permit
	// создал бы список, bind упал бы, и осиротевший unreferenced-список
	// (auto-delete не взведён) навсегда сохранился бы в конфиг. Флаг под
	// transitionMu (Reconcile). Галку _WEBADMIN_, снятую пользователем в
	// веб-морде при живом процессе, НЕ переустанавливаем намеренно — она
	// видна в UI как правило firewall, и её снятие — решение пользователя.
	if !s.fakeipACLAsserted && s.deps.OpkgTun != nil && probeErr == nil {
		if nerr := s.deps.OpkgTun.SetPermitAllACL(ctx, ndmsName); nerr != nil {
			s.appLog.Warn("fakeip-reconcile", iface, "permit acl: "+nerr.Error())
		} else {
			s.fakeipACLAsserted = true
		}
	}
	// v6-разрешение — отдельная сущность NDMS и свой флаг: успех v4 не должен
	// гасить ретрай упавшего v6. Гейт по адресу: без v6 разрешать нечего.
	if !s.fakeipACLv6Asserted && s.deps.OpkgTun != nil && probeErr == nil &&
		resolveFakeIPParams(s.deps.FakeIPTun, sr).TunAddr6 != "" {
		if nerr := s.deps.OpkgTun.SetPermitAllACLv6(ctx, ndmsName); nerr != nil {
			s.appLog.Warn("fakeip-reconcile", iface, "permit acl v6: "+nerr.Error())
		} else {
			s.fakeipACLv6Asserted = true
		}
	}

	// Re-add the pool routes ONLY on real drift (Fix B1): probe the v4 pool route
	// with the same fakeIPPoolRoutePresent seam GetStatus uses; an AddStaticRoute
	// fires only when the route is ABSENT. In steady state the route is present, so
	// this produces ZERO route POSTs per tick. Derive net/mask from the persisted
	// ranges exactly as Enable does (Masked first).
	var inet4Range, inet6Range string
	if st.FakeIP != nil {
		inet4Range, inet6Range = st.FakeIP.Inet4Range, st.FakeIP.Inet6Range
	}
	if s.deps.StaticRoutes != nil {
		if prefix, perr := netip.ParsePrefix(inet4Range); perr == nil {
			if poolNet4, poolMask4, derr := poolV4NetMask(inet4Range); derr == nil {
				// Probe v4 presence (same seam GetStatus uses); only re-add when absent.
				if !fakeIPPoolRoutePresent(iface, prefix.Masked()) {
					if e := s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
						Network: poolNet4, Mask: poolMask4, Interface: ndmsName, Comment: fakeIPPoolRouteComment,
					}); e != nil {
						s.appLog.Warn("fakeip-reconcile", iface, "re-add pool route v4: "+e.Error())
					} else {
						// Успешный drift-heal: маршрут пула пропадал (утечка
						// трафика мимо туннеля) и был восстановлен — событие,
						// а не рутинная проверка.
						s.appLog.Info("fakeip-reconcile", iface, "pool route v4 was absent, re-added (drift-heal)")
					}
					// v6 re-add is gated on the SAME v4-absence signal: routes are added
					// together at Enable, so v4-present ⇒ v6-present is a sound v1
					// heuristic (a dedicated v6 presence probe against /proc/net/ipv6_route
					// is a follow-up). When v4 was present we skip v6 too → zero POSTs.
					if inet6Range != "" {
						if e := s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
							V6: true, Network: inet6Range, Interface: ndmsName,
						}); e != nil {
							s.appLog.Warn("fakeip-reconcile", iface, "re-add pool route v6: "+e.Error())
						} else {
							s.appLog.Info("fakeip-reconcile", iface, "pool route v6 was absent, re-added (drift-heal)")
						}
					}
				}
			} else {
				s.appLog.Warn("fakeip-reconcile", iface, "derive pool v4 mask: "+derr.Error())
			}
		} else if inet4Range != "" {
			s.appLog.Warn("fakeip-reconcile", iface, "parse pool v4 range: "+perr.Error())
		}
	}

	// CIDR drift-heal (Tier-1 + Tier-2) shares a SINGLE config load+restore per tick.
	// Both tiers consume the same materialized config; loading it once avoids 2 disk
	// reads + 2 materializer passes per 30s tick (design §6 reconcile-cost). On a
	// load error BOTH tiers skip (best-effort, as before).
	if s.deps.StaticRoutes != nil {
		if cfg, cerr := s.loadFakeIPConfig(); cerr == nil {
			cfg = s.ruleSetMaterializer().restoreConfig(cfg)

			// Tier 1: re-assert specific CIDR routes (drift-heal, defense-in-depth).
			// Routes are NDMS-native and durable across reload; this backstops manual
			// removal / crash. Probe presence with the same seam the pool uses → zero
			// POSTs in steady state.
			dV4, dV6 := desiredTunCIDRs(cfg)
			for _, c := range dV4 {
				if pfx, perr := netip.ParsePrefix(c); perr == nil && !fakeIPPoolRoutePresent(iface, pfx.Masked()) {
					if e := s.addCIDRRoute(ctx, ndmsName, c, false); e != nil {
						s.appLog.Warn("fakeip-reconcile", iface, "re-add cidr route "+c+": "+e.Error())
					} else {
						s.appLog.Info("fakeip-reconcile", iface, "cidr route "+c+" was absent, re-added (drift-heal)")
					}
				}
			}
			// v6 CIDR routes are gated on a real v6 route-present probe (against
			// /proc/net/ipv6_route), exactly like the v4 loop. This re-adds only when
			// the route is ABSENT (zero steady-state POSTs) AND self-heals a v6-only
			// config (one with v6 CIDRs but no v4) — the old v4-drift heuristic never
			// gave such a config a heal signal.
			for _, c := range dV6 {
				if pfx, perr := netip.ParsePrefix(c); perr == nil && !fakeIPPoolRoute6Present(iface, pfx.Masked()) {
					if e := s.addCIDRRoute(ctx, ndmsName, c, true); e != nil {
						s.appLog.Warn("fakeip-reconcile", iface, "re-add cidr route v6 "+c+": "+e.Error())
					} else {
						s.appLog.Info("fakeip-reconcile", iface, "cidr route v6 "+c+" was absent, re-added (drift-heal)")
					}
				}
			}

			// Tier 2: remote rule-set CIDRs (network + decompile) — only reachable in the
			// periodic reconcile (the .srs may not be downloaded at edit time, so the
			// edit-time Tier-1 diff cannot see them). Best-effort: add any remote CIDR not
			// yet present. No removal here — Tier-1 diff-on-mutation owns removals; a remote
			// set merely contributes additional desired routes.
			rV4, rV6 := s.remoteTunCIDRs(ctx, cfg)
			if len(rV4) > 0 || len(rV6) > 0 {
				s.appLog.Info("fakeip-cidr-remote", ndmsName, fmt.Sprintf("remote cidrs: v4=%d v6=%d", len(rV4), len(rV6)))
			}
			for _, c := range rV4 {
				if pfx, perr := netip.ParsePrefix(c); perr == nil && !fakeIPPoolRoutePresent(iface, pfx.Masked()) {
					if e := s.addCIDRRoute(ctx, ndmsName, c, false); e != nil {
						s.appLog.Warn("fakeip-reconcile", iface, "add remote cidr "+c+": "+e.Error())
					}
				}
			}
			// Remote v6 is gated on the same v6 route-present probe as Tier-1 v6 —
			// per-CIDR, re-add only when absent. This closes the prior limitation that
			// a remote set with v6 CIDRs but no v4 never self-healed its v6 routes.
			for _, c := range rV6 {
				if pfx, perr := netip.ParsePrefix(c); perr == nil && !fakeIPPoolRoute6Present(iface, pfx.Masked()) {
					if e := s.addCIDRRoute(ctx, ndmsName, c, true); e != nil {
						s.appLog.Warn("fakeip-reconcile", iface, "add remote cidr v6 "+c+": "+e.Error())
					}
				}
			}
		}
	}

	// Ingress-заворот (issue #678): и drift-heal после сброса firewall NDMS, и
	// применение смены состава ingress-интерфейсов — UpdateSettings завершается
	// Reconcile'ом, поэтому галка у сервера отрабатывает сразу, а не через тик.
	s.ensureFakeIPIngress(ctx, s.fakeIPIngressSpecFor(ctx, st, sr))

	return nil
}
