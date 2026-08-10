package wdtt

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
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

// TestProcess_ReapsHelperOrphan_DoesNotGateOnPipeEOF — репро зомби-бага: ребёнок
// форкает фонового хелпера (sleep 60), унаследовавшего stderr, и сразу выходит.
// Хелпер живёт дольше startupGrace (1.5с), поэтому Start() в обоих кодах —
// старом и новом — возвращается через ветку time.After(startupGrace) (это
// свойство самой гонки select'ов, не признак бага): содержимое ошибки не
// проверяем. Дискриминатор — что происходит С ПРОЦЕССОМ дальше:
//  1. сразу после возврата Start() прямой ребёнок не должен быть зомби —
//     старый код не реапает его, пока жив хелпер (реап гейтится на EOF пайпов
//     через drainWG внутри cmd.Wait());
//  2. IsRunning()/startedAt должны самоисправиться на «не работает» быстро
//     (в разумных секундах), а не только когда хелпер сам умрёт (60с) —
//     это и есть «усилитель» из root cause: супервизор считает мёртвый
//     процесс работающим.
//
// Таймаут-обёртки на select не дают прогону зависнуть на реальные 60с
// хелпера, если баг воспроизведётся.
func TestProcess_ReapsHelperOrphan_DoesNotGateOnPipeEOF(t *testing.T) {
	var capturedCmd *exec.Cmd
	p := newProcess("client", "/bin/sh", t.TempDir())
	p.startCmd = func(_ string, _ ...string) *exec.Cmd {
		capturedCmd = exec.Command("/bin/sh", "-c", "sleep 60 <&- >&2 2>&2 &\nexit 0")
		return capturedCmd
	}

	type result struct{ err error }
	done := make(chan result, 1)
	go func() { done <- result{p.Start(nil)} }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start() не вернулся за 5с — жнец гейтится на EOF пайпов орфан-хелпера (зомби-баг)")
	}

	if capturedCmd == nil || capturedCmd.Process == nil {
		t.Fatal("cmd.Process не установлен")
	}
	pid := capturedCmd.Process.Pid
	// Хелпер (sleep 60) — сирота в той же группе (Setsid лидер = наш прямой
	// ребёнок): -pid валит всю группу. Гигиена теста, не часть проверяемого
	// поведения.
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	if state, ok := procStatState(pid); ok && state == "Z" {
		t.Fatalf("pid %d остался зомби сразу после возврата Start() — реап гейтится на EOF пайпов", pid)
	}

	notRunning := make(chan struct{})
	go func() {
		for {
			if running, _ := p.IsRunning(); !running {
				close(notRunning)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	select {
	case <-notRunning:
	case <-time.After(5 * time.Second):
		t.Fatal("IsRunning() всё ещё true спустя 5с — не самоисправляется, пока жив орфан-хелпер (зомби-баг, супервизор не увидит смерть процесса)")
	}
}

// procStatState читает третье поле /proc/<pid>/stat (state). Возвращает
// ok=false, если процесс уже не существует (что тоже означает «не зомби»).
func procStatState(pid int) (string, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", false
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return "", false
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}
