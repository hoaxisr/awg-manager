package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrPathDenied = errors.New("path not allowed")

// Root describes one allowed directory tree for the file manager.
type Root struct {
	Path     string
	ReadOnly bool
	Label    string
}

// DefaultRoots — Entware paths safe for remote editing from AWG Manager UI.
var DefaultRoots = []Root{
	{Path: "/opt/etc/awg-manager", Label: "AWG Manager"},
	{Path: "/opt/etc", Label: "Entware /opt/etc"},
	{Path: "/opt/var/log", Label: "Logs"},
	{Path: "/opt/bin", Label: "Binaries", ReadOnly: true},
	{Path: "/opt/var", Label: "Entware /opt/var"},
	{Path: "/opt", Label: "Entware /opt"},
	{Path: "/tmp", Label: "Temp"},
}

// Sandbox resolves and validates paths against configured roots.
type Sandbox struct {
	roots []Root
}

func NewSandbox(roots []Root) *Sandbox {
	if len(roots) == 0 {
		roots = DefaultRoots
	}
	normalized := make([]Root, 0, len(roots))
	for _, r := range roots {
		p := filepath.Clean(r.Path)
		if ep, err := filepath.EvalSymlinks(p); err == nil {
			p = ep
		}
		normalized = append(normalized, Root{Path: p, ReadOnly: r.ReadOnly, Label: r.Label})
	}
	return &Sandbox{roots: normalized}
}

func (s *Sandbox) Roots() []Root { return append([]Root(nil), s.roots...) }

// Resolve returns the absolute path if it lies inside an allowed root.
// Both the raw (cleaned) path and the symlink-resolved path must fall inside
// at least one root — otherwise a symlink created inside a writable root can
// be used to escape the sandbox.
func (s *Sandbox) Resolve(requested string) (abs string, root Root, err error) {
	if strings.TrimSpace(requested) == "" {
		if len(s.roots) == 0 {
			return "", Root{}, ErrPathDenied
		}
		root = s.roots[0]
		return root.Path, root, nil
	}
	clean := filepath.Clean(requested)
	if !filepath.IsAbs(clean) {
		return "", Root{}, fmt.Errorf("path must be absolute")
	}

	// Evaluate symlinks if the file exists. When the path is missing
	// (or EvalSymlinks errors for any reason) we fall back to `clean` and
	// the AND-check below reduces to a single-form comparison.
	resolved := clean
	if eval, err := filepath.EvalSymlinks(clean); err == nil {
		resolved = eval
	}

	for _, r := range s.roots {
		if pathWithin(resolved, r.Path) && pathWithin(clean, r.Path) {
			return resolved, r, nil
		}
	}
	return "", Root{}, ErrPathDenied
}

// ResolveWrite is Resolve plus read-only root check for mutating ops.
func (s *Sandbox) ResolveWrite(requested string) (abs string, err error) {
	abs, root, err := s.Resolve(requested)
	if err != nil {
		return "", err
	}
	if root.ReadOnly {
		return "", fmt.Errorf("read-only root: %s", root.Path)
	}
	return abs, nil
}

func pathWithin(path, root string) bool {
	if root == "/" {
		return filepath.IsAbs(path)
	}
	if path == root {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(path, root+sep)
}
