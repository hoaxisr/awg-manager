package lock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTryLock_SecondHolderGetsErrLockHeld(t *testing.T) {
	dir := t.TempDir()
	a := NewWithDir("tunnels", dir)
	if err := a.TryLock(); err != nil {
		t.Fatal(err)
	}
	pid, err := os.ReadFile(filepath.Join(dir, "tunnels.lock.d", "pid"))
	if err != nil || string(pid) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("pid-файл = %q err=%v", pid, err)
	}
	if err := NewWithDir("tunnels", dir).TryLock(); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("второй захват: %v, want ErrLockHeld", err)
	}
	if err := a.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := NewWithDir("tunnels", dir).TryLock(); err != nil {
		t.Fatalf("после Unlock захват обязан удаться: %v", err)
	}
}

func TestCleanStale_DeadPIDRemoved_LivePIDKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tunnels.lock.d")
	// Мёртвый PID: потомок /bin/true, дождались — номер свободен.
	cmd := exec.Command("/bin/true")
	if err := cmd.Run(); err != nil {
		t.Skipf("/bin/true: %v", err)
	}
	dead := cmd.ProcessState.Pid()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "pid"), []byte(strconv.Itoa(dead)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewWithDir("tunnels", dir).TryLock(); err != nil {
		t.Fatalf("протухший лок мёртвого PID обязан сниматься: %v", err)
	}
	// Живой PID (свой) — лок остаётся.
	if err := os.WriteFile(filepath.Join(path, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewWithDir("tunnels", dir).TryLock(); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("лок живого PID снят: %v", err)
	}
	// pid-файл не читается как число (оборванная запись) — владельца установить
	// нечем, и лок сносится: иначе он держался бы до истечения StaleAge.
	if err := os.WriteFile(filepath.Join(path, "pid"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewWithDir("tunnels", dir).TryLock(); err != nil {
		t.Fatalf("лок с нечитаемым pid обязан сниматься: %v", err)
	}
}

// Лок живого процесса не снимается по возрасту: владелец держит его сколько нужно, а
// ожидающий получает ErrLockHeld/таймаут (контракт стора, CLAUDE.md «Запись туннеля»).
// Раньше возраст mtime побеждал живой PID (план G).
func TestCleanStale_LivePIDBeatsAge(t *testing.T) {
	dir := t.TempDir()
	a := NewWithDir("tunnels", dir)
	if err := a.TryLock(); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-StaleAge - time.Minute)
	if err := os.Chtimes(a.path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := NewWithDir("tunnels", dir).TryLock(); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("живой владелец обязан удержать лок: got %v", err)
	}
}

// Лок без pid-файла (оборванный захват) снимается только по возрасту: свежий — держится.
func TestCleanStale_MissingPIDFallsBackToAge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tunnels.lock.d")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := NewWithDir("tunnels", dir).TryLock(); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("свежий лок без pid обязан держаться: %v", err)
	}
	old := time.Now().Add(-StaleAge - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := NewWithDir("tunnels", dir).TryLock(); err != nil {
		t.Fatalf("старый лок без pid обязан сниматься: %v", err)
	}
}

// Unlock не снимает лок, который после нашего сноса взял другой процесс (в pid чужое число).
func TestUnlock_LeavesForeignLockAlone(t *testing.T) {
	dir := t.TempDir()
	a := NewWithDir("tunnels", dir)
	if err := a.TryLock(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.path, "pid"), []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.path); err != nil {
		t.Fatal("чужой лок снят нашим Unlock")
	}
}

func TestWaitLockDir_TimeoutWithoutSleep(t *testing.T) {
	dir := t.TempDir()
	if err := NewWithDir("tunnels", dir).TryLock(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := WaitLockDir("tunnels", dir, 0)
	if err == nil || !strings.Contains(err.Error(), `lock "tunnels": timeout after 0s`) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("таймаут 0 не должен спать: %v", time.Since(start))
	}
	// Свободный лок — берётся с первой попытки.
	if l, err := WaitLockDir("other", dir, 0); err != nil || l == nil {
		t.Fatalf("свободный: %v", err)
	}
}
