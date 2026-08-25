package procres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
)

// fakeLink — скриптованная связь: очередь ответов State.
type fakeLink struct {
	st   awgmproto.State
	err  error
	snap *control.Snapshot
}

func (f *fakeLink) State(context.Context) (awgmproto.State, error) {
	if f.err != nil {
		return awgmproto.State{}, f.err
	}
	return f.st, nil
}

func (f *fakeLink) Snapshot() (control.Snapshot, bool) {
	if f.snap == nil {
		return control.Snapshot{}, false
	}
	return *f.snap, true
}

type fakeRunner struct {
	pid      int
	alive    bool
	started  [][]string
	stopped  []int
	startErr error
}

func (f *fakeRunner) Start(_ context.Context, args []string) (int, error) {
	if f.startErr != nil {
		return 0, f.startErr
	}
	f.started = append(f.started, args)
	f.pid, f.alive = 4821, true
	return f.pid, nil
}

func (f *fakeRunner) Stop(_ context.Context, pid int) error {
	f.stopped = append(f.stopped, pid)
	f.alive = false
	return nil
}

func (f *fakeRunner) AlivePID() (int, bool) { return f.pid, f.alive }

type okGate struct{ err error }

func (g okGate) Check(context.Context, string, string, string, []string) error { return g.err }

func newProc(link ProcessLink, r ProcRunner, gate BinaryGate, now func() time.Time) *Proc {
	return NewProc(ProcConfig{
		ID: "process", Instance: "default", Impl: "wt-client", Role: "client",
		Binary: "/opt/bin/wt-client", NeedCmds: []string{"state"},
		SocketPath: "/tmp/awgm/wt-client-client-default.sock",
		LogPath:    "/tmp/awgm/wt-client-client-default.log",
		Link:       link, Runner: r, Gate: gate, Now: now,
	})
}

func runningState(hash string) awgmproto.State {
	return awgmproto.State{
		Role: "client", Instance: "default", PID: 4821,
		ConfigHash: hash, BinarySHA256: "abc", UptimeS: 10,
		Address: "10.70.0.5", MTU: 1300,
		// last_error по-freeturn'овски: заполнено давним отказом и никогда не
		// очищается (§5.2/§6.1). Живой процесс с непустым полем — норма, шагов
		// из него не рождается.
		LastError: "relay handshake timeout",
		Tun:       &awgmproto.TunState{Iface: "opkgtun18", Attached: true},
	}
}

func TestProcSettledWhenRunningWithSameHash(t *testing.T) {
	args := []string{"-listen", "127.0.0.1:9000", "-peer", "x", "-password", "p", "-vk", "h"}
	link := &fakeLink{st: runningState(awgmproto.ConfigHash(args))}
	p := newProc(link, &fakeRunner{pid: 4821, alive: true}, okGate{}, time.Now)
	p.SetDesired(true, args, nil)

	obs, err := p.Observe(context.Background())
	if err != nil || !obs.Known || !obs.Exists {
		t.Fatalf("obs=%+v err=%v, ожидали живое наблюдение", obs, err)
	}
	if obs.Attrs["address"] != "10.70.0.5" || obs.Attrs["tun_attached"] != "true" {
		t.Fatalf("атрибуты не доехали: %+v", obs.Attrs)
	}
	if steps := p.Plan(obs); len(steps) != 0 {
		t.Fatalf("дрейфа нет — шагов быть не должно: %v", steps)
	}
}

func TestProcHashDriftRestarts(t *testing.T) {
	link := &fakeLink{st: runningState("устаревший")}
	p := newProc(link, &fakeRunner{pid: 4821, alive: true}, okGate{}, time.Now)
	p.SetDesired(true, []string{"-peer", "новый"}, nil)

	obs, _ := p.Observe(context.Background())
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "restart" {
		t.Fatalf("ожидали restart, получили %v", steps)
	}
}

