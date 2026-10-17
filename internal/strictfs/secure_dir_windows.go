//go:build !linux

package strictfs

import (
	"fmt"
	"os"
	"path/filepath"
)

// SecureDir provides filesystem operations confined to a specific base directory.
// On non-Linux platforms, this is a fallback that is vulnerable to TOCTOU.
type SecureDir struct {
	basePath string
}

// NewSecureDir creates a new SecureDir instance bound to the specified directory.
func NewSecureDir(basePath string) (*SecureDir, error) {
	cleanBase := filepath.Clean(basePath)
	info, err := os.Lstat(cleanBase)
	if err != nil {
		return nil, fmt.Errorf("stat secure dir base %s: %w", cleanBase, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("secure dir base %s is not a directory", cleanBase)
	}
	return &SecureDir{basePath: cleanBase}, nil
}

func (s *SecureDir) BasePath() string {
	return s.basePath
}

func (s *SecureDir) Close() error {
	return nil
}

func (s *SecureDir) resolveAndVerify(name string) (string, error) {
	if err := ValidateBasename(name); err != nil {
		return "", fmt.Errorf("invalid basename %q: %w", name, err)
	}
	targetPath := filepath.Join(s.basePath, name)

	evalPath, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			evalDir, dErr := filepath.EvalSymlinks(filepath.Dir(targetPath))
			if dErr != nil {
				return "", fmt.Errorf("eval parent symlinks: %w", dErr)
			}
			if evalDir != s.basePath {
				return "", fmt.Errorf("parent directory escaped secure dir: %s", evalDir)
			}
			return targetPath, nil
		}
		return "", fmt.Errorf("eval symlinks: %w", err)
	}

	if filepath.Dir(evalPath) != s.basePath {
		return "", fmt.Errorf("path escaped secure dir: %s", evalPath)
	}

	return targetPath, nil
}

func (s *SecureDir) ReadFile(name string) ([]byte, error) {
	target, err := s.resolveAndVerify(name)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(target)
}

func (s *SecureDir) WriteFileAtomic(name string, data []byte, perm os.FileMode) error {
	target, err := s.resolveAndVerify(name)
	if err != nil {
		return err
	}
	return StrictWriteAtomic(target, data, perm)
}

func (s *SecureDir) Remove(name string) error {
	target, err := s.resolveAndVerify(name)
	if err != nil {
		return err
	}
	return StrictUnlink(target)
}

func (s *SecureDir) ComputeDigest(name string) (string, error) {
	target, err := s.resolveAndVerify(name)
	if err != nil {
		return "", err
	}
	return ComputeFileDigest(target)
}

func (s *SecureDir) RemoveAll(name string) error {
	target, err := s.resolveAndVerify(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(target)
}

func (s *SecureDir) Rename(oldName, newName string) error {
	oldTarget, err := s.resolveAndVerify(oldName)
	if err != nil {
		return err
	}
	newTarget, err := s.resolveAndVerify(newName)
	if err != nil {
		return err
	}
	return os.Rename(oldTarget, newTarget)
}
