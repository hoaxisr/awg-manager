package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/ndmsres"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
)

// Адаптеры строковых провайдеров (router, ndmsres, api) к командам NDMS по
// query.Confirmed: подтверждение свежим списком на КАЖДЫЙ вызов (F546).
// Правило одно (решение 4): список не прочитан — ошибка, команды нет;
// интерфейса нет — настройка отвечает ошибкой, снос/опускание — nil (снимать
// нечего, а `interface X …` по отсутствующему X создаёт X или пишет E).

var (
	_ router.OpkgTunProvisioner   = confirmingOpkgTun{}
	_ ndmsres.Commands            = proxyNDMSCommands{}
	_ router.DefaultRouteProvider = confirmingDefaultRoute{}
	_ router.SegmentNATProvider   = confirmingSegmentNAT{}
	_ ndmsres.Permitter           = confirmingPermitter{}
	_ api.OrphanIfaceNDMS         = confirmingOpkgTun{}
)

// errIfaceAbsent — отказ настройки по отсутствующему интерфейсу. Страж
// router.ErrIfaceAbsent: router.segmentGone считает исчезнувший сегмент дрейфом.
func errIfaceAbsent(name string) error {
	return fmt.Errorf("интерфейса %s %w", name, router.ErrIfaceAbsent)
}

// confirmSet — настройка существующего: нет — ошибка.
func confirmSet(ctx context.Context, ifaces *ndmsquery.InterfaceStore, name string, do func(ndmsquery.Confirmed) error) error {
	c, _, ok, err := ifaces.Confirm(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return errIfaceAbsent(name)
	}
	return do(c)
}

// confirmTeardown — снос/опускание: нет — nil, снимать нечего.
func confirmTeardown(ctx context.Context, ifaces *ndmsquery.InterfaceStore, name string, do func(ndmsquery.Confirmed) error) error {
	c, _, ok, err := ifaces.Confirm(ctx, name)
	if err != nil || !ok {
		return err
	}
	return do(c)
}

// confirmingOpkgTun — router.OpkgTunProvisioner, командная часть
// ndmsres.Commands и api.OrphanIfaceNDMS (наборы методов пересекаются,
// правило одно).
type confirmingOpkgTun struct {
	cmds   *ndmscommand.InterfaceCommands
	ifaces *ndmsquery.InterfaceStore
}

// CreateOpkgTunWithSecurityLevel — создание записи только без устройства
// opkgtunN (F569): живое устройство NDMS отвергнет C 0xcffd00a9. Сносить его
// здесь некому — у sing-box (fakeip/policy-tun) и прокси-ролей держатель tun
// свой процесс; отказ называет держателя, RCI не шлётся.
func (a confirmingOpkgTun) CreateOpkgTunWithSecurityLevel(ctx context.Context, name, description, securityLevel string) error {
	kernel, ok := ndms.KernelName(name)
	if !ok {
		return fmt.Errorf("create opkgtun %s: имя не OpkgTunN", name)
	}
	free, err := netdev.Absent(kernel)
	if err != nil {
		if !errors.Is(err, netdev.ErrPresent) {
			return fmt.Errorf("create opkgtun %s: %w", name, err)
		}
		if held := backend.FindTunHolder(kernel); held != nil {
			return fmt.Errorf("устройство %s ещё существует (держатель %s, pid %d) — запись %s не создать: %w", kernel, held.Comm, held.PID, name, err)
		}
		return fmt.Errorf("устройство %s ещё существует — запись %s не создать: %w", kernel, name, err)
	}
	_, err = a.cmds.CreateOpkgTunWithSecurityLevel(ctx, name, description, securityLevel, free)
	return err
}

func (a confirmingOpkgTun) DeleteOpkgTun(ctx context.Context, name string) error {
	return confirmTeardown(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.DeleteOpkgTun(ctx, c) })
}

func (a confirmingOpkgTun) SetDescription(ctx context.Context, name, description string) error {
	return confirmSet(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.SetDescription(ctx, c, description) })
}

func (a confirmingOpkgTun) SetSecurityLevel(ctx context.Context, name, level string) error {
	return confirmSet(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.SetSecurityLevel(ctx, c, level) })
}

func (a confirmingOpkgTun) SetIPGlobal(ctx context.Context, name string) error {
	return confirmSet(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.SetIPGlobal(ctx, c) })
}

func (a confirmingOpkgTun) ClearIPGlobal(ctx context.Context, name string) error {
	return confirmTeardown(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.ClearIPGlobal(ctx, c) })
}

func (a confirmingOpkgTun) SetAddress(ctx context.Context, name, address, mask string) error {
	return confirmSet(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.SetAddress(ctx, c, address, mask) })
}

func (a confirmingOpkgTun) ClearAddress(ctx context.Context, name string) error {
	return confirmTeardown(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.ClearAddress(ctx, c) })
}

func (a confirmingOpkgTun) SetIPv6Address(ctx context.Context, name, address string) error {
	return confirmSet(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.SetIPv6Address(ctx, c, address) })
}

