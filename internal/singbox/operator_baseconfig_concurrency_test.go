package singbox

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// clashControllerOf достаёт experimental.clash_api.external_controller.
func clashControllerOf(t *testing.T, base map[string]any) string {
	t.Helper()
	exp, _ := base["experimental"].(map[string]any)
	api, _ := exp["clash_api"].(map[string]any)
	addr, _ := api["external_controller"].(string)
	return addr
}

func logLevelOf(t *testing.T, base map[string]any) string {
	t.Helper()
	logBlock, _ := base["log"].(map[string]any)
	lvl, _ := logBlock["level"].(string)
	return lvl
}

// Две параллельные правки РАЗНЫХ скаляров базы обязаны сойтись в файле обе.
// Прежде mutateBase читал 00-base.json сам, вне лока оркестратора, и писал
// снимок через Save: правка, приехавшая, пока мутатор работал со снимком,
// затиралась (дефект F41 — классический lost update).
//
// Конструкция детерминирована взаимоисключением, а не таймингом: A паркуется
// ВНУТРИ мутатора, B стартует после этого. На исправленном коде B заперт
// локом, poll просто истекает, обе правки ложатся последовательно. На
// дефектном — B успевает записаться за миллисекунды, poll это видит, и A
// затирает его своим снимком.
func TestMutateBase_ConcurrentEditsDoNotLoseUpdates(t *testing.T) {
	dir := t.TempDir()
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir})
	basePath := filepath.Join(dir, "config.d", "00-base.json")

	entered := make(chan struct{})
	release := make(chan struct{})
	wantAddr := ClashAddr(9095)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := op.mutateBase(func(base map[string]any) bool {
			close(entered)
			<-release
			return setLogLevel(base, "debug")
		}); err != nil {
			t.Errorf("mutateBase A: %v", err)
		}
	}()

	<-entered
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := op.mutateBase(func(base map[string]any) bool {
			return setClashController(base, wantAddr)
		}); err != nil {
			t.Errorf("mutateBase B: %v", err)
		}
	}()

	// Ждём появления правки B на диске, пока A запаркован. На исправленном
	// коде она появиться не может — 300 мс это просто верхняя граница
	// ожидания, а не гарантия чего-либо.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(basePath); err == nil && bytes.Contains(data, []byte(wantAddr)) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	wg.Wait()

	base := readBaseFixture(t, basePath)
	if got := logLevelOf(t, base); got != "debug" {
		t.Errorf("log.level = %q, want debug (правка A потеряна)", got)
	}
	if got := clashControllerOf(t, base); got != wantAddr {
		t.Errorf("external_controller = %q, want %q (правка B потеряна)", got, wantAddr)
	}
}

// Правки базы из разных вкладок настроек идут параллельно (порт Clash,
// уровень журнала, bootstrap-DNS). Под -race это ловит разделяемое состояние
// между вызовами mutateBase — карту базы или кандидата на восстановление,
// пережившего вызов.
func TestApplyBaseScalars_NoRaceUnderConcurrentAppliers(t *testing.T) {
	dir := t.TempDir()
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir})
	basePath := filepath.Join(dir, "config.d", "00-base.json")

	levels := []string{"debug", "info", "warn"}
	ports := []int{9091, 9092, 9093}
	resolvers := []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := op.ApplyLogLevel(levels[i%len(levels)]); err != nil {
				t.Errorf("ApplyLogLevel: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := op.ApplyClashPort(ports[i%len(ports)]); err != nil {
				t.Errorf("ApplyClashPort: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := op.ApplyBootstrapDNS(resolvers[i%len(resolvers)]); err != nil {
				t.Errorf("ApplyBootstrapDNS: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	base := readBaseFixture(t, basePath)
	if got := logLevelOf(t, base); !contains(levels, got) {
		t.Errorf("log.level = %q, want одно из %v", got, levels)
	}
	addr := clashControllerOf(t, base)
	okAddr := false
	for _, p := range ports {
		if addr == ClashAddr(p) {
			okAddr = true
		}
	}
	if !okAddr {
		t.Errorf("external_controller = %q, want адрес одного из портов %v", addr, ports)
	}
	if got := bootstrapServerOf(t, base); !contains(resolvers, got) {
		t.Errorf("dns-bootstrap.server = %q, want одно из %v", got, resolvers)
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
