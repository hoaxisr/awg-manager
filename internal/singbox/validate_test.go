// internal/singbox/validate_test.go
package singbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidator_Success(t *testing.T) {
	v := &Validator{
		binary: "echo",
		exec: func(_ context.Context, bin string, args ...string) ([]byte, error) {
			if bin != "echo" {
				t.Errorf("binary: %s", bin)
			}
			if len(args) != 3 || args[0] != "check" || args[1] != "-C" || args[2] != "/tmp/config.d" {
				t.Errorf("args: %v", args)
			}
			return nil, nil
		},
	}
	if err := v.Validate("/tmp/config.d"); err != nil {
		t.Fatal(err)
	}
}

func TestValidator_Failure(t *testing.T) {
	v := &Validator{
		binary: "sing-box",
		exec: func(_ context.Context, bin string, args ...string) ([]byte, error) {
			return []byte("config error: invalid outbound"), errors.New("exit 1")
		},
	}
	err := v.Validate("/tmp/config.d")
	if err == nil {
		t.Fatal("expected error")
	}
}

// F77: зависший `sing-box check` держал лок оркестратора вечно — под ним
// вся singbox-поверхность, включая read-пути. Проверка обязана быть
// ограничена сверху.
func TestValidator_TimesOutOnHungCheck(t *testing.T) {
	v := &Validator{
		binary:  "sing-box",
		timeout: 50 * time.Millisecond,
		exec: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			<-ctx.Done() // «зависший» процесс: живёт, пока его не отменят
			return nil, ctx.Err()
		},
	}

	done := make(chan error, 1)
	go func() { done <- v.Validate("/tmp/config.d") }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("таймаут сработал, но ошибка не возвращена")
		}
		if !strings.Contains(err.Error(), "не уложился") {
			t.Errorf("ошибка не про таймаут: %v", err)
		}
	case <-time.After(3 * time.Second):
		// Сторожевой таймер: без таймаута в Validate мы бы висели навсегда,
		// поэтому мутант «убрать ограничение» падает здесь, а не вешает пакет.
		t.Fatal("Validate не вернулся — ограничения по времени нет")
	}
}

// F116: OOM killer убивает `sing-box check` сигналом, и до фикса ошибка
// выглядела как обычный provал конфига ("exit status N") — непонятно было,
// что дело не в конфиге, а в памяти. Бинарь подменён реальным скриптом (как
// в operator_test.go, TestOperator_GetStatus_UpdateAvailableWhenSameVersionSHADiffers),
// а не фейковым exec-швом: нужен настоящий *exec.ExitError с сигналом в
// ProcessState, который фейковый seam не воспроизвёл бы.
func TestValidator_KilledBySignal(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "sing-box")
	body := []byte("#!/bin/sh\nkill -SEGV $$\n")
	if err := os.WriteFile(binary, body, 0755); err != nil {
		t.Fatal(err)
	}

	v := NewValidator(binary)
	err := v.Validate(dir)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "прерван сигналом") {
		t.Errorf("ошибка не про сигнал: %v", err)
	}
	if !strings.Contains(err.Error(), "MemAvailable") {
		t.Errorf("ошибка не упоминает MemAvailable: %v", err)
	}
}
