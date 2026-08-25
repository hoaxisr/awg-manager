// Package freeturn — декларации ролей FreeTurn-клиент и FreeTurn-сервер.
// Собственного каркаса нет: общие ресурсы, различия — данными (кандидат №2).
package freeturn

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/netres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/procres"
)

var errTypeMismatch = errors.New("конфигурация не FreeTurn*Config")

// ClientDeps — зависимости клиентской роли.
type ClientDeps struct {
	Instance     string
	Binary       string
	PinnedSHA256 string
	Link         procres.ProcessLink
	Runner       procres.ProcRunner
	Gate         procres.BinaryGate
	Sync         linkres.EndpointSync
	Occ          linkres.Occupancy
	Now          func() time.Time
}

// ClientRole: listen_port → process → linked_endpoint (§4.1; listen первым —
// приговор занятого порта до старта процесса). Ресурсы долгоживущие:
// создаются в NewClient один раз, Resources лишь обновляет их желаемое —
// пересоздание обнулило бы окно старта и backoff процесса (I5).
type ClientRole struct {
	listen *linkres.ListenPort
	proc   *procres.Proc
	linked *linkres.LinkedEndpoint
	inst   string
}

func NewClient(d ClientDeps) (*ClientRole, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	sock, err := control.SocketPath(roles.RuntimeDir, roles.ImplFtClient, roles.RoleClient, d.Instance)
	if err != nil {
		return nil, err
	}
	logPath, err := control.LogPath(roles.RuntimeDir, roles.ImplFtClient, roles.RoleClient, d.Instance)
	if err != nil {
		return nil, err
	}
	return &ClientRole{
		inst:   d.Instance,
		listen: linkres.NewListenPort(roles.RListenPort, d.Occ),
		proc: procres.NewProc(procres.ProcConfig{
			ID: roles.RProcess, Instance: d.Instance,
			Impl: roles.ImplFtClient, Role: roles.RoleClient,
			Binary: d.Binary, PinnedSHA256: d.PinnedSHA256,
			NeedCmds:   []string{"state"},
			SocketPath: sock, LogPath: logPath,
			Link: d.Link, Runner: d.Runner, Gate: d.Gate, Now: d.Now,
		}),
		linked: linkres.NewLinkedEndpoint(roles.RLinkedEndpoint, d.Sync),
	}, nil
}

func (r *ClientRole) Resources(intent proxyrt.Intent, cfg any, _ proxyrt.Observations) []proxyrt.Resource {
	c, ok := cfg.(roles.FreeTurnClientConfig)
	if !ok {
		r.proc.SetDesired(false, nil, errTypeMismatch)
		return []proxyrt.Resource{r.proc}
	}
	enabled := intent == proxyrt.IntentEnabled
	r.proc.SetDesired(enabled, roles.FreeTurnClientArgs(c), c.Validate())
	r.linked.SetDesired(r.inst, c.Listen, enabled)
	if !enabled {
		// У выключенного клиента ни доводки endpoint'ов, ни приговоров порта
		// (M11) — но linked_endpoint остаётся: он же опускает связанные
		// туннели. Без него туннель с адресом мёртвого процесса остаётся
		// «работающим» и тянет на себя маршруты (амендмент B).
		return []proxyrt.Resource{r.proc, r.linked}
	}
	r.listen.SetDesired(c.Listen)
	return []proxyrt.Resource{r.listen, r.proc, r.linked}
}

// ServerDeps — зависимости серверной роли.
type ServerDeps struct {
	Instance     string
	Binary       string
	PinnedSHA256 string
	Link         procres.ProcessLink
	Runner       procres.ProcRunner
	Gate         procres.BinaryGate
	FW           netres.FW
	Now          func() time.Time
}

// ServerRole: process → input_port (§4.1). Ресурсы долгоживущие: пересоздание
// InputPort обнулило бы ведомость разности (InputPort.prev) и прежний
// открытый порт перестал бы закрываться при смене порта или режима (I5).
type ServerRole struct {
	proc  *procres.Proc
	input *netres.InputPort
}

func NewServer(d ServerDeps) (*ServerRole, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	sock, err := control.SocketPath(roles.RuntimeDir, roles.ImplFtServer, roles.RoleServer, d.Instance)
	if err != nil {
		return nil, err
	}
	logPath, err := control.LogPath(roles.RuntimeDir, roles.ImplFtServer, roles.RoleServer, d.Instance)
	if err != nil {
		return nil, err
	}
	return &ServerRole{
		proc: procres.NewProc(procres.ProcConfig{
			ID: roles.RProcess, Instance: d.Instance,
			Impl: roles.ImplFtServer, Role: roles.RoleServer,
			Binary: d.Binary, PinnedSHA256: d.PinnedSHA256,
			NeedCmds:   []string{"state"},
			SocketPath: sock, LogPath: logPath,
			Link: d.Link, Runner: d.Runner, Gate: d.Gate, Now: d.Now,
		}),
		input: netres.NewInputPort(roles.RInputPort, d.FW),
	}, nil
}

func (r *ServerRole) Resources(intent proxyrt.Intent, cfg any, _ proxyrt.Observations) []proxyrt.Resource {
	c, ok := cfg.(roles.FreeTurnServerConfig)
	if !ok {
		r.proc.SetDesired(false, nil, errTypeMismatch)
		return []proxyrt.Resource{r.proc}
	}
	enabled := intent == proxyrt.IntentEnabled
	r.proc.SetDesired(enabled, roles.FreeTurnServerArgs(c), c.Validate())
	r.input.SetDesired(serverPorts(c, enabled))
	return []proxyrt.Resource{r.proc, r.input}
}

// serverPorts — протокол INPUT-правила следует за Mode, udp по умолчанию
// (serverListenPortSpec, freeturn/server_firewall.go:18-21).
func serverPorts(c roles.FreeTurnServerConfig, enabled bool) []netres.PortSpec {
	if !enabled || !c.OpenFirewall {
		return nil
	}
	// Локальный bind в firewall не открывается — паритет с
	// listenfirewall.WANListenPort (старый путь при Listen 127.0.0.1:3478
	// правила INPUT не ставил вовсе) и с wdttserver.wanPort соседней роли.
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(c.Listen))
	if err != nil {
		return nil
	}
	host = strings.TrimSpace(host)
	if host != "" && host != "0.0.0.0" && host != "::" && host != "[::]" {
		return nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil
	}
	proto := strings.ToLower(strings.TrimSpace(c.Mode))
	if proto == "" {
		proto = "udp"
	}
	return []netres.PortSpec{{Port: port, Proto: proto}}
}
