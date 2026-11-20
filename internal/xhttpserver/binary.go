package xhttpserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	DefaultBinaryPath  = "/opt/bin/awgm-xhttpd"
	DefaultConfigPath  = "/tmp/awgm-xhttpd.conf"
	ExpectedEngine     = "c/1"
	ExpectedVersion    = "1.0.0-c"
	ExpectedBannerPref = "awgm-xhttpd "
)

var RequiredCapabilities = []string{"vless-xhttp", "finiteproto-v2"}

// BinaryInfo describes the verified local awgm-xhttpd binary state.
type BinaryInfo struct {
	Path      string `json:"path"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ResolveBinaryPath resolves the awgm-xhttpd executable path.
// Order: explicit custom path -> AWGM_C_XHTTPD_BIN -> /opt/bin/awgm-xhttpd.
func ResolveBinaryPath(custom string) string {
	if s := strings.TrimSpace(custom); s != "" {
		return s
	}
	if env := strings.TrimSpace(os.Getenv("AWGM_C_XHTTPD_BIN")); env != "" {
		return env
	}
	return DefaultBinaryPath
}

// ResolveConfigPath resolves the runtime configuration file path for awgm-xhttpd.
func ResolveConfigPath(custom string) string {
	if s := strings.TrimSpace(custom); s != "" {
		return s
	}
	if env := strings.TrimSpace(os.Getenv("AWGM_C_XHTTPD_CONF")); env != "" {
		return env
	}
	return DefaultConfigPath
}

// IsDisabledByEnv reports whether transitional AWGM_DISABLE_C_XHTTPD explicitly forbids starting C.
// Note: AWGM_ENABLE_C_XHTTPD is intentionally ignored; C is mandatory and there is no Go fallback.
func IsDisabledByEnv() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AWGM_DISABLE_C_XHTTPD")))
	return v == "1" || v == "true" || v == "yes"
}

func isRegularExecutableFile(st os.FileInfo) error {
	if st == nil {
		return fmt.Errorf("%w: missing file info", ErrBinaryUnavailable)
	}
	if st.IsDir() {
		return fmt.Errorf("%w: path is a directory", ErrBinaryUnavailable)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%w: path is not a regular file", ErrBinaryUnavailable)
	}
	if runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
		return fmt.Errorf("%w: file is not executable (mode %04o)", ErrBinaryUnavailable, st.Mode().Perm())
	}
	return nil
}

// InspectBinary validates that awgm-xhttpd is not disabled by env, exists as a regular executable file,
// and (when checkVersion is true) responds to -version with ExpectedVersion.
func InspectBinary(customPath string) (BinaryInfo, error) {
	return inspectBinaryInternal(customPath, true)
}

func inspectBinaryInternal(customPath string, checkVersion bool) (BinaryInfo, error) {
	binPath := ResolveBinaryPath(customPath)
	info := BinaryInfo{Path: binPath}

	if IsDisabledByEnv() {
		info.Error = ErrCDaemonDisabled.Error()
		return info, ErrCDaemonDisabled
	}

	st, err := os.Stat(binPath)
	if err != nil {
		werr := fmt.Errorf("%w: %s: %v", ErrBinaryUnavailable, binPath, err)
		info.Error = werr.Error()
		return info, werr
	}
	if err := isRegularExecutableFile(st); err != nil {
		werr := fmt.Errorf("%s: %w", binPath, err)
		info.Error = werr.Error()
		return info, werr
	}

	if !checkVersion {
		info.Available = true
		info.Version = ExpectedVersion
		return info, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binPath, "-version").CombinedOutput()
	if err != nil {
		werr := fmt.Errorf("%w: %s -version failed: %v (%s)", ErrBinaryUnavailable, binPath, err, strings.TrimSpace(string(out)))
		info.Error = werr.Error()
		return info, werr
	}

	line := strings.TrimSpace(string(out))
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = strings.TrimSpace(line[:nl])
	}
	if !strings.HasPrefix(line, ExpectedBannerPref) {
		werr := fmt.Errorf("%w: unexpected -version banner from %s: %q", ErrBinaryUnavailable, binPath, line)
		info.Error = werr.Error()
		return info, werr
	}
	ver := strings.TrimSpace(strings.TrimPrefix(line, ExpectedBannerPref))
	if ver != ExpectedVersion {
		werr := fmt.Errorf("%w: version mismatch for %s: got %q, want %q", ErrBinaryUnavailable, binPath, ver, ExpectedVersion)
		info.Version = ver
		info.Error = werr.Error()
		return info, werr
	}

	info.Available = true
	info.Version = ver
	return info, nil
}
