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
