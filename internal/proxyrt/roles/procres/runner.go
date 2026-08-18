// Package procres — процессные ресурсы прокси-ролей: запуск ребёнка, гейт
// пригодности бинаря, ресурс process и передача TUN. Один каркас на все
// четыре роли; различия ролей приходят данными, не копиями кода.
package procres

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

// Runner — запуск и остановка одного дочернего процесса. Пайпов НЕТ: stdout и
// stderr — /dev/null (§2 протокола: журнал пишет сам процесс в файл в tmpfs,
// и умирающий менеджер больше не тянет ребёнка за собой через SIGPIPE).
// Отсюда же нет ни drain-горутин, ни startupGrace старого process.go: «умер
// на старте» ловит наблюдение (pid мёртв, сокет не открылся), причина — в
// журнале процесса.
type Runner struct {
	binary  string
	pidPath string
	env     []string
}

func NewRunner(binary, pidPath string, env []string) *Runner {
	return &Runner{binary: binary, pidPath: pidPath, env: env}
}

// Start порождает ребёнка и пишет pid-файл. Ошибок «уже бежит» здесь нет:
// это знание наблюдения, решение о старте принимает план ресурса.
//
// exec.Command БЕЗ контекста — намеренно и это несущая стена: жизнь ребёнка
// НЕ привязана к жизни менеджера. CommandContext с дефолтным Cancel убивал бы
// всех детей при остановке демона (отмена контекста воркера в Worker.Stop) —
// и хоронил бы усыновление (§5.2 протокола) вместе со свойством «перезапуск
// демона перестаёт быть событием» (спека §6). ctx поэтому в сигнатуре не
// используется для порождения — он остаётся ради симметрии интерфейса и
// проверки отмены ДО старта.
func (r *Runner) Start(ctx context.Context, args []string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err // демон уже останавливается — новых детей не порождаем
	}
	st, err := os.Stat(r.binary)
	if err != nil || st.IsDir() || st.Mode().Perm()&0o111 == 0 {
		return 0, fmt.Errorf("бинарь %s не найден или неисполним", r.binary)
	}
	if err := os.MkdirAll(filepath.Dir(r.pidPath), 0o755); err != nil {
		return 0, err
	}
	cmd := exec.Command(r.binary, args...)
	cmd.Env = append(os.Environ(), r.env...)
	// os/exec сам подставляет /dev/null при незаданных Stdout/Stderr —
	// назначаем явно, чтобы контракт не держался на умолчании.
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devnull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	childproc.SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", filepath.Base(r.binary), err)
	}
	pid := cmd.Process.Pid
	// Reaper: без Wait ребёнок остался бы зомби. Статус никому не нужен —
	// смерть детектится pid-проверкой и закрытием управляющего сокета.
	go func() { _, _ = cmd.Process.Wait() }()
	if err := os.WriteFile(r.pidPath, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		_ = childproc.TerminateGroup(pid)
		return 0, fmt.Errorf("pid-файл: %w", err)
	}
	return pid, nil
}

// Stop гасит группу процесса: TERM, до 3 с ожидания, затем KILL.
// pid приходит от вызывающего: для своего ребёнка — из Start, для
// усыновлённого — из hello управляющего сокета (§5.2: pid-файл после
// перезапуска менеджера доверия не заслуживает).
func (r *Runner) Stop(ctx context.Context, pid int) error {
	if pid <= 0 {
		return nil
	}
	_ = childproc.TerminateGroup(pid)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if !childproc.IsAlive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if childproc.IsAlive(pid) {
		_ = childproc.KillGroup(pid)
	}
	_ = os.Remove(r.pidPath)
	return nil
}

// AlivePID — жив ли процесс из pid-файла И наш ли он. Сверка с бинарём
// обязательна: pid-файл переживает ребут, номер мог достаться постороннему.
func (r *Runner) AlivePID() (int, bool) {
	b, err := os.ReadFile(r.pidPath)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if !childproc.IsAlive(pid) || !childproc.MatchesBinary(pid, r.binary) {
		return pid, false
	}
	return pid, true
}
