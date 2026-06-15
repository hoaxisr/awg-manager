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
// pool routes → health-gated DHCP DNS → persist enabled. Called with s.mu held
// by Enable. Honors the persist-before-create invariant (the startup reap only
// sees orphans by persisted index) and rolls back ALL partial work in reverse
// on any failure so no orphaned iface / half-applied DHCP / stale persist is
// left behind.
func (s *ServiceImpl) enableFakeIPTun(ctx context.Context, settings *storage.Settings, sr storage.SingboxRouterSettings) (err error) {
	p := s.deps.FakeIPTun

	// Fail-fast nil-guard: production wires every fakeip dep, but a degraded /
	// mis-wired build would otherwise nil-panic mid-provision. Refuse loudly
	// before touching any state.
	if s.deps.OpkgTun == nil || s.deps.StaticRoutes == nil || s.deps.DHCP == nil || s.deps.OpkgTunIndices == nil {
		return fmt.Errorf("fakeip-tun: provisioning deps not wired")
	}

	// Resolve egress from the persisted router config: the proxy tag is its
	// route.final, and the outbounds carry the proxy + direct definitions that
	// the fakeip config reuses verbatim. Refuse to provision a fakeip-tun with
	// no usable egress — a direct-only tun would fake-resolve every domain and
	// route it nowhere useful, a worse outage than staying off.
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return fmt.Errorf("enable fakeip-tun: load router config: %w", err)
	}
	// Coupling note: the fakeip egress follows the user's configured default
	// outbound (route.final). Changing route.final changes where every faked
	// domain is routed — there is no separate fakeip-egress knob in v1.
	// Validate the egress against ALL known outbound catalogs (router composites,
	// subscription composites, AWG-direct, sing-box tunnels, built-ins) — NOT just
	// cfg.Outbounds, because the egress (e.g. an AWG outbound "awg-awg10") lives in
	// 15-awg.json / another slot that sing-box merges, and is absent from the router
	// slot's own outbounds. Stand-verified 2026-06-15: the old cfg-only check
	// rejected a valid AWG egress. Mirrors SetRouteFinal's isKnownOutboundTag.
	proxyTag := cfg.Route.Final
	if proxyTag == "" || !s.isKnownOutboundTag(ctx, proxyTag, cfg) {
		return fmt.Errorf("enable fakeip-tun: no usable egress: route.final %q is not a known outbound", proxyTag)
	}

	// Derive the tun /30 dotted address + netmask for NDMS SetAddress.
	addr4, mask4, err := splitCIDRToAddrMask(p.TunAddr4)
	if err != nil {
		return fmt.Errorf("enable fakeip-tun: tun addr: %w", err)
	}
	// Derive the v4 fakeip pool network + dotted mask for the auto static route.
	// Mask the prefix first so a user-supplied non-masked pool (e.g.
	// "10.130.0.0/10") yields the network address ("10.128.0.0"), not a host —
	// a non-masked Network would make NDMS reject or mis-install the route.
	poolPrefix4, err := netip.ParsePrefix(p.Inet4Range)
	if err != nil {
		return fmt.Errorf("enable fakeip-tun: pool range: %w", err)
	}
	poolNet4, poolMask4, err := splitCIDRToAddrMask(poolPrefix4.Masked().String())
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
	// iface, and exhaust the 0..9 range. Full drift-reconcile (re-advertise health-
	// gated DNS, re-add routes, restart a dead sing-box) is Task 15 (1D.3); here we
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

	// Create the OpkgTun with security-level private (no SetIPGlobal: the tun
	// is an internal transport, not a global-routable iface).
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

	// Wipe the fakeip cache when the configured pool ranges differ from what the
	// persisted cache was built with — a stale map would hand out addresses from
	// the OLD pool. Best-effort BEFORE start; a removal error is non-fatal.
	if FakeIPCacheNeedsReset(prevState.Inet4Range, prevState.Inet6Range, p.Inet4Range, p.Inet6Range) {
		if e := ResetFakeIPCache(p.CachePath); e != nil {
			s.appLog.Warn("fakeip-cache", iface, "reset stale fakeip cache: "+e.Error())
		}
	}

	spec := FakeIPTunSpec{
		Iface:      iface,
		TunAddr4:   p.TunAddr4,
		TunAddr6:   p.TunAddr6,
		MTU:        p.MTU,
		Inet4Range: p.Inet4Range,
		Inet6Range: p.Inet6Range,
		CachePath:  p.CachePath,
		RealServer: p.RealServer,
		// Strip auto-managed direct outbounds (awg/nwg/wireguard bind_interface)
		// — they live in 15-awg.json and are merged by sing-box across config.d.
		// Re-emitting them here would FATAL the merged config with
		// "duplicate outbound tag" (stand-verified 2026-06-15). ProxyTag still
		// references one of them by tag; sing-box resolves it from 15-awg.json.
		// Mirrors the tproxy path (service.go: stripAutoManagedDirect).
		Outbounds: stripAutoManagedDirect(cfg.Outbounds),
		ProxyTag:  proxyTag,
		// v1: DomainRuleSets / SourceIPCIDR empty = fake all A/AAAA, all sources.
	}
	fcfg, err := BuildFakeIPTunConfig(spec)
	if err != nil {
		return fmt.Errorf("enable fakeip-tun: build config: %w", err)
	}

	// Promote SlotRouter to active FIRST (like the tproxy path) so
	// persistConfigDirect targets the active file and the orchestrator cold-
	// start reads it. Legacy fallback (no orch) uses an explicit Start.
	if s.deps.Orch != nil {
		if err = s.deps.Orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
			return fmt.Errorf("enable fakeip-tun: orchestrator enable router: %w", err)
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
			if e := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); e != nil {
				s.appLog.Warn("fakeip-rollback", iface, "disable slot: "+e.Error())
			}
		}
	})
	if err = s.persistConfigDirect(ctx, fcfg); err != nil {
		return fmt.Errorf("enable fakeip-tun: persist config: %w", err)
	}

	// Flush stale kernel addresses on the tun right before sing-box drives it,
	// then wait for it to be truly ready (process + tun carrier + live fakeip
	// DNS). HARD fail: an unready sing-box with the DHCP DNS advertised would
	// black-hole client DNS, so we roll the whole thing back.
	//
	// RISK (1F.1): the flush-vs-sing-box-attach ordering is PoC-derived and MUST
	// be asserted on the live stand — the orchestrator reload that makes sing-box
	// attach to the tun is debounced (~250ms), so the flush here may race the
	// attach. Verify the tun keeps the sing-box-assigned address on the stand.
	if err = fakeIPAddrFlush(ctx, iface); err != nil {
		return fmt.Errorf("enable fakeip-tun: addr flush: %w", err)
	}
	bootWait := env.DurationDefault("AWG_SINGBOX_BOOT_WAIT", 60*time.Second)
	if bootWait < 60*time.Second {
		bootWait = 60 * time.Second
	}
	if err = s.waitForSingbox(ctx, bootWait); err != nil {
		return fmt.Errorf("enable fakeip-tun: %w: waited %s (%v)", ErrSingboxNotReady, bootWait, err)
	}

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
		if err = s.deps.StaticRoutes.AddStaticRoute6(ctx, p.Inet6Range, ndmsName); err != nil {
			return fmt.Errorf("enable fakeip-tun: add pool route v6: %w", err)
		}
		push(func() {
			if e := s.deps.StaticRoutes.RemoveStaticRoute6(ctx, p.Inet6Range, ndmsName); e != nil {
				s.appLog.Warn("fakeip-rollback", iface, "remove pool route v6: "+e.Error())
			}
		})
	}

	// Health-gated DHCP DNS delivery: advertise the tun DNS to LAN clients only
	// when sing-box is running AND the egress is up; otherwise clear it so
	// clients fall back to the router's default DNS (no outage).
	if err = s.advertiseDNSIfHealthy(ctx, p.DHCPPool, tunDNS, iface, cfg); err != nil {
		return fmt.Errorf("enable fakeip-tun: advertise dns: %w", err)
	}
	push(func() {
		if e := s.deps.DHCP.ClearPoolDNS(ctx, p.DHCPPool); e != nil {
			s.appLog.Warn("fakeip-rollback", iface, "clear pool dns: "+e.Error())
		}
	})

	// Persist enabled LAST (success). From here we do NOT roll back.
	settings.SingboxRouter = sr
	if err = s.deps.Settings.Save(settings); err != nil {
		return fmt.Errorf("enable fakeip-tun: save settings: %w", err)
	}

	s.emitStatus(ctx)
	return nil
}

