package files

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxReadBytes = 512 * 1024
const maxUploadBytes = 10 * 1024 * 1024

// Entry is one directory listing item.
type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
}

// ListDir lists one directory (non-recursive).
func (s *Sandbox) ListDir(path string) ([]Entry, string, error) {
	abs, _, err := s.Resolve(path)
	if err != nil {
		return nil, "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() {
		return nil, "", fmt.Errorf("not a directory")
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, "", err
	}
	out := make([]Entry, 0, len(entries)+1)
	if abs != "/" {
		out = append(out, Entry{
			Name:  "..",
			Path:  filepath.Dir(abs),
			IsDir: true,
		})
	}
	for _, e := range entries {
		fi, statErr := e.Info()
		if statErr != nil {
			continue
		}
		out = append(out, entryFromInfo(filepath.Join(abs, e.Name()), fi))
	}
	return out, abs, nil
}

func entryFromInfo(path string, fi fs.FileInfo) Entry {
	return Entry{
		Name:    fi.Name(),
		Path:    path,
		IsDir:   fi.IsDir(),
		Size:    fi.Size(),
		Mode:    fi.Mode().Perm().String(),
		ModTime: fi.ModTime().Format(time.RFC3339),
	}
}

// ReadFile reads a regular file up to maxReadBytes.
func (s *Sandbox) ReadFile(path string) (content string, info Entry, err error) {
	abs, _, err := s.Resolve(path)
	if err != nil {
		return "", Entry{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", Entry{}, err
	}
	if fi.IsDir() {
		return "", Entry{}, fmt.Errorf("is a directory")
	}
	if fi.Size() > maxReadBytes {
		return "", Entry{}, fmt.Errorf("file too large (%d bytes, max %d)", fi.Size(), maxReadBytes)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", Entry{}, err
	}
	if strings.Contains(string(b[:min(len(b), 512)]), "\x00") {
		return "", Entry{}, fmt.Errorf("binary file cannot be edited as text")
	}
	return string(b), entryFromInfo(abs, fi), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// WriteFile overwrites a regular file.
func (s *Sandbox) WriteFile(path, content string) error {
	abs, err := s.ResolveWrite(path)
	if err != nil {
		return err
	}
	if len(content) > maxReadBytes {
		return fmt.Errorf("content too large (max %d bytes)", maxReadBytes)
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// Mkdir creates a directory.
func (s *Sandbox) Mkdir(path string) error {
	abs, err := s.ResolveWrite(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, 0o755)
}

// Remove deletes a file or empty directory.
func (s *Sandbox) Remove(path string) error {
	abs, err := s.ResolveWrite(path)
	if err != nil {
		return err
	}
	for _, r := range s.roots {
		if abs == r.Path {
			return errors.New("cannot delete sandbox root")
		}
	}
	return os.Remove(abs)
}

// Rename moves within the same writable root.
func (s *Sandbox) Rename(from, to string) error {
	src, err := s.ResolveWrite(from)
	if err != nil {
		return err
	}
	dst, err := s.ResolveWrite(to)
	if err != nil {
		return err
	}
	srcRoot := rootForPath(s.roots, src)
	dstRoot := rootForPath(s.roots, dst)
	if srcRoot == nil || dstRoot == nil || srcRoot.Path != dstRoot.Path {
		return errors.New("rename across roots is not allowed")
	}
	return os.Rename(src, dst)
}

func rootForPath(roots []Root, path string) *Root {
	for i := range roots {
		if pathWithin(path, roots[i].Path) {
			return &roots[i]
		}
	}
	return nil
}

// OpenDownload opens a file for streaming download.
func (s *Sandbox) OpenDownload(path string) (io.ReadCloser, fs.FileInfo, error) {
	abs, _, err := s.Resolve(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, nil, err
	}
	if fi.IsDir() {
		return nil, nil, fmt.Errorf("is a directory")
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("not a regular file")
	}
	if fi.Size() > 50*1024*1024 {
		return nil, nil, fmt.Errorf("file too large for download (max 50MB)")
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, nil, err
	}
	return f, fi, nil
}

// SaveUpload writes an uploaded file into a directory inside the sandbox.
func (s *Sandbox) SaveUpload(dirPath, fileName string, data []byte) (string, error) {
	if strings.TrimSpace(fileName) == "" || strings.Contains(fileName, "/") || strings.Contains(fileName, "..") {
		return "", fmt.Errorf("invalid file name")
	}
	if len(data) > maxUploadBytes {
		return "", fmt.Errorf("file too large (max %d bytes)", maxUploadBytes)
	}
	absDir, err := s.ResolveWrite(dirPath)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(absDir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("target is not a directory")
	}
	target := filepath.Join(absDir, fileName)
	return target, os.WriteFile(target, data, 0o644)
}

const maxChecksumBytes = 50 * 1024 * 1024

// Copy duplicates a regular file within the same writable root.
func (s *Sandbox) Copy(src, dst string) error {
	srcAbs, err := s.ResolveWrite(src)
	if err != nil {
		return err
	}
	dstAbs, err := s.ResolveWrite(dst)
	if err != nil {
		return err
	}
	srcRoot := rootForPath(s.roots, srcAbs)
	dstRoot := rootForPath(s.roots, dstAbs)
	if srcRoot == nil || dstRoot == nil || srcRoot.Path != dstRoot.Path {
		return errors.New("copy across roots is not allowed")
	}
	fi, err := os.Stat(srcAbs)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("is a directory")
	}
	in, err := os.Open(srcAbs)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dstAbs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// Chmod sets permission bits on a path inside a writable root.
func (s *Sandbox) Chmod(path, modeOctal string) error {
	abs, err := s.ResolveWrite(path)
	if err != nil {
		return err
	}
	modeStr := strings.TrimSpace(modeOctal)
	if modeStr == "" {
		return fmt.Errorf("mode required")
	}
	u, err := strconv.ParseUint(modeStr, 8, 32)
	if err != nil {
		return fmt.Errorf("invalid mode: %w", err)
	}
	// Mask out SUID/SGID and sticky bits, allow only standard rwx (0777)
	u = u & 0777
	return os.Chmod(abs, fs.FileMode(u))
}

// Checksum returns hex digest of a regular file (up to maxChecksumBytes).
func (s *Sandbox) Checksum(path, algo string) (string, Entry, error) {
	abs, _, err := s.Resolve(path)
	if err != nil {
		return "", Entry{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", Entry{}, err
	}
	if fi.IsDir() {
		return "", Entry{}, fmt.Errorf("is a directory")
	}
	if fi.Size() > maxChecksumBytes {
		return "", Entry{}, fmt.Errorf("file too large for checksum (max %d bytes)", maxChecksumBytes)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", Entry{}, err
	}
	defer f.Close()
	var digest string
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case "sha256", "sha-256":
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return "", Entry{}, err
		}
		digest = hex.EncodeToString(h.Sum(nil))
	default:
		h := md5.New()
		if _, err := io.Copy(h, f); err != nil {
			return "", Entry{}, err
		}
		digest = hex.EncodeToString(h.Sum(nil))
	}
	return digest, entryFromInfo(abs, fi), nil
}
