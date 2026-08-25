package procres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
)

// observeTimeout — собственный срок наблюдения. ОБЯЗАН быть ≥ CallTimeout
// (5 с): с более коротким сроком распознавание зависшего процесса не работает
// никогда (док-строка control/link.go, §7 протокола).
const observeTimeout = 6 * time.Second

// socketGrace — окно «процесс стартовал, сокета ещё нет». Равно
// ConnectDeadline (§7); стенд mips 2026-08-17: сокет поднимается за 4-5 с.
const socketGrace = 20 * time.Second

// socketWaitRecheck — будильник в окне старта: событий от процесса до
// появления сокета не бывает, разбудить реконсиляцию больше некому.
const socketWaitRecheck = 3 * time.Second

// snapMaxAge — свежесть снимка для соседних ресурсов (tun_handoff, адрес).
const snapMaxAge = 30 * time.Second

// Параметры анти-флаппинга: старт wdtt-сервера тянет NDMS/RCI-вызовы, повтор
// на каждом событии бессмысленно грузит роутер. Свой счётчик, а не
// proxysup.Backoff: тому нечем ответить «когда можно», а RecheckAfter обязан
// вернуть остаток паузы (иначе инстанс в failed никто не разбудит);
// proxysup умирает вместе с супервизорами в плане 5.
const (
	backoffBase = 5 * time.Second
	backoffMax  = 5 * time.Minute
)

// ProcessLink — срез control.Link, нужный ресурсу process.
type ProcessLink interface {
	State(ctx context.Context) (awgmproto.State, error)
	Snapshot() (control.Snapshot, bool)
}

// ProcRunner — срез Runner.
type ProcRunner interface {
	Start(ctx context.Context, args []string) (int, error)
	Stop(ctx context.Context, pid int) error
	AlivePID() (int, bool)
}

// BinaryGate — срез Gate.
type BinaryGate interface {
	Check(ctx context.Context, binary, impl, role string, needCmds []string) error
}

// ProcConfig — все зависимости ресурса, в конструкторе и только в нём (G4).
type ProcConfig struct {
	ID       proxyrt.ResourceID
	Instance string
	Impl     string
	Role     string
	Binary   string
	// PinnedSHA256 — пин бинаря; пусто = не сверять. Пустой binary_sha256 в
	// state означает «неизвестно», а не «не совпало» (§5.2) — шага не даёт.
	PinnedSHA256 string
	NeedCmds     []string
	SocketPath   string
	LogPath      string
	Link         ProcessLink
	Runner       ProcRunner
	Gate         BinaryGate
	Now          func() time.Time
}

// Proc — ресурс process: один тип на все четыре роли, различия — данными.
// Долгоживущий: живёт вместе с ролью, переживает прогоны (окно старта и
// backoff — состояние между прогонами).
type Proc struct {
	c ProcConfig

	// Желаемое; обновляет роль перед каждым прогоном (SetDesired).
	enabled  bool
	forkArgs []string
	wantHash string
	cfgErr   error

	// Состояние между прогонами. Гонок нет: воркер инстанса один — кроме
	// паузы анти-флаппинга, у неё есть второй писатель (см. bmu).
	spawnedAt *time.Time
	// unreachSince — начало окна переподключения при живом pid БЕЗ нашего
	// свежего старта (усыновлённый процесс, разрыв соединения): §7 требует
	// ретраев до 20 с прежде, чем выносить вердикт (I3 ревью).
	unreachSince *time.Time

	// bmu защищает ТОЛЬКО fails/nextAllowed. Остальные поля живут в одной
	// горутине воркера, а паузу анти-флаппинга снимает ещё и явное действие
	// пользователя (ResetStartBackoff) — оно приходит из чужой горутины.
	bmu         sync.Mutex
	fails       int
	nextAllowed time.Time
}

func NewProc(cfg ProcConfig) *Proc {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Proc{c: cfg}
}

