//go:build linux

package mihomo

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/sys/procnet"
)

type LinuxProcessVerifier struct{}

func newPlatformProcessVerifier() ProcessVerifier {
	return &LinuxProcessVerifier{}
}

func (v *LinuxProcessVerifier) VerifyIdentity(procDir string, pid int, expectedProcTicks uint64, expectedBinary, expectedConfigDir string) error {
	if pid <= 0 {
		return fmt.Errorf("%w: invalid pid %d", ErrProcessProofFailed, pid)
	}
	if procDir == "" {
		procDir = "/proc"
	}
	pidDir := filepath.Join(procDir, strconv.Itoa(pid))

	// 1. Verify start ticks from /proc/<pid>/stat
	statBytes, err := os.ReadFile(filepath.Join(pidDir, "stat"))
	if err != nil {
		return fmt.Errorf("%w: read /proc/%d/stat: %v", ErrProcessProofFailed, pid, err)
	}
	actualTicks, err := ParseProcStatStartTicks(string(statBytes))
	if err != nil {
		return fmt.Errorf("%w: parse /proc/%d/stat start ticks: %v", ErrProcessProofFailed, pid, err)
	}
	if expectedProcTicks > 0 && actualTicks != expectedProcTicks {
		return fmt.Errorf("%w: pid %d start ticks mismatch (got %d, want %d)", ErrProcessProofFailed, pid, actualTicks, expectedProcTicks)
	}

	// 2. Verify canonical executable from /proc/<pid>/exe
	exeTarget, err := os.Readlink(filepath.Join(pidDir, "exe"))
	if err != nil {
		return fmt.Errorf("%w: read /proc/%d/exe: %v", ErrProcessProofFailed, pid, err)
	}
	canonicalExe, err := CanonicalPath(exeTarget)
	if err != nil {
		return fmt.Errorf("%w: canonicalize /proc/%d/exe %q: %v", ErrProcessProofFailed, pid, exeTarget, err)
	}
	canonicalExpected, err := CanonicalPath(expectedBinary)
	if err != nil {
		canonicalExpected = filepath.Clean(expectedBinary)
	}
	if canonicalExe != canonicalExpected {
		return fmt.Errorf("%w: pid %d exe %q != expected %q", ErrExecutableMismatch, pid, canonicalExe, canonicalExpected)
	}

	// 3. Verify cmdline arguments
	cmdlineBytes, err := os.ReadFile(filepath.Join(pidDir, "cmdline"))
	if err != nil {
		return fmt.Errorf("%w: read /proc/%d/cmdline: %v", ErrProcessProofFailed, pid, err)
	}
	if !MatchManagedCmdline(cmdlineBytes, expectedBinary, expectedConfigDir) {
		return fmt.Errorf("%w: pid %d cmdline does not match expected managed daemon (-d %s)", ErrProcessProofFailed, pid, expectedConfigDir)
	}

	return nil
}

func (v *LinuxProcessVerifier) VerifySocketOwnership(procDir string, addr string, port int, network string, expectedPID int) error {
	if procDir == "" {
		procDir = "/proc"
	}
	lookup, err := procnet.FindListeningProcessNetwork(procDir, network, addr, port)
	if err != nil {
		return fmt.Errorf("verify socket %s:%d (%s): %w", addr, port, network, err)
	}
	if !lookup.SocketFound {
		return fmt.Errorf("%w: no listening socket found on %s:%d (%s)", ErrListenerUnavailable, addr, port, network)
	}
	if lookup.PID != expectedPID {
		return fmt.Errorf("%w: socket %s:%d (%s) owned by pid %d, want %d", ErrControllerSocketMismatch, addr, port, network, lookup.PID, expectedPID)
	}
	return nil
}

func (v *LinuxProcessVerifier) CaptureIdentity(procDir string, pid int, expectedBinary, configDir string, gen uint64) (RuntimeProcessIdentity, error) {
	if pid <= 0 {
		return RuntimeProcessIdentity{}, fmt.Errorf("%w: invalid pid %d", ErrProcessProofFailed, pid)
	}
	if procDir == "" {
		procDir = "/proc"
	}
	pidDir := filepath.Join(procDir, strconv.Itoa(pid))

	statBytes, err := os.ReadFile(filepath.Join(pidDir, "stat"))
	if err != nil {
		return RuntimeProcessIdentity{}, fmt.Errorf("%w: read /proc/%d/stat: %v", ErrProcessProofFailed, pid, err)
	}
	ticks, err := ParseProcStatStartTicks(string(statBytes))
	if err != nil {
		return RuntimeProcessIdentity{}, fmt.Errorf("%w: parse /proc/%d/stat start ticks: %v", ErrProcessProofFailed, pid, err)
	}

	exeTarget, err := os.Readlink(filepath.Join(pidDir, "exe"))
	if err != nil {
		return RuntimeProcessIdentity{}, fmt.Errorf("%w: read /proc/%d/exe: %v", ErrProcessProofFailed, pid, err)
	}
	canonicalExe, err := CanonicalPath(exeTarget)
	if err != nil {
		canonicalExe = filepath.Clean(exeTarget)
	}

	// Verify cmdline as well - fail closed on read error or parameter mismatch
	cmdlineBytes, err := os.ReadFile(filepath.Join(pidDir, "cmdline"))
	if err != nil {
		return RuntimeProcessIdentity{}, fmt.Errorf("%w: read /proc/%d/cmdline: %v", ErrProcessProofFailed, pid, err)
	}
	if !MatchManagedCmdline(cmdlineBytes, expectedBinary, configDir) {
		return RuntimeProcessIdentity{}, fmt.Errorf("%w: pid %d cmdline does not match expected managed daemon (-d %s)", ErrProcessProofFailed, pid, configDir)
	}

	return RuntimeProcessIdentity{
		PID:            pid,
		ProcStartTicks: ticks,
		Generation:     gen,
		ExecutablePath: canonicalExe,
		ConfigDir:      filepath.Clean(configDir),
	}, nil
}
