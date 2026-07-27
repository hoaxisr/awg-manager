package wdtt

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// newTestProcess подменяет реальный бинарь shell-скриптом через seam startCmd;
// p.binary=/bin/sh проходит binaryPresent, реальная команда — из script.
func newTestProcess(t *testing.T, script string) *process {
	t.Helper()
	p := newProcess("client", "/bin/sh", t.TempDir())
	p.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}
	return p
}

// TestProcess_StartupFailure_StderrCaptured_UnderDrainDelay — детерминированный
// регресс на гонку os/exec: Wait() не должен закрывать пайпы раньше drain.
func TestProcess_StartupFailure_StderrCaptured_UnderDrainDelay(t *testing.T) {
	p := newTestProcess(t, "echo boom >&2; exit 1")
	p.drainStartDelay = 50 * time.Millisecond
	err := p.Start(nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr должен быть в ошибке даже с задержкой drain, got: %v", err)
	}
}

// TestDrainCONFIGSurvivesEviction проверяет, что CONFIG-событие, пойманное в
// drain, остаётся доступным через Status() даже после того как исходная строка
// вытеснена из ring-буфера логов последующими STATS-строками.
func TestDrainCONFIGSurvivesEviction(t *testing.T) {
	p := newProcess("test", "", t.TempDir())

	var sb strings.Builder
	sb.WriteString(`__WDTT_EVENT__|CONFIG|{"config":"[Interface]\nPrivateKey = k\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = pk\nAllowedIPs = 0.0.0.0/0\n"}`)
	sb.WriteByte('\n')
	// Вытесняем CONFIG за пределы ring-буфера (processLogMaxLines=500).
	for i := 0; i < processLogMaxLines+100; i++ {
		sb.WriteString("__WDTT_EVENT__|STATS|{}\n")
	}

	p.drain(strings.NewReader(sb.String()))

	// CONFIG должен быть вытеснен из лога...
	if got := ExtractWGConfigFromLog(p.logTail.String()); got != "" {
		t.Fatalf("expected CONFIG evicted from log, got %q", got)
	}
	// ...но сохранён в поле процесса.
	if !strings.Contains(p.lastWgConfig, "PublicKey = pk") {
		t.Fatalf("lastWgConfig not retained: %q", p.lastWgConfig)
	}
	if got := p.Status().WgConfig; !strings.Contains(got, "PublicKey = pk") {
		t.Fatalf("Status().WgConfig=%q", got)
	}
}
