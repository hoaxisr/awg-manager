package autolistdetect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	PackageName = "awgmautolist"
	KeyFile     = "/opt/etc/awgmautolist.key"
)

var ErrNeedApiKey = errors.New("authorization is enabled, but no AWGM API key is configured")

var installerWaitDelay = initWaitDelay

type packageManager interface {
	Update() (string, error)
	Install([]string) (string, error)
}

type keySource interface {
	GetApiKey() string
	IsAuthEnabled() bool
}

// Installer owns package installation and API-key rotation, independently of
// candidate/status JSON. confirm must verify the running satellite after opkg.
type Installer struct {
	mu      sync.Mutex
	pkg     packageManager
	keys    keySource
	confirm func(context.Context) error

	keyPath     string
	enabledPath string
	initPath    string
	runInit     func(context.Context, string) error
}

func NewInstaller(pkg packageManager, keys keySource) *Installer {
	i := &Installer{pkg: pkg, keys: keys, keyPath: KeyFile, enabledPath: "/opt/etc/awgmautolist.enabled", initPath: "/opt/etc/init.d/S99awgmautolist"}
	i.runInit = i.runInitCommand
	return i
}

func (i *Installer) SetConfirm(confirm func(context.Context) error) { i.confirm = confirm }

// SetInstaller connects opkg and AWGM's key store to the detection service.
func (s *Service) SetInstaller(pkg packageManager, keys keySource) {
	installer := NewInstaller(pkg, keys)
	installer.SetConfirm(func(ctx context.Context) error { return s.confirmState(ctx, true) })
	s.installer = installer
}

func (s *Service) Install(ctx context.Context) error {
	if s.installer == nil {
		return errors.New("autolist installer is not configured")
	}
	return s.installer.Install(ctx)
}

// ApiKeyChanged persists the current key and restarts only an installed daemon.
func (s *Service) ApiKeyChanged() error {
	if s.installer == nil {
		return nil
	}
	return s.installer.ApiKeyChanged()
}

func (i *Installer) writeKey() error {
	if i.keys == nil {
		return errors.New("API key source is not configured")
	}
	key := strings.ReplaceAll(i.keys.GetApiKey(), "'", `'\''`)
	tmp, err := os.CreateTemp(filepath.Dir(i.keyPath), ".awgmautolist-key-*.tmp")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.WriteString("AWGM_API_KEY='" + key + "'\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(path, i.keyPath)
}

func (i *Installer) Install(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.pkg == nil || i.keys == nil {
		return errors.New("installer is not configured")
	}
	if i.keys.IsAuthEnabled() && i.keys.GetApiKey() == "" {
		return ErrNeedApiKey
	}
	if err := i.writeKey(); err != nil {
		return err
	}
	// Installation expresses an intent to run even if the previous package was
	// disabled. The enabled file is not package-owned and survives removal.
	if err := os.Remove(i.enabledPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updateOutput, updateErr := i.pkg.Update()
	output, installErr := i.pkg.Install([]string{PackageName})
	if installErr == nil {
		if _, err := os.Stat(i.initPath); err != nil {
			installErr = fmt.Errorf("package installed no init script: %w", err)
		}
	}
	if installErr != nil {
		err := fmt.Errorf("opkg install %s: %w: %s", PackageName, installErr, strings.TrimSpace(output))
		if updateErr != nil {
			err = errors.Join(err, fmt.Errorf("opkg update: %w: %s", updateErr, strings.TrimSpace(updateOutput)))
		}
		return err
	}
	if i.confirm == nil {
		return errors.New("satellite status confirmation is not configured")
	}
	return i.confirm(ctx)
}

func (i *Installer) ApiKeyChanged() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, err := os.Stat(i.initPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := i.writeKey(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return i.runInit(ctx, "restart")
}

func (i *Installer) runInitCommand(ctx context.Context, action string) error {
	cmd := exec.CommandContext(ctx, i.initPath, action)
	cmd.WaitDelay = installerWaitDelay
	output, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrWaitDelay) {
		// The init script exited; only a daemonized child still holds its pipes.
		return nil
	}
	if err != nil {
		return fmt.Errorf("autolist init %s: %w: %s", action, err, strings.TrimSpace(string(output)))
	}
	return nil
}
