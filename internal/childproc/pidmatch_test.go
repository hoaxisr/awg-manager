package childproc

import (
	"os"
	"path/filepath"
	"testing"
)

// Сверяемся на собственном pid: у только что запущенного через exec.Start
// ребёнка /proc/<pid>/cmdline ещё пуст (ядро заполняет его после закрытия
// CLOEXEC-пайпа, по которому Start узнаёт об успехе), и тест ловил гонку.
func TestMatchesBinary(t *testing.T) {
	pid := os.Getpid()

	if !MatchesBinary(pid, "/opt/bin/"+filepath.Base(os.Args[0])) {
		t.Fatal("сверка идёт по имени бинаря, а не по полному пути")
	}
	if MatchesBinary(pid, "/opt/bin/freeturn-client") {
		t.Fatal("посторонний процесс не должен считаться нашим")
	}
	if !MatchesBinary(pid, "") {
		t.Fatal("без пути к бинарю сверять нечего — должно быть совпадение")
	}
	if MatchesBinary(-1, "/bin/sleep") {
		t.Fatal("несуществующий pid не подтверждается")
	}
}

func TestMatchesAnyBinary(t *testing.T) {
	pid := os.Getpid()
	self := filepath.Base(os.Args[0])

	if !MatchesAnyBinary(pid, "freeturn-client", self, "wdtt-server") {
		t.Fatal("должно совпасть по одному из перечисленных имён")
	}
	if MatchesAnyBinary(pid, "freeturn-client", "wdtt-server") {
		t.Fatal("посторонний список имён не должен совпадать")
	}
	if MatchesAnyBinary(-1, self) {
		t.Fatal("несуществующий pid не подтверждается")
	}
	if MatchesAnyBinary(pid) {
		t.Fatal("пустой список basenames не должен совпадать ни с чем")
	}
}

// Поле 22 /proc/<pid>/stat берётся от ПОСЛЕДНЕЙ закрывающей скобки: поле 2 —
// имя бинаря в скобках, и в нём бывают и пробелы, и сами скобки. Разбиение
// строки по пробелам съезжает на столько позиций, сколько пробелов в имени.
func TestParseStartTimeIgnoresParensAndSpacesInComm(t *testing.T) {
	line := "7 (weird (name) here) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 " +
		"908877 4096 0 18446744073709551615\n"
	got, ok := parseStartTime(line)
	if !ok || got != 908877 {
		t.Fatalf("parseStartTime = (%d, %v), ждали 908877", got, ok)
	}
}

func TestParseStartTimeRejectsGarbage(t *testing.T) {
	for _, line := range []string{
		"",
		"7 (proc) S 1 2 3",                 // полей меньше 22
		"7 proc S 1 2 3 4 5 6 7 8 9 10 11", // нет скобок
	} {
		if _, ok := parseStartTime(line); ok {
			t.Fatalf("мусор принят за отпечаток: %q", line)
		}
	}
}

// Отпечаток своего процесса читается и не плывёт между вызовами; несуществующий
// pid — не ошибка, а «отпечатка нет».
func TestStartTimeOfLiveAndMissingProcess(t *testing.T) {
	first, ok := StartTime(os.Getpid())
	if !ok || first == 0 {
		t.Fatalf("StartTime своего процесса = (%d, %v)", first, ok)
	}
	second, _ := StartTime(os.Getpid())
	if second != first {
		t.Fatalf("отпечаток поплыл: %d != %d", second, first)
	}
	if _, ok := StartTime(1 << 30); ok {
		t.Fatal("отпечаток несуществующего процесса прочитан")
	}
}
