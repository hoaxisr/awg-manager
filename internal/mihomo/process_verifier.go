package mihomo

import (
	"bytes"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcessVerifier defines the contract for proving runtime process identity and socket ownership.
type ProcessVerifier interface {
	VerifyIdentity(procDir string, pid int, expectedProcTicks uint64, expectedBinary, expectedConfigDir string) error
	VerifySocketOwnership(procDir string, addr string, port int, network string, expectedPID int) error
	CaptureIdentity(procDir string, pid int, expectedBinary, configDir string, gen uint64) (RuntimeProcessIdentity, error)
}

// ParseProcStatStartTicks extracts field 22 (starttime) from a Linux /proc/<pid>/stat line.
func ParseProcStatStartTicks(statStr string) (uint64, error) {
	idx := strings.LastIndex(statStr, ")")
	if idx == -1 || idx+2 >= len(statStr) {
		return 0, errors.New("invalid stat format")
	}
	fields := strings.Fields(statStr[idx+2:])
	// Field 3 (state) is index 0
	// Field 22 (starttime) is index 19 (22 - 3 = 19)
	if len(fields) < 20 {
		return 0, errors.New("not enough fields in stat")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

// MatchManagedCmdline verifies that the NUL-separated cmdline matches the expected
// binary and '-d <configDir>' parameters.
func MatchManagedCmdline(cmdline []byte, expectedBinary, expectedConfigDir string) bool {
	parts := bytes.Split(cmdline, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 3 {
		return false
	}
	argv0 := filepath.Clean(string(parts[0]))
	expBin := filepath.Clean(expectedBinary)
	binMatches := argv0 == expBin || filepath.Base(argv0) == filepath.Base(expBin)
	if !binMatches {
		// Also check if canonical paths match
		if realArgv0, err := filepath.EvalSymlinks(argv0); err == nil {
			if realExp, err2 := filepath.EvalSymlinks(expBin); err2 == nil {
				binMatches = filepath.Clean(realArgv0) == filepath.Clean(realExp)
			}
		}
	}
	if !binMatches {
		return false
	}

	// Look for -d flag followed by expectedConfigDir
	for i := 1; i < len(parts)-1; i++ {
		if string(parts[i]) == "-d" {
			dir := filepath.Clean(string(parts[i+1]))
			expDir := filepath.Clean(expectedConfigDir)
			if dir == expDir {
				return true
			}
			if realDir, err := filepath.EvalSymlinks(dir); err == nil {
				if realExpDir, err2 := filepath.EvalSymlinks(expDir); err2 == nil {
					if filepath.Clean(realDir) == filepath.Clean(realExpDir) {
						return true
					}
				}
			}
		}
	}
	return false
}

// CanonicalPath resolves symlinks and cleans path for strict identity comparison.
func CanonicalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}
	clean := filepath.Clean(path)
	realPath, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return clean, err
	}
	return filepath.Clean(realPath), nil
}

// DefaultProcessVerifier holds the platform-specific default verifier implementation.
var DefaultProcessVerifier ProcessVerifier = newPlatformProcessVerifier()

// NoopProcessVerifier is a no-op implementation of ProcessVerifier for testing or non-Linux platforms.
type NoopProcessVerifier struct{}

func (n *NoopProcessVerifier) VerifyIdentity(procDir string, pid int, expectedProcTicks uint64, expectedBinary, expectedConfigDir string) error {
	return nil
}

func (n *NoopProcessVerifier) VerifySocketOwnership(procDir string, addr string, port int, network string, expectedPID int) error {
	return nil
}

func (n *NoopProcessVerifier) CaptureIdentity(procDir string, pid int, expectedBinary, configDir string, gen uint64) (RuntimeProcessIdentity, error) {
	return RuntimeProcessIdentity{
		PID:            pid,
		ProcStartTicks: 1000,
		Generation:     gen,
		ExecutablePath: expectedBinary,
		ConfigDir:      configDir,
	}, nil
}