// SetDesired — намерение этого прогона. cfgErr — вердикт Validate() конфига:
// невалидное намерение не запускает процесс, а выносит приговор с причиной.
func (p *Proc) SetDesired(enabled bool, forkArgs []string, cfgErr error) {
	p.enabled = enabled
	p.forkArgs = forkArgs
	p.wantHash = awgmproto.ConfigHash(forkArgs)
	p.cfgErr = cfgErr
}

func (p *Proc) ID() proxyrt.ResourceID { return p.c.ID }

func (p *Proc) Observe(ctx context.Context) (proxyrt.Observation, error) {
	octx, cancel := context.WithTimeout(ctx, observeTimeout)
	defer cancel()
	st, err := p.c.Link.State(octx)
	if err == nil {
		// Живой ответ закрывает окно старта, окно переподключения и серию
		// неудач.
		p.spawnedAt, p.unreachSince = nil, nil
		p.ResetStartBackoff()
		return obsFromState(st), nil
	}
	switch {
	case errors.Is(err, control.ErrEvicted):
		return proxyrt.Observation{Known: true, Exists: true,
			Attrs: map[string]string{"evicted": "1"}, Detail: err.Error()}, nil
	case errors.Is(err, control.ErrProtocolVersion):
		return proxyrt.Observation{Known: true, Exists: true,
			Attrs: map[string]string{"protocol": err.Error()}, Detail: err.Error()}, nil
	}
	pid, alive := p.c.Runner.AlivePID()
	now := p.c.Now()
	if !alive {
		// Процесса нет. Это факт, а не «не смогли посмотреть».
		if p.spawnedAt != nil && now.Sub(*p.spawnedAt) < socketGrace {
			// Смерть в окне старта = неудача старта: без этого крашлупа
			// «успешный Start → мгновенная смерть» рестартовала бы каждые
			// socketWaitRecheck без backoff навсегда (I1 ревью).
			p.recordFail(now)
		}
		p.spawnedAt, p.unreachSince = nil, nil
		return proxyrt.Observation{Known: true, Exists: false,
			Detail: fmt.Sprintf("процесс не запущен (%v)", err)}, nil
	}
	if p.spawnedAt != nil {
		if now.Sub(*p.spawnedAt) < socketGrace {
			// Окно старта: на слабом железе сокет поднимается секунды.
			// Unknown, НЕ failed — хвост блокируется, фаза waiting.
			return proxyrt.Observation{Known: false,
				Detail: "ждём управляющий сокет после старта процесса"}, nil
		}
		return proxyrt.Observation{Known: true, Exists: true,
			Attrs: map[string]string{"no_socket": err.Error(),
				"no_socket_kind": "never-opened", "pid": strconv.Itoa(pid)},
			Detail: "процесс жив, управляющий сокет так и не открылся"}, nil
	}
	// Живой pid без нашего свежего старта: усыновлённый процесс либо разрыв
	// соединения. §7 даёт окно переподключения (200 мс до 20 с) прежде, чем
	// выносить вердикт — приговор с первого 6-секундного Observe был бы
	// незаявленным отступлением от протокола (I3 ревью).
	if p.unreachSince == nil {
		t := now
		p.unreachSince = &t
	}
	if now.Sub(*p.unreachSince) < socketGrace {
		return proxyrt.Observation{Known: false,
			Detail: "связь с процессом потеряна, переподключаемся (§7: до 20 с)"}, nil
	}
	return proxyrt.Observation{Known: true, Exists: true,
		Attrs: map[string]string{"no_socket": err.Error(),
			"no_socket_kind": "lost", "pid": strconv.Itoa(pid)},
		Detail: "процесс жив, но связь не восстановилась за окно §7"}, nil
}

