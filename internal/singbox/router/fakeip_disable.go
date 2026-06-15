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
// is removed. Sized ~a DHCP lease so clients renew (ClearPoolDNS reverts them to
// the router's default DNS) and stop minting fakeip addresses before the
// fail-closed route disappears. During this window any client still holding a
// fakeip address is DROPPED, not routed to WAN (spec §5 leak). Removed off the
// lock (Disable holds s.mu; a blocking sleep there would stall everything).
var fakeIPDrainWindow = 30 * time.Second

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
// v6 asymmetry: AddStaticRoute6 has no Reject, so v6 gets NO explicit reject
// route — its drain is the route removal alone (no-route → the kernel drops the
// packets). Only v4 gets a fail-closed reject route.
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
	if haveV4 {
		if err := s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
			Network: poolNet4,
			Mask:    poolMask4,
			Reject:  true,
			Comment: fakeIPDrainComment,
		}); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "add drain reject route (pool NOT fail-closed): "+err.Error())
		}
	}

	// (4) Remove the pool auto-route(s). The reject route remains up for the drain
	// window. v6 has no reject route — removing its auto-route is the whole drain
	// (no route → kernel drop). Best-effort.
	if haveV4 {
		if err := s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
			Network: poolNet4, Mask: poolMask4, Interface: iface, Comment: fakeIPPoolRouteComment,
		}); err != nil {
			s.appLog.Warn("fakeip-disable", iface, "remove pool route: "+err.Error())
		}
	}
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
	// cannot deadlock on the lock the parent still holds.
	if haveV4 {
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
