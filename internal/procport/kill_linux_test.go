//go:build linux

package procport

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Страховка: тест, не собравший поддельный /proc, получает ошибку вместо
// сигнала. KillListener работает от root и шлёт SIGKILL — промах в тесте
// означал бы убитый процесс машины, где тест запущен.
func init() {
	sigTerm = refuseSignal
	sigKill = refuseSignal
}

func refuseSignal(pid int) error {
	return fmt.Errorf("тест: сигнал процессу %d без поддельного /proc", pid)
}

// Адреса в форме /proc/net (little-endian, как на aarch64/mipsel).
const (
	hexLoopback9000 = "0100007F:2328"
	hexLAN9000      = "0101A8C0:2328" // 192.168.1.1
	hexAny9000      = "00000000:2328"
	hexAny6_9000    = "00000000000000000000000000000000:2328"
)

// fakeProc — поддельный /proc в t.TempDir(): таблицы net/{tcp,udp}[6],
// /proc/<pid>/fd со ссылками socket:[inode] и comm. starttime живёт в карте:
// его подменяет processStartTime. Сигналы не шлются — записываются.
type fakeProc struct {
	t      *testing.T
	root   string
	rows   map[string][]string // таблица → строки
	start  map[int]uint64
	terms  []int
	kills  []int
	reads  int            // вызовы processStartTime
	onRead func(call int) // хук после чтения starttime
	onTerm func(pid int)  // хук после SIGTERM
}

func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	f := &fakeProc{t: t, root: t.TempDir(), rows: map[string][]string{}, start: map[int]uint64{}}
	if err := os.MkdirAll(filepath.Join(f.root, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tcp", "tcp6", "udp", "udp6"} {
		f.writeTable(table)
	}
	prevRoot, prevStart, prevGrace := procRoot, processStartTime, killGrace
	procRoot = f.root
	killGrace = 0
	processStartTime = func(pid int) (uint64, bool) {
		st, ok := f.start[pid]
		f.reads++
		if f.onRead != nil {
			f.onRead(f.reads)
		}
		return st, ok
	}
	sigTerm = func(pid int) error {
		f.terms = append(f.terms, pid)
		if f.onTerm != nil {
			f.onTerm(pid)
		}
		return nil
	}
	sigKill = func(pid int) error {
		f.kills = append(f.kills, pid)
		return nil
	}
	t.Cleanup(func() {
		procRoot, processStartTime, killGrace = prevRoot, prevStart, prevGrace
		sigTerm, sigKill = refuseSignal, refuseSignal
	})
	return f
}

