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
