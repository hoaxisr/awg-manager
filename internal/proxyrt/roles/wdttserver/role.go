// Package wdttserver — декларация роли «WDTT-сервер»: обе NDMS-половины
// (WG opkgtunN + raw opkgtunM), netfilter-обвязка raw-абонентов, NDMS-доступ
// WG-абонентов, ingress-refs и INPUT-порты. Единственная ведомость ресурсов.
package wdttserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/ndmsres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/netres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/procres"
)

// Адресные константы NDMS-половин (internal/wdtt/types.go:179-195).
const (
	wgGatewayAddr  = "10.66.0.1"
	wgGatewayMask  = "255.255.0.0"
	rawGatewayAddr = "10.70.0.1"
	rawGatewayMask = "255.255.0.0"
	rawPeerCIDR    = "10.70.0.0/16"
	rawProcessAddr = "10.70.66.1" // адрес самого wdtt-server на raw-TUN
	wgMTU          = 1280
	rawMTU         = 1300
)

var errTypeMismatch = errors.New("конфигурация не WdttServerConfig")

// Deps — зависимости роли (G4: всё в конструкторе).
type Deps struct {
	Instance      string
	Binary        string
	PinnedSHA256  string
	Link          procres.TunLink
	Runner        procres.ProcRunner
	Gate          procres.BinaryGate
	Cmds          ndmsres.Commands
	Query         ndmsres.Query
	IPT           netres.IPT
	FW            netres.FW
	RunHook       func(ctx context.Context, path, table string) error
	EnableForward func() error
	// IfaceExists — kernel-интерфейс жив (InterfaceChecker.InterfaceExists):
	// адрес NDMS ставится только после появления netdev от процесса — NDMS-
	// адрес без kernel-адреса крутит nginx-reload вечно (PR #544).
	IfaceExists func(name string) bool
	// KernelWAN — NDMS-имя WAN → kernel-имя (для internet-only MASQUERADE).
	KernelWAN func(ctx context.Context, ndmsName string) (string, error)
	// PolicyMark — fwmark политики для raw-половины (policy != none).
	PolicyMark func(ctx context.Context, policy string) (string, error)
	Access     AccessApplier
	Ingress    IngressEnsurer
	Now        func() time.Time
}

// Role — реализация proxyrt.Role для сервера. Ресурсы долгоживущие:
// создаются в New один раз, Resources лишь обновляет их желаемое. Ведомость
// разности (RuleSet.doomed, InputPort.prev) и защёлки (ndms_access,
// ingress_refs) живут в полях объектов — пересоздание обнулило бы их, и
// правила прежнего желаемого перестали бы сноситься (класс H1, PR #697).
type Role struct {
	deps Deps

	ifaceWG  *ndmsres.Iface
	ifaceRaw *ndmsres.Iface
	proc     *procres.Proc
	tunWG    *procres.TunHandoff
	tunRaw   *procres.TunHandoff
	addrWG   *ndmsres.Address
	adminWG  *ndmsres.AdminState
	addrRaw  *ndmsres.Address
	adminRaw *ndmsres.AdminState
	exit     *ndmsres.PolicyExit
	access   *NDMSAccess
	nat      *netres.RuleSet
	fwd      *netres.RuleSet
	mss      *netres.MSSClamp
	hook     *netres.Hook
	ingress  *IngressRefs
	input    *netres.InputPort
}