func obsFromState(st awgmproto.State) proxyrt.Observation {
	attrs := map[string]string{
		"pid":           strconv.Itoa(st.PID),
		"config_hash":   st.ConfigHash,
		"binary_sha256": st.BinarySHA256,
		"uptime_s":      strconv.FormatInt(st.UptimeS, 10),
		"mode":          st.Mode,
		"address":       st.Address,
		"mtu":           strconv.Itoa(st.MTU),
	}
	if st.Tun != nil {
		attrs["tun_iface"] = st.Tun.Iface
		attrs["tun_attached"] = strconv.FormatBool(st.Tun.Attached)
	}
	if st.Clients != nil {
		attrs["clients"] = strconv.Itoa(*st.Clients)
	}
	// last_error НЕ читается как признак отказа (§5.2/§6.1: freeturn поле не
	// очищает никогда, wt-client/wdtt-server не заполняют) — только в Detail.
	return proxyrt.Observation{Known: true, Exists: true, Attrs: attrs, Detail: st.LastError}
}

func (p *Proc) Plan(obs proxyrt.Observation) []proxyrt.Step {
	fail := func(reason string) []proxyrt.Step {
		return []proxyrt.Step{{Resource: p.c.ID, Op: "fail", Reason: reason}}
	}
	if !p.enabled {
		if obs.Attrs["evicted"] != "" {
			// Процессом по hello владеет ДРУГОЙ менеджер (два демона):
			// гасить чужое по своему выключению нельзя — гашение оставляем
			// владельцу, сами не трогаем (M-5 ревью-2). Фаза всё равно
			// disabled (намерение перекрывает ресурсы).
			return nil
		}
		if obs.Exists {
			return []proxyrt.Step{{Resource: p.c.ID, Op: "stop", Reason: "инстанс выключен"}}
		}
		return nil
	}
	if p.cfgErr != nil {
		return fail("конфигурация невалидна: " + p.cfgErr.Error())
	}
	if obs.Attrs["evicted"] != "" {
		return fail("инстансом владеет другой менеджер")
	}
	if obs.Attrs["protocol"] != "" {
		return []proxyrt.Step{{Resource: p.c.ID, Op: "restart",
			Reason: "несовместимая версия протокола: " + obs.Attrs["protocol"]}}
	}
	if obs.Attrs["no_socket"] != "" {
		if obs.Attrs["no_socket_kind"] == "lost" {
			// §7, строка «разорвано без evicted»: не восстановилось —
			// процесс считается мёртвым, вердикт — перезапуск, не приговор.
			return []proxyrt.Step{{Resource: p.c.ID, Op: "restart",
				Reason: "связь с процессом не восстановилась за окно §7: " + obs.Attrs["no_socket"]}}
		}
		return fail("процесс не открыл управляющий сокет: " + obs.Attrs["no_socket"])
	}
	if !obs.Exists {
		return []proxyrt.Step{{Resource: p.c.ID, Op: "start", Reason: "процесс не запущен"}}
	}
	if got := obs.Attrs["config_hash"]; got != "" && got != p.wantHash {
		return []proxyrt.Step{{Resource: p.c.ID, Op: "restart", Reason: "конфигурация изменилась"}}
	}
	if sha := obs.Attrs["binary_sha256"]; sha != "" && p.c.PinnedSHA256 != "" && sha != p.c.PinnedSHA256 {
		return []proxyrt.Step{{Resource: p.c.ID, Op: "restart", Reason: "бинарь обновлён"}}
	}
	return nil
}