func TestProcDeadStartsAndAppendsAwgmFlags(t *testing.T) {
	link := &fakeLink{err: control.ErrNoSocket}
	r := &fakeRunner{}
	p := newProc(link, r, okGate{}, time.Now)
	p.SetDesired(true, []string{"-peer", "x"}, nil)

	obs, _ := p.Observe(context.Background())
	if !obs.Known || obs.Exists {
		t.Fatalf("мёртвый процесс: ожидали Known+не-Exists, got %+v", obs)
	}
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "start" {
		t.Fatalf("ожидали start, получили %v", steps)
	}
	if err := p.Apply(context.Background(), steps[0]); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(r.started[0], " ")
	if !strings.Contains(got, "--awgm-control-socket=/tmp/awgm/wt-client-client-default.sock") ||
		!strings.Contains(got, "--awgm-log-file=/tmp/awgm/wt-client-client-default.log") {
		t.Fatalf("обвязочные флаги не добавлены: %q", got)
	}
}

func TestProcSocketWindowIsUnknownNotFailed(t *testing.T) {
	// mips-факт: сокет поднимается ~4-5 с после старта. В окне — Unknown.
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	link := &fakeLink{err: control.ErrNoSocket}
	r := &fakeRunner{}
	p := newProc(link, r, okGate{}, clock)
	p.SetDesired(true, []string{"-peer", "x"}, nil)

	obs, _ := p.Observe(context.Background())
	_ = p.Apply(context.Background(), p.Plan(obs)[0]) // start
	now = now.Add(3 * time.Second)

	obs2, err := p.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if obs2.Known {
		t.Fatalf("в окне старта «сокета нет» обязан быть Unknown: %+v", obs2)
	}
	if p.RecheckAfter() <= 0 {
		t.Fatal("в окне старта ресурс обязан просить подстраховочную сверку")
	}
}

func TestProcSocketNeverOpenedIsVerdict(t *testing.T) {
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	link := &fakeLink{err: control.ErrNoSocket}
	r := &fakeRunner{}
	p := newProc(link, r, okGate{}, clock)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	obs, _ := p.Observe(context.Background())
	_ = p.Apply(context.Background(), p.Plan(obs)[0]) // start; pid жив
	now = now.Add(25 * time.Second)                   // окно (20 с) истекло

	obs2, _ := p.Observe(context.Background())
	steps := p.Plan(obs2)
	if len(steps) != 1 || steps[0].Op != "fail" {
		t.Fatalf("ожидали приговор, получили %v", steps)
	}
	if err := p.Apply(context.Background(), steps[0]); err == nil ||
		!strings.Contains(err.Error(), "не открыл управляющий сокет") {
		t.Fatalf("Apply(fail) = %v", err)
	}
}

