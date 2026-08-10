package bypassset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// resetIPSetHealthForTest полностью очищает кэш health-check'а (включая
// state-карту переходов, которую прод-resetIPSetCache сознательно сохраняет)
// — тесты не должны видеть вердикты и transition-состояние друг друга.
func resetIPSetHealthForTest() {
	ipsetHealth.mu.Lock()
	defer ipsetHealth.mu.Unlock()
	ipsetHealth.path = ""
	ipsetHealth.checked = time.Time{}
	ipsetHealth.probing = false
	ipsetHealth.broken = false
	ipsetHealth.state = map[string]string{}
}

// writeStubIPSet кладёт исполняемый скрипт-заглушку ipset во временную
// директорию и возвращает путь.
func writeStubIPSet(t *testing.T, script string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ipset")
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// captureAppLog собирает вызовы AppLog для проверки журналирования вердиктов.
type captureAppLog struct {
	mu      sync.Mutex
	entries []string // "LEVEL action target message"
}

func (c *captureAppLog) AppLog(level logging.Level, group, subgroup, action, target, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, fmt.Sprintf("%s %s %s %s", level, action, target, message))
}

func (c *captureAppLog) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.entries...)
}

// withHealthTestEnv подменяет candidate paths, пробу и журнал health-check'а,
// восстанавливая всё по завершении теста. probe == nil оставляет реальную
// пробу. Подмена пробы и путей идёт под мьютексом кэша — фоновые
// refresher-горутины прошлых тестов не гоняются с записью.
func withHealthTestEnv(t *testing.T, paths []string, probe func(path string) (bool, string)) *captureAppLog {
	t.Helper()
	capture := &captureAppLog{}
	ipsetHealth.mu.Lock()
	origPaths := ipsetBinaryPaths
	origProbe := probeIPSetBinary
	origLog := healthLog
	ipsetBinaryPaths = paths
	if probe != nil {
		probeIPSetBinary = probe
	}
	healthLog = logging.NewScopedLogger(capture, logging.GroupRouting, logging.SubBypassSet)
	ipsetHealth.mu.Unlock()
	resetIPSetHealthForTest()
	t.Cleanup(func() {
		ipsetHealth.mu.Lock()
		ipsetBinaryPaths = origPaths
		probeIPSetBinary = origProbe
		healthLog = origLog
		ipsetHealth.mu.Unlock()
		resetIPSetHealthForTest()
	})
	return capture
}

// Битый первый кандидат (существует, но не исполняется) пропускается с Warn,
// выбирается следующий рабочий; повторный вызов отдаёт кэш без новых проб.
func TestIPSetBinary_SkipsBrokenCandidate(t *testing.T) {
	broken := writeStubIPSet(t, "#!/bin/sh\nexit 127\n")
	good := writeStubIPSet(t, "#!/bin/sh\nexit 0\n")

	var probes []string
	var probesMu sync.Mutex
	capture := withHealthTestEnv(t, []string{broken, good}, func(path string) (bool, string) {
		probesMu.Lock()
		probes = append(probes, path)
		probesMu.Unlock()
		if path == broken {
			return true, "error while loading shared libraries: libc.so"
		}
		return false, ""
	})

	if got := IPSetBinary(); got != good {
		t.Fatalf("expected %q, got %q", good, got)
	}
	if got := IPSetBinary(); got != good { // из кэша
		t.Fatalf("cached call: expected %q, got %q", good, got)
	}
	probesMu.Lock()
	n := len(probes)
	probesMu.Unlock()
	if n != 2 { // broken + good, второй вызов IPSetBinary проб не делает
		t.Errorf("expected 2 probes, got %d: %v", n, probes)
	}

	logs := capture.all()
	if len(logs) != 1 {
		t.Fatalf("expected exactly 1 log entry, got %d: %v", len(logs), logs)
	}
	if !strings.HasPrefix(logs[0], "warn ") {
		t.Errorf("expected warn-level entry, got %q", logs[0])
	}
	if !strings.Contains(logs[0], broken) || !strings.Contains(logs[0], "libc.so") {
		t.Errorf("warn must name the broken path and error, got %q", logs[0])
	}
	if !strings.Contains(logs[0], "--force-reinstall install") {
		t.Errorf("warn must carry the remediation hint, got %q", logs[0])
	}
	ipsetHealth.mu.Lock()
	brokenDetected := ipsetHealth.broken
	ipsetHealth.mu.Unlock()
	if !brokenDetected {
		t.Error("broken flag must be set for InstallIPSet's --force-reinstall path")
	}
}

// Все кандидаты битые: "" кэшируется (без проб на каждый вызов), Warn
// пишется на каждый СКАН (коалесцер журнала складывает повторы), а
// восстановление после resetIPSetCache журналируется Info-переходом.
func TestIPSetBinary_BrokenThenRecovered(t *testing.T) {
	bin := writeStubIPSet(t, "#!/bin/sh\nexit 0\n")

	var healthy bool
	var healthyMu sync.Mutex
	capture := withHealthTestEnv(t, []string{bin}, func(string) (bool, string) {
		healthyMu.Lock()
		defer healthyMu.Unlock()
		if healthy {
			return false, ""
		}
		return true, "exit status 127"
	})

	if got := IPSetBinary(); got != "" {
		t.Fatalf("expected empty for broken binary, got %q", got)
	}
	if got := IPSetBinary(); got != "" { // отрицательный вердикт тоже из кэша
		t.Fatalf("cached call: expected empty, got %q", got)
	}
	if logs := capture.all(); len(logs) != 1 {
		t.Fatalf("expected 1 warn (single scan), got %d: %v", len(logs), logs)
	}

	// Тот же сбой после сброса кэша — новый скан, новый Warn (повторы
	// схлопывает коалесцер журнала, а не health-check).
	resetIPSetCache()
	if got := IPSetBinary(); got != "" {
		t.Fatalf("still broken: expected empty, got %q", got)
	}
	if logs := capture.all(); len(logs) != 2 {
		t.Fatalf("second scan must re-warn, got %d: %v", len(logs), logs)
	}

	// «Переустановка пакета»: бинарь ожил, InstallIPSet сбрасывает кэш.
	healthyMu.Lock()
	healthy = true
	healthyMu.Unlock()
	resetIPSetCache()
	if got := IPSetBinary(); got != bin {
		t.Fatalf("after recovery: expected %q, got %q", bin, got)
	}
	logs := capture.all()
	if len(logs) != 3 {
		t.Fatalf("expected warn,warn,info — got %d: %v", len(logs), logs)
	}
	if !strings.HasPrefix(logs[2], "info ") || !strings.Contains(logs[2], "runnable again") {
		t.Errorf("expected recovery info entry, got %q", logs[2])
	}
}