func (p *Proc) Apply(ctx context.Context, s proxyrt.Step) error {
	switch s.Op {
	case "fail":
		return errors.New(s.Reason)
	case "stop":
		return p.stop(ctx)
	case "start":
		return p.start(ctx)
	case "restart":
		// Гейт и backoff — ДО stop (I-2 ревью-2): гасить живой (и, возможно,
		// пропускающий трафик) процесс, когда заменить его нечем — пин на
		// диске тоже стар — нельзя. При старом пине restart вырождается в
		// букву §7: процесс не тронут, фаза failed с причиной «пин бинаря не
		// обновлён».
		now := p.c.Now()
		if until := p.retryAt(); now.Before(until) {
			return fmt.Errorf("повтор старта отложен до %s (анти-флаппинг)",
				until.Format("15:04:05"))
		}
		if err := p.c.Gate.Check(ctx, p.c.Binary, p.c.Impl, p.c.Role, p.c.NeedCmds); err != nil {
			p.recordFail(now)
			return err
		}
		if err := p.stop(ctx); err != nil {
			return err
		}
		return p.spawn(ctx, p.c.Now())
	default:
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
}

func (p *Proc) stop(ctx context.Context) error {
	pid := 0
	if snap, ok := p.c.Link.Snapshot(); ok {
		pid = snap.State.PID
	}
	if pid == 0 {
		pid, _ = p.c.Runner.AlivePID()
	}
	p.spawnedAt, p.unreachSince = nil, nil
	return p.c.Runner.Stop(ctx, pid)
}

func (p *Proc) start(ctx context.Context) error {
	now := p.c.Now()
	if until := p.retryAt(); now.Before(until) {
		return fmt.Errorf("повтор старта отложен до %s (анти-флаппинг)",
			until.Format("15:04:05"))
	}
	if err := p.c.Gate.Check(ctx, p.c.Binary, p.c.Impl, p.c.Role, p.c.NeedCmds); err != nil {
		p.recordFail(now)
		return err
	}
	return p.spawn(ctx, now)
}

// spawn — порождение БЕЗ гейта и backoff: их прошёл вызывающий (start либо
// restart-ветка Apply, у той гейт стоит ДО stop).
func (p *Proc) spawn(ctx context.Context, now time.Time) error {
	args := append(append([]string{}, p.forkArgs...),
		// Форма --имя=значение: единственная, однозначная для значений,
		// начинающихся с дефиса (§5.5 п.3).
		"--"+awgmproto.FlagSocket+"="+p.c.SocketPath,
		"--"+awgmproto.FlagLogFile+"="+p.c.LogPath,
	)
	if _, err := p.c.Runner.Start(ctx, args); err != nil {
		p.recordFail(now)
		return err
	}
	t := now
	p.spawnedAt = &t
	p.unreachSince = nil
	return nil
}

// ResetStartBackoff снимает паузу анти-флаппинга: следующий старт пойдёт без
// задержки. Кроме живого ответа управляющего канала (Observe) зовётся на ЯВНОЕ
// действие пользователя — обновление подписки заменяет профиль, и прежние
// неудачи старта больше не показательны. Сам механизм этим не отменяется:
// процесс, который валится без вмешательства, набирает паузу по-прежнему.
//
// Безопасен из любой горутины: сброс приходит из ручки API, а не от воркера.
func (p *Proc) ResetStartBackoff() {
	p.bmu.Lock()
	defer p.bmu.Unlock()
	p.fails, p.nextAllowed = 0, time.Time{}
}

// retryAt — момент, раньше которого повтор старта запрещён.
func (p *Proc) retryAt() time.Time {
	p.bmu.Lock()
	defer p.bmu.Unlock()
	return p.nextAllowed
}

func (p *Proc) recordFail(now time.Time) {
	p.bmu.Lock()
	defer p.bmu.Unlock()
	p.fails++
	delay := backoffBase
	for i := 1; i < p.fails && delay < backoffMax; i++ {
		delay *= 2
	}
	if delay > backoffMax {
		delay = backoffMax
	}
	p.nextAllowed = now.Add(delay)
}

// RecheckAfter — будильники трёх состояний, в которых внешних событий не
// будет: окно старта сокета, окно переподключения (§7) и пауза
// анти-флаппинга.
func (p *Proc) RecheckAfter() time.Duration {
	now := p.c.Now()
	if p.spawnedAt != nil && now.Sub(*p.spawnedAt) < socketGrace {
		return socketWaitRecheck
	}
	if p.unreachSince != nil && now.Sub(*p.unreachSince) < socketGrace {
		return socketWaitRecheck
	}
	if until := p.retryAt(); now.Before(until) {
		return until.Sub(now)
	}
	return 0
}
