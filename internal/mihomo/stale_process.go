package mihomo

import (
	"bytes"
	"path/filepath"
)

// isManagedDaemonCmdline deliberately matches the complete command line used
// by Operator.Start. Config validation uses an additional -t argument and is
// therefore not mistaken for a stale managed daemon.
func isManagedDaemonCmdline(cmdline []byte, binaryPath, configDir string) bool {
	parts := bytes.Split(cmdline, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) != 3 {
		return false
	}
	return filepath.Clean(string(parts[0])) == filepath.Clean(binaryPath) &&
		string(parts[1]) == "-d" &&
		filepath.Clean(string(parts[2])) == filepath.Clean(configDir)
}
