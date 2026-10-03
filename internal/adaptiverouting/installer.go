package adaptiverouting

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/routerinfo"
)

const (
	SusaninVersion           = "v0.3.10"
	DefaultSusaninBinaryPath = "/opt/etc/awg-manager/susanin/bin/susanin-agent"
	ManagedSusaninDir        = "/opt/etc/awg-manager/susanin"
	ManagedSusaninBinaryPath = "/opt/etc/awg-manager/susanin/bin/susanin-agent"
	ManagedSusaninToolsDir   = "/opt/etc/awg-manager/susanin/tools"
	ManagedSusaninEtcDir     = "/opt/etc/awg-manager/susanin/etc"
	ManagedSusaninVarDir     = "/opt/etc/awg-manager/susanin/var"
)

var DefaultTelegramCIDRs = []string{
	"91.108.4.0/22",
	"91.108.8.0/22",
	"91.108.12.0/22",
	"91.108.16.0/22",
	"91.108.20.0/22",
	"91.108.56.0/22",
	"149.154.160.0/20",
	"149.154.164.0/22",
	"149.154.168.0/22",
	"149.154.172.0/22",
}


type BinarySpec struct {
	Version string
	URL     string
	SHA256  string
	Size    int64
}

var ReleaseBinaries = map[string]BinarySpec{
	"aarch64": {
		Version: SusaninVersion,
		URL:     "https://github.com/R17a/Susanin.Keenetic/releases/download/v0.3.10/susanin-keenetic-deploy-aarch64.tar.gz",
		SHA256:  "aa54f2b613c18dca759c1f6a887dfcdc34adedb0eae25c80561236faebf1864a",
		Size:    399563,
	},
	"aarch64-3.10": {
		Version: SusaninVersion,
		URL:     "https://github.com/R17a/Susanin.Keenetic/releases/download/v0.3.10/susanin-keenetic-deploy-aarch64.tar.gz",
		SHA256:  "aa54f2b613c18dca759c1f6a887dfcdc34adedb0eae25c80561236faebf1864a",
		Size:    399563,
	},
	"mips": {
		Version: SusaninVersion,
		URL:     "https://github.com/R17a/Susanin.Keenetic/releases/download/v0.3.10/susanin-keenetic-deploy-mips.tar.gz",
		SHA256:  "6abe353d01afd2f2527b1e49e9707af9a3c53c4015f4bd87bdc072268b8e3836",
		Size:    409372,
	},
	"mips-3.4": {
		Version: SusaninVersion,
		URL:     "https://github.com/R17a/Susanin.Keenetic/releases/download/v0.3.10/susanin-keenetic-deploy-mips.tar.gz",
		SHA256:  "6abe353d01afd2f2527b1e49e9707af9a3c53c4015f4bd87bdc072268b8e3836",
		Size:    409372,
	},
	"mipsel": {
		Version: SusaninVersion,
		URL:     "https://github.com/R17a/Susanin.Keenetic/releases/download/v0.3.10/susanin-keenetic-deploy-mipsel.tar.gz",
		SHA256:  "ee92a4a5e78e4151424fb56e115b53e43c5022bf2a29cef897d55452c61fc7b9",
		Size:    406187,
	},
	"mipsel-3.4": {
		Version: SusaninVersion,
		URL:     "https://github.com/R17a/Susanin.Keenetic/releases/download/v0.3.10/susanin-keenetic-deploy-mipsel.tar.gz",
		SHA256:  "ee92a4a5e78e4151424fb56e115b53e43c5022bf2a29cef897d55452c61fc7b9",
		Size:    406187,
	},
	"armv7": {
		Version: SusaninVersion,
		URL:     "https://github.com/R17a/Susanin.Keenetic/releases/download/v0.3.10/susanin-keenetic-deploy-armv7.tar.gz",
		SHA256:  "db22883d8af2abc1d29c7e7ddc8a0b4dec47ea3741c6de3dc3ada0a31e713638",
		Size:    333399,
	},
}

type SusaninInstaller struct {
	targetPath string
	arch       string
	freeDisk   func(path string) (int64, bool)
	client     *http.Client
	mu         sync.Mutex
}

func NewInstaller(arch string) *SusaninInstaller {
	return &SusaninInstaller{
		targetPath: ManagedSusaninBinaryPath,
		arch:       arch,
		freeDisk:   routerinfo.FreeBytes,
		client:     &http.Client{Timeout: 90 * time.Second},
	}
}

// ResolveBinary returns path to executable susanin-agent binary, or empty if none found.
func (i *SusaninInstaller) ResolveBinary() string {
	candidates := []string{
		i.targetPath,
		ManagedSusaninBinaryPath,
		DefaultSusaninBinaryPath,
		"/opt/bin/susanin-agent",
		"/opt/susanin/bin/susanin-agent",
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && (fi.Mode()&0111 != 0) {
			return p
		}
	}
	return ""
}