func (a confirmingOpkgTun) ClearIPv6Address(ctx context.Context, name string) error {
	return confirmTeardown(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.ClearIPv6Address(ctx, c) })
}

func (a confirmingOpkgTun) SetMTU(ctx context.Context, name string, mtu int) error {
	return confirmSet(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.SetMTU(ctx, c, mtu) })
}

func (a confirmingOpkgTun) InterfaceUp(ctx context.Context, name string) error {
	return confirmSet(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.InterfaceUp(ctx, c) })
}

func (a confirmingOpkgTun) InterfaceDown(ctx context.Context, name string) error {
	return confirmTeardown(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.InterfaceDown(ctx, c) })
}

func (a confirmingOpkgTun) SetPermitAllACL(ctx context.Context, name string) error {
	return confirmSet(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.SetPermitAllACL(ctx, c) })
}

func (a confirmingOpkgTun) RemovePermitAllACL(ctx context.Context, name string) error {
	return confirmTeardown(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.RemovePermitAllACL(ctx, c) })
}

func (a confirmingOpkgTun) SetPermitAllACLv6(ctx context.Context, name string) error {
	return confirmSet(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.SetPermitAllACLv6(ctx, c) })
}

func (a confirmingOpkgTun) RemovePermitAllACLv6(ctx context.Context, name string) error {
	return confirmTeardown(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.cmds.RemovePermitAllACLv6(ctx, c) })
}

// confirmingDefaultRoute — router.DefaultRouteProvider.
type confirmingDefaultRoute struct {
	routes *ndmscommand.RouteCommands
	ifaces *ndmsquery.InterfaceStore
}

func (a confirmingDefaultRoute) SetDefaultRoute(ctx context.Context, name string) error {
	return confirmSet(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.routes.SetDefaultRoute(ctx, c) })
}

func (a confirmingDefaultRoute) RemoveDefaultRoute(ctx context.Context, name string) error {
	return confirmTeardown(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.routes.RemoveDefaultRoute(ctx, c) })
}

func (a confirmingDefaultRoute) SetIPv6DefaultRoute(ctx context.Context, name string) error {
	return confirmSet(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.routes.SetIPv6DefaultRoute(ctx, c) })
}

func (a confirmingDefaultRoute) RemoveIPv6DefaultRoute(ctx context.Context, name string) error {
	return confirmTeardown(ctx, a.ifaces, name, func(c ndmsquery.Confirmed) error { return a.routes.RemoveIPv6DefaultRoute(ctx, c) })
}

// confirmingSegmentNAT — router.SegmentNATProvider. Static NAT ссылается на
// два интерфейса — оба подтверждаются одним списком.
type confirmingSegmentNAT struct {
	nat    *ndmscommand.NATCommands
	ifaces *ndmsquery.InterfaceStore
}

func (a confirmingSegmentNAT) SetSegmentNAT(ctx context.Context, seg string) error {
	return confirmSet(ctx, a.ifaces, seg, func(c ndmsquery.Confirmed) error { return a.nat.SetSegmentNAT(ctx, c) })
}

func (a confirmingSegmentNAT) RemoveSegmentNAT(ctx context.Context, seg string) error {
	return confirmTeardown(ctx, a.ifaces, seg, func(c ndmsquery.Confirmed) error { return a.nat.RemoveSegmentNAT(ctx, c) })
}

func (a confirmingSegmentNAT) SetStaticNAT(ctx context.Context, seg, wan string) error {
	m, err := a.ifaces.ConfirmEach(ctx, []string{seg, wan})
	if err != nil {
		return err
	}
	s, ok := m[seg]
	if !ok {
		return errIfaceAbsent(seg)
	}
	w, ok := m[wan]
	if !ok {
		// Без стража ErrIfaceAbsent: router.segmentGone принял бы снятый WAN
		// за снятый сегмент и молча пропустил бы его (F563).
		return fmt.Errorf("WAN %s нет в NDMS", wan)
	}
	return a.nat.SetStaticNAT(ctx, s, w)
}

func (a confirmingSegmentNAT) RemoveStaticNAT(ctx context.Context, seg, wan string) error {
	m, err := a.ifaces.ConfirmEach(ctx, []string{seg, wan})
	if err != nil {
		return err
	}
	s, okS := m[seg]
	w, okW := m[wan]
	if !okS || !okW {
		return nil
	}
	return a.nat.RemoveStaticNAT(ctx, s, w)
}

// confirmingPermitter — ndmsres.Permitter: разрешение в политике — настройка.
type confirmingPermitter struct {
	policies *ndmscommand.PolicyCommands
	ifaces   *ndmsquery.InterfaceStore
}

func (a confirmingPermitter) PermitInterface(ctx context.Context, name, iface string, order int) error {
	return confirmSet(ctx, a.ifaces, iface, func(c ndmsquery.Confirmed) error { return a.policies.PermitInterface(ctx, name, c, order) })
}
