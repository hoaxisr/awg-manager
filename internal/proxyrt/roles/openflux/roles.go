// Package openflux — декларации ролей OpenFlux-клиента и выходной ноды.
// Собственного каркаса нет: общие ресурсы, различия — данными (кандидат №2,
// паритет с roles/freeturn).
//
// У OpenFlux нет ни связанных AWG-туннелей (upstream не говорит на
// WireGuard), ни NDMS-интерфейсов (вход клиента — SOCKS5 на loopback), ни
// входящих портов (клиент и выход соединяются ЧЕРЕЗ релей, оба ходят наружу).
// Поэтому роль клиента — listen_port + process, роль выхода — process плюс
// (только l3) OUTPUT-правило против kernel-RST.
package openflux

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/netres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/procres"
)

var errTypeMismatch = errors.New("конфигурация не OpenFlux*Config")

// ClientDeps — зависимости клиентской роли.
type ClientDeps struct {
	Instance     string
	Binary       string
	PinnedSHA256 string
	Link         procres.ProcessLink
	Runner       procres.ProcRunner
	Gate         procres.BinaryGate
	Occ          linkres.Occupancy
	Now          func() time.Time
}

// ClientRole: listen_port → process (§4.1; listen первым — приговор занятого
// порта до старта процесса). Ресурсы долгоживущие: создаются в NewClient один
// раз, Resources лишь обновляет их желаемое — пересоздание обнулило бы окно
// старта и backoff процесса (I5).
type ClientRole struct {
	listen *linkres.ListenPort
	proc   *procres.Proc
	inst   string
}

func NewClient(d ClientDeps) (*ClientRole, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	sock, err := control.SocketPath(roles.RuntimeDir, roles.ImplOfClient, roles.RoleClient, d.Instance)
	if err != nil {
		return nil, err
	}
	logPath, err := control.LogPath(roles.RuntimeDir, roles.ImplOfClient, roles.RoleClient, d.Instance)
	if err != nil {
		return nil, err
	}
	return &ClientRole{
		inst:   d.Instance,
		listen: linkres.NewListenPort(roles.RListenPort, d.Occ),
		proc: procres.NewProc(procres.ProcConfig{
			ID: roles.RProcess, Instance: d.Instance,
			Impl: roles.ImplOfClient, Role: roles.RoleClient,
			Binary: d.Binary, PinnedSHA256: d.PinnedSHA256,
			NeedCmds:   []string{"state"},
			SocketPath: sock, LogPath: logPath,
			Link: d.Link, Runner: d.Runner, Gate: d.Gate, Now: d.Now,
		}),
	}, nil
}

// ResetStartBackoff снимает у процесса роли паузу повторного старта. Зовёт её
// единственная точка правки записи — manager.Update (proxyrt.BackoffResetter).
func (r *ClientRole) ResetStartBackoff() { r.proc.ResetStartBackoff() }

var _ proxyrt.BackoffResetter = (*ClientRole)(nil)

func (r *ClientRole) Resources(intent proxyrt.Intent, cfg any, _ proxyrt.Observations) []proxyrt.Resource {
	c, ok := cfg.(roles.OpenFluxClientConfig)
	if !ok {
		r.proc.SetDesired(false, nil, errTypeMismatch)
		return []proxyrt.Resource{r.proc}
	}
	enabled := intent == proxyrt.IntentEnabled
	r.proc.SetDesired(enabled, roles.OpenFluxClientArgs(c), c.Validate())
	if !enabled {
		return []proxyrt.Resource{r.proc}
	}
	r.listen.SetDesired(c.Listen)
	return []proxyrt.Resource{r.listen, r.proc}
}

// ServerDeps — зависимости роли выходной ноды.
type ServerDeps struct {
	Instance     string
	Binary       string
	PinnedSHA256 string
	Link         procres.ProcessLink
	Runner       procres.ProcRunner
	Gate         procres.BinaryGate
	IPT          netres.IPT
	Now          func() time.Time
}

// ServerRole: process плюс (только l3) rst_drop_rule плюс (только включённый
// SingboxRoute) singbox_jump. Ресурсы долгоживущие — та же причина, что у
// клиента (I5): пересоздание стёрло бы ведомость разности, и прежнее правило
// перестало бы сниматься при смене режима.
type ServerRole struct {
	proc *procres.Proc
	rst  *rstDrop
	jump *singboxJump
}