func (f *fakeProc) writeTable(table string) {
	f.t.Helper()
	body := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		strings.Join(f.rows[table], "")
	if err := os.WriteFile(filepath.Join(f.root, "net", table), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// process заводит процесс pid с именем comm.
func (f *fakeProc) process(pid int, comm string) {
	f.t.Helper()
	dir := filepath.Join(f.root, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.start[pid] = uint64(1000 + pid)
}

// listen — сокет inode на local в таблице table; pid > 0 держит его fd.
func (f *fakeProc) listen(pid int, table, local, inode string) {
	f.t.Helper()
	state := "07"
	if strings.HasPrefix(table, "tcp") {
		state = "0A"
	}
	row := fmt.Sprintf("  %d: %s 00000000:0000 %s 00000000:00000000 00:00000000 00000000     0        0 %s 2 00000000 0\n",
		len(f.rows[table]), local, state, inode)
	f.rows[table] = append(f.rows[table], row)
	f.writeTable(table)
	if pid > 0 {
		fd := filepath.Join(f.root, strconv.Itoa(pid), "fd", inode)
		if err := os.Symlink("socket:["+inode+"]", fd); err != nil {
			f.t.Fatal(err)
		}
	}
}

// exit — процесс завершился: строки его сокетов и каталог исчезли.
func (f *fakeProc) exit(pid int) {
	f.t.Helper()
	dir := filepath.Join(f.root, strconv.Itoa(pid))
	fds, _ := os.ReadDir(filepath.Join(dir, "fd"))
	for _, fd := range fds {
		f.closeSocket(fd.Name())
	}
	if err := os.RemoveAll(dir); err != nil {
		f.t.Fatal(err)
	}
	delete(f.start, pid)
}

func (f *fakeProc) closeSocket(inode string) {
	for table, rows := range f.rows {
		kept := rows[:0]
		for _, r := range rows {
			if fields := strings.Fields(r); len(fields) > 9 && fields[9] == inode {
				continue
			}
			kept = append(kept, r)
		}
		f.rows[table] = kept
		f.writeTable(table)
	}
}

func (f *fakeProc) noSignals() {
	f.t.Helper()
	if len(f.terms) != 0 || len(f.kills) != 0 {
		f.t.Fatalf("сигналов быть не должно: SIGTERM %v, SIGKILL %v", f.terms, f.kills)
	}
}

// Процесс на том же номере порта, но на другом конкретном IP bind'у
// 127.0.0.1 не мешает — останавливать его нельзя.
func TestKillListenerSkipsNonConflictingAddress(t *testing.T) {
	f := newFakeProc(t)
	f.process(100, "foreign")
	f.listen(100, "udp", hexLAN9000, "5001")

	if _, err := KillListener("127.0.0.1", 9000, ProtoUDP); err == nil {
		t.Fatal("процесс на 192.168.1.1:9000 не конфликтует с 127.0.0.1:9000")
	}
	f.noSignals()
}

func TestKillListenerStopsConflictingListener(t *testing.T) {
	cases := []struct {
		name, bind, table, local string
	}{
		{"точный адрес", "127.0.0.1", "udp", hexLoopback9000},
		{"wildcard-сокет мешает конкретному адресу", "127.0.0.1", "udp", hexAny9000},
		{"dual-stack :: мешает конкретному адресу", "127.0.0.1", "udp6", hexAny6_9000},
		{"bind на 0.0.0.0 конфликтует с любым IP", "0.0.0.0", "udp", hexLAN9000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeProc(t)
			f.process(100, "squatter")
			f.listen(100, tc.table, tc.local, "5001")
			f.onTerm = f.exit

			info, err := KillListener(tc.bind, 9000, ProtoUDP)
			if err != nil {
				t.Fatalf("KillListener: %v", err)
			}
			if info.PID != 100 || info.Comm != "squatter" {
				t.Fatalf("info = %+v", info)
			}
			if len(f.terms) != 1 || f.terms[0] != 100 || len(f.kills) != 0 {
				t.Fatalf("SIGTERM %v, SIGKILL %v; want SIGTERM [100]", f.terms, f.kills)
			}
		})
	}
}

// Один номер порта, разные протоколы: udp-запрос не задевает tcp-слушателя.
func TestKillListenerKeepsProtocolsApart(t *testing.T) {
	f := newFakeProc(t)
	f.process(100, "tcpd")
	f.listen(100, "tcp", hexLoopback9000, "5001")
	f.process(200, "udpd")
	f.listen(200, "udp", hexLoopback9000, "5002")
	f.onTerm = f.exit

	if _, err := KillListener("127.0.0.1", 9000, ProtoUDP); err != nil {
		t.Fatalf("KillListener: %v", err)
	}
	if len(f.terms) != 1 || f.terms[0] != 200 {
		t.Fatalf("SIGTERM %v, want [200]", f.terms)
	}
}

func TestKillListenerRefusesProtectedOrUnknownOwner(t *testing.T) {
	t.Run("init", func(t *testing.T) {
		f := newFakeProc(t)
		f.process(1, "init")
		f.listen(1, "udp", hexLoopback9000, "5001")
		if _, err := KillListener("127.0.0.1", 9000, ProtoUDP); err == nil {
			t.Fatal("PID 1 останавливать нельзя")
		}
		f.noSignals()
	})
	t.Run("сам демон", func(t *testing.T) {
		f := newFakeProc(t)
		f.process(os.Getpid(), "awg-manager")
		f.listen(os.Getpid(), "udp", hexLoopback9000, "5001")
		if _, err := KillListener("127.0.0.1", 9000, ProtoUDP); err == nil {
			t.Fatal("собственный процесс останавливать нельзя")
		}
		f.noSignals()
	})
	// Сокет унаследован от init: остановка соседа порт не освободит.
	t.Run("сокет делят init и потомок", func(t *testing.T) {
		f := newFakeProc(t)
		f.process(1, "init")
		f.process(100, "child")
		f.listen(1, "udp", hexLoopback9000, "5001")
		if err := os.Symlink("socket:[5001]", filepath.Join(f.root, "100", "fd", "5001")); err != nil {
			t.Fatal(err)
		}
		if _, err := KillListener("127.0.0.1", 9000, ProtoUDP); err == nil {
			t.Fatal("сокет init: отказ без сигнала")
		}
		f.noSignals()
	})
	t.Run("владелец не найден", func(t *testing.T) {
		f := newFakeProc(t)
		f.listen(0, "udp", hexLoopback9000, "5001")
		if _, err := KillListener("127.0.0.1", 9000, ProtoUDP); err == nil {
			t.Fatal("сокет без процесса: отказ")
		}
		f.noSignals()
	})
	t.Run("адрес не разобран", func(t *testing.T) {
		f := newFakeProc(t)
		f.process(100, "squatter")
		f.listen(100, "udp", hexLoopback9000, "5001")
		if _, err := KillListener("fe80::1", 9000, ProtoUDP); err == nil {
			t.Fatal("неподдерживаемый адрес: отказ")
		}
		f.noSignals()
	})
}

func TestKillListenerFailsClosedOnRaces(t *testing.T) {
	setup := func(t *testing.T) *fakeProc {
		f := newFakeProc(t)
		f.process(100, "squatter")
		f.listen(100, "udp", hexLoopback9000, "5001")
		return f
	}
	t.Run("/proc не читается", func(t *testing.T) {
		f := setup(t)
		procRoot = filepath.Join(f.root, "missing")
		if _, err := KillListener("127.0.0.1", 9000, ProtoUDP); err == nil {
			t.Fatal("нечитаемый /proc: отказ")
		}
		f.noSignals()
	})
	t.Run("starttime не читается", func(t *testing.T) {
		f := setup(t)
		delete(f.start, 100)
		if _, err := KillListener("127.0.0.1", 9000, ProtoUDP); err == nil {
			t.Fatal("неопознанный процесс: отказ")
		}
		f.noSignals()
	})
	// Сокет закрылся между обходом /proc и сигналом.
	t.Run("сокет исчез", func(t *testing.T) {
		f := setup(t)
		f.onRead = func(call int) {
			if call == 1 {
				f.closeSocket("5001")
			}
		}
		if _, err := KillListener("127.0.0.1", 9000, ProtoUDP); err == nil {
			t.Fatal("сокета уже нет: отказ")
		}
		f.noSignals()
	})
	// Процесс завершился, номер достался новому процессу с тем же сокетом
	// на руках (например, наследнику): отпечаток не совпал — сигнала нет.
	t.Run("номер достался другому процессу", func(t *testing.T) {
		f := setup(t)
		f.onRead = func(call int) {
			if call == 1 {
				f.start[100]++
			}
		}
		if _, err := KillListener("127.0.0.1", 9000, ProtoUDP); err == nil {
			t.Fatal("сменившийся процесс: отказ")
		}
		f.noSignals()
	})
	// Номер переиспользован за grace после SIGTERM: SIGKILL новому
	// процессу не уходит.
	t.Run("номер переиспользован после SIGTERM", func(t *testing.T) {
		f := setup(t)
		f.onTerm = func(pid int) { f.start[pid]++ }
		_, _ = KillListener("127.0.0.1", 9000, ProtoUDP)
		if len(f.terms) != 1 || len(f.kills) != 0 {
			t.Fatalf("SIGTERM %v, SIGKILL %v; want SIGTERM [100] без SIGKILL", f.terms, f.kills)
		}
	})
	// Тот же процесс SIGTERM проигнорировал — добивается, как и прежде.
	t.Run("SIGTERM проигнорирован", func(t *testing.T) {
		f := setup(t)
		_, _ = KillListener("127.0.0.1", 9000, ProtoUDP)
		if len(f.terms) != 1 || len(f.kills) != 1 || f.kills[0] != 100 {
			t.Fatalf("SIGTERM %v, SIGKILL %v; want оба по 100", f.terms, f.kills)
		}
	})
}
