package updater

import (
	"context"
	"fmt"
	osexec "os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	opkgUpdateTimeout     = 120 * time.Second
	opkgUpgradableTimeout = 30 * time.Second
	packageName           = "awg-manager"
)

// Check runs opkg update + opkg list-upgradable and returns update info.
func Check(ctx context.Context, currentVersion string) *UpdateInfo {
	info := &UpdateInfo{
		CurrentVersion: currentVersion,
		CheckedAt:      time.Now(),
	}

	// Step 1: refresh package indexes
	result, err := exec.RunWithOptions(ctx, "opkg", []string{"update"}, exec.Options{
		Timeout: opkgUpdateTimeout,
	})
	if err != nil {
		info.Error = fmt.Sprintf("opkg update: %s", formatOpkgError(result, err))
		return info
	}

	// Step 2: check upgradable packages
	result, err = exec.RunWithOptions(ctx, "opkg", []string{"list-upgradable"}, exec.Options{
		Timeout: opkgUpgradableTimeout,
	})
	if err != nil {
		info.Error = fmt.Sprintf("opkg list-upgradable: %s", formatOpkgError(result, err))
		return info
	}

	// Step 3: parse output for our package
	// Format: "awg-manager - 2.0.9-5 - 2.1.0-1"
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, packageName+" ") {
			continue
		}
		parts := strings.Split(line, " - ")
		if len(parts) == 3 {
			info.Available = true
			info.LatestVersion = strings.TrimSpace(parts[2])
		}
		break
	}

	return info
}

// Upgrade launches opkg upgrade in a fully detached process.
// Uses Setsid to create a new session immune to parent signals.
// Returns immediately — the upgrade will kill this process.
func Upgrade(_ context.Context) error {
	cmd := osexec.Command("sh", "-c", fmt.Sprintf("sleep 2 && opkg upgrade %s", packageName))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap zombie if upgrade fails and we survive
	return nil
}

func formatOpkgError(result *exec.Result, err error) string {
	if result != nil {
		stderr := strings.TrimSpace(result.Stderr)
		if stderr != "" {
			return stderr
		}
	}
	return err.Error()
}