func NewServer(d ServerDeps) (*ServerRole, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	sock, err := control.SocketPath(roles.RuntimeDir, roles.ImplOfServer, roles.RoleServer, d.Instance)
	if err != nil {
		return nil, err
	}
	logPath, err := control.LogPath(roles.RuntimeDir, roles.ImplOfServer, roles.RoleServer, d.Instance)
	if err != nil {
		return nil, err
	}
	return &ServerRole{
		proc: procres.NewProc(procres.ProcConfig{
			ID: roles.RProcess, Instance: d.Instance,
			Impl: roles.ImplOfServer, Role: roles.RoleServer,
			Binary: d.Binary, PinnedSHA256: d.PinnedSHA256,
			NeedCmds:   []string{"state"},
			SocketPath: sock, LogPath: logPath,
			Link: d.Link, Runner: d.Runner, Gate: d.Gate, Now: d.Now,
		}),
		rst:  newRSTDrop(roles.ROutputRule, d.IPT),
		jump: newSingboxJump(roles.Sub(roles.ROutputRule, "singbox"), d.IPT),
	}, nil
}

// ResetStartBackoff снимает у процесса роли паузу повторного старта.
func (r *ServerRole) ResetStartBackoff() { r.proc.ResetStartBackoff() }

var _ proxyrt.BackoffResetter = (*ServerRole)(nil)

func (r *ServerRole) Resources(intent proxyrt.Intent, cfg any, _ proxyrt.Observations) []proxyrt.Resource {
	c, ok := cfg.(roles.OpenFluxServerConfig)
	if !ok {
		r.proc.SetDesired(false, nil, errTypeMismatch)
		return []proxyrt.Resource{r.proc}
	}
	enabled := intent == proxyrt.IntentEnabled
	r.proc.SetDesired(enabled, roles.OpenFluxServerArgs(c), c.Validate())
	// Правило против kernel-RST — ТОЛЬКО l3 (в l4 терминирует gVisor, ядро
	// туннельных соединений не видит). Validate в l3 требует LocalIP, так что
	// пустое значение здесь означает лишь выключенный инстанс.
	if enabled && c.NormalizedMode() == roles.OpenFluxModeL3 {
		r.rst.SetDesired(c.LocalIP)
	} else {
		r.rst.SetDesired("")
	}
	// OUTPUT-прыжок в AWGM-REDIRECT — ТОЛЬКО включённый SingboxRoute (l4).
	r.jump.SetDesired(enabled && c.SingboxRoute)
	return []proxyrt.Resource{r.proc, r.rst, r.jump}
}

// rstDrop — ресурс rst_drop_rule: OUTPUT-правило против kernel-RST в режиме
// l3 (README OpenFlux, «l3 и kernel-RST»). Ядро видит ответные пакеты
// соединений, которые оно не открывало, и рвёт туннель своими RST; правило
// дропает ИСХОДЯЩИЕ RST с egress-адреса ноды, не трогая остальной трафик
// роутера. Содержание и снятие — одна форма (netres.Rule), ведомость
// разности — по Key, паритет с MSSClamp.
type rstDrop struct {
	id   proxyrt.ResourceID
	ipt  netres.IPT
	addr string
}

func newRSTDrop(id proxyrt.ResourceID, ipt netres.IPT) *rstDrop {
	return &rstDrop{id: id, ipt: ipt}
}

// SetDesired — egress-адрес правила; пусто = правила быть не должно.
func (r *rstDrop) SetDesired(addr string) { r.addr = addr }

func (r *rstDrop) ID() proxyrt.ResourceID { return r.id }

// rule — форма правила для -C/-I/-D разом. Поля в save-порядке ipt_entry
// (-s до -p), комментарий — метка владения в живом листинге.
func (r *rstDrop) rule() netres.Rule {
	return netres.Rule{
		Chain: "OUTPUT",
		Spec: []string{
			"-s", r.addr,
			"-p", "tcp",
			"--tcp-flags", "RST", "RST",
			"-m", "comment", "--comment", "awgm-openflux",
			"-j", "DROP",
		},
	}
}

func (r *rstDrop) Observe(ctx context.Context) (proxyrt.Observation, error) {
	if r.addr == "" {
		return proxyrt.Observation{Known: true, Exists: true, Detail: "правило не нужно"}, nil
	}
	exists := r.ipt.Run(ctx, r.rule().CheckArgs()...) == nil
	return proxyrt.Observation{Known: true, Exists: exists}, nil
}

func (r *rstDrop) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if obs.Exists {
		return nil
	}
	return []proxyrt.Step{{Resource: r.id, Op: "ensure", Reason: "RST-drop не собран"}}
}