// EnsureInstalled downloads and installs susanin-agent if not already present.
func (i *SusaninInstaller) EnsureInstalled(ctx context.Context) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if bin := i.ResolveBinary(); bin != "" {
		// Even if binary already exists, ensure datapath.sh is up to date with EnhancedDatapathScript
		baseDir := filepath.Dir(filepath.Dir(bin))
		toolsDir := filepath.Join(baseDir, "tools")
		if fi, err := os.Stat(toolsDir); err == nil && fi.IsDir() {
			target := filepath.Join(toolsDir, "datapath.sh")
			tmpFile := target + ".tmp"
			if err := os.WriteFile(tmpFile, []byte(EnhancedDatapathScript), 0755); err == nil {
				_ = os.Rename(tmpFile, target)
			} else {
				_ = os.WriteFile(target, []byte(EnhancedDatapathScript), 0755)
			}
		}
		return bin, nil
	}


	spec, ok := ReleaseBinaries[i.arch]
	if !ok {
		// try generic prefix
		for k, v := range ReleaseBinaries {
			if strings.HasPrefix(i.arch, k) {
				spec = v
				ok = true
				break
			}
		}
	}
	if !ok {
		return "", fmt.Errorf("no prebuilt susanin-agent release for arch %q", i.arch)
	}

	// Check disk space (need ~5MB slack)
	if i.freeDisk != nil {
		if free, ok := i.freeDisk(filepath.Dir(i.targetPath)); ok && free < 5<<20 {
			return "", fmt.Errorf("insufficient disk space for susanin-agent: %d bytes free", free)
		}
	}

	if err := os.MkdirAll(filepath.Dir(i.targetPath), 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(i.targetPath), err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := i.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download susanin-agent archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download susanin-agent archive: HTTP %d", resp.StatusCode)
	}

	// Hash while downloading
	hasher := sha256.New()
	tee := io.TeeReader(resp.Body, hasher)

	baseDir := ManagedSusaninDir
	if i.targetPath != ManagedSusaninBinaryPath {
		baseDir = filepath.Dir(filepath.Dir(i.targetPath))
	}

	tmpPath := i.targetPath + ".tmp"
	_ = os.Remove(tmpPath)

	if err := extractArchive(tee, baseDir, i.targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("extract susanin archive: %w", err)
	}

	// Drain any remaining bytes to ensure complete hash calculation
	_, _ = io.Copy(io.Discard, tee)

	if spec.SHA256 != "" {
		actualSha := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(actualSha, spec.SHA256) {
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("checksum mismatch for susanin-agent: expected %s, got %s", spec.SHA256, actualSha)
		}
	}

	if err := os.Rename(tmpPath, i.targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("activate susanin binary: %w", err)
	}

	// Symlink to /opt/bin/susanin-agent
	_ = os.MkdirAll("/opt/bin", 0755)
	_ = os.Remove("/opt/bin/susanin-agent")
	_ = os.Symlink(i.targetPath, "/opt/bin/susanin-agent")

	// Backwards compatibility symlink /opt/susanin -> ManagedSusaninDir
	if _, err := os.Lstat("/opt/susanin"); os.IsNotExist(err) {
		_ = os.Symlink(ManagedSusaninDir, "/opt/susanin")
	}

	return i.targetPath, nil
}

func extractArchive(r io.Reader, baseDir, targetBinPath string) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gzr.Close()

	binDir := filepath.Dir(targetBinPath)
	toolsDir := filepath.Join(baseDir, "tools")
	etcDir := filepath.Join(baseDir, "etc")
	varDir := filepath.Join(baseDir, "var")

	_ = os.MkdirAll(binDir, 0755)
	_ = os.MkdirAll(toolsDir, 0755)
	_ = os.MkdirAll(etcDir, 0755)
	_ = os.MkdirAll(varDir, 0755)

	foundBin := false
	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		baseName := filepath.Base(header.Name)
		if header.Typeflag != tar.TypeReg {
			continue
		}

		switch baseName {
		case "susanin-agent":
			tmpPath := targetBinPath + ".tmp"
			outFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				_ = outFile.Close()
				_ = os.Remove(tmpPath)
				return err
			}
			_ = outFile.Close()
			if err := os.Chmod(tmpPath, 0755); err != nil {
				_ = os.Remove(tmpPath)
				return err
			}
			foundBin = true

		case "datapath.sh":
			datapathPath := filepath.Join(toolsDir, "datapath.sh")
			_, _ = io.Copy(io.Discard, tr)
			_ = os.WriteFile(datapathPath, []byte(EnhancedDatapathScript), 0755)

		case "vpn_always.txt":
			alwaysPath := filepath.Join(etcDir, "vpn_always.txt")
			data, err := io.ReadAll(tr)
			if err == nil {
				existing, _ := os.ReadFile(alwaysPath)
				var lines []string
				if len(existing) > 0 {
					lines = strings.Split(string(existing), "\n")
				} else {
					lines = strings.Split(string(data), "\n")
				}
				_ = os.WriteFile(alwaysPath, []byte(strings.Join(lines, "\n")), 0644)
			}

		case "vpn_never.txt":
			neverPath := filepath.Join(etcDir, "vpn_never.txt")
			if _, err := os.Stat(neverPath); os.IsNotExist(err) {
				data, err := io.ReadAll(tr)
				if err == nil {
					_ = os.WriteFile(neverPath, data, 0644)
				}
			}
		}
	}

	if !foundBin {
		return fmt.Errorf("binary 'susanin-agent' not found in archive")
	}

	return nil
}

// Uninstall stops susanin-agent and removes installed binaries and symlinks.
func (i *SusaninInstaller) Uninstall() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Stop any running process
	_ = exec.Command("killall", "-9", "susanin-agent").Run()
	time.Sleep(300 * time.Millisecond)

	bin := i.ResolveBinary()
	if bin != "" {
		_ = os.Remove(bin)
	}
	_ = os.Remove("/opt/bin/susanin-agent")
	_ = os.Remove(ManagedSusaninBinaryPath)
	return nil
}
