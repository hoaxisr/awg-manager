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
