// Package wdttclient — декларация роли «WDTT-клиент» (режимы raw и wg).
// Единственная ведомость ресурсов роли: хвост активации raw-клиента, который
// старый код выписывал трижды по-разному, здесь существует один раз — как
// порядок списка Resources.
package wdttclient

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/ndmsres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/procres"
)

// rawClientMask — маску сервер не присылает, менеджер ставит свою константу
// (§5.2 протокола; DefaultRawClientMask старого мира, types.go:195).
const rawClientMask = "255.255.255.255"

// rawTunnelIDSafe — паритет wdtt.RawTunnelID (raw_tunnel_meta.go:18-34):
// санитайз и усечение обязаны совпадать ПОБАЙТОВО, иначе id разойдётся со
// старой зеркальной записью и правилами clientroute (M4 ревью).
var rawTunnelIDSafe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// RawTunnelID — id зеркальной записи tunnel-store и правил clientroute.
func RawTunnelID(instance string) string {
	id := strings.TrimSpace(instance)
	if id == "" {
		id = "default"
	}
	safe := rawTunnelIDSafe.ReplaceAllString(id, "-")
	safe = strings.Trim(safe, "-")
	if safe == "" {
		safe = "client"
	}
	if len(safe) > 20 {
		safe = safe[:20]
	}
	return "wdttraw-" + safe
}

// Deps — все зависимости роли, конструктором и только им (G4).
type Deps struct {
	Instance     string
	Binary       string
	PinnedSHA256 string
	Link         procres.TunLink
	Runner       procres.ProcRunner
	Gate         procres.BinaryGate
	Cmds         ndmsres.Commands
	Query        ndmsres.Query
	Policies     ndmsres.PolicyLister
	Permit       ndmsres.Permitter
	Hooks        linkres.RouteHooks
	Registry     linkres.ExitRegistry
	Sync         linkres.EndpointSync
	Occ          linkres.Occupancy
	Now          func() time.Time
}

// Role — реализация proxyrt.Role. Ресурсы — долгоживущие: защёлки
// (окно старта, backoff, уведомления clientroute) переживают прогоны.
type Role struct {
	deps Deps

	listen *linkres.ListenPort
	proc   *procres.Proc
	iface  *ndmsres.Iface
	addr   *ndmsres.Address
	admin  *ndmsres.AdminState
	exit   *ndmsres.PolicyExit
	tun    *procres.TunHandoff
	member *ndmsres.Membership
	routes *linkres.ClientRoutes
	rexit  *linkres.RoutableExit
	linked *linkres.LinkedEndpoint
}

// errTypeMismatch — чужой тип конфигурации: дефект проводки, а не роли.
var errTypeMismatch = errors.New("конфигурация не WdttClientConfig")

func New(d Deps) (*Role, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	sock, err := control.SocketPath(roles.RuntimeDir, roles.ImplWtClient, roles.RoleClient, d.Instance)
	if err != nil {
		return nil, err
	}
	logPath, err := control.LogPath(roles.RuntimeDir, roles.ImplWtClient, roles.RoleClient, d.Instance)
	if err != nil {
		return nil, err
	}
	r := &Role{deps: d}
	r.build(sock, logPath)
	return r, nil
}

func (r *Role) build(sock, logPath string) {
	d := r.deps
	r.listen = linkres.NewListenPort(roles.RListenPort, d.Occ)
	r.proc = procres.NewProc(procres.ProcConfig{
		ID: roles.RProcess, Instance: d.Instance,
		Impl: roles.ImplWtClient, Role: roles.RoleClient,
		Binary: d.Binary, PinnedSHA256: d.PinnedSHA256,
		// Гейт клиента требует attach-tun: состав commands по ролям
		// различается, серверам достаточно state.
		NeedCmds:   []string{"state", "attach-tun", "detach-tun"},
		SocketPath: sock, LogPath: logPath,
		Link: d.Link, Runner: d.Runner, Gate: d.Gate, Now: d.Now,
	})
	r.iface = ndmsres.NewIface(roles.RNdmsIface, d.Cmds, d.Query)
	r.addr = ndmsres.NewAddress(roles.RNdmsAddress, d.Cmds, d.Query)
	r.admin = ndmsres.NewAdminState(roles.RAdminState, d.Cmds, d.Query)
	r.exit = ndmsres.NewPolicyExit(roles.RPolicyExit, d.Cmds, d.Query)
	r.tun = procres.NewTunHandoff(roles.RTunHandoff, d.Link, procres.OpenTunFD, d.Now)
	r.member = ndmsres.NewMembership(roles.RPolicyMembership, d.Policies, d.Permit)
	r.routes = linkres.NewClientRoutes(roles.RClientRoutes, d.Hooks)
	r.rexit = linkres.NewRoutableExit(roles.RRoutableExit, d.Registry)
	r.linked = linkres.NewLinkedEndpoint(roles.RLinkedEndpoint, d.Sync)
}

