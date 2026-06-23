package router

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/env"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// fakeIPTunDescription is the NDMS interface description stamped on the
// fakeip-tun OpkgTun at creation. Stable so a description-based reap fallback
// could match it later (v1 reaps by persisted index only).
const fakeIPTunDescription = "awgm fakeip-tun"

// fakeIPPoolRouteComment labels the fakeip pool auto static route so it is
// recognizable in NDMS running-config and reap.
const fakeIPPoolRouteComment = "awgm fakeip pool"

// fakeIPAddrFlush clears the kernel addresses on the tun iface right before
// sing-box starts and assigns the tun address from its own config (PoC-derived
// ordering; stand-verified in 1F.1). Seam var for tests.
var fakeIPAddrFlush = func(ctx context.Context, iface string) error {
	_, err := sysexec.Run(ctx, "ip", "addr", "flush", "dev", iface)
	return err
}

// enableFakeIPTun provisions the full fakeip-tun path: persist index → create
// OpkgTun → addr/mtu/up → write+start sing-box slot → flush+wait readiness →
// pool routes → persist enabled. Called with s.mu held by Enable. Honors the
// persist-before-create invariant (the startup reap only sees orphans by
// persisted index) and rolls back ALL partial work in reverse on any failure so
// no orphaned iface / stale persist is left behind.
func (s *ServiceImpl) enableFakeIPTun(ctx context.Context, settings *storage.Settings, sr storage.SingboxRouterSettings) (err error) {
	// resolveFakeIPParams overlays user-editable settings (pool4/6, MTU) from sr
	// onto the wired static defaults. Single source of truth — shared with the
	// fakeip config overlay (ensureFakeIPOverlayFromState).
	p := resolveFakeIPParams(s.deps.FakeIPTun, sr)

	// Fail-fast nil-guard: production wires every fakeip dep, but a degraded /
	// mis-wired build would otherwise nil-panic mid-provision. Refuse loudly
	// before touching any state.
	if s.deps.OpkgTun == nil || s.deps.StaticRoutes == nil || s.deps.OpkgTunIndices == nil {
		return fmt.Errorf("fakeip-tun: provisioning deps not wired")
	}

	// A. Load the fakeip config from SlotFakeIP (user-editable, 21-fakeip.json).
	// When the slot is empty (first enable) seed a starter A/AAAA→fakeip DNS rule
	// so the chip shows useful defaults, and set route.final to the built-in
	// "direct" outbound (always known, no dangling reference). The user picks a
	// real proxy egress afterward via the fakeip page (WYSIWYG).
	fcfg, err := s.loadFakeIPConfig()
	if err != nil {
		return fmt.Errorf("enable fakeip-tun: load fakeip config: %w", err)
	}
	if fakeIPConfigEmpty(fcfg) {
		fcfg.DNS.Rules = append(fcfg.DNS.Rules, DNSRule{Action: "route", Server: "fakeip", QueryType: []string{"A", "AAAA"}})
		fcfg.Route.Final = "direct"
	}

	// C. Egress validation from the fakeip config.
	// "direct" is a built-in known outbound — passes. Refuse to provision with
	// no usable egress (empty or unknown tag).
	proxyTag := fcfg.Route.Final
	if proxyTag == "" || !s.isKnownOutboundTag(ctx, proxyTag, fcfg) {
		return fmt.Errorf("enable fakeip-tun: no usable egress: route.final %q is not a known outbound", proxyTag)
	}

	// Derive the tun /30 dotted address + netmask for NDMS SetAddress.
	addr4, mask4, err := splitCIDRToAddrMask(p.TunAddr4)
	if err != nil {
		return fmt.Errorf("enable fakeip-tun: tun addr: %w", err)
	}
	// Derive the v4 fakeip pool network + dotted mask for the auto static route.
	poolNet4, poolMask4, err := poolV4NetMask(p.Inet4Range)
	if err != nil {
		return fmt.Errorf("enable fakeip-tun: pool range: %w", err)
	}
	tunDNS, err := DeriveTunDNS(p.TunAddr4)
	if err != nil {
		return fmt.Errorf("enable fakeip-tun: derive tun dns: %w", err)
	}

	live, err := s.deps.OpkgTunIndices.LiveOpkgTunIndices(ctx)
	if err != nil {
		return fmt.Errorf("enable fakeip-tun: list opkgtun indices: %w", err)
	}

	// Idempotency guard (CRITICAL): fakeip-tun installs no iptables, so Reconcile's
	// installed-check is always false and routes every scheduler tick + startup here.
	// If we are already provisioned with a LIVE iface, this is a no-op reconcile —
	// re-provisioning would allocate a new index, clobber persist, orphan the prior
	// iface, and exhaust the 0..9 range. Full drift-reconcile (re-add routes,
	// restart a dead sing-box) is handled by reconcileFakeIPTun; here we
	// only prevent the leak. Sits BEFORE allocate/SetFakeIPState/Create — the
	// no-op return runs before any rollback is pushed.
	if prev := settings.FakeIP; prev != nil && prev.Provisioned {
		if live[prev.Index] {
			return nil // already provisioned + iface live → no-op (Enabled already persisted)
		}
		// provisioned but iface NOT live (crash/manual removal) → fall through and
		// re-provision (allocateFakeIPIndex reuses the now-free index; old iface gone, no leak).
	}

	idx, err := allocateFakeIPIndex(live)
	if err != nil {
		return fmt.Errorf("enable fakeip-tun: allocate index: %w", err)
	}
	// Two names per index (stand-verified): NDMS RCI rejects the lowercase kernel
	// name, so every NDMS op (create/delete, address/mtu, up/down, static routes)
	// takes the CamelCase ndmsName; the kernel sees iface (sing-box config, ip
	// flush, /sys, /proc) under the lowercase name.
	ndmsName := fakeIPNDMSName(idx)
	iface := fakeIPIfaceName(idx)

	// Capture the FakeIP state as it was BEFORE this Enable so we can detect a
	// pool-range change and wipe the stale fakeip cache before sing-box starts.
	var prevState storage.FakeIPState
	if settings.FakeIP != nil {
		prevState = *settings.FakeIP
	}

	// rollback is a LIFO stack of inverse operations. Each resource-creating
	// step pushes its undo AFTER it succeeds; on any later error we run the
	// whole stack in reverse (best-effort, logged) and return the wrapped error.
	var rollback []func()
	defer func() {
		if err == nil {
			return
		}
		for i := len(rollback) - 1; i >= 0; i-- {
			rollback[i]()
		}
	}()
	push := func(undo func()) { rollback = append(rollback, undo) }

	// INVARIANT: persist FakeIP state FIRST, before creating the iface, so a
	// crash between here and CreateOpkgTun leaves a persist the startup reap can
	// find (it reaps strictly by persisted index).
	if err = s.deps.Settings.SetFakeIPState(&storage.FakeIPState{
		Provisioned: true,
		Index:       idx,
		Inet4Range:  p.Inet4Range,
		Inet6Range:  p.Inet6Range,
	}); err != nil {
		return fmt.Errorf("enable fakeip-tun: persist fakeip state: %w", err)
	}
	push(func() {
		if e := s.deps.Settings.SetFakeIPState(nil); e != nil {
			s.appLog.Warn("fakeip-rollback", iface, "clear fakeip persist: "+e.Error())
		}
	})

	// Create the OpkgTun as security-level PRIVATE and WITHOUT `ip global`:
	// steering is via specific pool/CIDR static routes onto the tun, not via an
	// access-policy exit (the old policy-exit model is abandoned). A private,
	// non-global tun routes traffic fine (stand-verified).
	if err = s.deps.OpkgTun.CreateOpkgTunWithSecurityLevel(ctx, ndmsName, fakeIPTunDescription, "private"); err != nil {
		return fmt.Errorf("enable fakeip-tun: create opkgtun: %w", err)
	}
	push(func() {
		if e := s.deps.OpkgTun.InterfaceDown(ctx, ndmsName); e != nil {
			s.appLog.Warn("fakeip-rollback", iface, "iface down: "+e.Error())
		}
		if e := s.deps.OpkgTun.DeleteOpkgTun(ctx, ndmsName); e != nil {
			s.appLog.Warn("fakeip-rollback", iface, "delete opkgtun: "+e.Error())
		}
	})

	if err = s.deps.OpkgTun.SetAddress(ctx, ndmsName, addr4, mask4); err != nil {
		return fmt.Errorf("enable fakeip-tun: set address: %w", err)
	}
	if p.TunAddr6 != "" {
		// SetIPv6Address wants a bare address (it appends /128 internally); the
		// param carries a /126 CIDR, so strip the prefix.
		addr6, e := bareAddrFromCIDR(p.TunAddr6)
		if e != nil {
			err = fmt.Errorf("enable fakeip-tun: tun addr6: %w", e)
			return err
		}
		if err = s.deps.OpkgTun.SetIPv6Address(ctx, ndmsName, addr6); err != nil {
			return fmt.Errorf("enable fakeip-tun: set ipv6 address: %w", err)
		}
	}
	if err = s.deps.OpkgTun.SetMTU(ctx, ndmsName, p.MTU); err != nil {
		return fmt.Errorf("enable fakeip-tun: set mtu: %w", err)
	}

	if err = s.deps.OpkgTun.InterfaceUp(ctx, ndmsName); err != nil {
		return fmt.Errorf("enable fakeip-tun: iface up: %w", err)
	}

	// NB: the pool/CIDR routes were moved to AFTER sing-box is confirmed up (see
	// below, post-waitForSingbox). Applying them HERE (pre-start) raced sing-box's
	// tun attach — the NDMS route-table rebuild (slow RCI) churned the kernel right
	// as sing-box opened the gvisor tun, so carrier never settled and the process
	// died ~80s in (stand-verified 2026-06-17). The tun device/address provisioning
	// above is all sing-box needs to attach; route steering follows once it's live.

	// Wipe the fakeip cache when the configured pool ranges differ from what the
	// persisted cache was built with — a stale map would hand out addresses from
	// the OLD pool. Best-effort BEFORE start; a removal error is non-fatal.
	if FakeIPCacheNeedsReset(prevState.Inet4Range, prevState.Inet6Range, p.Inet4Range, p.Inet6Range) {
		if e := ResetFakeIPCache(p.CachePath); e != nil {
			s.appLog.Warn("fakeip-cache", iface, "reset stale fakeip cache: "+e.Error())
		}
	}

	// B. Inject engine-locked bits via explicit-spec overlay (replaces
	// BuildFakeIPTunConfig). Using the local iface/p/sr vars directly avoids
	// ordering coupling with ensureFakeIPOverlayFromState (which reads
	// settings.FakeIP.Index — not yet persisted at this point in the flow).
	spec := FakeIPTunSpec{
		Iface:      iface,
		TunAddr4:   p.TunAddr4,
		TunAddr6:   p.TunAddr6,
		MTU:        p.MTU,
		Inet4Range: p.Inet4Range,
		Inet6Range: p.Inet6Range,
		CachePath:  p.CachePath,
		RealServer: p.RealServer,
		Stack:      sr.FakeIPStack,
	}
	ensureFakeIPOverlay(fcfg, spec)

	// Flush stale kernel addresses on the tun BEFORE sing-box starts, while the
	// tun is still bare (NDMS assigned its address above via SetAddress; we drop
	// it here so sing-box's gvisor attach re-adds its own configured inet4_address
	// cleanly). Doing the flush PRE-start closes the 1F.1 race: the old post-start
	// placement could flush right as the debounced (~250ms) orchestrator reload
	// made sing-box attach to the tun, killing the just-attached address and the
	// process. HARD fail: a flush error rolls the whole thing back.
	if err = fakeIPAddrFlush(ctx, iface); err != nil {
		return fmt.Errorf("enable fakeip-tun: addr flush: %w", err)
	}

	// D. Slot XOR: enable SlotFakeIP, disable SlotRouter (fakeip and tproxy router
	// slots are mutually exclusive — sing-box must load exactly one routing config).
	// Capture SlotRouter's prior enabled-state BEFORE the flip so rollback restores
	// THAT, not a hardcoded true — booting into fakeip (or a first enable) has
	// SlotRouter already off, and a hardcoded re-enable would wrongly turn tproxy on.
	// Legacy fallback (no orch) uses an explicit Start.
	prevRouterEnabled := false
	if s.deps.Orch != nil {
		for _, st := range s.deps.Orch.Snapshot() {
			if st.Slot == orchestrator.SlotRouter {
				prevRouterEnabled = st.Enabled
				break
			}
		}
		if err = s.deps.Orch.SetEnabled(orchestrator.SlotFakeIP, true); err != nil {
			return fmt.Errorf("enable fakeip-tun: orchestrator enable fakeip slot: %w", err)
		}
		if err = s.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
			return fmt.Errorf("enable fakeip-tun: orchestrator disable router slot: %w", err)
		}
	} else {
		if running, _ := s.deps.Singbox.IsRunning(); !running {
			if err = s.deps.Singbox.Start(); err != nil {
				return fmt.Errorf("enable fakeip-tun: sing-box start: %w", err)
			}
		}
	}
	push(func() {
		if s.deps.Orch != nil {
			if e := s.deps.Orch.SetEnabled(orchestrator.SlotFakeIP, false); e != nil {
				s.appLog.Warn("fakeip-rollback", iface, "disable fakeip slot: "+e.Error())
			}
			if e := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, prevRouterEnabled); e != nil {
				s.appLog.Warn("fakeip-rollback", iface, "restore router slot: "+e.Error())
			}
		}
	})
	if err = s.persistFakeIPConfig(ctx, fcfg); err != nil {
		return fmt.Errorf("enable fakeip-tun: persist fakeip config: %w", err)
	}

	// Wait for sing-box to be truly ready (process + tun carrier + live fakeip
	// DNS). The address flush already ran PRE-start (above), so the tun keeps the
	// address sing-box assigns on attach. HARD fail: an unready sing-box means the
	// tun and its hijack-dns path never come up, so we roll the whole thing back.
	bootWait := bootWaitWithFloor()
	if err = s.waitForSingbox(ctx, bootWait); err != nil {
		return fmt.Errorf("enable fakeip-tun: %w: waited %s (%v)", ErrSingboxNotReady, bootWait, err)
	}

	// Routing NDMS mutations run AFTER sing-box is confirmed up (carrier) —
	// applying them pre-start raced the gvisor tun attach (see the note after
	// InterfaceUp). Post-readiness sing-box is attached against a quiet NDMS, so
	// these steer routing without disturbing the live tun.

	// NDMS auto static routes steer the fakeip pool(s) into the tun.
	if err = s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
		Network:   poolNet4,
		Mask:      poolMask4,
		Interface: ndmsName,
		Comment:   fakeIPPoolRouteComment,
	}); err != nil {
		return fmt.Errorf("enable fakeip-tun: add pool route: %w", err)
	}
	push(func() {
		if e := s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
			Network: poolNet4, Mask: poolMask4, Interface: ndmsName, Comment: fakeIPPoolRouteComment,
		}); e != nil {
			s.appLog.Warn("fakeip-rollback", iface, "remove pool route: "+e.Error())
		}
	})
	if p.Inet6Range != "" {
		if err = s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
			V6: true, Network: p.Inet6Range, Interface: ndmsName,
		}); err != nil {
			return fmt.Errorf("enable fakeip-tun: add pool route v6: %w", err)
		}
		push(func() {
			if e := s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
				V6: true, Network: p.Inet6Range, Interface: ndmsName,
			}); e != nil {
				s.appLog.Warn("fakeip-rollback", iface, "remove pool route v6: "+e.Error())
			}
		})
	}

	// Specific CIDR routes for proxy-routed dst CIDRs (loop-safe pure-dst rules
	// only — see desiredTunCIDRs): full apply on enable. Best-effort per CIDR — one
	// bad entry must not fail the whole enable; rollback removes the ones that succeeded.
	enableCIDRV4, enableCIDRV6 := desiredTunCIDRs(fcfg)
	for _, c := range enableCIDRV4 {
		if e := s.addCIDRRoute(ctx, ndmsName, c, false); e != nil {
			s.appLog.Warn("fakeip", iface, "add cidr route "+c+": "+e.Error())
			continue
		}
		cc := c
		push(func() {
			if e := s.removeCIDRRoute(ctx, ndmsName, cc, false); e != nil {
				s.appLog.Warn("fakeip-rollback", iface, "remove cidr route "+cc+": "+e.Error())
			}
		})
	}
	for _, c := range enableCIDRV6 {
		if e := s.addCIDRRoute(ctx, ndmsName, c, true); e != nil {
			s.appLog.Warn("fakeip", iface, "add cidr route v6 "+c+": "+e.Error())
			continue
		}
		cc := c
		push(func() {
			if e := s.removeCIDRRoute(ctx, ndmsName, cc, true); e != nil {
				s.appLog.Warn("fakeip-rollback", iface, "remove cidr route v6 "+cc+": "+e.Error())
			}
		})
	}

	// Best-effort live-DNS confirmation (NOT a gate). sing-box is already up by
	// carrier and the pool route to the tun now exists, so a single .2→fakeip
	// query should answer. We run it ONCE (here, not in the poll loop, so no log
	// spam) purely as a diagnostic: if it does not answer within the probe
	// window the path is up but DNS delivery may be briefly degraded — we WARN
	// and continue, never failing Enable. This preserves the functional signal
	// the live DNS probe gave without making it a flaky readiness gate (the
	// Go resolver honors resolv.conf attempts:1, stand-verified 2026-06-15).
	poolPrefix4, _ := netip.ParsePrefix(p.Inet4Range) // already validated by poolV4NetMask above
	fakeipNet4 := poolPrefix4.Masked()
	dnsConfirmCtx, dnsConfirmCancel := context.WithTimeout(ctx, fakeIPDNSConfirmTimeout)
	if !fakeIPDNSProbe(dnsConfirmCtx, tunDNS, fakeipNet4) {
		s.appLog.Warn("fakeip-dns-confirm", iface, "fakeip DNS delivery could not be confirmed within the probe window — sing-box is up (carrier), but the .2→fakeip round-trip did not answer in time; DNS delivery may be briefly degraded (not fatal)")
	}
	dnsConfirmCancel()

	// awg-manager no longer advertises the tun DNS to LAN clients — the user
	// configures client DNS manually. The fakeip DNS server still receives queries
	// via the hijack-dns route rule once a client points at the tun .2.

	// Persist enabled LAST (success). From here we do NOT roll back.
	settings.SingboxRouter = sr
	if err = s.deps.Settings.Save(settings); err != nil {
		return fmt.Errorf("enable fakeip-tun: save settings: %w", err)
	}

	s.emitStatus(ctx)
	return nil
}

