//go:build linux

package awgmproto

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestEnforceCapSeesWritesPastLogWrite(t *testing.T) {
	// После RedirectStdio форк пишет прямо в дескриптор 1, мимо Log.Write:
	// потолок обязан держаться по факту размера файла, а не по счётчику.
	path := filepath.Join(t.TempDir(), "a.log")
	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	raw, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	chunk := []byte(strings.Repeat("x", 64*1024))
	for written := 0; written <= LogCapBytes; written += len(chunk) {
		if _, err := raw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}

	if err := lg.EnforceCap(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 0 {
		t.Fatalf("журнал %d байт после усечения по потолку", st.Size())
	}
}

func TestEnforceCapKeepsFileAtCap(t *testing.T) {
	// Точная граница: ровно потолок — законный размер, потолок плюс байт —
	// уже нет. Проверяется здесь, а не «заведомо больше», иначе сравнение
	// можно было бы поменять на строгое или нестрогое незамеченным.
	for _, c := range []struct {
		name string
		size int64
		want int64
	}{
		{"ровно потолок", LogCapBytes, LogCapBytes},
		{"потолок плюс байт", LogCapBytes + 1, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "a.log")
			lg, err := OpenLog(path)
			if err != nil {
				t.Fatal(err)
			}
			defer lg.Close()

			// Одна запись длиннее потолка проходит мимо усечения в Write
			// (усекается ДО записи, а пишется целиком) — EnforceCap обязан
			// поправить это по факту размера.
			if _, err := lg.Write(make([]byte, c.size)); err != nil {
				t.Fatal(err)
			}
			if err := lg.EnforceCap(); err != nil {
				t.Fatal(err)
			}
			st, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if st.Size() != c.want {
				t.Fatalf("журнал %d байт, ожидали %d при потолке %d", st.Size(), c.want, LogCapBytes)
			}
		})
	}
}

func TestWatchCapEnforcesAndStops(t *testing.T) {
	// В рантайме потолок держит только WatchCap: сам по себе EnforceCap никто
	// не зовёт. Проверяется и то, что цикл действительно усекает, и то, что он
	// уходит по контексту — иначе горутина переживёт процесс запуска.
	path := filepath.Join(t.TempDir(), "a.log")
	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()
	if _, err := lg.Write(make([]byte, LogCapBytes+1)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		lg.WatchCap(ctx, time.Millisecond)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() == 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("журнал %d байт: WatchCap не усёк его за 5 секунд", st.Size())
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WatchCap не вернулся после отмены контекста")
	}
}

func TestRedirectStdioSendsPrintToLog(t *testing.T) {
	// Печать форка (fmt.Printf, log) обязана попадать в журнал без правки
	// каждого места печати.
	path := filepath.Join(t.TempDir(), "a.log")
	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	saveOut, saveErr := dupStdio(t)
	defer restoreStdio(t, saveOut, saveErr)

	if err := RedirectStdio(lg); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("RAWCONF|10.70.0.5|8.8.8.8|1300\n")
	fmt.Fprintln(os.Stderr, "[RAW] TUN готов")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "RAWCONF|10.70.0.5") || !strings.Contains(string(data), "TUN готов") {
		t.Fatalf("вывод процесса не попал в журнал: %q", data)
	}
}

// dupStdio сохраняет дескрипторы 1 и 2, чтобы вернуть их после теста: без
// этого весь остальной вывод go test уехал бы во временный файл.
func dupStdio(t *testing.T) (int, int) {
	t.Helper()
	out, err := unix.Dup(1)
	if err != nil {
		t.Fatal(err)
	}
	errFD, err := unix.Dup(2)
	if err != nil {
		t.Fatal(err)
	}
	return out, errFD
}

func restoreStdio(t *testing.T, out, errFD int) {
	t.Helper()
	if err := unix.Dup3(out, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := unix.Dup3(errFD, 2, 0); err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(out)
	_ = unix.Close(errFD)
}
