package router

import (
	"context"
	"fmt"
)

// fakeip_nat.go implements the static-NAT delivery-segment source-preservation
// path (Task PE-D, spec §3.2/§3.3). When FakeIPSourcePreserve is on, the
// delivery segment's NAT mode is flipped from dynamic masquerade to static-NAT
// (`no ip nat <Seg>` + `ip static <Seg> <WAN>`): SNAT then happens ONLY on the
// real WAN egress, so traffic from the LAN segment into the fakeip opkgtun is
// NOT masqueraded and the client source IP is preserved (proven on a live
// router, spec §2 fact 4). Dynamic masquerade is restored on teardown.
//
// RESOLVER PATHS (resolveDeliverySegmentAndWAN):
//   (a) pool→segment: the delivery DHCP pool (s.deps.FakeIPTun.DHCPPool) is
//       resolved to its bound NDMS segment via DHCPPoolSegments.SegmentForPool,
//       implemented over the DHCPPool query store (DHCPPool.Interface carries
//       the `bind` segment, e.g. "Home").
//   (b) active WAN → NDMS id: WAN-autodetect (sr.WANAutoDetect) uses
//       DefaultGateway.DefaultGatewayInterface — which returns the NDMS id
//       (e.g. "PPPoE0") DIRECTLY, the same value internal/managed's internet-only
//       NAT feeds straight into `ip static`'s to-interface (verified precedent).
//       Pinned WAN (sr.WANInterface, a KERNEL system-name like "ppp0") has no
//       reverse NDMS resolver, so we scan WANInterfaces.ListWAN matching
//       WANInterfaceInfo.Name (kernel) → .ID (NDMS).
//
// IPv6 (spec §3.6): no separate v6 static-NAT path is needed. Keenetic's
// `ip nat`/`ip static` are IPv4 NAT controls (masquerade/SNAT) keyed only on
// interface/to-interface — there is no v4/v6 discriminator in the payload, and
// Keenetic does no NAT66 by default. Source-rewriting (the thing static-NAT
// prevents) is an IPv4-masquerade phenomenon; IPv6 into the tun is already
// un-NAT'd (source preserved) without any command. So the single v4 `ip static`
// fully covers NAT source-preservation; v6 fail-closed is handled at the route
// level in PE-C, not here.

// resolveDeliverySegmentAndWAN resolves the delivery DHCP pool to its bound NDMS
// segment and the active WAN to its NDMS interface id, for `ip static <Seg> <WAN>`.
// Returns ("","",nil) — NOT an error — when a required dep is unwired, so callers
// can skip static-NAT cleanly in degraded/test builds. Any real lookup failure
// (dep wired but query failed, pool/WAN not found) IS returned as an error so the
// caller can warn-and-continue (enable does not fail-hard on resolve failure).
func (s *ServiceImpl) resolveDeliverySegmentAndWAN(ctx context.Context) (seg, wanID string, err error) {
	if s.deps.DHCPPoolSegments == nil {
		return "", "", nil // unwired (test/degraded) → skip static-NAT, no error
	}
	pool := s.deps.FakeIPTun.DHCPPool
	if pool == "" {
		return "", "", fmt.Errorf("static-NAT resolve: empty delivery DHCP pool")
	}
	seg, err = s.deps.DHCPPoolSegments.SegmentForPool(ctx, pool)
	if err != nil {
		return "", "", fmt.Errorf("static-NAT resolve segment for pool %q: %w", pool, err)
	}
	if seg == "" {
		return "", "", fmt.Errorf("static-NAT resolve: pool %q is not bound to a segment", pool)
	}

	wanID, err = s.resolveWANNDMSID(ctx)
	if err != nil {
		return "", "", err
	}
	return seg, wanID, nil
}

// resolveWANNDMSID resolves the active WAN to its NDMS interface id (the
// to-interface for `ip static`). Autodetect → DefaultGateway (already an NDMS
// id). Pinned → reverse-map the kernel system-name to its NDMS id via the WAN
// list (WANInterfaceInfo.Name → .ID). Reads the WAN mode from persisted settings.
func (s *ServiceImpl) resolveWANNDMSID(ctx context.Context) (string, error) {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return "", fmt.Errorf("static-NAT resolve WAN: load settings: %w", err)
	}
	sr, _ := NormalizeSingboxRouterSettings(settings.SingboxRouter)

	if sr.WANAutoDetect {
		if s.deps.DefaultGateway == nil {
			return "", fmt.Errorf("static-NAT resolve WAN: default-gateway resolver not wired")
		}
		id, err := s.deps.DefaultGateway.DefaultGatewayInterface(ctx)
		if err != nil {
			return "", fmt.Errorf("static-NAT resolve WAN: default gateway: %w", err)
		}
		if id == "" {
			return "", fmt.Errorf("static-NAT resolve WAN: no default-gateway interface")
		}
		return id, nil
	}

	// Pinned WAN: sr.WANInterface is a KERNEL system-name → reverse-map to NDMS id.
	return s.ndmsIDForKernelName(ctx, sr.WANInterface)
}

// ndmsIDForKernelName scans the WAN interface list for the entry whose kernel
// system-name (WANInterfaceInfo.Name) matches kernelName and returns its NDMS id
// (WANInterfaceInfo.ID). The router has only a forward NDMS-id→kernel resolver, so
// this minimal reverse scan over the existing WAN-lister dep covers the pinned case.
func (s *ServiceImpl) ndmsIDForKernelName(ctx context.Context, kernelName string) (string, error) {
	if kernelName == "" {
		return "", fmt.Errorf("static-NAT resolve WAN: pinned WAN interface is empty")
	}
	if s.deps.WANInterfaces == nil {
		return "", fmt.Errorf("static-NAT resolve WAN: WAN-interface lister not wired")
	}
	wans, err := s.deps.WANInterfaces.ListWAN(ctx)
	if err != nil {
		return "", fmt.Errorf("static-NAT resolve WAN: list WAN interfaces: %w", err)
	}
	for _, w := range wans {
		if w.Name == kernelName {
			if w.ID == "" {
				return "", fmt.Errorf("static-NAT resolve WAN: WAN %q has no NDMS id", kernelName)
			}
			return w.ID, nil
		}
	}
	return "", fmt.Errorf("static-NAT resolve WAN: pinned WAN %q not found in WAN list", kernelName)
}

// applyStaticNAT: dynamic NAT off, SNAT only on WAN → segment→opkgtun un-masqueraded
// (source preserved), segment→WAN still NAT'd. Spec §3.2/§3.3.
func (s *ServiceImpl) applyStaticNAT(ctx context.Context, seg, wan string) error {
	if err := s.deps.SegmentNAT.RemoveSegmentNAT(ctx, seg); err != nil {
		return err
	}
	return s.deps.SegmentNAT.SetStaticNAT(ctx, seg, wan)
}

// teardownStaticNAT restores dynamic masquerade NAT on the segment: drop the
// static-NAT mapping, then re-enable `ip nat <Seg>`. Best-effort symmetry — the
// first error is returned but both steps are attempted (a failed RemoveStaticNAT
// must not skip restoring dynamic NAT, or the segment is left with no NAT at all).
func (s *ServiceImpl) teardownStaticNAT(ctx context.Context, seg, wan string) error {
	rerr := s.deps.SegmentNAT.RemoveStaticNAT(ctx, seg, wan)
	if e := s.deps.SegmentNAT.SetSegmentNAT(ctx, seg); e != nil && rerr == nil {
		rerr = e
	}
	return rerr
}
