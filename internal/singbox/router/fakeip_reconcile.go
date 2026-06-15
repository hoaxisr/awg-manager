package router

import (
	"bufio"
	"context"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/env"
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
//     per step (mirrors reconcileInstalled). Restart a dead sing-box, re-add the
//     pool routes idempotently, re-advertise the health-gated DNS, and run the
//     source-preservation assert. Never re-allocates an index or re-creates the
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
	// A probe error is treated as "nothing live" → re-provision via Enable,
	// whose guard re-checks liveness; never silently skip a heal on an error.
	var live map[int]bool
	if s.deps.OpkgTunIndices != nil {
		live, _ = s.deps.OpkgTunIndices.LiveOpkgTunIndices(ctx)
	}

	if st == nil || !st.Provisioned || !live[st.Index] {
		// Not provisioned, or the iface vanished (crash / manual removal) →
		// (re-)provision. Enable's idempotency guard short-circuits the
		// already-provisioned+live case, so this is safe to call unconditionally.
		return s.Enable(ctx)
	}

	// ---- DRIFT-HEAL (provisioned + live) ---------------------------------
	// Best-effort: each step logs + continues so one drifted resource cannot
	// abort the heal of the others. NEVER re-allocate an index or re-create the
	// iface here — that is Enable's job, gated on the liveness check above.
	iface := fakeIPIfaceName(st.Index)

	// Restart a dead sing-box. The idempotency guard skips this; the drift-heal
	// MUST do it or a crashed process stays down until the next Enable. Bounded
	// wait: log on timeout, don't hard-fail a reconcile.
	if running, _ := s.deps.Singbox.IsRunning(); !running {
		if s.deps.Orch != nil {
			if e := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, true); e != nil {
				s.appLog.Warn("fakeip-reconcile", iface, "restart sing-box (orch): "+e.Error())
			}
		} else if e := s.deps.Singbox.Start(); e != nil {
			s.appLog.Warn("fakeip-reconcile", iface, "restart sing-box (legacy): "+e.Error())
		}
		bootWait := env.DurationDefault("AWG_SINGBOX_BOOT_WAIT", 60*time.Second)
		if e := s.waitForSingbox(ctx, bootWait); e != nil {
			s.appLog.Warn("fakeip-reconcile", iface, "sing-box not ready after restart: "+e.Error())
		}
	}

	// Re-add the pool routes idempotently (NDMS auto:true is idempotent). Derive
	// net/mask from the persisted ranges exactly as Enable does (Masked first).
	if s.deps.StaticRoutes != nil {
		if prefix, perr := netip.ParsePrefix(st.Inet4Range); perr == nil {
			if poolNet4, poolMask4, derr := splitCIDRToAddrMask(prefix.Masked().String()); derr == nil {
				if e := s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
					Network: poolNet4, Mask: poolMask4, Interface: iface, Comment: fakeIPPoolRouteComment,
				}); e != nil {
					s.appLog.Warn("fakeip-reconcile", iface, "re-add pool route v4: "+e.Error())
				}
			} else {
				s.appLog.Warn("fakeip-reconcile", iface, "derive pool v4 mask: "+derr.Error())
			}
		} else if st.Inet4Range != "" {
			s.appLog.Warn("fakeip-reconcile", iface, "parse pool v4 range: "+perr.Error())
		}
		if st.Inet6Range != "" {
			if e := s.deps.StaticRoutes.AddStaticRoute6(ctx, st.Inet6Range, iface); e != nil {
				s.appLog.Warn("fakeip-reconcile", iface, "re-add pool route v6: "+e.Error())
			}
		}
	}

	// Re-advertise the health-gated DNS. advertiseDNSIfHealthy clears the pool
	// DNS when sing-box is down / egress dead, so a still-broken path correctly
	// reverts clients to the router's default DNS (no black-hole).
	if cfg, e := s.loadRouterConfig(); e != nil {
		s.appLog.Warn("fakeip-reconcile", iface, "load router config: "+e.Error())
	} else if tunDNS, de := DeriveTunDNS(s.deps.FakeIPTun.TunAddr4); de != nil {
		s.appLog.Warn("fakeip-reconcile", iface, "derive tun dns: "+de.Error())
	} else if ae := s.advertiseDNSIfHealthy(ctx, s.deps.FakeIPTun.DHCPPool, tunDNS, iface, cfg); ae != nil {
		s.appLog.Warn("fakeip-reconcile", iface, "re-advertise dns: "+ae.Error())
	}

	// Source-preservation assert (Task 15): advisory only — stores the verdict
	// for GetStatus, never fails the reconcile.
	tunOwnAddr := ""
	if a, _, err := splitCIDRToAddrMask(s.deps.FakeIPTun.TunAddr4); err == nil {
		tunOwnAddr = a
	}
	s.assertSourcePreserved(iface, tunOwnAddr)

	return nil
}

