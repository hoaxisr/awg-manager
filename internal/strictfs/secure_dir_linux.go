//go:build linux

package strictfs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// SecureDir provides filesystem operations confined to a specific base directory.
// On Linux, it uses descriptor-relative paths (openat, renameat, unlinkat)
// to prevent TOCTOU vulnerabilities and symlink escapes.
type SecureDir struct {
	basePath string
	dirFd    int
}

// NewSecureDir creates a new SecureDir instance bound to the specified directory.
func NewSecureDir(basePath string) (*SecureDir, error) {
	cleanBase := filepath.Clean(basePath)

	// Open the directory file descriptor with O_CLOEXEC
	fd, err := unix.Open(cleanBase, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open secure dir base %s: %w", cleanBase, err)
	}

	return &SecureDir{
		basePath: cleanBase,
		dirFd:    fd,
	}, nil
}

func (s *SecureDir) BasePath() string {
	return s.basePath
}

func (s *SecureDir) Close() error {
	if s.dirFd >= 0 {
		err := unix.Close(s.dirFd)
		s.dirFd = -1
		return err
	}
	return nil
}

// openat securely opens a file within the directory fd.
// We attempt openat2 with RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS.
// If ENOSYS, fallback to openat with O_NOFOLLOW.
func (s *SecureDir) openat(name string, flags int, perm uint32) (int, error) {
	if err := ValidateBasename(name); err != nil {
		return -1, fmt.Errorf("invalid basename %q: %w", name, err)
	}

	// Try openat2
	how := &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Mode:    uint64(perm),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	fd, err := unix.Openat2(s.dirFd, name, how)
	if err == nil {
		return fd, nil
	}

	// Fallback to openat only if ENOSYS or EINVAL (meaning openat2 or the specific flags are not supported)
	if err == unix.ENOSYS || err == unix.EINVAL {
		flags |= unix.O_CLOEXEC | unix.O_NOFOLLOW
		fd, err = unix.Openat(s.dirFd, name, flags, perm)
		if err != nil {
			return -1, fmt.Errorf("openat fallback %q: %w", name, err)
		}

		// Ensure it's not a symlink that somehow bypassed O_NOFOLLOW
		// (though O_NOFOLLOW explicitly rejects symlinks as the final component)
		var st unix.Stat_t
		if fstatErr := unix.Fstat(fd, &st); fstatErr != nil {
			unix.Close(fd)
			return -1, fmt.Errorf("fstat after openat %q: %w", name, fstatErr)
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			unix.Close(fd)
			return -1, fmt.Errorf("openat %q resolved to a symlink", name)
		}
		return fd, nil
	}

	return -1, fmt.Errorf("openat2 %q: %w", name, err)
}

func (s *SecureDir) ReadFile(name string) ([]byte, error) {
	fd, err := s.openat(name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()

	return io.ReadAll(f)
}

func (s *SecureDir) WriteFileAtomic(name string, data []byte, perm os.FileMode) error {
	if err := ValidateBasename(name); err != nil {
		return fmt.Errorf("invalid basename %q: %w", name, err)
	}

	tempName := fmt.Sprintf(".tmp.%s", name)

	// Open temp file
	fd, err := s.openat(tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC, uint32(perm))
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	f := os.NewFile(uintptr(fd), tempName)
	closed := false
	defer func() {
		if !closed {
			f.Close()
			unix.Unlinkat(s.dirFd, tempName, 0)
		}
	}()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write data: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	closed = true

	// Renameat
	if err := unix.Renameat(s.dirFd, tempName, s.dirFd, name); err != nil {
		unix.Unlinkat(s.dirFd, tempName, 0)
		return fmt.Errorf("renameat: %w", err)
	}

	// Fsync directory
	if err := unix.Fsync(s.dirFd); err != nil {
		return fmt.Errorf("fsync directory: %w", err)
	}

	return nil
}

func (s *SecureDir) Remove(name string) error {
	if err := ValidateBasename(name); err != nil {
		return fmt.Errorf("invalid basename %q: %w", name, err)
	}
	if err := unix.Unlinkat(s.dirFd, name, 0); err != nil && err != unix.ENOENT {
		return fmt.Errorf("unlinkat %q: %w", name, err)
	}
	return nil
}

func (s *SecureDir) ComputeDigest(name string) (string, error) {
	fd, err := s.openat(name, unix.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read file for digest: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *SecureDir) RemoveAll(name string) error {
	if err := ValidateBasename(name); err != nil {
		return fmt.Errorf("invalid basename %q: %w", name, err)
	}
	return removeAllRelative(s.dirFd, name)
}

func removeAllRelative(parentFd int, name string) error {
	// Try to remove as a file or empty dir first
	err := unix.Unlinkat(parentFd, name, 0)
	if err == nil || err == unix.ENOENT {
		return nil
	}
	if err == unix.EISDIR || err == unix.EPERM {
		// It's a directory
		fd, err := unix.Openat(parentFd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			if err == unix.ENOENT {
				return nil
			}
			return err
		}

		f := os.NewFile(uintptr(fd), name)
		names, err := f.Readdirnames(-1)
		f.Close() // also closes fd

		if err != nil {
			return err
		}

		// Open again to unlinkat relative to it
		subFd, err := unix.Openat(parentFd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}

		var subErr error
		for _, child := range names {
			if err := removeAllRelative(subFd, child); err != nil {
				subErr = err
				break
			}
		}
		unix.Close(subFd)

		if subErr != nil {
			return subErr
		}
		return unix.Unlinkat(parentFd, name, unix.AT_REMOVEDIR)
	}
	return err
}

func (s *SecureDir) Rename(oldName, newName string) error {
	if err := ValidateBasename(oldName); err != nil {
		return fmt.Errorf("invalid old basename %q: %w", oldName, err)
	}
	if err := ValidateBasename(newName); err != nil {
		return fmt.Errorf("invalid new basename %q: %w", newName, err)
	}

	if err := unix.Renameat(s.dirFd, oldName, s.dirFd, newName); err != nil {
		return fmt.Errorf("renameat %q -> %q: %w", oldName, newName, err)
	}
	if err := unix.Fsync(s.dirFd); err != nil {
		return fmt.Errorf("fsync directory after rename: %w", err)
	}
	return nil
}
