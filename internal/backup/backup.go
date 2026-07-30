package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ManifestName  = "awg-manager-backup.json"
	FileType      = "awg-manager-full-backup"
	FileVersion   = 1
	maxArchiveLen = 512 << 20 // 512 MiB
)

// Manifest describes a full awg-manager data-dir backup archive.
type Manifest struct {
	Version    int       `json:"version"`
	Type       string    `json:"type"`
	ExportedAt time.Time `json:"exportedAt"`
	AppVersion string    `json:"appVersion,omitempty"`
}

// Export writes a gzip-compressed tar of dataDir to w. Runtime caches are skipped.
func Export(dataDir, appVersion string, w io.Writer) error {
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	if dataDir == "" {
		return fmt.Errorf("data-dir не задан")
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		return fmt.Errorf("data-dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("data-dir не каталог")
	}

	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	manifest := Manifest{
		Version:    FileVersion,
		Type:       FileType,
		ExportedAt: time.Now().UTC(),
		AppVersion: strings.TrimSpace(appVersion),
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writeTarBytes(tw, ManifestName, raw, manifest.ExportedAt); err != nil {
		return err
	}

	return filepath.WalkDir(dataDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if shouldSkip(relSlash) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			hdr := &tar.Header{
				Name:     relSlash + "/",
				Typeflag: tar.TypeDir,
				Mode:     0o755,
				ModTime:  info.ModTime(),
			}
			return tw.WriteHeader(hdr)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		hdr := &tar.Header{
			Name:    relSlash,
			Mode:    int64(info.Mode().Perm()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		return err
	})
}

// Restore replaces dataDir contents from r (gzip tar). Current dir is renamed aside first.
func Restore(dataDir string, r io.Reader) error {
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	if dataDir == "" {
		return fmt.Errorf("data-dir не задан")
	}
	parent := filepath.Dir(dataDir)
	staging := filepath.Join(parent, ".awg-manager-restore-"+time.Now().UTC().Format("20060102-150405"))
	previous := filepath.Join(parent, filepath.Base(dataDir)+".pre-restore-"+time.Now().UTC().Format("20060102-150405"))

	if err := extractArchive(r, staging); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := validateStaging(staging); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}

	if _, err := os.Stat(dataDir); err == nil {
		if err := os.Rename(dataDir, previous); err != nil {
			_ = os.RemoveAll(staging)
			return fmt.Errorf("не удалось сохранить текущие данные: %w", err)
		}
	} else if !os.IsNotExist(err) {
		_ = os.RemoveAll(staging)
		return err
	}

	if err := os.Rename(staging, dataDir); err != nil {
		// Best-effort rollback.
		_ = os.Rename(previous, dataDir)
		_ = os.RemoveAll(staging)
		return fmt.Errorf("не удалось применить резервную копию: %w", err)
	}
	if err := WritePostRestoreMarker(dataDir); err != nil {
		return fmt.Errorf("не удалось записать маркер post-restore: %w", err)
	}
	return nil
}

func shouldSkip(rel string) bool {
	if rel == ManifestName {
		return true
	}
	if rel == "run" || strings.HasPrefix(rel, "run/") {
		return true
	}
	if rel == "locks" || strings.HasPrefix(rel, "locks/") {
		return true
	}
	switch rel {
	case "singbox/cache.db", "singbox/cache.db-shm", "singbox/cache.db-wal":
		return true
	}
	if strings.HasPrefix(rel, "singbox/cache.db") {
		return true
	}
	if strings.HasSuffix(rel, ".tmp") || strings.HasSuffix(rel, ".pid") {
		return true
	}
	if strings.HasSuffix(rel, ".lock") || strings.HasSuffix(rel, ".lock.d") {
		return true
	}
	if strings.Contains(rel, ".pre-restore-") || strings.HasPrefix(rel, ".awg-manager-restore-") {
		return true
	}
	return false
}

func writeTarBytes(tw *tar.Writer, name string, data []byte, mod time.Time) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    int64(len(data)),
		ModTime: mod,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func extractArchive(r io.Reader, destDir string) error {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	gr, err := gzip.NewReader(io.LimitReader(r, maxArchiveLen+1))
	if err != nil {
		return fmt.Errorf("не gzip-архив: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("ошибка чтения tar: %w", err)
		}
		if hdr == nil {
			continue
		}
		name := filepath.Clean(hdr.Name)
		name = filepath.ToSlash(name)
		if name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
			return fmt.Errorf("небезопасный путь в архиве: %q", hdr.Name)
		}
		target := filepath.Join(destDir, filepath.FromSlash(name))
		if !strings.HasPrefix(target, destDir+string(os.PathSeparator)) && target != destDir {
			return fmt.Errorf("небезопасный путь в архиве: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			total += hdr.Size
			if total > maxArchiveLen {
				return fmt.Errorf("архив слишком большой (лимит %d МБ)", maxArchiveLen>>20)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode).Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			if hdr.Typeflag == 0 && hdr.Size >= 0 {
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return err
				}
				total += hdr.Size
				if total > maxArchiveLen {
					return fmt.Errorf("архив слишком большой (лимит %d МБ)", maxArchiveLen>>20)
				}
				f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode).Perm())
				if err != nil {
					return err
				}
				if _, err := io.Copy(f, tr); err != nil {
					f.Close()
					return err
				}
				if err := f.Close(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateStaging(dir string) error {
	manifestPath := filepath.Join(dir, ManifestName)
	var manifest Manifest
	if raw, err := os.ReadFile(manifestPath); err == nil {
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return fmt.Errorf("некорректный %s: %w", ManifestName, err)
		}
		if manifest.Type != "" && manifest.Type != FileType {
			return fmt.Errorf("неподдерживаемый тип архива: %q", manifest.Type)
		}
		if manifest.Version > 0 && manifest.Version > FileVersion {
			return fmt.Errorf("версия архива %d не поддерживается", manifest.Version)
		}
	}
	settingsPath := filepath.Join(dir, "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		return fmt.Errorf("в архиве нет settings.json — это не резервная копия awg-manager")
	}
	return nil
}

// Filename returns a suggested download name for a backup export.
func Filename(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return fmt.Sprintf("awg-manager-backup-%s.tar.gz", now.Format("2006-01-02-150405"))
}

// PeekManifest reads manifest from an in-memory gzip tar prefix (for UI hints).
func PeekManifest(data []byte) (*Manifest, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("manifest not found")
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name != ManifestName {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return nil, err
		}
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return &m, nil
	}
}
