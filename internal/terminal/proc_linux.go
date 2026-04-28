//go:build linux

package terminal

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func setTerminalSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		// PR_SET_PDEATHSIG: kernel sends SIGTERM to ttyd when our process
		// dies (including SIGKILL/crash). Without this the child outlives
		// us and keeps holding its TCP port — over multiple awg-manager
		// restarts the [7681..N] range fills with orphaned ttyd zombies.
		Pdeathsig: syscall.SIGTERM,
	}
}

func terminateProcess(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}

// killOrphanTtyd scans /proc for ttyd processes whose cmdline matches the
// pattern we spawn (--writable --port <p> --interface lo --once) and kills
// them. Defends against orphans left by SIGKILL'd / crashed previous
// awg-manager instances — Pdeathsig only protects against future deaths,
// not past ones.
func killOrphanTtyd() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var killed []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		// /proc cmdline uses NUL separators between argv items.
		args := bytes.Split(bytes.TrimRight(raw, "\x00"), []byte{0})
		if len(args) < 2 {
			continue
		}
		exe := string(args[0])
		if filepath.Base(exe) != ttydBinary {
			continue
		}
		// Match our spawn signature: --writable + --interface lo + --once.
		joined := strings.Join(stringsFromBytes(args[1:]), " ")
		if !strings.Contains(joined, "--writable") ||
			!strings.Contains(joined, "--interface lo") ||
			!strings.Contains(joined, "--once") {
			continue
		}
		// SIGKILL — orphaned --once ttyd may already be holding a session,
		// SIGTERM with grace period would prolong startup unnecessarily.
		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			killed = append(killed, pid)
		}
	}
	return killed
}

func stringsFromBytes(in [][]byte) []string {
	out := make([]string, len(in))
	for i, b := range in {
		out[i] = string(b)
	}
	return out
}
