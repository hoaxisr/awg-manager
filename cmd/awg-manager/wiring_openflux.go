package main

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/openflux"
	"github.com/hoaxisr/awg-manager/internal/opkgtun"
)

func (a *app) setupOpenFlux() {
	openfluxArch := detectArch()
	a.openfluxService = openflux.New(a.dataDir, openfluxArch, func(ctx context.Context) string {
		host, _ := a.proxyExternalIP(ctx)
		return host
	})
	if a.ndmsCommands != nil && a.ndmsQueries != nil {
		a.openfluxService.SetRouterCommands(&openfluxRouterAdapter{
			opkg:     confirmingOpkgTun{a.ndmsCommands.Interfaces, a.ndmsQueries.Interfaces},
			defrt:    confirmingDefaultRoute{a.ndmsCommands.Routes, a.ndmsQueries.Interfaces},
			permit:   confirmingPermitter{a.ndmsCommands.Policies, a.ndmsQueries.Interfaces},
			policies: a.ndmsCommands.Policies,
			ifaces:   a.ndmsQueries.Interfaces,
			pool:     a.opkgPool,
			leases:   make(map[string]*opkgtun.Reservation),
		})
	}
	if a.awgStore != nil {
		a.openfluxService.SetTunnelStore(a.awgStore)
	}
	a.openfluxService.SetPolicyResolver(func(ctx context.Context, policyName string) (string, error) {
		if a.ndmsQueries == nil || a.ndmsQueries.Policies == nil {
			return "", nil
		}
		policies, err := a.ndmsQueries.Policies.List(ctx)
		if err != nil {
			return "", err
		}
		var targetIface string
		for _, p := range policies {
			if strings.EqualFold(p.Name, policyName) {
				for _, iface := range p.Interfaces {
					if !iface.Denied && strings.TrimSpace(iface.Name) != "" {
						targetIface = iface.Name
						break
					}
				}
				break
			}
		}
		if targetIface == "" {
			return "", nil
		}
		if a.ndmsQueries.Interfaces != nil {
			if ifaces, err := a.ndmsQueries.Interfaces.List(ctx); err == nil {
				for _, ifi := range ifaces {
					if strings.EqualFold(ifi.ID, targetIface) || strings.EqualFold(ifi.SystemName, targetIface) {
						if ifi.Address != "" {
							return ifi.Address, nil
						}
						if ifi.SystemName != "" {
							if ip := openflux.ResolveInterfaceIPv4(ifi.SystemName); ip != "" {
								return ip, nil
							}
						}
					}
				}
			}
		}
		return openflux.ResolveInterfaceIPv4(targetIface), nil
	})
	a.deferOnExit(func() {
		a.openfluxService.Shutdown(context.Background())
	})
	_ = a.openfluxService.StartIfEnabled(context.Background())
	a.openfluxHandler = api.NewOpenFluxHandler(a.openfluxService)
}

type openfluxRouterAdapter struct {
	mu       sync.Mutex
	opkg     confirmingOpkgTun
	defrt    confirmingDefaultRoute
	permit   confirmingPermitter
	policies *ndmscommand.PolicyCommands
	ifaces   *ndmsquery.InterfaceStore
	pool     *opkgtun.Pool
	leases   map[string]*opkgtun.Reservation
}

func (r *openfluxRouterAdapter) releaseLeaseLocked(ndmsName string) {
	if r.leases == nil {
		return
	}
	if lease, ok := r.leases[ndmsName]; ok && lease != nil {
		lease.Close()
		delete(r.leases, ndmsName)
	}
}

func (r *openfluxRouterAdapter) AllocateTunInterface(ctx context.Context, preferred string) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leases == nil {
		r.leases = make(map[string]*opkgtun.Reservation)
	}
	if r.pool != nil {
		if preferred != "" {
			r.releaseLeaseLocked(preferred)
		}
		self := opkgtun.TunnelHolder("openflux-client", "OpenFlux")
		req := opkgtun.Want(self)
		if idx, ok := opkgtun.NDMSIndexOf(preferred); ok && idx >= 2 {
			req = opkgtun.WantPinned(self, idx)
		}
		res, err := r.pool.Reserve(ctx, req)
		if err == nil {
			num := res.Numbers()[0]
			ndms := fmt.Sprintf("OpkgTun%d", num)
			tun := fmt.Sprintf("opkgtun%d", num)
			r.releaseLeaseLocked(ndms)
			r.leases[ndms] = res
			return ndms, tun, nil
		}
	}
	ndms, tun := openflux.AutoDiscoverFreeTun(preferred)
	return ndms, tun, nil
}

var openfluxConflictIfaceRe = regexp.MustCompile(`(?i)conflicts with interface ["']?(opkgtun\d+)["']?`)

func extractOpenfluxConflictIface(errStr string) string {
	m := openfluxConflictIfaceRe.FindStringSubmatch(errStr)
	if len(m) > 1 {
		match := m[1]
		for _, prefix := range []string{"OpkgTun", "opkgtun"} {
			if strings.HasPrefix(strings.ToLower(match), strings.ToLower(prefix)) {
				numStr := match[len(prefix):]
				if n, err := strconv.Atoi(numStr); err == nil {
					return fmt.Sprintf("OpkgTun%d", n)
				}
			}
		}
		return match
	}
	return ""
}