func New(d Deps) (*Role, error) {
	// G4: NDMSAccess.Apply и IngressRefs.Apply дереференсят зависимость без
	// nil-гарда — непроведённый производитель это паника на первом прогоне
	// инстанса, а не деградация. Форма — instance.New.
	if d.Access == nil || d.Ingress == nil {
		panic("wdttserver.New: неполные зависимости (G4)")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	sock, err := control.SocketPath(roles.RuntimeDir, roles.ImplWdttServer, roles.RoleServer, d.Instance)
	if err != nil {
		return nil, err
	}
	logPath, err := control.LogPath(roles.RuntimeDir, roles.ImplWdttServer, roles.RoleServer, d.Instance)
	if err != nil {
		return nil, err
	}
	r := &Role{deps: d}
	r.ifaceWG = ndmsres.NewIface(roles.Sub(roles.RNdmsIface, "wg"), d.Cmds, d.Query)
	r.ifaceRaw = ndmsres.NewIface(roles.Sub(roles.RNdmsIface, "raw"), d.Cmds, d.Query)
	r.proc = procres.NewProc(procres.ProcConfig{
		ID: roles.RProcess, Instance: d.Instance,
		Impl: roles.ImplWdttServer, Role: roles.RoleServer,
		Binary: d.Binary, PinnedSHA256: d.PinnedSHA256,
		// Обе половины сервера работают на TUN, который создал NDMS и передал
		// менеджер: без attach-tun бинарь поднимет своё устройство поверх
		// чужого имени, и после его выхода запись NDMS осиротеет.
		NeedCmds:   []string{"state", "attach-tun", "detach-tun"},
		SocketPath: sock, LogPath: logPath,
		Link: d.Link, Runner: d.Runner, Gate: d.Gate, Now: d.Now,
	})
	r.tunWG = procres.NewTunHandoff(roles.Sub(roles.RTunHandoff, "wg"), d.Link, procres.OpenTunFD, d.Now)
	r.tunRaw = procres.NewTunHandoff(roles.Sub(roles.RTunHandoff, "raw"), d.Link, procres.OpenTunFD, d.Now)
	r.addrWG = ndmsres.NewAddress(roles.Sub(roles.RNdmsAddress, "wg"), d.Cmds, d.Query)
	r.adminWG = ndmsres.NewAdminState(roles.Sub(roles.RAdminState, "wg"), d.Cmds, d.Query)
	r.addrRaw = ndmsres.NewAddress(roles.Sub(roles.RNdmsAddress, "raw"), d.Cmds, d.Query)
	r.adminRaw = ndmsres.NewAdminState(roles.Sub(roles.RAdminState, "raw"), d.Cmds, d.Query)
	r.exit = ndmsres.NewPolicyExit(roles.RPolicyExit, d.Cmds, d.Query)
	r.access = NewNDMSAccess(roles.RNdmsAccess, d.Access)
	r.nat = netres.NewRuleSet(roles.RNatRules, d.IPT, nil)
	r.fwd = netres.NewRuleSet(roles.RForwardRules, d.IPT, d.EnableForward)
	r.mss = netres.NewMSSClamp(roles.RMssClamp, d.IPT)
	r.hook = netres.NewHook(roles.RNetfilterHook, netres.HookPath, d.RunHook)
	r.ingress = NewIngressRefs(roles.RIngressRefs, d.Ingress)
	r.input = netres.NewInputPort(roles.RInputPort, d.FW)
	return r, nil
}

// ResetStartBackoff снимает у процесса роли паузу повторного старта. Зовёт её
// единственная точка правки записи — manager.Update (proxyrt.BackoffResetter).
func (r *Role) ResetStartBackoff() { r.proc.ResetStartBackoff() }

var _ proxyrt.BackoffResetter = (*Role)(nil)

func (r *Role) Resources(intent proxyrt.Intent, cfg any, _ proxyrt.Observations) []proxyrt.Resource {
	c, ok := cfg.(roles.WdttServerConfig)
	if !ok {
		r.proc.SetDesired(false, nil, errTypeMismatch)
		return []proxyrt.Resource{r.proc}
	}
	enabled := intent == proxyrt.IntentEnabled
	level := "private"
	if c.ExposeToPolicies {
		level = "public"
	}
	cfgErr := c.Validate()
	r.proc.SetDesired(enabled, roles.WdttServerArgs(c), cfgErr)
	if enabled && cfgErr != nil {
		// Конфиг заведомо нерабочий: ведомость — один процесс с приговором.
		// Обе NDMS-половины объявлены ВЫШЕ процесса, и без этого гейта
		// роутеру уезжали бы два create OpkgTun прежде, чем причина дойдёт до
		// пользователя (I3 ревью). Выключенный инстанс гейта не знает: там
		// желаемое — снятие, и его надо доводить на любом конфиге.
		return []proxyrt.Resource{r.proc}
	}
	r.ifaceWG.SetDesired(ndmsres.IfaceDesired{Present: enabled, Name: c.NdmsIface,
		Description: roles.LabelServerWG, SecurityLevel: level, MTU: wgMTU})
	r.ifaceRaw.SetDesired(ndmsres.IfaceDesired{Present: enabled, Name: c.RawNdmsIface,
		Description: roles.LabelServerRaw, SecurityLevel: level, MTU: rawMTU})

	r.addrWG.SetDesired(r.addrDesired(enabled, c.NdmsIface, c.WgIface, wgGatewayAddr, wgGatewayMask, wgMTU))
	r.adminWG.SetDesired(ndmsres.AdminDesired{Name: c.NdmsIface, Up: enabled})
	r.addrRaw.SetDesired(r.addrDesired(enabled, c.RawNdmsIface, c.RawIface, rawGatewayAddr, rawGatewayMask, rawMTU))
	r.adminRaw.SetDesired(ndmsres.AdminDesired{Name: c.RawNdmsIface, Up: enabled})

	r.exit.SetDesired(ndmsres.PolicyExitDesired{Name: c.NdmsIface,
		SecurityLevel: "public", IPGlobal: true, PermitAllACL: true,
		DefaultCandidacy: false}) // сервер — вход, кандидатуры нет
	r.access.SetDesired(c.NdmsIface, c.NatMode, c.StaticNATList(), c.Policy,
		wgGatewayAddr, wgGatewayMask, c.LanSegments, enabled)

	if enabled {
		r.nat.SetDesired(r.natGroups(c))
		r.fwd.SetDesired(netres.StaticGroups(netres.ForwardGroups([]string{c.RawIface})))
		r.mss.SetDesired([]string{rawPeerCIDR})
		r.hook.SetDesired(r.hookGroups(c))
		r.input.SetDesired(inputPorts(c))
	} else {
		// Опустевшее желаемое — снятие тем же механизмом (G1).
		r.nat.SetDesired(nil)
		r.fwd.SetDesired(nil)
		r.mss.SetDesired(nil)
		r.hook.SetDesired(nil)
		r.input.SetDesired(nil)
	}
	r.ingress.SetDesired(c.WgIface, c.RawIface, enabled)

	r.tunWG.SetDesired(c.WgIface)
	r.tunRaw.SetDesired(c.RawIface)

	// Дескрипторы передаются ПОСЛЕ старта процесса и ДО адресов: половины
	// сервера ждут их, чтобы подняться, а адрес ставится на живой интерфейс.
	res := []proxyrt.Resource{r.ifaceWG, r.ifaceRaw, r.proc}
	if enabled {
		res = append(res, r.tunWG, r.tunRaw)
	}
	res = append(res, r.addrWG, r.adminWG, r.addrRaw, r.adminRaw)
	if enabled && c.ExposeToPolicies {
		res = append(res, r.exit)
	}
	return append(res, r.access, r.nat, r.fwd, r.mss, r.hook, r.ingress, r.input)
}

// addrDesired: адрес ставится только когда kernel-интерфейс от процесса жив —
// иначе NDMS-адрес без kernel-адреса (PR #544). disabled — адрес снят.
func (r *Role) addrDesired(enabled bool, ndmsName, kernel, addr, mask string, mtu int) ndmsres.AddressDesired {
	if !enabled {
		return ndmsres.AddressDesired{Name: ndmsName, Clear: true}
	}
	return ndmsres.AddressDesired{Name: ndmsName, Want: func() (ndmsres.AddrWant, bool, string) {
		if !r.deps.IfaceExists(kernel) {
			return ndmsres.AddrWant{}, false, "kernel-интерфейс " + kernel + " ещё не создан процессом"
		}
		return ndmsres.AddrWant{Address: addr, Mask: mask, MTU: mtu}, true, ""
	}}
}

// natGroups — MASQUERADE + DNS-перехват + policy-mark; провайдер, потому что
// internet-only требует разрешить kernel-имя WAN в момент наблюдения.
func (r *Role) natGroups(c roles.WdttServerConfig) netres.GroupProvider {
	return func(ctx context.Context) ([]netres.Group, error) {
		if c.NatMode == "none" {
			return nil, nil
		}
		wanDev := ""
		if c.NatMode == "internet-only" {
			wans := c.StaticNATList()
			if len(wans) == 0 {
				return nil, fmt.Errorf("internet-only: WAN не выбран (natStaticWANs пуст)")
			}
			// Своя MASQUERADE-цепочка на raw-половине умеет ровно один
			// выходной интерфейс, поэтому здесь берётся первый. Список целиком
			// нужен NDMS static-NAT (SetDesired выше): там правило ставится на
			// КАЖДЫЙ ip global. Расхождение осознанное — MasqGroups под
			// несколько выходов не рассчитан.
			dev, err := r.deps.KernelWAN(ctx, wans[0])
			if err != nil || dev == "" {
				return nil, fmt.Errorf("internet-only: WAN %q не разрешён: %v", wans[0], err)
			}
			wanDev = dev
		}
		groups := netres.MasqGroups(
			[]netres.MasqPlan{{Iface: c.RawIface, CIDR: rawPeerCIDR}}, c.NatMode, wanDev)
		dns := []netres.DNSHijack{{Iface: c.RawIface, Gateway: rawProcessAddr}}
		if c.RelayMode == "wg" {
			dns = append(dns, netres.DNSHijack{Iface: c.WgIface, Gateway: wgGatewayAddr})
		}
		groups = append(groups, netres.DNSGroups(dns)...)
		if c.Policy != "" && c.Policy != "none" {
			mark, err := r.deps.PolicyMark(ctx, c.Policy)
			if err != nil {
				return nil, fmt.Errorf("policy mark %s: %w", c.Policy, err)
			}
			if mark != "" {
				groups = append(groups, netres.PolicyMarkGroup(c.RawIface, mark))
			}
		}
		return groups, nil
	}
}

// hookGroups — хук несёт ТЕ ЖЕ правила: FORWARD + NAT/DNS/mark.
func (r *Role) hookGroups(c roles.WdttServerConfig) netres.GroupProvider {
	nat := r.natGroups(c)
	return func(ctx context.Context) ([]netres.Group, error) {
		groups, err := nat(ctx)
		if err != nil {
			return nil, err
		}
		if len(groups) == 0 {
			return nil, nil
		}
		return append(netres.ForwardGroups([]string{c.RawIface}), groups...), nil
	}
}

// inputPorts — WAN-порты сервера (паритет serverFirewallPortSpecs,
// server_firewall.go:9-27): DTLS, raw (DTLS+1 либо явный), direct.
func inputPorts(c roles.WdttServerConfig) []netres.PortSpec {
	if !c.OpenFirewall {
		return nil
	}
	var out []netres.PortSpec
	add := func(addr string) {
		if p, ok := wanPort(addr); ok {
			out = append(out, netres.PortSpec{Port: p, Proto: "udp"})
		}
	}
	add(c.Listen)
	add(c.EffectiveRawListen())
	if d := c.DirectListen; d != "" && d != c.Listen {
		add(d)
	}
	return dedupePorts(out)
}

// wanPort — порт нужен в INPUT, только если listen смотрит наружу (паритет
// listenfirewall.WANListenPort, firewall.go:28-45): локальный bind правила не
// требует, и это не ошибка, а законный исход — отсюда bool, а не error.
func wanPort(addr string) (int, bool) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return 0, false
	}
	host = strings.TrimSpace(host)
	if host != "" && host != "0.0.0.0" && host != "::" && host != "[::]" {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// dedupePorts — по паре (порт, протокол): DTLS и direct-listen часто совпадают.
func dedupePorts(in []netres.PortSpec) []netres.PortSpec {
	seen := make(map[string]bool, len(in))
	var out []netres.PortSpec
	for _, s := range in {
		key := s.Proto + "/" + strconv.Itoa(s.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}
