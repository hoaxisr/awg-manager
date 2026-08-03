package backup

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

const orphanKillWait = 3 * time.Second

// KillOrphanProxyProcesses terminates freeturn/wdtt child processes whose PID
// files remain in runDir (including orphans not tracked by the in-memory registry).
func KillOrphanProxyProcesses(runDir string) {
	runDir = strings.TrimSpace(runDir)
	if runDir == "" {
		return
	}
	matches, err := filepath.Glob(filepath.Join(runDir, "*.pid"))
	if err != nil {
		return
	}
	for _, path := range matches {
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "freeturn-") && !strings.HasPrefix(base, "wdtt-") {
			continue
		}
		killPIDFile(path)
	}
}

func killPIDFile(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		_ = os.Remove(path)
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		_ = os.Remove(path)
		return
	}
	if !childproc.IsAlive(pid) {
		_ = os.Remove(path)
		return
	}
	_ = childproc.Terminate(pid)
	deadline := time.Now().Add(orphanKillWait)
	for time.Now().Before(deadline) {
		if !childproc.IsAlive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if childproc.IsAlive(pid) {
		_ = childproc.Kill(pid)
	}
	_ = os.Remove(path)
}