// splitCIDRToAddrMask splits a CIDR into its bare address string and dotted-quad
// (v4) netmask, e.g. "172.18.0.1/30" → ("172.18.0.1", "255.255.255.252") and
// "198.18.0.0/15" → ("198.18.0.0", "255.254.0.0"). v4-only (NDMS SetAddress /
// the pool auto-route are v4); errors on non-v4 or malformed input.
func splitCIDRToAddrMask(cidr string) (addr, mask string, err error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", "", fmt.Errorf("parse %q: %w", cidr, err)
	}
	if !p.Addr().Is4() {
		return "", "", fmt.Errorf("%q is not IPv4", cidr)
	}
	m := net.CIDRMask(p.Bits(), 32)
	return p.Addr().String(), net.IP(m).String(), nil
}

// poolV4NetMask derives the v4 fakeip pool network address + dotted mask from a
// pool CIDR, masking the prefix first so a user-supplied non-masked pool (e.g.
// "198.19.0.0/15") yields the network address ("198.18.0.0"), not a host — a
// non-masked Network would make NDMS reject or mis-install the route. Single
// source of truth for the four sites (enable/disable/reconcile/reap) that need
// the pool route's network+mask; each caller keeps its own Warn/error handling.
func poolV4NetMask(inet4Range string) (network, mask string, err error) {
	prefix, err := netip.ParsePrefix(inet4Range)
	if err != nil {
		return "", "", err
	}
	return splitCIDRToAddrMask(prefix.Masked().String())
}

// bootWaitWithFloor returns the sing-box boot-wait timeout from
// AWG_SINGBOX_BOOT_WAIT (default 60s), clamped to a 60s floor. Router-package-
// local so the three wait sites (fakeip enable, fakeip reconcile, tproxy
// switch) share one definition — sidestepping the import cycle that blocks
// reusing the parent singbox.maxSingboxBootWait helper directly.
func bootWaitWithFloor() time.Duration {
	bootWait := env.DurationDefault("AWG_SINGBOX_BOOT_WAIT", 60*time.Second)
	if bootWait < 60*time.Second {
		bootWait = 60 * time.Second
	}
	return bootWait
}

// bareAddrFromCIDR returns just the address portion of a CIDR (drops the
// prefix length), e.g. "fdfe:dcba:9876::1/126" → "fdfe:dcba:9876::1".
func bareAddrFromCIDR(cidr string) (string, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", cidr, err)
	}
	return p.Addr().String(), nil
}
