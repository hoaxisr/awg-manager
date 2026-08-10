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

// freeturnBinaryNames/wdttBinaryNames — basenames бинарей, которым может
// принадлежать freeturn-*/wdtt-* pidfile (см. cmd/awg-manager wiring:
// /opt/bin/freeturn-client, /opt/bin/freeturn-server, /opt/bin/wdtt-client,
// /opt/bin/wdtt-server). По имени pidfile нельзя различить клиент/сервер —
// сверяем с обоими.
var freeturnBinaryNames = []string{"freeturn-client", "freeturn-server"}
var wdttBinaryNames = []string{"wdtt-client", "wdtt-server"}

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
		var binaries []string
		switch {
		case strings.HasPrefix(base, "freeturn-"):
			binaries = freeturnBinaryNames
		case strings.HasPrefix(base, "wdtt-"):
			binaries = wdttBinaryNames
		default:
			continue
		}
		killPIDFile(path, binaries)
	}
}

func killPIDFile(path string, binaries []string) {
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
	if !childproc.MatchesAnyBinary(pid, binaries...) {
		// Pidfile переживает ребут (лежит на флешке), а pid после ребута мог
		// достаться постороннему процессу — сигнал ему не шлём, только чистим файл.
		_ = os.Remove(path)
		return
	}
	// Свои процессы стартуют с Setsid (childproc.SetProcessGroup) — глушим всю
	// группу, а не только сам pid, как и Stop() в internal/wdtt, internal/freeturn.
	_ = childproc.TerminateGroup(pid)
	deadline := time.Now().Add(orphanKillWait)
	for time.Now().Before(deadline) {
		if !childproc.IsAlive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if childproc.IsAlive(pid) {
		// До 3 с прошло между первой проверкой владельца и этим моментом — pid
		// мог успеть освободиться и достаться другому процессу. Не глушим,
		// если он больше не наш.
		if childproc.MatchesAnyBinary(pid, binaries...) {
			_ = childproc.KillGroup(pid)
		}
	}
	_ = os.Remove(path)
}
