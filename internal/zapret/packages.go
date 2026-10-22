package zapret

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

func defaultOpkgListInstalled(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, opkgTimeout)
	defer cancel()
	result, err := sysexec.RunWithOptions(ctx, "/opt/bin/opkg", []string{"list-installed"}, sysexec.Options{Timeout: opkgTimeout})
	if err != nil {
		// A missing opkg binary means the router has no Entware package
		// manager. Timeouts and command failures remain errors so they are
		// never cached as package absence.
		if errors.Is(err, osexec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", sysexec.FormatError(result, err)
	}
	return result.Stdout, nil
}

// zapretPackageKeyword — по которему ищем пакет. Пользователь ставит
// zapret22 / zapret2 / zapret / zapret-gui-keenetic; в репозитории
// Entware такого пакета нет вовсе (проверено по Packages.gz), поэтому
// сравнение по точному имени здесь бесполезно — ищем по подстроке.
const zapretPackageKeyword = "zapret"

// installedLineRe разбирает строку «имя - версия» из opkg list-installed.
var installedLineRe = regexp.MustCompile(`^(\S+)\s+-\s+(\S+)`)

// listInstalledPackages отдаёт пары (имя, версия) zapret-пакетов.
//
// Кэш: TTL 24 часа при наличии, 10 минут при отсутствии. Промах по
// отрицательному ответу стоит пользователю 10 минут ожидания перед
// появлением установленного пакета; промах по положительному —
// сутки устаревания версии, что для anti-DPI некритично.
func (s *Service) listInstalledPackages(ctx context.Context) map[string]string {
	s.mu.Lock()
	ttl := packagesTTLAbsent
	if len(s.packagesCached) > 0 {
		ttl = packagesTTLPresent
	}
	now := s.currentTime()
	if !s.packagesAt.IsZero() && now.Sub(s.packagesAt) < ttl {
		cached := s.cachedPackagesLocked()
		s.mu.Unlock()
		return cached
	}
	// A failed command has a short retry delay, separate from the absence
	// cache, so a broken opkg cannot turn into a cached "not installed".
	if !s.packagesErrorAt.IsZero() && now.Sub(s.packagesErrorAt) < 30*time.Second {
		cached := s.cachedPackagesLocked()
		s.mu.Unlock()
		return cached
	}
	// Keep the previous snapshot while one caller probes opkg. The command
	// runs without mu so other status and mutation requests can proceed.
	if s.probeInFlight {
		cached := s.cachedPackagesLocked()
		s.mu.Unlock()
		return cached
	}
	s.probeInFlight = true
	s.mu.Unlock()

	out, err := s.opkgListInstalled(ctx)
	s.mu.Lock()
	s.probeInFlight = false
	if err != nil {
		s.lastError = fmt.Sprintf("opkg list-installed: %v", err)
		s.packagesErrorAt = s.currentTime()
		cached := s.cachedPackagesLocked()
		s.mu.Unlock()
		return cached
	}

	found := parseZapretPackages(out)
	s.packagesCached = found
	s.packagesAt = s.currentTime()
	s.packagesErrorAt = time.Time{}
	s.lastError = ""
	s.mu.Unlock()
	return found
}

func (s *Service) cachedPackagesLocked() map[string]string {
	out := make(map[string]string, len(s.packagesCached))
	for k, v := range s.packagesCached {
		out[k] = v
	}
	return out
}

// parseZapretPackages отбирает из вывода opkg всё, что похоже на zapret.
// Ключевое слово — «zapret» в имени, регистронезависимо и по подстроке:
// покрывает zapret22, zapret2, zapret, zapret-gui-keenetic и любые
// будущие имена с этим словом.
func parseZapretPackages(out string) map[string]string {
	found := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		m := installedLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) != 3 {
			continue
		}
		name, version := m[1], m[2]
		if !strings.Contains(strings.ToLower(name), zapretPackageKeyword) {
			continue
		}
		found[name] = version
	}
	return found
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
