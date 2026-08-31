// internal/singbox/validate.go
package singbox

import (
	"context"
	"fmt"
	"os/exec"
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
		return fmt.Errorf("sing-box check failed: %s: %w", string(out), err)
	}
	return nil
}
