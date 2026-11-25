package singbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// Гоча стенда 2026-06-17 (не пиннутый бинарь): SIGHUP при живом tun =
// TUNSETIFF busy → FATAL; пиннутый переживает (стенд 2026-09-25). Process
// решает Stop+Start по ReloadNeedsRestart, а её проводка к оркестратору
// (замыкание `ReloadNeedsRestart`, которое ставит `NewOperator`) не пиновалась:
// `return false` был зелёным.
func TestOperator_ReloadNeedsRestart_FollowsOrchestratorTun(t *testing.T) {
	op := NewOperator(OperatorDeps{Dir: t.TempDir()})
	if op.Process().ReloadNeedsRestart == nil {
		t.Fatal("NewOperator обязан привязать ReloadNeedsRestart")
	}
	if op.Process().ReloadNeedsRestart() {
		t.Fatal("без оркестратора рестарт не нужен")
	}

	proc := &integrationProc{} // не запущен → Reload делает Start
	orch := singboxorch.NewWithAppliedPath(op.ConfigDir(), proc,
		filepath.Join(t.TempDir(), "singbox-applied.json"))
	t.Cleanup(orch.Close)
	for _, meta := range singboxorch.KnownSlots() {
		if meta.Slot == singboxorch.SlotBase || meta.Slot == singboxorch.SlotRouter {
			if err := orch.Register(meta); err != nil {
				t.Fatalf("register %s: %v", meta.Slot, err)
			}
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := orch.SaveSilent(singboxorch.SlotBase,
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`)); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	withTun := `{"inbounds":[{"type":"tun","tag":"tun-in","interface_name":"opkgtun3","address":["172.19.7.1/30"]}],"route":{"final":"direct"}}`
	if err := orch.SaveSilent(singboxorch.SlotRouter, []byte(withTun)); err != nil {
		t.Fatalf("seed router: %v", err)
	}
	if err := orch.SetEnabledSilent(singboxorch.SlotRouter, true); err != nil {
		t.Fatalf("enable router: %v", err)
	}
	if err := orch.ReloadNow(); err != nil {
		t.Fatalf("ReloadNow (tun): %v", err)
	}
	if !orch.CurrentHasTun() {
		t.Fatal("фикстура: после Reload с tun-inbound CurrentHasTun обязан быть true")
	}

	op.SetOrch(orch)
	if !op.Process().ReloadNeedsRestart() {
		t.Fatal("при живом tun Reload обязан быть Stop+Start, а не SIGHUP")
	}

	// Пиннутый бинарь переживает SIGHUP с tun (стенд 25.09.2026) — рестарт не
	// нужен. Бинарь «пиннутый» по свежему сайдкару с версией пина.
	bin := filepath.Join(t.TempDir(), "sing-box")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeSidecar(bin, installer.RequiredVersion); err != nil {
		t.Fatal(err)
	}
	op.binary = bin
	op.resetVersionCache()
	if !op.TunHotReload() {
		t.Fatal("фикстура: пиннутый бинарь обязан давать TunHotReload")
	}
	if op.Process().ReloadNeedsRestart() {
		t.Fatal("пиннутый бинарь при живом tun — SIGHUP, не Stop+Start")
	}
	op.binary = filepath.Join(t.TempDir(), "absent")
	op.resetVersionCache()

	noTun := `{"route":{"final":"direct"}}`
	if err := orch.SaveSilent(singboxorch.SlotRouter, []byte(noTun)); err != nil {
		t.Fatalf("router без tun: %v", err)
	}
	if err := orch.ReloadNow(); err != nil {
		t.Fatalf("ReloadNow (no tun): %v", err)
	}
	if op.Process().ReloadNeedsRestart() {
		t.Fatal("без tun рестарт не нужен — SIGHUP")
	}
}

// Гейт горячего reload смотрит и на процесс: файл на диске подменён на
// пиннутый под живым чужим процессом — SIGHUP получил бы процесс, не знающий
// external_configuration. Пока процесс не из этого файла — рестарт.
func TestOperator_TunHotReload_RequiresRunningExeIsBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sing-box")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeSidecar(bin, installer.RequiredVersion); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(OperatorDeps{Dir: dir, Binary: bin})
	if err := os.WriteFile(op.pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	op.proc.matchBinaryFn = func(int) bool { return true }

	op.exeMatches = func(int, string) bool { return false }
	if op.TunHotReload() {
		t.Error("процесс не из пиннутого файла — горячий reload запрещён")
	}
	op.exeMatches = func(int, string) bool { return true }
	if !op.TunHotReload() {
		t.Error("процесс из пиннутого файла — горячий reload разрешён")
	}
}

// Пустая версия (чужой бинарь, проба не удалась) не перепробуется на каждом
// вызове: TunHotReload зовут тик reconcile и каждый Process.Reload под локами.
func TestDetectVersion_EmptyResultRetriedAfterTTL(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sing-box")
	count := filepath.Join(dir, "count")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho x >> "+count+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(OperatorDeps{Dir: dir, Binary: bin})
	probes := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}

	op.detectVersionAndFeaturesCached(context.Background())
	op.detectVersionAndFeaturesCached(context.Background())
	if n := probes(); n != 1 {
		t.Fatalf("в пределах TTL пустая версия из кэша: проб %d, want 1", n)
	}
	op.versionProbeRetryAt = time.Time{} // TTL истёк
	op.detectVersionAndFeaturesCached(context.Background())
	if n := probes(); n != 2 {
		t.Fatalf("после TTL проба обязана повториться: проб %d, want 2", n)
	}
}

// Пиннутый бинарь — want и known. Неизвестная версия — см. тест ниже.
func TestOperator_TunExternalConfig_Pinned(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sing-box")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(OperatorDeps{Dir: dir, Binary: bin})
	if err := writeSidecar(bin, installer.RequiredVersion); err != nil {
		t.Fatal(err)
	}
	op.resetVersionCache()
	if want, known := op.TunExternalConfig(); !want || !known {
		t.Errorf("пиннутый бинарь: want=%v known=%v, ожидалось true,true", want, known)
	}
}

// Неизвестная версия + процесс не запущен (мог отвергнуть незнакомый ключ) —
// known=true, want=false: флаг снимается, иначе движок так и не поднимется.
// Процесс из этого файла жив — known=false: флаг не трогаем.
func TestOperator_TunExternalConfig_UnknownVersionDependsOnRunning(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sing-box")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(OperatorDeps{Dir: dir, Binary: bin})
	if want, known := op.TunExternalConfig(); want || !known {
		t.Errorf("не запущен: want=%v known=%v, ожидалось false,true", want, known)
	}
	if err := os.WriteFile(op.pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	op.proc.matchBinaryFn = func(int) bool { return true }
	op.exeMatches = func(int, string) bool { return true }
	if want, known := op.TunExternalConfig(); want || known {
		t.Errorf("процесс из файла жив: want=%v known=%v, ожидалось false,false", want, known)
	}
}

// Ключ и SIGHUP с tun знает любая сборка форка с awgm.28, а не только пин: иначе
// панель с новым пином снимала флаг у прежнего ядра, и то падало на старте (F911).
func TestOperator_TunExternalConfig_OlderForkBuild(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"42", true},
		{"41", true},
		{"1.15.0-alpha.13-awgm.40", true},
		{"1.15.0-alpha.8-awgm.28", true},
		{"1.15.0-alpha.8-awgm.27", false},
		{"27", false},
		{"1.15.0-alpha.13", false},
		{"1.14.0-awg3.5", false},
		{"1.2.3", false},
		{"awgm.", false},
		{"-1", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "awgm-box")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			op := NewOperator(OperatorDeps{Dir: dir, Binary: bin})
			if err := writeSidecar(bin, tc.version); err != nil {
				t.Fatal(err)
			}
			op.resetVersionCache()
			if want, known := op.TunExternalConfig(); want != tc.want || !known {
				t.Errorf("want=%v known=%v, ожидалось %v,true", want, known, tc.want)
			}
			if got := op.TunHotReload(); got != tc.want {
				t.Errorf("TunHotReload = %v, ожидалось %v", got, tc.want)
			}
		})
	}
}

// Ответный сокет TPROXY не занимает адрес роутера с awgm-box 44 (F910). Чужая,
// прежняя и неизвестная сборка — false: им нужен DNS-RESCUE.
func TestOperator_ReplySocketIsolated(t *testing.T) {
	newOp := func(t *testing.T, version string) *Operator {
		t.Helper()
		dir := t.TempDir()
		bin := filepath.Join(dir, "awgm-box")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		op := NewOperator(OperatorDeps{Dir: dir, Binary: bin})
		if version != "" {
			if err := writeSidecar(bin, version); err != nil {
				t.Fatal(err)
			}
		}
		op.resetVersionCache()
		return op
	}
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"44", true},
		{"45", true},
		{"43", false},
		{"1.15.0-alpha.13-awgm.40", false},
		{"1.15.0-alpha.13", false},
		{"", false},
	} {
		t.Run("версия "+tc.version, func(t *testing.T) {
			if got := newOp(t, tc.version).ReplySocketIsolated(); got != tc.want {
				t.Errorf("ReplySocketIsolated = %v, ожидалось %v", got, tc.want)
			}
		})
	}

	// Файл уже новый, процесс ещё прежний: сокеты держит процесс.
	for _, fromFile := range []bool{true, false} {
		op := newOp(t, "44")
		if err := os.WriteFile(op.pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			t.Fatal(err)
		}
		op.proc.matchBinaryFn = func(int) bool { return true }
		op.exeMatches = func(int, string) bool { return fromFile }
		if got := op.ReplySocketIsolated(); got != fromFile {
			t.Errorf("процесс из файла=%v: ReplySocketIsolated = %v", fromFile, got)
		}
	}
}