// Resources — единственная ведомость роли. Порядок значим: список — цепочка
// «предпосылка → следствие», Blocked каскадится по ней (движок, план 1).
func (r *Role) Resources(intent proxyrt.Intent, cfg any, _ proxyrt.Observations) []proxyrt.Resource {
	c, ok := cfg.(roles.WdttClientConfig)
	if !ok {
		// Не наша конфигурация — процесс с приговором; состав минимальный.
		r.proc.SetDesired(false, nil, errTypeMismatch)
		return []proxyrt.Resource{r.proc}
	}
	enabled := intent == proxyrt.IntentEnabled
	r.proc.SetDesired(enabled, roles.WdttClientArgs(c), c.Validate())
	r.listen.SetDesired(c.Listen)
	// Желаемое связанных туннелей — РОВНО намерение владельца инстанса, и
	// одинаково в обоих режимах: связь ставится, пока клиент в режиме wg, а
	// смена режима на raw её не снимает (мастер просто не импортирует туннель,
	// запись со ссылкой остаётся), плюс есть ручной импорт с явным указанием
	// клиента. Старые ручки работали независимо от режима — их предикат
	// отсекал зеркала, а не режим.
	r.linked.SetDesired(r.deps.Instance, c.Listen, enabled)

	if c.Mode == "wg" {
		// §4.1 объявляет wg-клиента как process + linked_endpoint;
		// listen_port добавлен по факту кода (порт нужен всем клиентам,
		// service.go:583) — отступление зафиксировано в шапке задачи.
		// Disabled — процесс и linked_endpoint: доводка endpoint'ов и
		// приговоры порта у выключенного инстанса — реальные мутации без
		// нужды (M11), а вот ОПУСКАНИЕ связанных туннелей нужно, и делать
		// его больше некому. Без этого туннель с адресом мёртвого процесса
		// остаётся «работающим» и тянет на себя маршруты (амендмент B).
		if !enabled {
			return []proxyrt.Resource{r.proc, r.linked}
		}
		return []proxyrt.Resource{r.listen, r.proc, r.linked}
	}

	// raw. linked_endpoint стоит сразу за процессом, а не в хвосте: его
	// единственная предпосылка — живой процесс (endpoint связанного туннеля
	// это 127.0.0.1:<listen>). Хвост цепочки сделал бы предпосылками ресурсы
	// NDMS, и тогда медленный или отказавший RCI блокировал бы ОПУСКАНИЕ
	// связанного туннеля — ровно тот исход, ради которого амендмент писался.
	r.iface.SetDesired(ndmsres.IfaceDesired{
		Present: enabled, Name: c.NdmsIface,
		Description:   roles.ClientDescription(c.Name),
		SecurityLevel: "public", // public с создания — client_ndms.go:126-127
		MTU:           1300,     // только на create; владелец MTU — ndms_address (I2)
	})
	if enabled {
		r.addr.SetDesired(ndmsres.AddressDesired{Name: c.NdmsIface, Want: r.addrWant})
	} else {
		r.addr.SetDesired(ndmsres.AddressDesired{Name: c.NdmsIface, Clear: true})
	}
	r.admin.SetDesired(ndmsres.AdminDesired{Name: c.NdmsIface, Up: enabled})
	r.exit.SetDesired(ndmsres.PolicyExitDesired{
		Name: c.NdmsIface, SecurityLevel: "public",
		IPGlobal: true, PermitAllACL: true,
		DefaultCandidacy: true, // клиентский выход; стенд-гейт §13 — предусловие волны
	})
	r.tun.SetDesired(c.RawIface)
	refs := make([]ndmsres.PolicyRef, 0, len(c.Policies))
	for _, p := range c.Policies {
		refs = append(refs, ndmsres.PolicyRef{Name: p.Name, Order: p.Order})
	}
	r.member.SetDesired(c.NdmsIface, refs)
	r.routes.SetDesired(RawTunnelID(r.deps.Instance), c.RawIface, enabled)
	r.rexit.SetDesired(linkres.ExitInfo{
		ID: RawTunnelID(r.deps.Instance), NDMSName: c.NdmsIface,
		KernelIface: c.RawIface, Ready: enabled && r.processReady(),
	})

	if !enabled {
		// Та же ведомость, другое желаемое: процесс стоп, адрес снят, down,
		// clientroute уведомлён об остановке, выход не Ready. Интерфейс и
		// членство ЖИВУТ (уборка — только sweeper при deleted).
		return []proxyrt.Resource{
			r.proc, r.linked, r.iface, r.addr, r.admin, r.routes, r.rexit,
		}
	}
	return []proxyrt.Resource{
		r.listen, r.proc, r.linked, r.iface, r.addr, r.admin,
		r.exit, r.tun, r.member, r.routes, r.rexit,
	}
}

// ResetStartBackoff снимает у процесса роли паузу анти-флаппинга. Шов для
// явного действия пользователя: обновление подписки заменяет профиль, и
// клиент, который человек чинит руками, обязан пробовать старт сразу.
// Прод-значение подставляет проводка.
func (r *Role) ResetStartBackoff() { r.proc.ResetStartBackoff() }

// addrWant — желаемый адрес из наблюдения процесса (снимок Link, обновлён
// process-ресурсом этого же прогона). Спека §3: адрес выдаёт VPS, он не может
// быть намерением.
func (r *Role) addrWant() (ndmsres.AddrWant, bool, string) {
	snap, ok := r.deps.Link.Snapshot()
	if !ok || r.deps.Now().Sub(snap.At) > 30*time.Second {
		return ndmsres.AddrWant{}, false, "нет свежего снимка процесса"
	}
	if snap.State.Address == "" {
		return ndmsres.AddrWant{}, false, "адрес ещё не выдан сервером (ждём RAWCONF)"
	}
	mtu := snap.State.MTU
	if mtu < 576 {
		mtu = 1300 // паритет activateClientNDMSOpkgTun (client_ndms.go:142-145)
	}
	return ndmsres.AddrWant{Address: snap.State.Address, Mask: rawClientMask, MTU: mtu}, true, ""
}

func (r *Role) processReady() bool {
	snap, ok := r.deps.Link.Snapshot()
	if !ok {
		return false
	}
	return snap.State.Address != "" && snap.State.Tun != nil && snap.State.Tun.Attached
}
