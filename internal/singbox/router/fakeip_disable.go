package router

import (
	"context"
	"net/netip"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

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
// Safe ordering (each step is leak-conscious):
//  1. nothing provisioned → just persist Enabled=false (idempotent).
//  2. ClearPoolDNS FIRST — revert clients to the router's default DNS before the
//     path is torn down, so they stop resolving via the fakeip tun.
//  3. ADD the v4 reject route BEFORE removing the auto-route — fail-closed: a
//     client still using a fakeip address gets DROPPED, not leaked to WAN.
//  4. remove the pool auto-route(s) (v4 + v6). The reject route remains.
//  5. stop sing-box, 6. delete the iface, 7. clear persist, 8. persist disabled.
//  9. schedule the reject-route removal AFTER the drain window, off-lock.
//
// Asymmetry vs Enable (which rolls back on the first error): Disable PUSHES
// THROUGH on best-effort step errors (log + continue). A half-removed fakeip is
// worse than a fully-attempted teardown, so steps 2–7 never abort; only the
// persist (step 7+8) and the drain schedule (step 9) are mandatory.
//
// v6 asymmetry (FAIL-OPEN, honest): AddStaticRoute6 has no Reject param, so v6
// gets NO explicit reject route — its drain is the pool-route removal alone. On a
// dual-stack router that has a v6 WAN default route (::/0), removing the pool's
// more-specific v6 route does NOT drop fakeip-v6 packets — they fall through to
// ::/0 via WAN and LEAK. The v6 drain is therefore currently fail-open. Closing
// it needs a v6 reject/blackhole route, which requires extending the NDMS
// AddStaticRoute6 to support reject — see the TODO(fakeip-v6-drain) marker below.
// Only v4 gets a real fail-closed reject route today.
func (s *ServiceImpl) disableFakeIPTun(ctx context.Context, settings *storage.Settings) error {
	st := settings.FakeIP

	// Nothing provisioned (or persist already cleared) → idempotent: just persist
	// the disabled flag and emit. No NDMS teardown to do.
	if st == nil || !st.Provisioned {
		settings.SingboxRouter.Enabled = false
		if err := s.deps.Settings.Save(settings); err != nil {
			return err
		}
		s.emitStatus(ctx)
		return nil
	}

	iface := fakeIPIfaceName(st.Index)

	// Derive the v4 pool network + dotted mask (Masked, mirroring Enable) for both
	// the reject route and the auto-route removal. If the persisted range is
	// malformed we cannot build the v4 routes; log and skip them (the rest of the
	// teardown — stop sing-box, delete iface, clear persist — still runs).
	var poolNet4, poolMask4 string
	if prefix, perr := netip.ParsePrefix(st.Inet4Range); perr == nil {
		if n, m, derr := splitCIDRToAddrMask(prefix.Masked().String()); derr == nil {
			poolNet4, poolMask4 = n, m
		} else {
			s.appLog.Warn("fakeip-disable", iface, "derive pool v4 mask: "+derr.Error())
		}
	} else if st.Inet4Range != "" {
		s.appLog.Warn("fakeip-disable", iface, "parse pool v4 range: "+perr.Error())
	}
	haveV4 := poolNet4 != "" && poolMask4 != ""
	haveV6 := st.Inet6Range != ""

	// (2) ClearPoolDNS FIRST — revert clients to the router's default DNS before
	// the path goes down. Best-effort.
	if err := s.deps.DHCP.ClearPoolDNS(ctx, s.deps.FakeIPTun.DHCPPool); err != nil {
		s.appLog.Warn("fakeip-disable", iface, "clear pool dns: "+err.Error())
	}

	// (3) ADD the v4 reject route BEFORE removing the auto-route. This fail-closes
	// the pool so any client still on a fakeip address is DROPPED, not leaked to
	// WAN (spec §5). Best-effort but log LOUDLY: a failed reject-add means the
	// drain window is not actually fail-closed.
	//
	// rejectAdded gates the v4 auto-route removal below: removing the auto-route
	// while NO reject route is in place would leave fakeip packets with no pool
	// route AND no reject → they fall to the WAN default and LEAK (the exact spec
	// §5 condition) between here and iface delete. So on a failed reject-add we
	// SKIP removing the v4 auto-route — traffic dead-ends at the about-to-be-
	// deleted tun (dropped), never leaked.
	rejectAdded := false
	if haveV4 {
		if err := s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
			Network: poolNet4,
			Mask:    poolMask4,
			Reject:  true,
			Comment: fakeIPDrainComment,
		}); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "add drain reject route FAILED — pool NOT fail-closed, KEEPING v4 auto-route to avoid a WAN leak: "+err.Error())
		} else {
			rejectAdded = true
		}
	}

	// (4) Remove the pool auto-route(s). The reject route remains up for the drain
	// window. v6 removal is the whole v6 drain (see the FAIL-OPEN note above — on a
	// dual-stack router with a v6 default it does NOT drop, it leaks). Best-effort.
	//
	// v4 gated on rejectAdded (Fix 2): only remove the v4 auto-route once the reject
	// route is actually up, so there is never a window with neither route present.
	if haveV4 && rejectAdded {
		if err := s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
			Network: poolNet4, Mask: poolMask4, Interface: iface, Comment: fakeIPPoolRouteComment,
		}); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "remove pool route: "+err.Error())
		}
	}
	// TODO(fakeip-v6-drain): v6 is fail-open on dual-stack routers with a v6 default
	// route (no reject equivalent). Closing it needs AddStaticRoute6 to support a
	// reject/blackhole route (ndms work + stand verification) — not done in v1.
	if haveV6 {
		if err := s.deps.StaticRoutes.RemoveStaticRoute6(ctx, st.Inet6Range, iface); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "remove pool route v6: "+err.Error())
		}
	}

	// (5) Stop sing-box (move 20-router.json under disabled/). Legacy (no orch):
	// skip — there is no in-place inbound to strip for fakeip-tun. Best-effort.
	if s.deps.Orch != nil {
		if err := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "disable slot: "+err.Error())
		}
	}

	// (6) Delete the iface (down then delete). Best-effort.
	if err := s.deps.OpkgTun.InterfaceDown(ctx, iface); err != nil {
		s.appLog.Warn("fakeip-disable", iface, "iface down: "+err.Error())
	}
	if err := s.deps.OpkgTun.DeleteOpkgTun(ctx, iface); err != nil {
		s.appLog.Warn("fakeip-disable", iface, "delete opkgtun: "+err.Error())
	}

	// (7) Clear the fakeip persist — MANDATORY (push through even if a step above
	// errored). A stale persist would make the startup reap chase a gone iface.
	if err := s.deps.Settings.SetFakeIPState(nil); err != nil {
		s.appLog.Warn("fakeip-disable", iface, "clear fakeip persist: "+err.Error())
	}

	// (8) Persist disabled — MANDATORY. This is the durable on/off truth.
	settings.SingboxRouter.Enabled = false
	if err := s.deps.Settings.Save(settings); err != nil {
		return err
	}

	// (9) Schedule the reject-route removal AFTER the drain window, OFF the lock
	// (Disable holds s.mu; a blocking sleep here would stall everything). Use a
	// background context — the request ctx may be cancelled when Disable returns.
	// The closure touches NO s.mu-protected state: it only calls NDMS, so it
	// cannot deadlock on the lock the parent still holds. Only scheduled when the
	// reject route was actually added (rejectAdded) — a failed reject-add left no
	// route to remove, and the startup sweep (ReapOrphanedFakeIPTun) is the safety
	// net for any stale reject route that does linger.
	if haveV4 && rejectAdded {
		net4, mask4 := poolNet4, poolMask4
		s.scheduleFakeIPDrain(net4, mask4, iface)
	}

	s.emitStatus(ctx)
	return nil
}

// scheduleFakeIPDrain schedules removal of the v4 fail-closed reject route after
// the drain window. Split out so the closure captures only plain strings + the
// service (NDMS dep) — never lock-held state.
func (s *ServiceImpl) scheduleFakeIPDrain(poolNet4, poolMask4, iface string) {
	fakeIPScheduleDrain(func() {
		// Background ctx: the Disable request ctx is likely cancelled by now.
		if err := s.deps.StaticRoutes.RemoveStaticRoute(context.Background(), StaticRouteSpec{
			Network: poolNet4, Mask: poolMask4, Reject: true, Comment: fakeIPDrainComment,
		}); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "remove drain reject route: "+err.Error())
		}
	})
}
