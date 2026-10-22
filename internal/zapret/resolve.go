package zapret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type layoutResolution struct {
	layout       layout
	candidates   []layout
	activeBase   string
	selectedPIDs []int
	anyOwned     bool
}

// resolveLayout returns the one installation that is safe to operate on.
// The live process is authoritative. When stopped, a persisted last-running
// base is preferred; modification time is never used as a selection signal.
func (s *Service) resolveLayout() (layout, error) {
	r, err := s.resolveLayoutState()
	return r.layout, err
}

func (s *Service) resolveLayoutState() (layoutResolution, error) {
	r := layoutResolution{candidates: discoverLayouts()}
	// Без установок выбирать не из чего, и /proc не сканируем: на роутерах
	// без Zapret это единственный путь статуса, он обязан быть дешёвым.
	if len(r.candidates) == 0 {
		return r, ErrNotInstalled
	}
	byBase := make(map[string]layout, len(r.candidates))
	for _, candidate := range r.candidates {
		byBase[canonicalBase(candidate.base)] = candidate
	}

	owners := make(map[string][]int)
	var unknown []int
	for _, proc := range nfqProcessesHook() {
		if proc.exe == "" {
			// The process name matched nfqws/tpws, but /proc did not let us
			// identify its executable. Do not fall back to a stopped copy.
			unknown = append(unknown, proc.pid)
			r.anyOwned = true
			continue
		}
		base := baseOfExe(proc.exe)
		if base == "" {
			// Ignore known NFQWS binaries outside /opt unless their path is
			// Zapret-shaped. An unclassifiable process under /opt is a conflict.
			if strings.Contains(strings.ToLower(proc.exe), "zapret") || pathWithin(optRoot, proc.exe) {
				unknown = append(unknown, proc.pid)
				r.anyOwned = true
			}
			continue
		}
		base = canonicalBase(base)
		if _, ok := byBase[base]; !ok {
			unknown = append(unknown, proc.pid)
			r.anyOwned = true
			continue
		}
		owners[base] = append(owners[base], proc.pid)
		r.anyOwned = true
	}

	if len(unknown) > 0 {
		return r, fmt.Errorf("%w: could not map PID(s) %v to a valid installation", ErrConflict, unknown)
	}
	if len(owners) > 1 {
		bases := sortedOwnerBases(owners)
		return r, fmt.Errorf("%w: daemons from multiple installations are running: %s", ErrConflict, strings.Join(bases, ", "))
	}
	if len(owners) == 1 {
		for base, pids := range owners {
			r.layout = byBase[base]
			r.activeBase = r.layout.base
			r.selectedPIDs = append([]int(nil), pids...)
			if err := s.rememberRunningBase(base); err != nil {
				return r, fmt.Errorf("%w: could not save the last running installation: %v", ErrConflict, err)
			}
			return r, nil
		}
	}

	cached, err := s.readLastRunningBase()
	if err != nil {
		return r, fmt.Errorf("%w: could not read the last running installation: %v", ErrConflict, err)
	}
	if cached != "" {
		cached = canonicalBase(cached)
		if candidate, ok := byBase[cached]; ok {
			r.layout = candidate
			return r, nil
		}
		// A remembered installation disappearing is a state change that can
		// invalidate a pending operation. Do not silently redirect mutations
		// to the only remaining stopped copy; wait until a live process
		// identifies the new owner and replaces the cache.
		return r, fmt.Errorf("%w: saved installation %s is missing; found %d other installations", ErrConflict, cached, len(r.candidates))
	}
	if len(r.candidates) == 1 {
		r.layout = r.candidates[0]
		return r, nil
	}
	return r, fmt.Errorf("%w: Zapret is stopped; found %d installations and no last running installation", ErrConflict, len(r.candidates))
}

func sortedOwnerBases(owners map[string][]int) []string {
	bases := make([]string, 0, len(owners))
	for base := range owners {
		bases = append(bases, base)
	}
	sort.Strings(bases)
	return bases
}

func (s *Service) readLastRunningBase() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastRunningLoaded {
		return s.lastRunningBase, nil
	}
	if s.dataDir == "" {
		s.lastRunningLoaded = true
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(s.dataDir, "zapret-last-running"))
	if errors.Is(err, os.ErrNotExist) {
		s.lastRunningLoaded = true
		return "", nil
	}
	if err != nil {
		return "", err
	}
	base := strings.TrimSpace(string(data))
	if base == "" || !filepath.IsAbs(base) || filepath.Clean(base) != base {
		return "", fmt.Errorf("invalid cached base %q", base)
	}
	s.lastRunningBase = base
	s.lastRunningLoaded = true
	return base, nil
}

func (s *Service) rememberRunningBase(base string) error {
	if _, err := s.readLastRunningBase(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastRunningLoaded && s.lastRunningBase == base {
		return nil
	}
	if s.dataDir != "" {
		if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(s.dataDir, "zapret-last-running"), []byte(base+"\n"), 0o600); err != nil {
			return err
		}
	}
	s.lastRunningBase = base
	s.lastRunningLoaded = true
	return nil
}
