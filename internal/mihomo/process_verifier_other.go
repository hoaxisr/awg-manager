//go:build !linux

package mihomo

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type NonLinuxProcessVerifier struct{}

func newPlatformProcessVerifier() ProcessVerifier {
	return &NonLinuxProcessVerifier{}
}

func (v *NonLinuxProcessVerifier) VerifyIdentity(procDir string, pid int, expectedProcTicks uint64, expectedBinary, expectedConfigDir string) error {
	if pid <= 0 {
		return fmt.Errorf("%w: invalid pid %d", ErrProcessProofFailed, pid)
	}
	if procDir != "" && procDir != "/proc" {
		pidDir := filepath.Join(procDir, strconv.Itoa(pid))
		if statBytes, err := os.ReadFile(filepath.Join(pidDir, "stat")); err == nil {
			actualTicks, err := ParseProcStatStartTicks(string(statBytes))
			if err == nil && expectedProcTicks > 0 && actualTicks != expectedProcTicks {
				return fmt.Errorf("%w: pid %d start ticks mismatch (got %d, want %d)", ErrProcessProofFailed, pid, actualTicks, expectedProcTicks)
			}
		}
		if exeTarget, err := os.Readlink(filepath.Join(pidDir, "exe")); err == nil {
			canonicalExe, _ := CanonicalPath(exeTarget)
			canonicalExpected, _ := CanonicalPath(expectedBinary)
			if canonicalExe != canonicalExpected {
				return fmt.Errorf("%w: pid %d exe %q != expected %q", ErrExecutableMismatch, pid, canonicalExe, canonicalExpected)
			}
		}
		if cmdBytes, err := os.ReadFile(filepath.Join(pidDir, "cmdline")); err == nil {
			if !MatchManagedCmdline(cmdBytes, expectedBinary, expectedConfigDir) {
				return fmt.Errorf("%w: pid %d cmdline mismatch", ErrProcessProofFailed, pid)
			}
		}
	}
	return nil
}

func (v *NonLinuxProcessVerifier) VerifySocketOwnership(procDir string, addr string, port int, network string, expectedPID int) error {
	return nil
}

func (v *NonLinuxProcessVerifier) CaptureIdentity(procDir string, pid int, expectedBinary, configDir string, gen uint64) (RuntimeProcessIdentity, error) {
	if pid <= 0 {
		return RuntimeProcessIdentity{}, fmt.Errorf("%w: invalid pid %d", ErrProcessProofFailed, pid)
	}
	var ticks uint64
	canonicalExe, _ := CanonicalPath(expectedBinary)
	if procDir != "" && procDir != "/proc" {
		pidDir := filepath.Join(procDir, strconv.Itoa(pid))
		if statBytes, err := os.ReadFile(filepath.Join(pidDir, "stat")); err == nil {
			ticks, _ = ParseProcStatStartTicks(string(statBytes))
		}
		if exeTarget, err := os.Readlink(filepath.Join(pidDir, "exe")); err == nil {
			canonicalExe, _ = CanonicalPath(exeTarget)
		}
	}
	return RuntimeProcessIdentity{
		PID:            pid,
		ProcStartTicks: ticks,
		Generation:     gen,
		ExecutablePath: canonicalExe,
		ConfigDir:      filepath.Clean(configDir),
	}, nil
}
