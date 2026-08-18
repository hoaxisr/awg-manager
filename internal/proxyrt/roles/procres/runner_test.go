package procres

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// writeScript кладёт исполняемый скрипт — настоящий дочерний процесс, не мок.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// shellRunner — ребёнок = сам /bin/sh: MatchesBinary сверяет basename argv0
// из /proc/cmdline, и только так живость наблюдаема честно (см. Замысел).
func shellRunner(t *testing.T, dir string) *Runner {
	t.Helper()
	return NewRunner("/bin/sh", filepath.Join(dir, "child.pid"), nil)
}

// Замыкающее `:` обязательно. `sh -c "sleep 30"` шелл НЕ выполняет, а
// подменяет собой через execve (оптимизация последней простой команды в
// bash/dash) — argv0 ребёнка становится "sleep", и MatchesBinary("/bin/sh")
// перестаёт признавать его нашим. Лишняя команда в хвосте отключает
// оптимизацию: процесс остаётся /bin/sh, живость наблюдаема честно.
var sleepArgs = []string{"-c", "sleep 30; :"}

func TestRunnerStartWritesPidAndAliveSeesChild(t *testing.T) {
	r := shellRunner(t, t.TempDir())

	pid, err := r.Start(context.Background(), sleepArgs)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Stop(context.Background(), pid) }()

	got, ok := r.AlivePID()
	if !ok || got != pid {
		t.Fatalf("AlivePID = %d,%v; ожидали живой %d", got, ok, pid)
	}
}

func TestRunnerStopKillsGroupAndRemovesPidfile(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	r := NewRunner("/bin/sh", pidPath, nil)
	pid, err := r.Start(context.Background(), sleepArgs)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Stop(context.Background(), pid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := r.AlivePID(); !ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := r.AlivePID(); ok {
		t.Fatal("после Stop процесс обязан быть мёртв")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("pid-файл обязан быть снят, stat: %v", err)
	}
}

func TestRunnerAliveRejectsStalePidfile(t *testing.T) {
	// pid-файл пережил ребут: номер мог достаться чужому процессу. Признание
	// только через сверку /proc с бинарём (childproc.MatchesBinary), не сигналом.
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	// Чужой pid — сам тестовый процесс: он гарантированно жив, а argv0
	// (procres.test) заведомо не sh. PID 1 сюда не годится: kill(1, 0) из-под
	// непривилегированного пользователя даёт EPERM, IsAlive возвращает false,
	// и сверка с бинарём не успевает сработать — страж молчал бы и без неё.
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRunner("/bin/sh", pidPath, nil)
	if _, ok := r.AlivePID(); ok {
		t.Fatal("чужой живой pid не должен признаваться нашим")
	}
}

func TestRunnerStartFailsWithoutBinary(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(filepath.Join(dir, "no-such"), filepath.Join(dir, "p.pid"), nil)
	if _, err := r.Start(context.Background(), nil); err == nil {
		t.Fatal("старт без бинаря обязан отказывать с причиной")
	}
}

func TestRunnerChildSurvivesCanceledStartContext(t *testing.T) {
	// НЕСУЩАЯ СТЕНА: жизнь ребёнка не привязана к контексту менеджера.
	// Отмена контекста, с которым звался Start (= остановка демона), НЕ
	// должна убивать процесс — иначе усыновление после рестарта демона
	// мертво (§5.2 протокола, спека §6 «перезапуск демона — не событие»).
	// Ребёнок — /bin/sh: при возврате CommandContext SIGKILL по cancel
	// убьёт его, AlivePID станет false, и страж честно умрёт.
	r := shellRunner(t, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	pid, err := r.Start(ctx, sleepArgs)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Stop(context.Background(), pid) }()

	cancel()
	time.Sleep(300 * time.Millisecond) // дать гипотетическому Cancel сработать
	if _, ok := r.AlivePID(); !ok {
		t.Fatal("ребёнок умер от отмены контекста менеджера — CommandContext прокрался обратно")
	}
}
