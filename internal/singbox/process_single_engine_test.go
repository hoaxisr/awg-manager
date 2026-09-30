package singbox

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// F583: заглушка как на стенде 30.09 — `run` превращается в sleep (Clash API
// не открывает, а /proc-опознание видит «sleep», не «sing-box», и IsRunning
// отвечает «не работает» при живом ребёнке), `check` отрабатывает и выходит.
func writeEngineStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sing-box")
	script := "#!/bin/sh\ncase \"$1\" in\n  check) sleep 1; exit 0 ;;\n  run) exec sleep 30 ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// newStubEngine — Process на заглушке с настоящими сигналами. Cleanup
// добивает всё, что осталось живым (иначе Close ждал бы мониторы вечно).
func newStubEngine(t *testing.T) (*Process, *[]*processGen) {
	t.Helper()
	dir := t.TempDir()
	p := NewProcess(writeEngineStub(t), filepath.Join(dir, "missing-config.d"), filepath.Join(dir, "sing-box.pid"))
	p.logDir = dir
	gens := &[]*processGen{}
	t.Cleanup(func() {
		for _, g := range *gens {
			if g.alive() {
				_ = syscall.Kill(g.pid, syscall.SIGKILL)
			}
		}
		p.Close()
	})
	return p, gens
}

// noteGen запоминает поколение, заспавненное последним Start.
func noteGen(p *Process, gens *[]*processGen) {
	if p.curGen == nil {
		return
	}
	for _, g := range *gens {
		if g == p.curGen {
			return
		}
	}
	*gens = append(*gens, p.curGen)
}

func liveEngines(gens []*processGen) int {
	n := 0
	for _, g := range gens {
		if g.alive() {
			n++
		}
	}
	return n
}

// startAndWaitOperator — Operator ровно для startAndWait: конфига нет, поэтому
// configDeclaresClashAPI консервативно включает гейт готовности, а Clash на
// закрытом порту готовым не станет никогда.
func startAndWaitOperator(t *testing.T, p *Process) *Operator {
	t.Helper()
	prev := maxSingboxBootWait
	maxSingboxBootWait = 300 * time.Millisecond
	t.Cleanup(func() { maxSingboxBootWait = prev })
	return &Operator{
		proc:       p,
		configPath: p.configPath,
		clash:      NewClashClient("127.0.0.1:1"),
		log:        slog.Default(),
	}
}

// (a) Старт, признанный неудачным по готовности, гасит и пожинает СВОЕГО
// ребёнка до возврата ошибки, и после Close не остаётся ни одной горутины
// (монитор cmd.Wait в их числе).
func TestStartAndWait_ReadinessFailureReapsChild(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	p, gens := newStubEngine(t)
	o := startAndWaitOperator(t, p)

	if _, err := o.startAndWait(context.Background()); err == nil {
		t.Fatal("startAndWait = nil, want readiness error (Clash never answers)")
	}
	noteGen(p, gens)
	if len(*gens) != 1 {
		t.Fatalf("spawned generations = %d, want 1", len(*gens))
	}
	if (*gens)[0].alive() {
		t.Fatalf("engine pid %d still alive (not reaped) after failed start", (*gens)[0].pid)
	}
	p.Close()
	goleak.VerifyNone(t, ignore)
}

// (b) Череда неудачных стартов движка — как на стенде: холодный старт
// оркестратора (proc.Start без гейта) вперемешку с авто-рестартом сторожа
// (startAndWait, гейт не проходит) — оставляет не больше одного живого
// движка после каждого шага. До F583 каждый Start при «не работает» от
// IsRunning спавнил ещё одного, не трогая прежнего.
func TestProcess_FailedStartsLeaveAtMostOneEngine(t *testing.T) {
	p, gens := newStubEngine(t)
	o := startAndWaitOperator(t, p)

	for i := 0; i < 4; i++ {
		if err := p.Start(); err != nil {
			t.Fatalf("iter %d: Start: %v", i, err)
		}
		noteGen(p, gens)
		if n := liveEngines(*gens); n > 1 {
			t.Fatalf("iter %d after Start: %d live engines, want ≤1", i, n)
		}
		if _, err := o.startAndWait(context.Background()); err == nil {
			t.Fatalf("iter %d: startAndWait = nil, want readiness error", i)
		}
		noteGen(p, gens)
		if n := liveEngines(*gens); n > 1 {
			t.Fatalf("iter %d after startAndWait: %d live engines, want ≤1", i, n)
		}
	}
	if len(*gens) != 8 {
		t.Fatalf("spawned generations = %d, want 8", len(*gens))
	}
}

// Движок, у которого pid-файл пропал при живом ребёнке, гасится по ссылке
// на поколение, а не остаётся сиротой: следующий Start пожинает его и
// спавнит одного нового.
func TestProcess_StartReapsEngineWithoutPidFile(t *testing.T) {
	p, gens := newStubEngine(t)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	noteGen(p, gens)
	if err := os.Remove(p.pidPath); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	noteGen(p, gens)
	if len(*gens) != 2 {
		t.Fatalf("spawned generations = %d, want 2", len(*gens))
	}
	if (*gens)[0].alive() {
		t.Fatalf("first engine pid %d survived the second Start", (*gens)[0].pid)
	}
	if !(*gens)[1].alive() {
		t.Fatal("second engine is not running")
	}
}

// Короткоживущие вызовы бинаря (`check`) — не движок: рестарт движка их не
// трогает, даже когда они идут одновременно.
func TestProcess_EngineRestartSparesConcurrentCheck(t *testing.T) {
	p, gens := newStubEngine(t)
	v := NewValidator(p.binary)
	checkErr := make(chan error, 1)
	go func() { checkErr <- v.Validate(t.TempDir()) }()

	for i := 0; i < 2; i++ {
		if err := p.Start(); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		noteGen(p, gens)
	}
	if n := liveEngines(*gens); n != 1 {
		t.Fatalf("live engines = %d, want 1", n)
	}
	select {
	case err := <-checkErr:
		if err != nil {
			t.Fatalf("concurrent check failed (killed by engine restart?): %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("check did not finish")
	}
}

// (c) Обычный путь не изменился: живой опознанный движок — повторный Start
// no-op, Stop гасит его, пожинает, снимает pid-файл и метит выход как наш.
func TestProcess_NormalStartStopUnchanged(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess("/bin/sleep", filepath.Join(dir, "config.d"), filepath.Join(dir, "sing-box.pid"))
	p.logDir = dir
	p.startCmd = func(bin string, args ...string) (*exec.Cmd, error) { return exec.Command("/bin/sleep", "30"), nil }
	exited := make(chan bool, 1)
	p.OnExit = func(_ error, _ string, deliberate bool) { exited <- deliberate }
	t.Cleanup(p.Close)

	spawned, err := p.StartSpawned()
	if err != nil || !spawned {
		t.Fatalf("StartSpawned = (%v, %v), want (true, nil)", spawned, err)
	}
	gen := p.curGen
	if spawned, err := p.StartSpawned(); err != nil || spawned {
		t.Fatalf("second StartSpawned = (%v, %v), want (false, nil) no-op", spawned, err)
	}
	if p.curGen != gen {
		t.Fatal("no-op Start replaced the generation")
	}
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if gen.alive() {
		t.Fatal("engine not reaped after Stop")
	}
	if _, err := os.Stat(p.pidPath); !os.IsNotExist(err) {
		t.Fatalf("pid file left after Stop: %v", err)
	}
	select {
	case deliberate := <-exited:
		if !deliberate {
			t.Fatal("OnExit deliberate=false for our own Stop")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnExit not called")
	}
}