func (r *rstDrop) Apply(ctx context.Context, s proxyrt.Step) error {
	if s.Op != "ensure" {
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
	// Идемпотентная форма MSSClamp: вставляем ТОЛЬКО на ruleAbsent, отказ
	// «не похожий на отсутствие правила» не маскируем (F347).
	err := r.ipt.Run(ctx, r.rule().CheckArgs()...)
	if err == nil {
		return nil
	}
	if !netres.RuleAbsent(err) {
		return err
	}
	return r.ipt.Run(ctx, r.rule().InsertArgs()...)
}

func (r *rstDrop) RecheckAfter() time.Duration {
	if r.addr == "" {
		return 0
	}
	return netres.RuleRecheckAfter
}

// singboxJump — ресурс singbox_jump: OUTPUT-правило, направляющее помеченные
// (--fwmark) TCP-пакеты данных-сокетов ноды в цепочку AWGM-REDIRECT sing-box
// (nat OUTPUT; TPROXY в OUTPUT невозможен, REDIRECT — канонический путь для
// локально-порождённого TCP). Цепочку ведёт sing-box-роутер awg-manager: его
// содержимое — та же маршрутизация, что и у ingress-трафика LAN. Роль только
// прыгает в неё и не разбирает содержимое; UDP и транспорт до релея не метятся
// и идут напрямую.
type singboxJump struct {
	id   proxyrt.ResourceID
	ipt  netres.IPT
	want bool
}

func newSingboxJump(id proxyrt.ResourceID, ipt netres.IPT) *singboxJump {
	return &singboxJump{id: id, ipt: ipt}
}

// SetDesired — включён ли перехват sing-box.
func (r *singboxJump) SetDesired(on bool) { r.want = on }

func (r *singboxJump) ID() proxyrt.ResourceID { return r.id }

// rule — форма правила для -C/-I/-D разом. Вставка на позицию 1: OUTPUT
// роутера содержит чужие ACCEPT'ы, и наш прыжок обязан отрабатывать раньше
// их. Поля в save-порядке ipt_entry.
func (r *singboxJump) rule() netres.Rule {
	return netres.Rule{
		Table: "nat",
		Chain: "OUTPUT",
		Pos:   1,
		Spec: []string{
			"-p", "tcp",
			"-m", "mark", "--mark", strconv.Itoa(roles.OpenFluxSingboxMark),
			"-j", netres.SingboxRedirectChain,
		},
	}
}

// chainReady — жива ли цепочка sing-box. Отсутствие означает, что sing-box
// не запущен или работает без перехвата (policy-tun): прыгать некуда.
func (r *singboxJump) chainReady(ctx context.Context) bool {
	_, err := r.ipt.Output(ctx, "-t", "nat", "-S", netres.SingboxRedirectChain)
	return err == nil
}

func (r *singboxJump) Observe(ctx context.Context) (proxyrt.Observation, error) {
	if !r.want {
		return proxyrt.Observation{Known: true, Exists: true, Detail: "перехват sing-box выключен"}, nil
	}
	exists := r.ipt.Run(ctx, r.rule().CheckArgs()...) == nil
	return proxyrt.Observation{Known: true, Exists: exists,
		Attrs: map[string]string{"chain": strconv.FormatBool(r.chainReady(ctx))}}, nil
}

func (r *singboxJump) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if obs.Exists {
		return nil
	}
	return []proxyrt.Step{{Resource: r.id, Op: "ensure", Reason: "OUTPUT-прыжок в sing-box не собран"}}
}

func (r *singboxJump) Apply(ctx context.Context, s proxyrt.Step) error {
	if s.Op != "ensure" {
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
	if !r.chainReady(ctx) {
		// Fail-closed: правило без цепочки молча никуда бы не вёл, а судить
		// об исправности тумблера пользователь должен по фазе, а не по tcpdump.
		return fmt.Errorf("цепочка %s не найдена — sing-box не запущен или работает без перехвата",
			netres.SingboxRedirectChain)
	}
	err := r.ipt.Run(ctx, r.rule().CheckArgs()...)
	if err == nil {
		return nil
	}
	if !netres.RuleAbsent(err) {
		return err
	}
	return r.ipt.Run(ctx, r.rule().InsertArgs()...)
}

func (r *singboxJump) RecheckAfter() time.Duration {
	if !r.want {
		return 0
	}
	return netres.RuleRecheckAfter
}
