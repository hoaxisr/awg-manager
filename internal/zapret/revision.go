package zapret

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Revision binds an edit to both its original installation and file contents.
func Revision(path, content string) string {
	if content == "" {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return missingRevision(path)
		}
	}
	return revisionForState(path, "present", content)
}

func missingRevision(path string) string {
	return revisionForState(path, "missing", "")
}

func revisionForState(path, state, content string) string {
	input := state + "\x00" + filepath.Clean(path) + "\x00" + content
	return fmt.Sprintf("%x", sha256.Sum256([]byte(input)))
}

// checkRevision requires the revision the client last read: an empty one is a
// conflict, so a write can never skip the precondition.
func checkRevision(path, expected string) error {
	body, err := os.ReadFile(path)
	var current string
	if errors.Is(err, os.ErrNotExist) {
		current = missingRevision(path)
	} else if err != nil {
		return err
	} else {
		current = revisionForState(path, "present", string(body))
	}
	if expected == "" || expected != current {
		return ErrConflict
	}
	return nil
}

func (s *Service) checkSelected(want layout) error {
	got, err := s.resolveLayout()
	if err != nil {
		return err
	}
	if got.base != want.base {
		return ErrConflict
	}
	return nil
}

// Refuse symlinked write targets: replacing a symlink would detach the config,
// while following it could overwrite a file outside the selected installation.
func checkWriteTarget(l layout, path string) error {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	if parent != canonicalBase(l.base) && !pathWithin(canonicalBase(l.base), parent) {
		return fmt.Errorf("write target escapes installation: %s", path)
	}
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("write target is not a regular file: %s", path)
	}
	return nil
}
