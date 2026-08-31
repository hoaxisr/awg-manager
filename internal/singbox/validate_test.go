// internal/singbox/validate_test.go
package singbox

import (
	"context"
	"errors"
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
