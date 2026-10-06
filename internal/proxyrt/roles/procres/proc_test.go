package procres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestProcRequestRestartLifecycle(t *testing.T) {
	link := &fakeLink{st: awgmproto.State{PID: 100, ConfigHash: "h1"}}
	r := &fakeRunner{pid: 100, alive: true}
	p := newProc(link, r, okGate{}, time.Now)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.wantHash = "h1"

	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if steps := p.Plan(obs); len(steps) != 0 {
		t.Fatalf("до запроса план обязан быть пуст: %v", steps)
	}

	p.recordFail(time.Now())
	if p.RecheckAfter() == 0 {
		t.Fatal("backoff обязан быть взведён")
	}

	p.RequestRestart("тест")
	if got := p.RecheckAfter(); got == 0 {
		t.Fatal("RequestRestart НЕ должен сбрасывать backoff")
	}
	p.ResetStartBackoff()
	if got := p.RecheckAfter(); got != 0 {
		t.Fatalf("ResetStartBackoff обязан сбросить backoff, осталось %v", got)
	}

	steps1 := p.Plan(obs)
	if len(steps1) != 1 || steps1[0].Op != "restart" || steps1[0].Reason != "тест" {
		t.Fatalf("первый Plan = %v, ожидали [restart:тест]", steps1)
	}
	steps2 := p.Plan(obs)
	if len(steps2) != 1 || steps2[0].Op != "restart" || steps2[0].Reason != "тест" {
		t.Fatalf("второй Plan без Apply обязан также вернуть [restart:тест]: %v", steps2)
	}

	if err := p.Apply(context.Background(), steps1[0]); err != nil {
		t.Fatalf("Apply(restart) err: %v", err)
	}
	if stepsAfter := p.Plan(obs); len(stepsAfter) != 0 {
		t.Fatalf("после Apply(restart) план обязан быть пуст: %v", stepsAfter)
	}
}

// Успешный start уже поднял процесс заново: запрос перезапуска, пришедший
// до него, выполнен и снимается — иначе следующий прогон перезапустил бы
// только что стартовавший процесс.
func TestProcStartClearsRestartRequest(t *testing.T) {
	link := &fakeLink{st: awgmproto.State{PID: 100, ConfigHash: "h1"}}
	r := &fakeRunner{pid: 100, alive: true}
	p := newProc(link, r, okGate{}, time.Now)
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.wantHash = "h1"

	p.RequestRestart("тест")
	if err := p.Apply(context.Background(), proxyrt.Step{Resource: "process", Op: "start"}); err != nil {
		t.Fatalf("Apply(start) err: %v", err)
	}
	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if steps := p.Plan(obs); len(steps) != 0 {
		t.Fatalf("после успешного start запрос обязан сняться, план: %v", steps)
	}
}

