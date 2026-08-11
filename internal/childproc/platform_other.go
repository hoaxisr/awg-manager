//go:build !linux

package childproc

import (
	"os"
	"os/exec"
	"syscall"
)

// These best-effort implementations exist only so the package compiles on a
// non-Linux dev machine (e.g. building awg-manager on macOS/Windows to check
// it compiles). The daemons themselves only ever run on the Linux/ARM router,
// where platform_linux.go is used instead.

func SetProcessGroup(_ *exec.Cmd) {
	// No process-group/session handling on non-Linux dev hosts.
}

func Terminate(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

func Kill(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// TerminateGroup delegates to Terminate: no process-group semantics on
// non-Linux dev hosts (see the package doc comment above).
func TerminateGroup(pid int) error {
	return Terminate(pid)
}

// KillGroup delegates to Kill: no process-group semantics on non-Linux dev
// hosts (see the package doc comment above).
func KillGroup(pid int) error {
	return Kill(pid)
}

// Signal is a no-op stub on non-Linux dev hosts.
func Signal(_ int, _ syscall.Signal) error {
	return nil
}

// IsAlive probes liveness with signal 0. os.FindProcess never fails on
// Unix (it just wraps the pid), so the real check is Signal(0): it returns
// nil while the process exists and an error (ESRCH / "process already
// finished") once it's gone. This is the portable liveness probe — the old
// "FindProcess succeeded → assume alive" reported every pid as alive.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
