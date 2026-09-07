// internal/singbox/validate.go
package singbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// checkTimeout ограничивает `sing-box check` сверху. Без него зависший
// процесс (например, D-state на битом ubifs — бинарь лежит на флеше)
// держал бы лок оркестратора ВЕЧНО, а под этим локом сидит вся
// singbox-поверхность, включая read-пути; лечилось бы только рестартом
// awg-manager (F77).
//
// 60 с — это порядок запаса, а не рабочая величина: на mipsel замерено
// 2.3 с на нагруженном конфиге минимальной сборкой, прод-бинарь тяжелее.
// Смысл в том, чтобы отличить «медленно» от «никогда», не задевая первое.
const checkTimeout = 60 * time.Second

// waitDelay — сколько ждать смерти процесса после отмены контекста.
// Без него `Wait` зависшего в D-state процесса повис бы вместе с ним:
// отмена шлёт сигнал, но не гарантирует, что он будет доставлен.
const waitDelay = 5 * time.Second

type Validator struct {
	binary string
	// timeout нулевой в тестовых конструкциях — тогда берётся checkTimeout.
	timeout time.Duration
	exec    func(ctx context.Context, bin string, args ...string) ([]byte, error)
}

// DefaultBinaryPath returns the canonical on-disk path for the managed
// sing-box binary (mirrors installer.DefaultBinaryPath via the package-
// level defaultBinary const so validator callers don't need to import the
// installer sub-package).
func DefaultBinaryPath() string { return defaultBinary }

func NewValidator(binary string) *Validator {
	return &Validator{
		binary:  binary,
		timeout: checkTimeout,
		exec: func(ctx context.Context, bin string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.WaitDelay = waitDelay
			return cmd.CombinedOutput()
		},
	}
}

func (v *Validator) Validate(configDir string) error {
	timeout := v.timeout
	if timeout <= 0 {
		timeout = checkTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := v.exec(ctx, v.binary, "check", "-C", configDir)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("sing-box check не уложился в %s: %s", timeout, string(out))
		}
		// OOM killer убивает check сигналом (обычно SIGKILL/SIGSEGV) до того,
		// как он успеет напечатать FATAL-диагностику конфига — без этой
		// проверки такой сбой неотличим от обычной ошибки конфигурации
		// (exit status N), и человек чинит не то.
		if sig, ok := signalFromExecError(err); ok {
			return fmt.Errorf(
				"sing-box check прерван сигналом %s (вероятно, не хватило памяти: MemAvailable %s) — конфигурация не проверена",
				sig, readMemAvailable(),
			)
		}
		return fmt.Errorf("sing-box check failed: %s: %w", string(out), err)
	}
	return nil
}

// signalFromExecError сообщает, был ли процесс убит сигналом, и каким.
func signalFromExecError(err error) (string, bool) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ProcessState == nil {
		return "", false
	}
	ws, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return "", false
	}
	return ws.Signal().String(), true
}

// readMemAvailable читает MemAvailable из /proc/meminfo (только Linux).
// "неизвестно" при ошибке чтения или отсутствии строки.
func readMemAvailable() string {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return "неизвестно"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return "неизвестно"
}