func TestProcCrashAfterStartFeedsBackoff(t *testing.T) {
	// I1: успешный Start + смерть до открытия сокета — крашлупа. Без учёта
	// этой смерти как неудачи старта рестарт шёл бы каждые 3 с навсегда.
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	link := &fakeLink{err: control.ErrNoSocket}
	r := &fakeRunner{}
	p := newProc(link, r, okGate{}, clock)
	p.SetDesired(true, []string{"-peer", "x"}, nil)

	obs, _ := p.Observe(context.Background())
	if err := p.Apply(context.Background(), p.Plan(obs)[0]); err != nil { // start
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	r.alive = false // ребёнок умер в окне старта

	obs, _ = p.Observe(context.Background()) // фиксирует неудачу старта
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "start" {
		t.Fatalf("мёртвый процесс — start: %v", steps)
	}
	if err := p.Apply(context.Background(), steps[0]); err == nil ||
		!strings.Contains(err.Error(), "отложен") {
		t.Fatalf("немедленный рестарт после смерти в окне старта обязан быть отложен backoff'ом: %v", err)
	}
	if p.RecheckAfter() <= 0 {
		t.Fatal("отложенный рестарт без будильника — вечный failed")
	}
}

func TestProcAdoptedGetsReconnectWindowBeforeVerdict(t *testing.T) {
	// I3: усыновлённый процесс (мы не стартовали) с недоступным сокетом
	// получает окно §7 (Unknown + будильник), а не приговор с первого Observe.
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	link := &fakeLink{err: control.ErrNoSocket}
	r := &fakeRunner{pid: 4821, alive: true} // pid жив, spawnedAt нет
	p := newProc(link, r, okGate{}, clock)
	p.SetDesired(true, []string{"-peer", "x"}, nil)

	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if obs.Known {
		t.Fatalf("первый недоступный Observe при живом pid — окно §7, не вердикт: %+v", obs)
	}
	if p.RecheckAfter() <= 0 {
		t.Fatal("в окне переподключения ресурс обязан просить будильник")
	}

	now = now.Add(25 * time.Second) // окно истекло
	obs, _ = p.Observe(context.Background())
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "restart" {
		t.Fatalf("не восстановилось — «процесс мёртв», вердикт §7 это перезапуск: %v", steps)
	}
}

func TestProcEvictedIsVerdict(t *testing.T) {
	link := &fakeLink{err: control.ErrEvicted}
	p := newProc(link, &fakeRunner{pid: 4821, alive: true}, okGate{}, time.Now)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	obs, _ := p.Observe(context.Background())
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "fail" {
		t.Fatalf("ожидали приговор, получили %v", steps)
	}
	if err := p.Apply(context.Background(), steps[0]); err == nil ||
		!strings.Contains(err.Error(), "другой менеджер") {
		t.Fatalf("Apply(fail) = %v", err)
	}
}

func TestProcProtocolMismatchRestarts(t *testing.T) {
	// Чужой мажор: процесс жив, но говорит на другом языке. ОБЪЯВЛЕННОЕ
	// отступление от буквы §7 («терминально failed»): restart — самолечение
	// случая «диск новый, процесс старый» (после апгрейда пакета). Буква §7
	// сохранена ПОРЯДКОМ применения: гейт стоит ДО stop, и при старом пине
	// на диске процесс не гасится, а инстанс уходит в failed с причиной
	// «пин бинаря не обновлён» — см. TestProcRestartGatesBeforeStop.
	link := &fakeLink{err: control.ErrProtocolVersion}
	p := newProc(link, &fakeRunner{pid: 4821, alive: true}, okGate{}, time.Now)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	obs, _ := p.Observe(context.Background())
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "restart" {
		t.Fatalf("ожидали restart, получили %v", steps)
	}
}

func TestProcRestartGatesBeforeStop(t *testing.T) {
	// I-2: restart НЕ гасит живой процесс, пока гейт не подтвердил, что есть
	// чем его заменить. Старый пин → ошибка гейта, r.stopped пуст, процесс
	// продолжает пропускать трафик; фаза — failed с причиной «пин».
	link := &fakeLink{err: control.ErrProtocolVersion}
	r := &fakeRunner{pid: 4821, alive: true}
	p := newProc(link, r, okGate{err: errors.New("пин бинаря не обновлён")}, time.Now)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	obs, _ := p.Observe(context.Background())
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "restart" {
		t.Fatalf("ожидали restart, получили %v", steps)
	}
	if err := p.Apply(context.Background(), steps[0]); err == nil ||
		!strings.Contains(err.Error(), "пин") {
		t.Fatalf("гейт обязан отказать с причиной: %v", err)
	}
	if len(r.stopped) != 0 {
		t.Fatalf("живой процесс погашен ДО гейта — терять его при старом пине нельзя: %v", r.stopped)
	}
}

func TestProcGateFailureFailsStart(t *testing.T) {
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	link := &fakeLink{err: control.ErrNoSocket}
	p := newProc(link, &fakeRunner{}, okGate{err: errors.New("пин бинаря не обновлён")}, clock)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	obs, _ := p.Observe(context.Background())
	step := p.Plan(obs)[0]
	err := p.Apply(context.Background(), step)
	if err == nil || !strings.Contains(err.Error(), "пин") {
		t.Fatalf("гейт обязан валить старт с причиной: %v", err)
	}
	// Отказ гейта — такая же неудача старта, как и отказ Start: он обязан
	// питать backoff. Иначе повтор на каждом событии гоняет пробу бинаря
	// вхолостую, а инстанс в failed остаётся без будильника.
	if err := p.Apply(context.Background(), step); err == nil ||
		!strings.Contains(err.Error(), "отложен") {
		t.Fatalf("повтор после отказа гейта обязан быть отложен backoff'ом: %v", err)
	}
	if p.RecheckAfter() <= 0 {
		t.Fatal("отказ гейта без будильника — вечный failed")
	}
}

func TestProcStartBackoffDelaysRetry(t *testing.T) {
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	link := &fakeLink{err: control.ErrNoSocket}
	r := &fakeRunner{startErr: errors.New("нет бинаря")}
	p := newProc(link, r, okGate{}, clock)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	obs, _ := p.Observe(context.Background())
	step := p.Plan(obs)[0]

	if err := p.Apply(context.Background(), step); err == nil {
		t.Fatal("первый старт обязан упасть")
	}
	// Повтор сразу — отложен, и ресурс просит будильник.
	if err := p.Apply(context.Background(), step); err == nil ||
		!strings.Contains(err.Error(), "отложен") {
		t.Fatalf("повтор в окне backoff: %v", err)
	}
	if p.RecheckAfter() <= 0 {
		t.Fatal("backoff без будильника вешает инстанс в failed навсегда")
	}
	now = now.Add(time.Minute)
	r.startErr = nil
	if err := p.Apply(context.Background(), step); err != nil {
		t.Fatalf("после паузы старт обязан пройти: %v", err)
	}
}

func TestProcDisabledStopsProcess(t *testing.T) {
	args := []string{"-peer", "x"}
	link := &fakeLink{st: runningState(awgmproto.ConfigHash(args))}
	r := &fakeRunner{pid: 4821, alive: true}
	p := newProc(link, r, okGate{}, time.Now)
	p.SetDesired(false, args, nil)

	obs, _ := p.Observe(context.Background())
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "stop" {
		t.Fatalf("ожидали stop, получили %v", steps)
	}
	if err := p.Apply(context.Background(), steps[0]); err != nil {
		t.Fatal(err)
	}
	if len(r.stopped) != 1 || r.stopped[0] != 4821 {
		t.Fatalf("Stop не дошёл до pid из state: %v", r.stopped)
	}
}

func TestProcDisabledDoesNotStopEvicted(t *testing.T) {
	// M-5: инстансом владеет другой менеджер — выключение у НАС не гасит
	// ЕГО процесс.
	link := &fakeLink{err: control.ErrEvicted}
	r := &fakeRunner{pid: 4821, alive: true}
	p := newProc(link, r, okGate{}, time.Now)
	p.SetDesired(false, []string{"-peer", "x"}, nil)
	obs, _ := p.Observe(context.Background())
	if steps := p.Plan(obs); len(steps) != 0 {
		t.Fatalf("чужой процесс при disabled не трогается: %v", steps)
	}
}

func TestProcInvalidConfigIsVerdict(t *testing.T) {
	link := &fakeLink{err: control.ErrNoSocket}
	p := newProc(link, &fakeRunner{}, okGate{}, time.Now)
	p.SetDesired(true, nil, errors.New("не задан пароль подключения (-password)"))
	obs, _ := p.Observe(context.Background())
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "fail" {
		t.Fatalf("невалидный конфиг — приговор до старта, получили %v", steps)
	}
}

// Проверка контракта: Proc обязан быть настоящим proxyrt.Resource.
var _ proxyrt.Resource = (*Proc)(nil)

// Явное действие пользователя (обновление подписки) обязано снимать паузу
// анти-флаппинга: профиль заменён, прежние неудачи больше не показательны, и
// заставлять человека ждать до пяти минут ровно в момент починки нельзя.
func TestProcResetStartBackoffAllowsImmediateRetry(t *testing.T) {
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	link := &fakeLink{err: control.ErrNoSocket}
	r := &fakeRunner{startErr: errors.New("нет бинаря")}
	p := newProc(link, r, okGate{}, clock)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	obs, _ := p.Observe(context.Background())
	step := p.Plan(obs)[0]

	if err := p.Apply(context.Background(), step); err == nil {
		t.Fatal("первый старт обязан упасть")
	}
	if err := p.Apply(context.Background(), step); err == nil ||
		!strings.Contains(err.Error(), "отложен") {
		t.Fatalf("повтор в окне backoff: %v", err)
	}

	p.ResetStartBackoff()
	r.startErr = nil
	// Часы НЕ двигаются: сброс обязан снять паузу сам, а не дождаться её.
	if err := p.Apply(context.Background(), step); err != nil {
		t.Fatalf("после сброса старт обязан пройти без задержки: %v", err)
	}
	if len(r.started) != 1 {
		t.Fatalf("ожидали ровно один состоявшийся старт, получили %v", r.started)
	}
}

// Сброс снимает паузу, но не отменяет механизм: клиент, который валится сам по
// себе, обязан замедляться по-прежнему и с той же лестницы.
func TestProcResetStartBackoffKeepsAntiFlapping(t *testing.T) {
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	link := &fakeLink{err: control.ErrNoSocket}
	r := &fakeRunner{startErr: errors.New("нет бинаря")}
	p := newProc(link, r, okGate{}, clock)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	obs, _ := p.Observe(context.Background())
	step := p.Plan(obs)[0]

	if err := p.Apply(context.Background(), step); err == nil {
		t.Fatal("первый старт обязан упасть")
	}
	if got := p.RecheckAfter(); got != 5*time.Second {
		t.Fatalf("первая неудача: пауза %v, ожидали 5s", got)
	}
	now = now.Add(6 * time.Second)
	if err := p.Apply(context.Background(), step); err == nil {
		t.Fatal("второй старт обязан упасть")
	}
	if got := p.RecheckAfter(); got != 10*time.Second {
		t.Fatalf("вторая неудача: пауза %v, ожидали 10s", got)
	}

	p.ResetStartBackoff()
	if got := p.RecheckAfter(); got != 0 {
		t.Fatalf("после сброса будильник паузы %v, ожидали 0", got)
	}
	if err := p.Apply(context.Background(), step); err == nil {
		t.Fatal("старт обязан упасть и после сброса")
	}
	// Ровно 5s, а не 20s: сброс обнуляет и счётчик неудач, иначе лестница
	// продолжится с прежней ступени.
	if got := p.RecheckAfter(); got != 5*time.Second {
		t.Fatalf("первая неудача после сброса: пауза %v, ожидали 5s", got)
	}
}

// Живой ответ управляющего канала снимает паузу повторного старта: процесс
// отозвался, серия неудач кончилась. Строка сброса в Observe не была закреплена
// ничем — её удаление проходило зелёным.
func TestProcObserveClearsStartBackoffOnLiveReply(t *testing.T) {
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	link := &fakeLink{err: control.ErrNoSocket}
	r := &fakeRunner{startErr: errors.New("нет бинаря")}
	p := newProc(link, r, okGate{}, clock)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	obs, _ := p.Observe(context.Background())
	if err := p.Apply(context.Background(), p.Plan(obs)[0]); err == nil {
		t.Fatal("первый старт обязан упасть")
	}
	if got := p.RecheckAfter(); got != 5*time.Second {
		t.Fatalf("пауза %v, ожидали 5s", got)
	}

	// Процесс отозвался — с ЧУЖОЙ конфигурацией, чтобы родился шаг перезапуска.
	link.err, link.st = nil, runningState("чужой-хеш")
	r.startErr, r.pid, r.alive = nil, 4821, true
	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if got := p.RecheckAfter(); got != 0 {
		t.Fatalf("живой ответ обязан снять паузу, осталось %v", got)
	}
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "restart" {
		t.Fatalf("план %+v, ожидали restart", steps)
	}
	if err := p.Apply(context.Background(), steps[0]); err != nil {
		t.Fatalf("перезапуск обязан пройти без паузы: %v", err)
	}
}

// Пауза держит и ветку ПЕРЕЗАПУСКА, причём отказ выносится ДО гашения: гасить
// живой процесс, когда заменить его нечем, нельзя (I-2 ревью-2).
func TestProcRestartRespectsStartBackoff(t *testing.T) {
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	link := &fakeLink{err: control.ErrNoSocket}
	r := &fakeRunner{startErr: errors.New("нет бинаря")}
	p := newProc(link, r, okGate{}, clock)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	obs, _ := p.Observe(context.Background())
	if err := p.Apply(context.Background(), p.Plan(obs)[0]); err == nil {
		t.Fatal("первый старт обязан упасть")
	}

	// Несовместимая версия протокола — единственный путь к шагу restart, не
	// проходящий через живой ответ: тот паузу снимает.
	link.err = control.ErrProtocolVersion
	r.startErr, r.pid, r.alive = nil, 4821, true
	obs, _ = p.Observe(context.Background())
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "restart" {
		t.Fatalf("план %+v, ожидали restart", steps)
	}
	if err := p.Apply(context.Background(), steps[0]); err == nil ||
		!strings.Contains(err.Error(), "отложен") {
		t.Fatalf("перезапуск в окне паузы: %v", err)
	}
	if len(r.stopped) != 0 || len(r.started) != 0 {
		t.Fatalf("процесс тронут вопреки паузе: stopped=%v started=%v", r.stopped, r.started)
	}
}

// Сброс приходит из горутины ручки API (manager.Update), пока воркер инстанса
// гоняет свой цикл. Пауза — единственное состояние ресурса с двумя писателями.
//
// Обе горутины стартуют с барьера и работают одинаково долго: без этого они
// расходятся во времени, счётчик неудач почти не пересекается, и снятие замка
// в recordFail детектор ловит через раз. Утверждения в хвосте проверяют, что
// состояние осталось связным, и не зависят от флага детектора.
func TestProcResetStartBackoffIsRaceFree(t *testing.T) {
	link := &fakeLink{err: control.ErrNoSocket}
	r := &fakeRunner{startErr: errors.New("нет бинаря")}
	p := newProc(link, r, okGate{}, time.Now)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	step := proxyrt.Step{Resource: "process", Op: "start"}

	const iters = 5000
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iters; i++ {
			p.ResetStartBackoff()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iters; i++ {
			_ = p.Apply(context.Background(), step)
			_ = p.RecheckAfter()
		}
	}()
	close(start)
	wg.Wait()

	// Состояние связно: сброс обнуляет паузу, а следующая неудача взводит
	// ПЕРВУЮ ступень лестницы (5 с), а не какую-то из уехавших.
	p.ResetStartBackoff()
	if got := p.RecheckAfter(); got != 0 {
		t.Fatalf("после сброса пауза %v, ожидали 0", got)
	}
	if err := p.Apply(context.Background(), step); err == nil {
		t.Fatal("старт обязан упасть")
	}
	if got := p.RecheckAfter(); got <= 4*time.Second || got > 5*time.Second {
		t.Fatalf("первая неудача после сброса: пауза %v, ожидали первую ступень (5s)", got)
	}
}
