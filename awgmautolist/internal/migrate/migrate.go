// Package migrate moves the satellite's flat /opt/etc files into its own
// configuration directory. Before this layout the satellite kept its files
// next to each other in /opt/etc (awgmautolist.json, -state.json,
// -history.json); now they live together in one directory (AWGM_AUTOLIST_DIR,
// by default /opt/etc/awgmautolist) as config.json, state.json, history.json.
// The move happens once, at satellite start, before anything reads the new
// paths; a failed move keeps the old files in place and is retried on the
// next start.
package migrate

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DefaultDir returns the satellite's configuration directory:
// AWGM_AUTOLIST_DIR when set to a non-empty value, otherwise
// /opt/etc/awgmautolist. The init script resolves the same variable, so a
// relocation set in the conf moves intent, key and config together.
func DefaultDir() string {
	if v := strings.TrimSpace(os.Getenv("AWGM_AUTOLIST_DIR")); v != "" {
		return v
	}
	return "/opt/etc/awgmautolist"
}

// Flat holds the historical flat-layout paths. LegacyFlat returns the ones
// installed under /opt/etc before the directory layout; tests pass their own.
type Flat struct {
	Config  string
	State   string
	History string
}

// LegacyFlat returns the flat paths of the pre-directory layout.
func LegacyFlat() Flat {
	return Flat{
		Config:  "/opt/etc/awgmautolist.json",
		State:   "/opt/etc/awgmautolist-state.json",
		History: "/opt/etc/awgmautolist-history.json",
	}
}

// FromFlatFiles moves the legacy files into dir as config.json, state.json
// and history.json. A missing legacy file is skipped silently; a legacy file
// whose new counterpart already exists is left in place on purpose — it is
// no longer read by anything, and deleting user files on a heuristic is not
// worth the risk of destroying data a half-finished earlier migration left
// behind. The returned error joins every per-file failure; files that moved
// stay moved.
func FromFlatFiles(dir string, legacy Flat, logf func(format string, args ...any)) (int, error) {
	pairs := []struct{ old, new string }{
		{legacy.Config, filepath.Join(dir, "config.json")},
		{legacy.State, filepath.Join(dir, "state.json")},
		{legacy.History, filepath.Join(dir, "history.json")},
	}
	return movePairs(dir, pairs, logf)
}

// ShellFiles holds the historical flat paths of the shell-sourced files.
// LegacyShell returns the ones installed
// under /opt/etc before the directory layout; tests pass their own.
type ShellFiles struct {
	Key     string
	Enabled string
	Conf    string
}

// LegacyShell returns the flat shell-file paths of the pre-directory layout.
func LegacyShell() ShellFiles {
	return ShellFiles{
		Key:     "/opt/etc/awgmautolist.key",
		Enabled: "/opt/etc/awgmautolist.enabled",
		Conf:    "/opt/etc/awgmautolist.conf",
	}
}

// MoveShellFiles performs the one-time move of the legacy shell-sourced files
// into dir as key, enabled and awgmautolist.conf. The same rules as FromFlatFiles apply: the
// move happens only while the new file is absent (a fresh key/enabled always
// wins and is never overwritten or read from the old path), permissions are
// preserved, a failed move leaves the source in place and is reported. The
// init script runs the equivalent migration before sourcing so the daemon
// environment keeps working on the first start after an upgrade; this
// Go-side counterpart keeps standalone starts on the same single-directory
// layout.
func MoveShellFiles(dir string, legacy ShellFiles, logf func(format string, args ...any)) (int, error) {
	pairs := []struct{ old, new string }{
		{legacy.Key, filepath.Join(dir, "key")},
		{legacy.Enabled, filepath.Join(dir, "enabled")},
		{legacy.Conf, filepath.Join(dir, "awgmautolist.conf")},
	}
	return movePairs(dir, pairs, logf)
}

// movePairs moves old->new pairs into dir under the shared migration rules
// (see FromFlatFiles).
func movePairs(dir string, pairs []struct{ old, new string }, logf func(format string, args ...any)) (int, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	moved := 0
	var errs []error
	for _, p := range pairs {
		if p.old == "" {
			continue
		}
		info, err := os.Stat(p.old)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if info.IsDir() {
			continue // not a satellite file; never touch directories
		}
		switch _, err := os.Stat(p.new); {
		case err == nil:
			continue // already migrated (or a fresh install) — keep the old file
		case !errors.Is(err, os.ErrNotExist):
			errs = append(errs, err)
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := moveFile(p.old, p.new, info.Mode().Perm()); err != nil {
			errs = append(errs, err)
			continue
		}
		moved++
		logf("moved %s -> %s", p.old, p.new)
	}
	return moved, errors.Join(errs...)
}

// moveFile renames old to new; across filesystems (EXDEV and other rename
// failures) it falls back to copy+remove, preserving the mode. The copy lands
// in the destination directory first so the final rename is atomic on the
// same filesystem as new.
func moveFile(old, new string, perm os.FileMode) error {
	if err := os.Rename(old, new); err == nil {
		return nil
	}
	src, err := os.Open(old)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(new), ".migrate-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, new); err != nil {
		return err
	}
	return os.Remove(old)
}
