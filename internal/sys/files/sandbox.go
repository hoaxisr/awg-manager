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
	// Path — путь в том виде, в каком его задали и в каком он показывается
	// пользователю (/opt/etc). На Keenetic /opt сам является симлинком на
	// /tmp/mnt/<диск>/install, поэтому решение о допуске принимается не по
	// нему, а по resolved.
	Path     string
	ReadOnly bool
	Label    string
	// resolved — Path с раскрытыми симлинками. Заполняется NewSandbox.
	resolved string
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
		normalized = append(normalized, Root{
			Path:     p,
			ReadOnly: r.ReadOnly,
			Label:    r.Label,
			resolved: evalDeepest(p),
		})
	}
	return &Sandbox{roots: normalized}
}

func (s *Sandbox) Roots() []Root { return append([]Root(nil), s.roots...) }

// Resolve returns the real (symlink-resolved) path if it lies inside an
// allowed root. Решение принимается ТОЛЬКО по разрешённому пути и по
// разрешённой форме корня: симлинк, созданный внутри writable-корня и
// смотрящий наружу, отбивается, а корень, который сам является симлинком
// (/opt на Keenetic), остаётся доступным — включая ещё не существующие
// файлы внутри него.
func (s *Sandbox) Resolve(requested string) (abs string, root Root, err error) {
	if strings.TrimSpace(requested) == "" {
		if len(s.roots) == 0 {
			return "", Root{}, ErrPathDenied
		}
		root = s.roots[0]
		return root.resolved, root, nil
	}
	clean := filepath.Clean(requested)
	if !filepath.IsAbs(clean) {
		return "", Root{}, fmt.Errorf("path must be absolute")
	}

	resolved := evalDeepest(clean)
	for _, r := range s.roots {
		if pathWithin(resolved, r.resolved) {
			return resolved, r, nil
		}
	}
	return "", Root{}, ErrPathDenied
}

// evalDeepest раскрывает симлинки настолько, насколько путь существует:
// EvalSymlinks на несуществующем файле возвращает ошибку, поэтому спускаемся
// к ближайшему существующему предку и приклеиваем хвост обратно. Без этого
// создать новый файл внутри корня-симлинка было бы нельзя, а проверка на
// несуществующем пути обходилась бы симлинком в его родителе.
func evalDeepest(p string) string {
	if ep, err := filepath.EvalSymlinks(p); err == nil {
		return ep
	}
	dir := filepath.Dir(p)
	if dir == p {
		return p
	}
	return filepath.Join(evalDeepest(dir), filepath.Base(p))
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
