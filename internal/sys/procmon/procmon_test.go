package procmon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestParseProcStat(t *testing.T) {
	tmpDir := t.TempDir()
	statFile := filepath.Join(tmpDir, "stat")
	content := "1351 (awg-manager) S 1 1351 1351 0 -1 4194304 3144 0 0 0 120 45 0 0 20 0 8 0 1234567 104857600 2560 18446744073709551615 0 0 0 0 0 0 0 2147483647 0 0 0 0 17 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
	if err := os.WriteFile(statFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	p, err := parseProcStat(statFile)
	if err != nil {
		t.Fatalf("parseProcStat failed: %v", err)
	}

	if p.comm != "awg-manager" {
		t.Errorf("expected comm 'awg-manager', got %q", p.comm)
	}
	if p.state != "S" {
		t.Errorf("expected state 'S', got %q", p.state)
	}
	if p.utime != 120 {
		t.Errorf("expected utime 120, got %d", p.utime)
	}
	if p.stime != 45 {
		t.Errorf("expected stime 45, got %d", p.stime)
	}
	if p.threads != 8 {
		t.Errorf("expected threads 8, got %d", p.threads)
	}
	if p.rssBytes != 2560*4096 {
		t.Errorf("expected rssBytes %d, got %d", 2560*4096, p.rssBytes)
	}
}

func TestReadMemInfo(t *testing.T) {
	tmpDir := t.TempDir()
	memFile := filepath.Join(tmpDir, "meminfo")
	content := `MemTotal:         516096 kB
MemFree:          124500 kB
MemAvailable:     345000 kB
Buffers:           15000 kB
Cached:           220000 kB
SwapTotal:        262144 kB
SwapFree:         262144 kB
`
	if err := os.WriteFile(memFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	mem, err := readMemInfo(memFile)
	if err != nil {
		t.Fatalf("readMemInfo failed: %v", err)
	}

	if mem.Total != 516096*1024 {
		t.Errorf("expected total %d, got %d", 516096*1024, mem.Total)
	}
	if mem.Available != 345000*1024 {
		t.Errorf("expected available %d, got %d", 345000*1024, mem.Available)
	}
	if mem.Used != (516096-345000)*1024 {
		t.Errorf("expected used %d, got %d", (516096-345000)*1024, mem.Used)
	}
}

func TestReadCPUStat(t *testing.T) {
	tmpDir := t.TempDir()
	statFile := filepath.Join(tmpDir, "stat")
	content := `cpu  1200 50 800 15000 300 20 50 0 0 0
cpu0 600 25 400 7500 150 10 25 0 0 0
cpu1 600 25 400 7500 150 10 25 0 0 0
intr 1234567
`
	if err := os.WriteFile(statFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cpus, err := readCPUStat(statFile)
	if err != nil {
		t.Fatalf("readCPUStat failed: %v", err)
	}

	if len(cpus) != 3 {
		t.Fatalf("expected 3 cpus (total, cpu0, cpu1), got %d", len(cpus))
	}
	if cpus["total"].user != 1200 {
		t.Errorf("expected user 1200, got %d", cpus["total"].user)
	}
	if cpus["cpu0"].idle != 7500 {
		t.Errorf("expected idle 7500, got %d", cpus["cpu0"].idle)
	}
}

// Замер не чаще minSampleInterval, как кнопки интервала в панели (5с/30с):
// повторный запрос раньше получает тот же снимок, без прохода по /proc.
func TestSnapshot_NotMoreOftenThanMinInterval(t *testing.T) {
	s := NewSampler()
	s.procDir = t.TempDir()

	first, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	again, _ := s.Snapshot()
	if again != first {
		t.Fatal("повторный замер раньше 5 с должен вернуть прошлый снимок")
	}

	s.lastSample = time.Now().Add(-minSampleInterval)
	fresh, _ := s.Snapshot()
	if fresh == first {
		t.Fatal("по истечении интервала ждали новый замер")
	}
}

// Снятие процесса сбрасывает снимок: панель обновляет список сразу после
// kill, и снятый процесс не должен висеть живым до конца интервала.
func TestKillProcess_DropsCachedSnapshot(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip("нет sleep: " + err.Error())
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	s := NewSampler()
	s.procDir = t.TempDir()
	first, _ := s.Snapshot()
	if err := s.KillProcess(cmd.Process.Pid, "SIGKILL"); err != nil {
		t.Fatalf("KillProcess: %v", err)
	}
	if after, _ := s.Snapshot(); after == first {
		t.Fatal("после снятия процесса ждали новый замер, а не прошлый снимок")
	}
}
