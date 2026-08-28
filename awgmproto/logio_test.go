//go:build linux

package awgmproto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

func TestEnforceCapResetsCounter(t *testing.T) {
	// EnforceCap обязан обнулить и счётчик записей. Оставшись на потолке, он
	// заставит СЛЕДУЮЩУЮ запись через Log.Write усечь файл заново — и снесёт
	// строки, которые к тому моменту написали в журнал наследники через
	// унаследованный дескриптор. Симптома нет: файл есть, пишется, туннель жив.
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

	if _, err := lg.Write(make([]byte, LogCapBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := lg.EnforceCap(); err != nil {
		t.Fatal(err)
	}

	if _, err := raw.Write([]byte("хвост наследника\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := lg.Write([]byte("наша строка\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "хвост наследника\nнаша строка\n" {
		t.Fatalf("журнал = %q: после EnforceCap счётчик не обнулён, запись усекла чужие строки", data)
	}
}

// sigpipeChildEnv — режим дочернего прогона: тот же тестовый бинарь ломает
// себе stdout и пишет в него.
const sigpipeChildEnv = "AWGM_TEST_SIGPIPE_CHILD"

func TestIgnoreSIGPIPEKeepsProcessAlive(t *testing.T) {
	// §2 спеки: запись в закрытый stdout не должна убивать процесс — иначе
	// усыновляемый умрёт через секунды после смерти менеджера, ровно в том
	// сценарии, ради которого протокол и пишется. В своём процессе это не
	// проверить: диспозиция сигнала одна на весь тестовый бинарь, а промах
	// убил бы сам прогон. Поэтому подпроцесс.
	switch os.Getenv(sigpipeChildEnv) {
	case "ignore":
		sigpipeChild(true)
	case "plain":
		sigpipeChild(false)
	}

	// Контроль: без IgnoreSIGPIPE процесс обязан умереть от сигнала. Без этой
	// половины положительный случай ничего не доказывает — вдруг SIGPIPE и так
	// никого не убивает на этой машине.
	ps := runSigpipeChild(t, "plain")
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("непонятный статус подпроцесса: %v", ps)
	}
	if !ws.Signaled() || ws.Signal() != syscall.SIGPIPE {
		t.Fatalf("без IgnoreSIGPIPE процесс обязан умереть от SIGPIPE, а он завершился как %v", ps)
	}

	ps = runSigpipeChild(t, "ignore")
	if code := ps.ExitCode(); code != sigpipeChildEPIPE {
		t.Fatalf("с IgnoreSIGPIPE ожидали выход %d (запись вернула EPIPE), получили %v", sigpipeChildEPIPE, ps)
	}
}

// Коды выхода подпроцесса.
const (
	sigpipeChildEPIPE = 7 // запись вернула EPIPE — процесс жив
	sigpipeChildWrote = 8 // запись прошла или дала другую ошибку
	sigpipeChildSetup = 9 // подготовить сломанный stdout не удалось
)

func sigpipeChild(ignore bool) {
	if ignore {
		IgnoreSIGPIPE()
	}
	r, w, err := os.Pipe()
	if err != nil {
		os.Exit(sigpipeChildSetup)
	}
	if err := unix.Dup3(int(w.Fd()), 1, 0); err != nil {
		os.Exit(sigpipeChildSetup)
	}
	_ = w.Close()
	// Читающий конец закрыт — stdout сломан ровно так же, как у усыновлённого
	// процесса после смерти менеджера.
	_ = r.Close()
	// Писать обязательно через os.Stdout: убивает процесс не ядро, а проверка
	// epipecheck в пакете os, и она смотрит на объект файла, а не на номер
	// дескриптора. Форки печатают через fmt.Printf, то есть ровно сюда.
	if _, err := os.Stdout.Write([]byte("проверка\n")); errors.Is(err, syscall.EPIPE) {
		os.Exit(sigpipeChildEPIPE)
	}
	os.Exit(sigpipeChildWrote)
}

func runSigpipeChild(t *testing.T, mode string) *os.ProcessState {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestIgnoreSIGPIPEKeepsProcessAlive$")
	cmd.Env = append(os.Environ(), sigpipeChildEnv+"="+mode)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	_ = cmd.Run()
	if cmd.ProcessState == nil {
		t.Fatalf("подпроцесс не запустился: %s", errBuf.String())
	}
	if code := cmd.ProcessState.ExitCode(); code == sigpipeChildSetup {
		t.Fatalf("подпроцесс не смог сломать себе stdout: %s", errBuf.String())
	}
	return cmd.ProcessState
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
