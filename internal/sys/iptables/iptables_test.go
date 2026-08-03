package iptables

import (
	"context"
	"strings"
	"testing"
)

// Правило с `-m comment` обязано пройти через ленивую подгрузку xt_comment:
// на прошивках, где NDMS сам комменты не использует и sing-box не установлен,
// модуль никем не грузится и ядро отвергает правило с «No chain/target/match
// by that name» (issue #666).
func TestRunEnsuresCommentModuleOnlyForCommentRules(t *testing.T) {
	orig := ensureCommentModuleFn
	t.Cleanup(func() { ensureCommentModuleFn = orig })
	calls := 0
	ensureCommentModuleFn = func(context.Context) { calls++ }

	_ = Run(context.Background(), "-t", "nat", "-S", "POSTROUTING")
	if calls != 0 {
		t.Fatalf("правило без -m comment не должно трогать модуль, вызовов: %d", calls)
	}

	_ = Run(context.Background(), "-t", "nat", "-I", "POSTROUTING", "1",
		"-s", "10.66.66.0/24", "-o", "eth3",
		"-m", "comment", "--comment", "AWGM_WDTT", "-j", "MASQUERADE")
	if calls != 1 {
		t.Fatalf("правило с -m comment должно подгрузить xt_comment, вызовов: %d", calls)
	}
}

// Ошибка правила с комментом при незагруженном модуле должна прямо называть
// причину: голое «No chain/target/match by that name» стоило репортёру #666
// целого раунда переписки.
func TestRunHintsMissingCommentModule(t *testing.T) {
	origEnsure := ensureCommentModuleFn
	origLoaded := commentLoaded
	t.Cleanup(func() {
		ensureCommentModuleFn = origEnsure
		commentLoaded = origLoaded
	})
	ensureCommentModuleFn = func(context.Context) {}
	commentLoaded = false

	// Бинарь /opt/sbin/iptables в тестовой среде отсутствует — Run вернёт
	// ошибку запуска, нам важен только хвост подсказки.
	err := Run(context.Background(), "-t", "nat", "-I", "POSTROUTING", "1",
		"-m", "comment", "--comment", "AWGM_WDTT", "-j", "MASQUERADE")
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "xt_comment") {
		t.Fatalf("нет подсказки про xt_comment: %v", err)
	}

	commentLoaded = true
	err = Run(context.Background(), "-t", "nat", "-I", "POSTROUTING", "1",
		"-m", "comment", "--comment", "AWGM_WDTT", "-j", "MASQUERADE")
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if strings.Contains(err.Error(), "xt_comment") {
		t.Fatalf("модуль загружен — подсказка лишняя: %v", err)
	}
}