// advertiseDNSIfHealthy sets the DHCP pool DNS to the tun DNS only when sing-box
// is running AND the egress is up; otherwise clears it so clients fall back to
// the router's default DNS (no outage while the proxy path is down).
//
// Change-detection (Fix B1/B2): the DHCP write only fires when the DESIRED state
// (advertise vs clear) differs from the last-applied state cached on the service
// (fakeIPDNSAdvertised). In steady state the desired state is stable, so the
// per-tick drift-reconcile makes ZERO DHCP writes. A genuine flip (sing-box
// dies, egress carrier drops, or recovers) still applies exactly once — the
// correct intent. The cache is guarded by the dedicated fakeIPDNSMu, never s.mu,
// because this is reached from both the s.mu-holding Enable path and the
// lock-free Reconcile path.
func (s *ServiceImpl) advertiseDNSIfHealthy(ctx context.Context, pool, tunDNS, iface string, cfg *RouterConfig) error {
	running, _ := s.deps.Singbox.IsRunning()
	desired := running && s.fakeIPEgressUp(cfg)

	s.fakeIPDNSMu.Lock()
	unchanged := s.fakeIPDNSAdvertised != nil && *s.fakeIPDNSAdvertised == desired
	s.fakeIPDNSMu.Unlock()
	if unchanged {
		return nil // desired DHCP-DNS state already applied → no write
	}

	var err error
	if desired {
		err = s.deps.DHCP.SetPoolDNS(ctx, pool, []string{tunDNS})
	} else {
		err = s.deps.DHCP.ClearPoolDNS(ctx, pool)
	}
	if err != nil {
		return err // leave the cache untouched so the next tick retries
	}

	s.fakeIPDNSMu.Lock()
	d := desired
	s.fakeIPDNSAdvertised = &d
	s.fakeIPDNSMu.Unlock()
	return nil
}

