package cloudagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const processName = "coalagent"

// Reset signals running Keenetic/Netcraze cloud agent processes (coalagent) to
// drop stale UDP sockets and immediately re-establish connections over the
// active outbound route. Returns the number of signaled processes.
func Reset(ctx context.Context) (int, error) {
	return reset(ctx)
}

func resetWith(ctx context.Context, procRoot string, killFn func(int) error) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", procRoot, err)
	}
	selfPID := os.Getpid()
	signaled := 0
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return signaled, err
		}
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == selfPID {
			continue
		}
		procDir := filepath.Join(procRoot, entry.Name())
		if !isCloudAgentProcess(procDir) {
			continue
		}
		if killFn != nil {
			if kerr := killFn(pid); kerr != nil {
				errs = append(errs, fmt.Errorf("signal pid %d: %w", pid, kerr))
				continue
			}
		}
		signaled++
	}
	return signaled, errors.Join(errs...)
}

func isCloudAgentProcess(procDir string) bool {
	commRaw, err := os.ReadFile(filepath.Join(procDir, "comm"))
	if err != nil || strings.TrimSpace(string(commRaw)) != processName {
		return false
	}
	cmdRaw, err := os.ReadFile(filepath.Join(procDir, "cmdline"))
	if err != nil {
		return false
	}
	args := bytes.Split(cmdRaw, []byte{0})
	if len(args) == 0 {
		return false
	}
	argv0 := strings.TrimSpace(string(args[0]))
	if argv0 == "" {
		return false
	}
	return path.Base(strings.ReplaceAll(argv0, "\\", "/")) == processName
}
