package childproc

import (
	"os/exec"
	"testing"
)

func TestMatchesBinary(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	if !MatchesBinary(pid, "/opt/bin/sleep") {
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
