// Package installer manages the sing-box binary lifecycle outside the
// Entware opkg pipeline. Downloads come from our entware-repo server
// over HTTP (integrity guarded by SHA256 baked into this build of
// awg-manager), and land at an absolute path in our config directory
// so they cannot be confused with a user-installed sing-box on PATH.
package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// DefaultBinaryPath is where the managed sing-box binary is placed on disk.
// Inside our own config directory so cleanup is trivial (rm -rf the dir).
const DefaultBinaryPath = "/opt/etc/awg-manager/singbox/sing-box"

// BinarySpec is one architecture's download metadata. Embedded in the
// build via embedded.go so a compromised server cannot serve a tampered
// binary that awg-manager would still trust.
type BinarySpec struct {
	Version string
	URL     string
	SHA256  string
}

// Installer downloads, verifies, and activates sing-box binaries.
type Installer struct {
	binaryPath string
	arch       string
	spec       BinarySpec
	httpClient *http.Client
	appLog     *logging.ScopedLogger

	// opkgRemove is overridable for tests; production uses defaultOpkgRemove.
	// Hooked here (not in installer constructor) so Phase 3.3 (Migrate) can
	// inject its own.
	opkgRemove        func(context.Context) error
	opkgListInstalled func(context.Context) (string, error)
}

// New builds an Installer. arch maps into EmbeddedBinaries; spec is what
// this build is pinned to. appLogger may be nil (tests).
func New(binaryPath, arch string, spec BinarySpec, appLogger logging.AppLogger) *Installer {
	return &Installer{
		binaryPath: binaryPath,
		arch:       arch,
		spec:       spec,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		appLog:     logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBProcess),
	}
}

// BinaryPath is the absolute filesystem path where the managed binary
// lives once activated. Callers wire this into the Operator.
func (i *Installer) BinaryPath() string { return i.binaryPath }

// RequiredVersion is the version this awg-manager build is pinned to.
func (i *Installer) RequiredVersion() string { return i.spec.Version }

// Download fetches the binary to <binaryPath>.tmp and verifies SHA256.
// On verification failure or HTTP error the tmp file is removed so the
// caller does not activate corrupted contents. Returns the tmp path on
// success.
func (i *Installer) Download(ctx context.Context) (string, error) {
	tmp := i.binaryPath + ".tmp"
	if err := os.MkdirAll(filepath.Dir(i.binaryPath), 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(i.binaryPath), err)
	}
	_ = os.Remove(tmp)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.spec.URL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := i.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", i.spec.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: status %d", i.spec.URL, resp.StatusCode)
	}

	out, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", tmp, err)
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(out, hasher), resp.Body)
	closeErr := out.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("read body: %w", err)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("close %s: %w", tmp, closeErr)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(got, i.spec.SHA256) {
		_ = os.Remove(tmp)
		i.appLog.Warn("download", i.spec.URL, fmt.Sprintf("sha256 mismatch: got %s, want %s", got, i.spec.SHA256))
		return "", fmt.Errorf("sha256 mismatch (downloaded %d bytes): got %s, want %s", written, got, i.spec.SHA256)
	}
	i.appLog.Info("download", i.spec.URL, fmt.Sprintf("downloaded %d bytes, sha256 verified", written))
	return tmp, nil
}

// Activate atomically replaces the live binary with the verified tmp,
// sets executable bit, and removes the tmp on failure.
//
// IMPORTANT: tmpPath MUST live on the same filesystem as binaryPath so
// os.Rename can succeed atomically. Cross-device rename returns EXDEV
// and the cleanup path here would discard the verified download —
// callers placing tmp elsewhere should copy+unlink instead. Download
// satisfies this by placing tmp at "<binaryPath>.tmp".
func (i *Installer) Activate(tmpPath string) error {
	if err := os.Chmod(tmpPath, 0755); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, i.binaryPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, i.binaryPath, err)
	}
	i.appLog.Info("activate", i.binaryPath, "managed binary in place")
	return nil
}

// CurrentVersion runs `<binaryPath> version` and returns the parsed
// version string, or "" if the binary is missing/unexecutable.
func (i *Installer) CurrentVersion(ctx context.Context) string {
	if _, err := os.Stat(i.binaryPath); err != nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, i.binaryPath, "version").Output()
	if err != nil {
		return ""
	}
	versionRe := regexp.MustCompile(`(?i)\bsing-?box\b\s+version\b\s+([^\s]+)`)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if m := versionRe.FindStringSubmatch(line); len(m) == 2 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}
