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

	"github.com/hoaxisr/awg-manager/internal/sys/semver"
)

const (
	PackageName = "awgmautolist"
	KeyFile     = "/opt/etc/awgmautolist.key"
	// controlFile is opkg's record of the installed package.
	controlFile = "/opt/lib/opkg/info/awgmautolist.control"

	autoUpgradeAttempts = 5
)

// autoUpgradeDelay: the panel's own upgrade restarts this daemon while opkg
// still holds its lock, so the satellite upgrade waits and retries.
var autoUpgradeDelay = 2 * time.Minute

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
	confirm func(ctx context.Context, running bool) error

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

func (i *Installer) SetConfirm(confirm func(ctx context.Context, running bool) error) {
	i.confirm = confirm
}

// SetInstaller connects opkg and AWGM's key store to the detection service.
// panelVersion is the awg-manager version; the satellite package is released
// with the same version, so an installed package below it is outdated.
func (s *Service) SetInstaller(pkg packageManager, keys keySource, panelVersion string) {
	installer := NewInstaller(pkg, keys)
	installer.SetConfirm(s.confirmState)
	s.installer = installer
	s.panelVersion = panelVersion
}

func (s *Service) controlFilePath() string {
	if s.controlPath != "" {
		return s.controlPath
	}
	return controlFile
}

// packageState reports the installed package version and whether it is
// below the panel version. Build metadata (+rN) is ignored: the feed carries
// release versions only.
func (s *Service) packageState() (version string, outdated bool) {
	b, err := os.ReadFile(s.controlFilePath())
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "Version:"); ok {
			version = strings.TrimSpace(v)
			break
		}
	}
	return version, version != "" && semver.Compare(version, s.panelVersion) < 0
}

// AutoUpgrade brings an installed, outdated satellite up to the panel
// version. It returns the version it started from ("" — nothing to do) and
// the last error once the attempts are exhausted. An upgrade that succeeds
// while the feed still lacks the panel version is reported as an error too.
func (s *Service) AutoUpgrade(ctx context.Context) (string, error) {
	from, outdated := s.packageState()
	if !outdated || s.installer == nil {
		return "", nil
	}
	var err error
	for range autoUpgradeAttempts {
		select {
		case <-ctx.Done():
			return from, ctx.Err()
		case <-time.After(autoUpgradeDelay):
		}
		err = s.Install(ctx)
		// opkg may have upgraded the package even when the state
		// confirmation failed: the package version is the verdict.
		v, still := s.packageState()
		if !still {
			return from, err
		}
		if err == nil {
			return from, fmt.Errorf("feed has no %s package newer than %s", PackageName, v)
		}
		if errors.Is(err, ErrNeedApiKey) {
			return from, err
		}
	}
	return from, err
}

// Install shares controlMu with SetEnabled: opkg's prerm/postinst run the
// init script, which must not interleave with an enable/disable.
func (s *Service) Install(ctx context.Context) error {
	if s.installer == nil {
		return errors.New("autolist installer is not configured")
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
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
	// An installed package is upgraded in place and keeps the user's
	// enable/disable intent. A fresh installation expresses an intent to run
	// even if the previous package was disabled: the enabled file is not
	// package-owned and survives removal.
	_, statErr := os.Stat(i.initPath)
	upgrade := statErr == nil
	if !upgrade {
		if err := os.Remove(i.enabledPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
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
	return i.confirm(ctx, !upgrade || enabledIntent(i.enabledPath))
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