// fakeIPEgressUp reports whether the proxy egress is usable. If the route.final
// outbound binds an interface (direct + bind_interface), egress readiness is
// that interface's carrier; otherwise (a proxy outbound with no bind) there is
// no carrier signal to gate on, so it is treated as up (sing-box owns the
// tunnel's health).
func (s *ServiceImpl) fakeIPEgressUp(cfg *RouterConfig) bool {
	for _, o := range cfg.Outbounds {
		if o.Tag != cfg.Route.Final {
			continue
		}
		if o.BindInterface != "" {
			return tunReadyProbe(o.BindInterface)
		}
		// RISK (blind true): a bind-less proxy / composite outbound has no carrier
		// signal, so we return up unconditionally. A real proxy-egress health gate
		// would need a Clash-API delay probe against the outbound (roadmap) — until
		// then a dead proxy still advertises DNS and clients black-hole silently.
		return true
	}
	return false
}


// splitCIDRToAddrMask splits a CIDR into its bare address string and dotted-quad
// (v4) netmask, e.g. "172.18.0.1/30" → ("172.18.0.1", "255.255.255.252") and
// "10.128.0.0/10" → ("10.128.0.0", "255.192.0.0"). v4-only (NDMS SetAddress /
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
	dotted := fmt.Sprintf("%d.%d.%d.%d", m[0], m[1], m[2], m[3])
	return p.Addr().String(), dotted, nil
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