func TestProcAutoReconnect_LogFatalError(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(logFile, []byte("some initial log\nerror 401: Unauthorized\n"), 0644); err != nil {
		t.Fatal(err)
	}

	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	link := &fakeLink{st: awgmproto.State{PID: 100, UptimeS: 30}}
	r := &fakeRunner{pid: 100, alive: true}
	p := NewProc(ProcConfig{
		ID: "process", Instance: "default", Impl: "wt-client", Role: "client",
		Binary: "/opt/bin/wt-client", NeedCmds: []string{"state"},
		SocketPath: "/tmp/sock", LogPath: logFile,
		Link: link, Runner: r, Gate: okGate{}, Now: clock,
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(true, 0)
	p.logStartOffset = 0 // симулируем процесс, запущенный через spawn
	defer p.stopLogWatcher()

	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	if obs.Attrs["fatal_error"] != "error 401: Unauthorized" {
		t.Fatalf("fatal_error = %q, want 'error 401: Unauthorized'", obs.Attrs["fatal_error"])
	}

	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "restart" {
		t.Fatalf("Plan = %+v, want [restart]", steps)
	}
	if !strings.Contains(steps[0].Reason, "сбой сессии в журнале") {
		t.Fatalf("Reason = %q, want session failure mention", steps[0].Reason)
	}

	if err := p.Apply(context.Background(), steps[0]); err != nil {
		t.Fatalf("Apply err: %v", err)
	}
	// После сбоя сессии взведён backoff (5s)
	if until := p.retryAt(); until.Sub(now) != 5*time.Second {
		t.Fatalf("retryAt() = %v, want 5s backoff from %v", until, now)
	}
	// Для только что запущенного процесса будильник — socketWaitRecheck (3s)
	if got := p.RecheckAfter(); got != socketWaitRecheck {
		t.Fatalf("RecheckAfter() = %v, want %v", got, socketWaitRecheck)
	}
	// Повторный рестарт до истечения backoff отклоняется анти-флаппингом
	if err := p.Apply(context.Background(), steps[0]); err == nil || !strings.Contains(err.Error(), "отложен") {
		t.Fatalf("повторный рестарт обязан быть отложен backoff'ом: %v", err)
	}
}

func TestProcAutoReconnect_AdoptedProcessIgnoresOldLogFatal(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(logFile, []byte("старый журнал демона\nall streams down\n"), 0644); err != nil {
		t.Fatal(err)
	}

	link := &fakeLink{st: awgmproto.State{PID: 100, UptimeS: 30}}
	r := &fakeRunner{pid: 100, alive: true}
	p := NewProc(ProcConfig{
		ID: "process", Instance: "default", Impl: "wt-client", Role: "client",
		Binary: "/opt/bin/wt-client", NeedCmds: []string{"state"},
		SocketPath: "/tmp/sock", LogPath: logFile,
		Link: link, Runner: r, Gate: okGate{}, Now: time.Now,
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(true, 0)
	defer p.stopLogWatcher()

	// Первый Observe: подхваченный процесс (logStartOffset был -1).
	// Старая строка 'all streams down' в журнале не должна вызывать перезапуск!
	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	if obs.Attrs["fatal_error"] != "" {
		t.Fatalf("старая фатальная ошибка подхваченного процесса не должна обнаруживаться: %q", obs.Attrs["fatal_error"])
	}
	if steps := p.Plan(obs); len(steps) != 0 {
		t.Fatalf("план подхваченного процесса без свежих сбоев должен быть пуст: %v", steps)
	}

	// Процесс дописывает свежий сбой в журнал:
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("сессия разорвана\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Второй Observe: свежая ошибка должна быть обнаружена!
	obs2, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe 2 err: %v", err)
	}
	if obs2.Attrs["fatal_error"] != "сессия разорвана" {
		t.Fatalf("свежий сбой сессии должен быть обнаружен, got %q", obs2.Attrs["fatal_error"])
	}
	steps2 := p.Plan(obs2)
	if len(steps2) != 1 || steps2[0].Op != "restart" {
		t.Fatalf("план после свежего сбоя должен быть [restart], got: %v", steps2)
	}
}

func TestProcAutoReconnect_Disabled(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(logFile, []byte("error 401: Unauthorized\n"), 0644); err != nil {
		t.Fatal(err)
	}

	link := &fakeLink{st: awgmproto.State{PID: 100, UptimeS: 30}}
	r := &fakeRunner{pid: 100, alive: true}
	p := NewProc(ProcConfig{
		ID: "process", Instance: "default", Impl: "wt-client", Role: "client",
		Binary: "/opt/bin/wt-client", NeedCmds: []string{"state"},
		SocketPath: "/tmp/sock", LogPath: logFile,
		Link: link, Runner: r, Gate: okGate{}, Now: time.Now,
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(false, 0) // Выключено!

	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	if obs.Attrs["fatal_error"] != "" {
		t.Fatalf("fatal_error should be empty when autoReconnect is false, got %q", obs.Attrs["fatal_error"])
	}

	steps := p.Plan(obs)
	if len(steps) != 0 {
		t.Fatalf("Plan = %+v, want empty steps when autoReconnect is disabled", steps)
	}
}

func TestProcAutoReconnect_IntervalExpiration(t *testing.T) {
	link := &fakeLink{st: awgmproto.State{PID: 100, UptimeS: 3605}}
	r := &fakeRunner{pid: 100, alive: true}
	p := NewProc(ProcConfig{
		ID: "process", Instance: "default", Impl: "wt-client", Role: "client",
		Binary: "/opt/bin/wt-client", NeedCmds: []string{"state"},
		SocketPath: filepath.Join(t.TempDir(), "sock"), LogPath: filepath.Join(t.TempDir(), "test.log"),
		Link: link, Runner: r, Gate: okGate{}, Now: time.Now,
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(true, 1*time.Hour)
	defer p.stopLogWatcher()

	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	if obs.Attrs["reconnect_due"] != "interval" {
		t.Fatalf("reconnect_due = %q, want 'interval'", obs.Attrs["reconnect_due"])
	}

	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "restart" {
		t.Fatalf("Plan = %+v, want [restart]", steps)
	}
}

func TestProcAutoReconnect_StartupFailureDetected(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(logFile, []byte("error 401: Unauthorized\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Сбой в первые 20 с (UptimeS = 5) ОБЯЗАН обнаруживаться и приводить к перезапуску
	link := &fakeLink{st: awgmproto.State{PID: 100, UptimeS: 5}}
	r := &fakeRunner{pid: 100, alive: true}
	p := NewProc(ProcConfig{
		ID: "process", Instance: "default", Impl: "wt-client", Role: "client",
		Binary: "/opt/bin/wt-client", NeedCmds: []string{"state"},
		SocketPath: "/tmp/sock", LogPath: logFile,
		Link: link, Runner: r, Gate: okGate{}, Now: time.Now,
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(true, 0)
	p.logStartOffset = 0
	defer p.stopLogWatcher()

	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	if obs.Attrs["fatal_error"] != "error 401: Unauthorized" {
		t.Fatalf("ожидали обнаружение fatal_error на старте, получили %q", obs.Attrs["fatal_error"])
	}
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "restart" {
		t.Fatalf("Plan = %+v, want [restart]", steps)
	}
}

func TestProcAutoReconnect_ReaderDeadlineSignature(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(logFile, []byte("2026/10/02 08:14:00 [RAW #3] Ошибка Reader: deadline exceeded\n"), 0644); err != nil {
		t.Fatal(err)
	}

	link := &fakeLink{st: awgmproto.State{PID: 100, UptimeS: 30}}
	r := &fakeRunner{pid: 100, alive: true}
	p := NewProc(ProcConfig{
		ID: "process", Instance: "default", Impl: "wt-client", Role: "client",
		Binary: "/opt/bin/wt-client", NeedCmds: []string{"state"},
		SocketPath: "/tmp/sock", LogPath: logFile,
		Link: link, Runner: r, Gate: okGate{}, Now: time.Now,
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(true, 0)
	p.logStartOffset = 0
	defer p.stopLogWatcher()

	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	if obs.Attrs["fatal_error"] == "" {
		t.Fatal("ожидали обнаружение ошибки Reader в логе")
	}
}

func TestProcAutoReconnect_IgnoresGenericSocketErrors(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(logFile, []byte("client error: write: broken pipe\nread tcp 127.0.0.1: connection reset by peer\ncontext deadline exceeded\n"), 0644); err != nil {
		t.Fatal(err)
	}

	link := &fakeLink{st: awgmproto.State{PID: 100, UptimeS: 30}}
	r := &fakeRunner{pid: 100, alive: true}
	p := NewProc(ProcConfig{
		ID: "process", Instance: "default", Impl: "wt-client", Role: "client",
		Binary: "/opt/bin/wt-client", NeedCmds: []string{"state"},
		SocketPath: "/tmp/sock", LogPath: logFile,
		Link: link, Runner: r, Gate: okGate{}, Now: time.Now,
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(true, 0)
	p.logStartOffset = 0
	defer p.stopLogWatcher()

	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	if obs.Attrs["fatal_error"] != "" {
		t.Fatalf("fatal_error should NOT trigger on generic socket errors, got %q", obs.Attrs["fatal_error"])
	}
}

func TestProc_SessionFailureAccumulatesBackoff(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	link := &fakeLink{st: awgmproto.State{PID: 100, UptimeS: 25}}
	r := &fakeRunner{pid: 100, alive: true}
	p := NewProc(ProcConfig{
		ID: "process", Instance: "default", Impl: "wt-client", Role: "client",
		Binary: "/opt/bin/wt-client", NeedCmds: []string{"state"},
		SocketPath: filepath.Join(t.TempDir(), "sock"),
		Link:       link, Runner: r, Gate: okGate{}, Now: clock,
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(true, 0)
	defer p.stopLogWatcher()

	// 1-й сбой сессии: Step с cause="session_failure"
	step := proxyrt.Step{
		Resource: p.ID(),
		Op:       "restart",
		Args:     map[string]string{argCause: causeSessionFailure},
		Reason:   "сбой сессии в журнале: error 401: Unauthorized",
	}
	if err := p.Apply(context.Background(), step); err != nil {
		t.Fatalf("Apply err: %v", err)
	}
	if p.fails != 1 {
		t.Fatalf("fails = %d, want 1", p.fails)
	}
	retry1 := p.retryAt()
	if !retry1.Equal(now.Add(backoffBase)) {
		t.Fatalf("retryAt = %v, want %v", retry1, now.Add(backoffBase))
	}

	// Симулируем перезапущенный процесс (uptime = 20s < stableGrace)
	now = now.Add(backoffBase + 20*time.Second)
	link.st = awgmproto.State{PID: 101, UptimeS: 20}
	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	_ = obs
	// Поскольку uptime < stableGrace, backoff НЕ должен сбрасываться!
	if p.fails != 1 {
		t.Fatalf("fails сбросился раньше stableGrace: %d, want 1", p.fails)
	}

	// 2-й сбой сессии должен удвоить backoff
	if err := p.Apply(context.Background(), step); err != nil {
		t.Fatalf("Apply err: %v", err)
	}
	if p.fails != 2 {
		t.Fatalf("fails = %d, want 2", p.fails)
	}
	retry2 := p.retryAt()
	if !retry2.Equal(now.Add(2 * backoffBase)) {
		t.Fatalf("retryAt = %v, want %v", retry2, now.Add(2*backoffBase))
	}

	// Сбой после 65s (больше 60s) не должен терять backoff: stableGrace = 10m
	now = now.Add(2*backoffBase + 65*time.Second)
	link.st = awgmproto.State{PID: 102, UptimeS: 65}
	obs, err = p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	if p.fails != 2 {
		t.Fatalf("fails сбросился раньше stableGrace: %d, want 2", p.fails)
	}

	// 3-й сбой после 65s работы накапливает backoff дальше
	if err := p.Apply(context.Background(), step); err != nil {
		t.Fatalf("Apply err: %v", err)
	}
	if p.fails != 3 {
		t.Fatalf("fails = %d, want 3", p.fails)
	}
	retry3 := p.retryAt()
	if !retry3.Equal(now.Add(4 * backoffBase)) {
		t.Fatalf("retryAt = %v, want %v", retry3, now.Add(4*backoffBase))
	}

	// Только после достижения stableGrace (>= 10 мин) без ошибок backoff сбрасывается
	now = now.Add(4*backoffBase + 11*time.Minute)
	link.st = awgmproto.State{PID: 103, UptimeS: int64(11 * 60)}
	obs, err = p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	if p.fails != 0 {
		t.Fatalf("fails не сбросился после stableGrace: %d, want 0", p.fails)
	}
}

func TestProcAutoReconnect_LogTruncatedOrRotated(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test.log")
	initialData := []byte("строка номер один\nстрока номер два\n")
	if err := os.WriteFile(logFile, initialData, 0644); err != nil {
		t.Fatal(err)
	}

	offset := int64(100) // смещение больше размера файла (имитация ротации)
	sig := scanLogForFatal(logFile, &offset)
	if sig != "" {
		t.Fatalf("ожидали пустую сигнатуру, получили %q", sig)
	}
	if offset != int64(len(initialData)) {
		t.Fatalf("offset = %d, want %d", offset, len(initialData))
	}

	// Внешняя ротация/усечение: файл перезаписан фатальной ошибкой
	if err := os.WriteFile(logFile, []byte("сессия разорвана\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// scanLogForFatal должен сбросить смещение и прочесть новый короткий файл
	sig = scanLogForFatal(logFile, &offset)
	if sig != "сессия разорвана" {
		t.Fatalf("ожидали прочесть усечённый лог, получили: %q", sig)
	}
}

func TestProc_RestartSpawnFailureAccumulatesBackoff(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	link := &fakeLink{st: awgmproto.State{PID: 101, UptimeS: 3600}}
	spawnErr := errors.New("cannot spawn binary")
	runner := &fakeRunner{pid: 101, alive: true, startErr: spawnErr}
	p := newProc(link, runner, okGate{}, clock)
	p.enabled = true
	p.autoReconnect = true
	p.reconnectInterval = time.Hour
	defer p.stopLogWatcher()

	step := proxyrt.Step{
		Resource: p.c.ID,
		Op:       "restart",
		Reason:   "плановое переподключение (interval)",
		Args:     map[string]string{argCause: causeInterval},
	}
	err := p.Apply(context.Background(), step)
	if err == nil {
		t.Fatal("ожидали ошибку spawn")
	}
	if p.fails != 1 {
		t.Fatalf("fails = %d, want 1 (backoff должен накапливаться при ошибке spawn)", p.fails)
	}
}

func TestProc_StopResetsLastUptimeS(t *testing.T) {
	p := newProc(&fakeLink{}, &fakeRunner{}, okGate{}, time.Now)
	p.lastUptimeS = 7200
	if err := p.stop(context.Background()); err != nil {
		t.Fatalf("stop err: %v", err)
	}
	if p.lastUptimeS != 0 {
		t.Fatalf("lastUptimeS = %d, want 0 после stop", p.lastUptimeS)
	}
}

func TestProc_DeferredRestartKeepsRestartWanted(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	link := &fakeLink{st: awgmproto.State{PID: 101, UptimeS: 3600}}
	runner := &fakeRunner{pid: 101, alive: true}
	p := newProc(link, runner, okGate{}, clock)
	p.enabled = true

	// Имитируем активный backoff
	p.recordFail(now)
	p.RequestRestart("ручной перезапуск")

	step := proxyrt.Step{
		Resource: p.c.ID,
		Op:       "restart",
		Reason:   "ручной перезапуск",
		Args:     map[string]string{argCause: causeRequested},
	}
	// Apply должен отклонить старт из-за backoff
	err := p.Apply(context.Background(), step)
	if err == nil || !strings.Contains(err.Error(), "анти-флаппинг") {
		t.Fatalf("Apply ожидался с ошибкой анти-флаппинга, получили: %v", err)
	}

	// restartWanted НЕ должен сбрасываться, пока перезапуск не выполнен!
	p.rmu.Lock()
	wanted := p.restartWanted
	reason := p.restartReason
	p.rmu.Unlock()
	if !wanted || reason != "ручной перезапуск" {
		t.Fatalf("restartWanted потерян при отложенном старте: wanted=%v, reason=%q", wanted, reason)
	}

	// Когда пауза истекает, следующий Apply сбрасывает флаг
	now = now.Add(backoffBase + time.Second)
	if err := p.Apply(context.Background(), step); err != nil {
		t.Fatalf("Apply после истечения паузы: %v", err)
	}
	p.rmu.Lock()
	wanted = p.restartWanted
	p.rmu.Unlock()
	if wanted {
		t.Fatal("restartWanted должен сброситься после успешного перезапуска")
	}
}

func TestProc_StopCleansUpPrevLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "proc.log")
	prevPath := logPath + ".prev"
	if err := os.WriteFile(prevPath, []byte("старый лог"), 0644); err != nil {
		t.Fatal(err)
	}

	p := NewProc(ProcConfig{
		ID:      "proc",
		LogPath: logPath,
		Runner:  &fakeRunner{},
		Link:    &fakeLink{},
		Gate:    okGate{},
		Now:     time.Now,
	})
	if err := p.stop(context.Background()); err != nil {
		t.Fatalf("stop err: %v", err)
	}
	if _, err := os.Stat(prevPath); !os.IsNotExist(err) {
		t.Fatalf(".prev файл не был удалён при stop: err=%v", err)
	}
}

func TestProc_LogWatcherWakesOnFatalError(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "watch.log")
	if err := os.WriteFile(logPath, []byte("старт процесса\n"), 0644); err != nil {
		t.Fatal(err)
	}

	wakeCh := make(chan struct{}, 5)
	p := NewProc(ProcConfig{
		ID:      "proc",
		LogPath: logPath,
		Runner:  &fakeRunner{},
		Link:    &fakeLink{},
		Gate:    okGate{},
		Now:     time.Now,
		Wake:    func() { wakeCh <- struct{}{} },
	})
	p.enabled = true
	p.autoReconnect = true
	p.ensureLogWatcher()
	defer p.stopLogWatcher()

	// Нефатальная строка: не должна будить
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("[СТАТИСТИКА] 100 пакетов\n")
	_ = f.Close()

	select {
	case <-wakeCh:
		t.Fatal("получен wake на нефатальную строку")
	case <-time.After(150 * time.Millisecond):
	}

	// Фатальная строка: должна вызвать Wake()
	f, err = os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("Ошибка Reader: EOF\n")
	_ = f.Close()

	select {
	case <-wakeCh:
		// успешно!
	case <-time.After(2 * time.Second):
		t.Fatal("таймаут ожидания wake при записи фатальной ошибки в лог")
	}
}

func TestProcAutoReconnect_SplitLineSignature(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "split.log")

	// 1. Запись первой части строки без перевода строки
	if err := os.WriteFile(logPath, []byte("[VK Auth] Multi"), 0644); err != nil {
		t.Fatal(err)
	}

	offset := int64(0)
	sig := scanLogForFatal(logPath, &offset)
	if sig != "" {
		t.Fatalf("неполная строка не должна возвращать сигнатуру, got %q", sig)
	}
	if offset != 0 {
		t.Fatalf("offset не должен смещаться на неполной строке, got %d", offset)
	}

	// 2. Дозапись остатка строки с переводом строки
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("ple auth errors detected\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	sig = scanLogForFatal(logPath, &offset)
	if sig != "[VK Auth] Multiple auth errors detected" {
		t.Fatalf("ожидали '[VK Auth] Multiple auth errors detected', got %q", sig)
	}
	if offset <= 0 {
		t.Fatalf("offset должен продвинуться после завершения строки, got %d", offset)
	}
}

func TestProcAutoReconnect_EarlyUptimeFailureTriggersRestart(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "early.log")
	if err := os.WriteFile(logPath, []byte("старт\n"), 0644); err != nil {
		t.Fatal(err)
	}

	wakeCh := make(chan struct{}, 5)
	link := &fakeLink{st: awgmproto.State{PID: 201, UptimeS: 5}}
	runner := &fakeRunner{pid: 201, alive: true}
	p := NewProc(ProcConfig{
		ID:      "proc",
		LogPath: logPath,
		Runner:  runner,
		Link:    link,
		Gate:    okGate{},
		Now:     time.Now,
		Wake:    func() { wakeCh <- struct{}{} },
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(true, 0)
	defer p.stopLogWatcher()

	// Первый Observe: процесс подхвачен на 5-й секунде
	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	if obs.Attrs[attrFatalError] != "" {
		t.Fatalf("не ожидали ошибку на чистом старте, got %q", obs.Attrs[attrFatalError])
	}

	// Клиент пишет фатальную ошибку на 5-й секунде работы
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("error 401: Unauthorized\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Watcher обязан разбудить воркер
	select {
	case <-wakeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("таймаут ожидания wake при сбое на 5-й секунде аптайма")
	}

	// Observe обязан вернуть fatal_error, несмотря на UptimeS < socketGrace!
	obs2, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe 2 err: %v", err)
	}
	if obs2.Attrs[attrFatalError] != "error 401: Unauthorized" {
		t.Fatalf("ожидали fatal_error 'error 401: Unauthorized', got %q", obs2.Attrs[attrFatalError])
	}

	// Plan обязан вернуть перезапуск
	steps := p.Plan(obs2)
	if len(steps) != 1 || steps[0].Op != "restart" {
		t.Fatalf("Plan = %+v, want [restart]", steps)
	}
}

func TestProcAutoReconnect_AdoptedProcessStartsWatcher(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "adopted.log")
	if err := os.WriteFile(logPath, []byte("прежний лог до старта awg-manager\n"), 0644); err != nil {
		t.Fatal(err)
	}

	wakeCh := make(chan struct{}, 5)
	link := &fakeLink{st: awgmproto.State{PID: 301, UptimeS: 3600}}
	runner := &fakeRunner{pid: 301, alive: true}
	p := NewProc(ProcConfig{
		ID:      "proc",
		LogPath: logPath,
		Runner:  runner,
		Link:    link,
		Gate:    okGate{},
		Now:     time.Now,
		Wake:    func() { wakeCh <- struct{}{} },
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(true, 0)
	defer p.stopLogWatcher()

	// Первый Observe: процесс подхвачен без spawn
	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	if obs.Attrs[attrFatalError] != "" {
		t.Fatalf("старый лог не должен давать ошибку: %q", obs.Attrs[attrFatalError])
	}

	// Проверяем, что watcher действительно поднят и ловит новые сбои:
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[VK Auth] Persona burned\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	select {
	case <-wakeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("таймаут ожидания wake на подхваченном процессе")
	}

	obs2, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe 2 err: %v", err)
	}
	if obs2.Attrs[attrFatalError] != "[VK Auth] Persona burned" {
		t.Fatalf("ожидали '[VK Auth] Persona burned', got %q", obs2.Attrs[attrFatalError])
	}
}

func TestProcAutoReconnect_EnabledOnRunningProcess(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "running.log")
	if err := os.WriteFile(logPath, []byte("работает без автопереподключения\n"), 0644); err != nil {
		t.Fatal(err)
	}

	wakeCh := make(chan struct{}, 5)
	link := &fakeLink{st: awgmproto.State{PID: 401, UptimeS: 500}}
	runner := &fakeRunner{pid: 401, alive: true}
	p := NewProc(ProcConfig{
		ID:      "proc",
		LogPath: logPath,
		Runner:  runner,
		Link:    link,
		Gate:    okGate{},
		Now:     time.Now,
		Wake:    func() { wakeCh <- struct{}{} },
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(false, 0) // выключено!
	defer p.stopLogWatcher()

	obs, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe err: %v", err)
	}
	_ = obs

	// Включаем автопереподключение у работающего клиента:
	p.SetAutoReconnect(true, 0)

	obs2, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe 2 err: %v", err)
	}
	_ = obs2

	// Проверяем, что watcher активен
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("failed to allocate TURN\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	select {
	case <-wakeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("таймаут ожидания wake после включения автопереподключения")
	}

	obs3, err := p.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe 3 err: %v", err)
	}
	if obs3.Attrs[attrFatalError] != "failed to allocate TURN" {
		t.Fatalf("ожидали 'failed to allocate TURN', got %q", obs3.Attrs[attrFatalError])
	}
}

func TestProcAutoReconnect_InotifyDirectoryIsolation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "my_proc.log")
	otherLogPath := filepath.Join(dir, "other_proc.log")
	if err := os.WriteFile(logPath, []byte("лог нашего процесса\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherLogPath, []byte("лог чужого процесса\n"), 0644); err != nil {
		t.Fatal(err)
	}

	wakeCh := make(chan struct{}, 5)
	p := NewProc(ProcConfig{
		ID:      "proc",
		LogPath: logPath,
		Runner:  &fakeRunner{pid: 501, alive: true},
		Link:    &fakeLink{st: awgmproto.State{PID: 501, UptimeS: 100}},
		Gate:    okGate{},
		Now:     time.Now,
		Wake:    func() { wakeCh <- struct{}{} },
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(true, 0)
	defer p.stopLogWatcher()

	if _, err := p.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Запись фатальной ошибки в ЧУЖОЙ лог в том же каталоге
	f, err := os.OpenFile(otherLogPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("error 401: Unauthorized\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Наш watcher НЕ должен реагировать на запись в чужой лог!
	select {
	case <-wakeCh:
		t.Fatal("получен wake на запись в чужой файл лога в том же каталоге!")
	case <-time.After(200 * time.Millisecond):
		// успешно — изоляция работает!
	}
}

func TestProcAutoReconnect_ConcurrentInotifyAndObserve(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "race.log")
	if err := os.WriteFile(logPath, []byte("старый лог\n"), 0644); err != nil {
		t.Fatal(err)
	}

	wakeCh := make(chan struct{}, 100)
	link := &fakeLink{st: awgmproto.State{PID: 601, UptimeS: 120}}
	runner := &fakeRunner{pid: 601, alive: true}
	p := NewProc(ProcConfig{
		ID:      "proc",
		LogPath: logPath,
		Runner:  runner,
		Link:    link,
		Gate:    okGate{},
		Now:     time.Now,
		Wake: func() {
			select {
			case wakeCh <- struct{}{}:
			default:
			}
		},
	})
	p.SetDesired(true, []string{"-peer", "x"}, nil)
	p.SetAutoReconnect(true, 0)
	defer p.stopLogWatcher()

	if _, err := p.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Один воркер вызывает Observe, а в фоне идут дозаписи в лог
	stop := make(chan struct{})
	var workerDone sync.WaitGroup
	workerDone.Add(1)
	go func() {
		defer workerDone.Done()
		for {
			select {
			case <-stop:
				return
			default:
				obs, err := p.Observe(context.Background())
				if err == nil && obs.Attrs[attrFatalError] == "error 401: Unauthorized" {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()

	// Дописываем ошибку
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("error 401: Unauthorized\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Ждём либо wake, либо завершение воркера
	select {
	case <-wakeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("таймаут ожидания wake")
	}

	close(stop)
	workerDone.Wait()

	obsFinal, err := p.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if obsFinal.Attrs[attrFatalError] != "error 401: Unauthorized" {
		t.Fatalf("финальный Observe не зафиксировал фатальную ошибку: %q", obsFinal.Attrs[attrFatalError])
	}
}
