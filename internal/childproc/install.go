package childproc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// downloadSlack tops up the transfer cap over the expected size so a
// legitimate asset a few bytes larger than pinned doesn't fail mid-transfer
// (it would still fail SHA256 afterwards — the slack only moves WHERE it
// fails).
const downloadSlack = 1 << 20

// Downloader is the narrow download contract; the adapter in cmd/awg-manager
// bridges it to the shared downloader.Service (timeouts, redirects, limits).
type Downloader interface {
	// DownloadFile fetches url into destPath (mode 0644, non-atomic —
	// Install activates via chmod+rename). maxBytes hard-caps the transfer.
	DownloadFile(ctx context.Context, url, destPath string, maxBytes int64) error
}

// ChecksumError is returned by Install when the downloaded file's SHA256 does
// not match the pinned value. It carries both hashes so a caller can log the
// mismatch without re-parsing the message.
type ChecksumError struct {
	Got  string
	Want string
}

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("контрольная сумма не совпала (получено %s, ожидалось %s)", e.Got, e.Want)
}

// Install downloads url into binPath.tmp, verifies its SHA256 (if sha256hex is
// non-empty), chmods it 0755 and atomically renames it into place. On any
// failure the tmp file is removed and binPath is left untouched. A SHA256
// mismatch is reported as *ChecksumError.
func Install(ctx context.Context, d Downloader, binPath, url, sha256hex string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		return err
	}
	tmp := binPath + ".tmp"
	_ = os.Remove(tmp)
	if err := d.DownloadFile(ctx, url, tmp, size+downloadSlack); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("загрузка %s: %w", url, err)
	}
	if sha256hex != "" {
		got, err := sha256File(tmp)
		if err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if !strings.EqualFold(got, sha256hex) {
			_ = os.Remove(tmp)
			return &ChecksumError{Got: got, Want: sha256hex}
		}
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Atomic activation: same directory → same filesystem, rename не рвётся.
	if err := os.Rename(tmp, binPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