func (r *openfluxRouterAdapter) CreateInterface(ctx context.Context, ndmsName, desc string, mtu int, ipWithMask string, global bool, defaultRoute bool) error {
	if r.opkg.cmds == nil || ndmsName == "" {
		return nil
	}
	ip, mask := parseIPAndMask(ipWithMask)
	if err := r.opkg.CreateOpkgTunWithSecurityLevel(ctx, ndmsName, desc, "public"); err != nil {
		r.mu.Lock()
		r.releaseLeaseLocked(ndmsName)
		r.mu.Unlock()
		return fmt.Errorf("create %s: %w", ndmsName, err)
	}

	rollback := func(cause error) error {
		r.mu.Lock()
		r.releaseLeaseLocked(ndmsName)
		r.mu.Unlock()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.opkg.DeleteOpkgTun(cleanupCtx, ndmsName)
		return cause
	}

	resolveConflict := func(err error) bool {
		if err == nil {
			return false
		}
		conflictIface := extractOpenfluxConflictIface(err.Error())
		if conflictIface != "" && conflictIface != "OpkgTun0" && conflictIface != "OpkgTun1" && conflictIface != ndmsName {
			r.mu.Lock()
			r.releaseLeaseLocked(conflictIface)
			r.mu.Unlock()
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = r.opkg.InterfaceDown(cleanupCtx, conflictIface)
			_ = r.opkg.DeleteOpkgTun(cleanupCtx, conflictIface)
			return true
		}
		return false
	}

	if ip != "" && mask != "" {
		if err := r.opkg.SetAddress(ctx, ndmsName, ip, mask); err != nil {
			if resolveConflict(err) {
				err = r.opkg.SetAddress(ctx, ndmsName, ip, mask)
			}
			if err != nil {
				return rollback(fmt.Errorf("set address %s on %s: %w", ipWithMask, ndmsName, err))
			}
		}
	}
	if mtu > 0 {
		if err := r.opkg.SetMTU(ctx, ndmsName, mtu); err != nil {
			return rollback(fmt.Errorf("set mtu %d on %s: %w", mtu, ndmsName, err))
		}
	}
	if global {
		if err := r.opkg.SetIPGlobal(ctx, ndmsName); err != nil {
			return rollback(fmt.Errorf("set ip global on %s: %w", ndmsName, err))
		}
	}
	if err := r.opkg.SetPermitAllACL(ctx, ndmsName); err != nil {
		return rollback(fmt.Errorf("set permit all acl on %s: %w", ndmsName, err))
	}
	if err := r.opkg.InterfaceUp(ctx, ndmsName); err != nil {
		if resolveConflict(err) {
			err = r.opkg.InterfaceUp(ctx, ndmsName)
		}
		if err != nil {
			return rollback(fmt.Errorf("interface up %s: %w", ndmsName, err))
		}
	}
	if defaultRoute {
		if err := r.defrt.SetDefaultRoute(ctx, ndmsName); err != nil {
			return rollback(fmt.Errorf("set default route on %s: %w", ndmsName, err))
		}
	}
	return nil
}

func (r *openfluxRouterAdapter) DownInterface(ctx context.Context, ndmsName string) error {
	r.mu.Lock()
	r.releaseLeaseLocked(ndmsName)
	r.mu.Unlock()
	if r.opkg.cmds == nil || ndmsName == "" {
		return nil
	}
	return r.opkg.InterfaceDown(ctx, ndmsName)
}

func (r *openfluxRouterAdapter) DeleteInterface(ctx context.Context, ndmsName string) error {
	r.mu.Lock()
	r.releaseLeaseLocked(ndmsName)
	r.mu.Unlock()
	if r.opkg.cmds == nil || ndmsName == "" {
		return nil
	}
	return r.opkg.DeleteOpkgTun(ctx, ndmsName)
}

func (r *openfluxRouterAdapter) PermitPolicy(ctx context.Context, policy, iface string) error {
	if r.permit.policies == nil || policy == "" || policy == "none" {
		return nil
	}
	return r.permit.PermitInterface(ctx, policy, iface, 0)
}

func (r *openfluxRouterAdapter) DenyPolicy(ctx context.Context, policy, iface string) error {
	if r.policies == nil || r.ifaces == nil || policy == "" || policy == "none" {
		return nil
	}
	return confirmTeardown(ctx, r.ifaces, iface, func(c ndmsquery.Confirmed) error {
		return r.policies.DenyInterface(ctx, policy, c)
	})
}

func parseIPAndMask(cidr string) (string, string) {
	s := strings.TrimSpace(cidr)
	if s == "" {
		return "10.10.10.2", "255.255.255.0"
	}
	ip, ipNet, err := net.ParseCIDR(s)
	if err == nil {
		mask := net.IP(ipNet.Mask).String()
		return ip.String(), mask
	}
	if plain := net.ParseIP(s); plain != nil {
		return plain.String(), "255.255.255.0"
	}
	return "10.10.10.2", "255.255.255.0"
}
