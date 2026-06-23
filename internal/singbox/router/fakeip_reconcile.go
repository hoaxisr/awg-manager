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
	if !sr.Enabled {
		return s.Disable(ctx)
	}

	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	st := settings.FakeIP

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

	// Restart a dead sing-box. The idempotency guard skips this; the drift-heal
	// MUST do it or a crashed process stays down until the next Enable. Bounded
	// wait: log on timeout, don't hard-fail a reconcile.
	if running, _ := s.deps.Singbox.IsRunning(); !running {
		// Ensure the slot file is enabled (idempotent). NB: SetEnabled is a
		// no-op when the slot is already enabled (orchestrator.go), so it can
		// NOT revive a process that died with the slot on — we must start the
		// process directly below.
		if s.deps.Orch != nil {
			if e := s.deps.Orch.SetEnabled(orchestrator.SlotFakeIP, true); e != nil {
				s.appLog.Warn("fakeip-reconcile", iface, "enable slot: "+e.Error())
			}
		}
		// Start the dead process directly (real spawn). This is the actual
		// recovery — gated on !running above, so it never double-starts.
		if e := s.deps.Singbox.Start(); e != nil {
			s.appLog.Warn("fakeip-reconcile", iface, "restart sing-box: "+e.Error())
		}
		if e := s.waitForSingbox(ctx, bootWaitWithFloor()); e != nil {
			s.appLog.Warn("fakeip-reconcile", iface, "sing-box not ready after restart: "+e.Error())
		}
	}

	// Re-add the pool routes ONLY on real drift (Fix B1): probe the v4 pool route
	// with the same fakeIPPoolRoutePresent seam GetStatus uses; an AddStaticRoute
	// fires only when the route is ABSENT. In steady state the route is present, so
	// this produces ZERO route POSTs per tick. Derive net/mask from the persisted
	// ranges exactly as Enable does (Masked first).
	if s.deps.StaticRoutes != nil {
		if prefix, perr := netip.ParsePrefix(st.Inet4Range); perr == nil {
			if poolNet4, poolMask4, derr := poolV4NetMask(st.Inet4Range); derr == nil {
				// Probe v4 presence (same seam GetStatus uses); only re-add when absent.
				if !fakeIPPoolRoutePresent(iface, prefix.Masked()) {
					if e := s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
						Network: poolNet4, Mask: poolMask4, Interface: ndmsName, Comment: fakeIPPoolRouteComment,
					}); e != nil {
						s.appLog.Warn("fakeip-reconcile", iface, "re-add pool route v4: "+e.Error())
					}
					// v6 re-add is gated on the SAME v4-absence signal: routes are added
					// together at Enable, so v4-present ⇒ v6-present is a sound v1
					// heuristic (a dedicated v6 presence probe against /proc/net/ipv6_route
					// is a follow-up). When v4 was present we skip v6 too → zero POSTs.
					if st.Inet6Range != "" {
						if e := s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
							V6: true, Network: st.Inet6Range, Interface: ndmsName,
						}); e != nil {
							s.appLog.Warn("fakeip-reconcile", iface, "re-add pool route v6: "+e.Error())
						}
					}
				}
			} else {
				s.appLog.Warn("fakeip-reconcile", iface, "derive pool v4 mask: "+derr.Error())
			}
		} else if st.Inet4Range != "" {
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

	return nil
}
