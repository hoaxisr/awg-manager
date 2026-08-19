package ports

import (
	"os"
	"testing"
)

// Гейты kill, которые можно проверить не убивая ничего: PID<=1 и сам себя.
func TestKillProcess_RefusesInitAndSelf(t *testing.T) {
	s := &Scanner{}
	for _, pid := range []int{0, 1, -5} {
		if err := s.KillProcess(pid, "SIGTERM"); err == nil {
			t.Errorf("kill PID %d разрешён", pid)
		}
	}
	if err := s.KillProcess(os.Getpid(), "SIGKILL"); err == nil {
		t.Error("self-kill разрешён")
	}
}
