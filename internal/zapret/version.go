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

// versionCache — последняя прочитанная версия демона и последняя неудачная
// проба, каждая привязана к отпечатку бинаря. Защищается Service.mu.
type versionCache struct {
	value, finger  string
	at             time.Time
	err, errFinger string
	errAt          time.Time
	inFlight       bool
}

// expire заставляет следующее чтение статуса пробовать заново, но оставляет
// прошлое значение для показа, пока проба идёт.
func (c *versionCache) expire() {
	c.at, c.finger = time.Time{}, ""
	c.err, c.errAt, c.errFinger = "", time.Time{}, ""
}

// getVersionCached returns the cached daemon version and probes outside mu.
func (s *Service) getVersionCached(binary string) string {
	finger := binaryFingerprint(binary)
	if finger == "" {
		return ""
	}

	s.mu.Lock()
	c := &s.version
	if c.finger != "" && c.finger != finger {
		*c = versionCache{inFlight: c.inFlight}
	}
	now := s.currentTime()
	fresh := c.finger == finger && !c.at.IsZero() && now.Sub(c.at) < versionTTL
	failedRecently := c.errFinger == finger && !c.errAt.IsZero() && now.Sub(c.errAt) >= 0 && now.Sub(c.errAt) < versionRetryTTL
	if fresh || failedRecently || c.inFlight {
		version := c.value
		s.mu.Unlock()
		return version
	}
	c.inFlight = true
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), versionProbeTimeout)
	v, err := s.runVersion(ctx, binary)
	cancel()
	if err == nil && strings.TrimSpace(v) == "" {
		err = errors.New("version probe returned no version")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	c.inFlight = false
	if err != nil {
		c.err, c.errAt, c.errFinger = fmt.Sprintf("zapret version: %v", err), s.currentTime(), finger
		return c.value
	}
	v = strings.TrimSpace(v)
	*c = versionCache{value: v, at: s.currentTime(), finger: finger}
	return v
}
