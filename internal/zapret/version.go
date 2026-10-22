package zapret

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

func defaultRunVersion(ctx context.Context, binary string) (string, error) {
	var lastErr error
	for _, args := range versionArgs {
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		ctxRun, cancel := context.WithTimeout(ctx, versionProbeTimeout)
		result, err := sysexec.RunWithOptions(ctxRun, binary, args, sysexec.Options{Timeout: versionProbeTimeout})
		cancel()
		if err != nil {
			lastErr = sysexec.FormatError(result, err)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if v := parseVersionOutput(result.Stdout + "\n" + result.Stderr); v != "" {
			return v, nil
		}
		lastErr = fmt.Errorf("%s %s returned no parseable version", binary, strings.Join(args, " "))
	}
	if lastErr == nil {
		lastErr = errors.New("version probe did not run")
	}
	return "", fmt.Errorf("could not read zapret version: %w", lastErr)
}

// versionRe требует хотя бы одну точку в числе: у self-built сборок в
// выводе первым кандидатом иначе оказывается «Sep  9 2026» — дата, а не
// версия. Настоящие версии zapret — 1.71.4, 2.0.1-p86.16, 2.1.0.
var versionRe = regexp.MustCompile(`(?i)\bv?(\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.\-]+)?)\b`)

// parseVersionOutput достаёт версию из вывода демона. nfqws2 печатает
// строку вида «github version 2.1.0 (abc1234) lua_compat_ver 3», но
// сборки без ZAPRET_GH_VER дают «self-built version <дата> <время>»,
// поэтому берём первое число с плавающей точкой и не привязываемся к
// формату.
func parseVersionOutput(out string) string {
	m := versionRe.FindStringSubmatch(out)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

// binaryFingerprint — ключ кэша версии. Смена размера или mtime бинаря
// означает новую установку/обновление, и кэш надо сбросить. Формат
// взят из hydraroute.getVersionCachedLocked: версии у нас меняются
// только при замене файла.
func binaryFingerprint(path string) string {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return ""
	}
	return fmt.Sprintf("%s|%s|%s|%d", filepath.Clean(path),
		st.ModTime().UTC().Format(time.RFC3339Nano), st.Mode().String(), st.Size())
}

// versionCached returns the cached daemon version and probes outside mu.
func (s *Service) getVersionCached(binary string) string {
	finger := binaryFingerprint(binary)
	if finger == "" {
		return ""
	}

	s.mu.Lock()
	if s.versionFinger != "" && s.versionFinger != finger {
		s.versionCached, s.versionAt, s.versionFinger = "", time.Time{}, ""
		s.versionError, s.versionErrorAt, s.versionErrorFinger = "", time.Time{}, ""
	}
	now := s.currentTime()
	if s.versionFinger == finger && !s.versionAt.IsZero() && now.Sub(s.versionAt) < versionTTL {
		version := s.versionCached
		s.mu.Unlock()
		return version
	}
	if s.versionErrorFinger == finger && !s.versionErrorAt.IsZero() && now.Sub(s.versionErrorAt) >= 0 && now.Sub(s.versionErrorAt) < versionRetryTTL {
		version := s.versionCached
		s.mu.Unlock()
		return version
	}
	if s.versionProbeInFlight {
		version := s.versionCached
		s.mu.Unlock()
		return version
	}
	s.versionProbeInFlight = true
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), versionProbeTimeout)
	v, err := s.runVersion(ctx, binary)
	cancel()
	if err == nil && strings.TrimSpace(v) == "" {
		err = errors.New("version probe returned no version")
	}

	s.mu.Lock()
	s.versionProbeInFlight = false
	if err != nil {
		s.versionError = fmt.Sprintf("zapret version: %v", err)
		s.versionErrorAt = s.currentTime()
		s.versionErrorFinger = finger
		version := s.versionCached
		s.mu.Unlock()
		return version
	}
	v = strings.TrimSpace(v)
	s.versionCached, s.versionAt, s.versionFinger = v, s.currentTime(), finger
	s.versionError, s.versionErrorAt, s.versionErrorFinger = "", time.Time{}, ""
	s.mu.Unlock()
	return v
}