// sourcePreservedProbe is the seam: inspects whether forward-client traffic into
// the tun keeps its source IP (NOT SNAT'd to the opkgtun addr). Overridable in
// tests. Returns (preserved, checked): checked=false when it cannot determine
// (no conntrack data / no fakeip flows yet) — the caller treats unknown as
// "don't warn".
var sourcePreservedProbe = liveSourcePreservedProbe

// liveSourcePreservedProbe parses /proc/net/nf_conntrack to decide whether a
// forward-client flow through the tun keeps its source IP. SNAT is detected when
// a flow's REPLY-tuple dst equals the tun's own address (tunOwnAddr) — i.e. the
// original src was rewritten to the tun address → preserved=false, checked=true.
// A clean opkgtun-bound flow whose src is unchanged → preserved=true,
// checked=true. No relevant flow → checked=false. Fail-closed to checked=false
// on any read/parse error so a missing/exotic conntrack format never false-alarms.
//
// STAND-GATE (1F.1): conntrack line semantics (field order, dual-stack lines,
// the exact "involves the tun" signal) are stand-dependent — the real verdict
// must be asserted on the live router. Unit tests exercise the seam, not this
// body.
func liveSourcePreservedProbe(iface, tunOwnAddr string) (preserved bool, checked bool) {
	if tunOwnAddr == "" {
		return false, false
	}
	f, err := os.Open("/proc/net/nf_conntrack")
	if err != nil {
		return false, false // fail-closed: don't warn on a read error
	}
	defer f.Close()

	tunAddr, perr := netip.ParseAddr(tunOwnAddr)
	if perr != nil {
		return false, false
	}

	sc := bufio.NewScanner(f)
	sawTunFlow := false
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// Split a conntrack line into its original tuple (before the second
		// "src=") and the reply tuple. The reply dst= reveals SNAT: if the reply
		// is destined to the tun's own address, the original src was rewritten.
		var origSrc, origDst, replyDst string
		srcSeen := 0
		for _, fld := range fields {
			switch {
			case strings.HasPrefix(fld, "src="):
				srcSeen++
				if srcSeen == 1 {
					origSrc = strings.TrimPrefix(fld, "src=")
				}
			case strings.HasPrefix(fld, "dst="):
				if srcSeen == 1 && origDst == "" {
					origDst = strings.TrimPrefix(fld, "dst=")
				} else if srcSeen >= 2 {
					replyDst = strings.TrimPrefix(fld, "dst=")
				}
			}
		}
		if origSrc == "" {
			continue
		}
		// Reply dst == tun's own address → original src was SNAT'd to the tun.
		if replyDst != "" {
			if ra, e := netip.ParseAddr(replyDst); e == nil && ra == tunAddr {
				return false, true // SNAT detected — definitive
			}
		}
		// Track whether we saw any flow whose dst is in the tun's network as a
		// signal that fakeip flows exist at all (so checked can be true with a
		// clean, non-SNAT'd flow).
		if origDst != "" {
			if da, e := netip.ParseAddr(origDst); e == nil && da == tunAddr {
				sawTunFlow = true
			}
		}
	}
	if sawTunFlow {
		return true, true
	}
	return false, false // no relevant flow → unknown, don't warn
}

// assertSourcePreserved runs the source-preservation seam and, on a definitive
// SNAT verdict (checked && !preserved), logs a WARNING. It always stores the
// verdict so GetStatus can surface it: a SNAT verdict stores false; a clean
// verdict stores true; an "unknown" (checked==false) stores nil (no warning, no
// false alarm). Best-effort / advisory — NEVER returns an error and NEVER fails
// the reconcile.
func (s *ServiceImpl) assertSourcePreserved(iface, tunOwnAddr string) {
	preserved, checked := sourcePreservedProbe(iface, tunOwnAddr)
	if !checked {
		s.storeSourcePreserved(nil)
		return
	}
	if !preserved {
		s.appLog.Warn("fakeip-source", iface, "forward-client source IP is being SNAT'd to the tun address — per-device targeting and source rules will misbehave (set the client segment to a no-masquerade / static-NAT mode)")
	}
	v := preserved
	s.storeSourcePreserved(&v)
}

// storeSourcePreserved records the source-preservation verdict under the small
// mutex shared with the GetStatus reader.
func (s *ServiceImpl) storeSourcePreserved(v *bool) {
	s.fakeIPSrcMu.Lock()
	s.fakeIPSourcePreserved = v
	s.fakeIPSrcMu.Unlock()
}

// loadSourcePreserved returns the last stored source-preservation verdict.
func (s *ServiceImpl) loadSourcePreserved() *bool {
	s.fakeIPSrcMu.Lock()
	defer s.fakeIPSrcMu.Unlock()
	return s.fakeIPSourcePreserved
}