// Протухший вердикт освежается фоном: вызывающий получает прежний путь
// МГНОВЕННО, пока проба ещё висит, и не блокируется на ней.
func TestIPSetBinary_StaleWhileRevalidate(t *testing.T) {
	bin := writeStubIPSet(t, "#!/bin/sh\nexit 0\n")

	probeStarted := make(chan struct{})
	probeRelease := make(chan struct{})
	var refreshCalls int
	var refreshMu sync.Mutex
	_ = withHealthTestEnv(t, []string{bin}, func(string) (bool, string) {
		refreshMu.Lock()
		refreshCalls++
		n := refreshCalls
		refreshMu.Unlock()
		if n > 1 { // фоновый refresh — держим до release
			close(probeStarted)
			<-probeRelease
		}
		return false, ""
	})

	if got := IPSetBinary(); got != bin { // первый вызов — синхронный скан
		t.Fatalf("first call: expected %q, got %q", bin, got)
	}

	// Протухание позитивного вердикта.
	ipsetHealth.mu.Lock()
	ipsetHealth.checked = time.Now().Add(-ipsetHealthyTTL - time.Minute)
	ipsetHealth.mu.Unlock()

	done := make(chan string, 1)
	go func() { done <- IPSetBinary() }()
	select {
	case got := <-done:
		if got != bin {
			t.Fatalf("stale call: expected %q, got %q", bin, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("IPSetBinary blocked on the background probe — stale-while-revalidate broken")
	}

	// Дожидаемся, что фоновый скан реально стартовал, и отпускаем его.
	select {
	case <-probeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("background refresh never started")
	}
	close(probeRelease)

	// Публикация фонового вердикта: probing снят, checked освежён.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ipsetHealth.mu.Lock()
		fresh := !ipsetHealth.probing && time.Since(ipsetHealth.checked) < time.Minute
		ipsetHealth.mu.Unlock()
		if fresh {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background refresh never published")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := IPSetBinary(); got != bin {
		t.Fatalf("after refresh: expected %q, got %q", bin, got)
	}
}

// Классификация пробы: «битый» — только exec-класс сбоев. Таймаут и
// ненулевой exit ≠126/127 (ipset ≥7 не достучался до ядра на раннем буте)
// НЕ выносят вердикт о бинаре.
func TestClassifyIPSetProbe(t *testing.T) {
	if broken, _ := classifyIPSetProbe(&sysexec.Result{}, nil); broken {
		t.Error("clean exit must not be broken")
	}
	if broken, _ := classifyIPSetProbe(&sysexec.Result{}, sysexec.ErrTimeout); broken {
		t.Error("timeout on a loaded router must not mark the binary broken")
	}
	if broken, _ := classifyIPSetProbe(&sysexec.Result{}, fmt.Errorf("wrapped: %w", sysexec.ErrTimeout)); broken {
		t.Error("wrapped timeout must not mark the binary broken")
	}
	startErr := errors.New("fork/exec /opt/sbin/ipset: permission denied")
	if broken, detail := classifyIPSetProbe(nil, startErr); !broken || detail == "" {
		t.Error("process start failure must be broken with detail")
	}
}

// Дефолтная проба (реальный запуск `<bin> version`) различает:
// исполняемый скрипт — ок; exit 1 (ядро недоступно у здорового ipset ≥7) —
// НЕ битый; exit 127 (ld.so при битой установке) — битый, stderr в detail.
func TestProbeIPSetBinary_RealExecution(t *testing.T) {
	good := writeStubIPSet(t, "#!/bin/sh\nexit 0\n")
	if broken, _ := probeIPSetBinary(good); broken {
		t.Error("healthy stub must pass probe")
	}

	kernelErr := writeStubIPSet(t, "#!/bin/sh\necho 'Kernel error received: set type not supported' >&2\nexit 1\n")
	if broken, _ := probeIPSetBinary(kernelErr); broken {
		t.Error("exit 1 (kernel/runtime error) must not mark the binary broken")
	}

	loaderErr := writeStubIPSet(t, "#!/bin/sh\necho 'error while loading shared libraries: libc.so' >&2\nexit 127\n")
	broken, detail := probeIPSetBinary(loaderErr)
	if !broken {
		t.Fatal("exit 127 (loader failure) must mark the binary broken")
	}
	if !strings.Contains(detail, "libc.so") {
		t.Errorf("probe detail must surface stderr, got %q", detail)
	}

	if broken, _ := probeIPSetBinary(filepath.Join(t.TempDir(), "missing")); !broken {
		t.Error("non-startable path must be broken")
	}
}
