package zapret

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	osexec "os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

func defaultOpkgListInstalled(ctx context.Context) (string, error) {
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

// zapretPackageKeyword — по которому ищем пакет. Пользователь ставит
// zapret22 / zapret2 / zapret / zapret-gui-keenetic; в репозитории
// Entware такого пакета нет вовсе (проверено по Packages.gz), поэтому
// сравнение по точному имени здесь бесполезно — ищем по подстроке.
const zapretPackageKeyword = "zapret"

// installedLineRe разбирает строку «имя - версия» из opkg list-installed.
var installedLineRe = regexp.MustCompile(`^(\S+)\s+-\s+(\S+)`)

// packagesCache — последний ответ opkg и последняя неудачная проба.
// Защищается Service.mu. found хранит пары имя→версия целиком: версия идёт
// в карточку установки.
type packagesCache struct {
	found    map[string]string
	at       time.Time
	err      string
	errAt    time.Time
	inFlight bool
}

// due сообщает, пора ли снова спрашивать opkg. TTL несимметричный:
// появление пакета хотим увидеть быстро (10 минут), а наличие меняется
// редко (сутки) — та же асимметрия, что в singbox/router/bypassset для
// ipset. Ошибка повторяется через packagesRetryTTL и никогда не
// кэшируется как «пакета нет».
func (c *packagesCache) due(now time.Time) bool {
	if c.inFlight {
		return false
	}
	ttl := packagesTTLAbsent
	if len(c.found) > 0 {
		ttl = packagesTTLPresent
	}
	if !c.at.IsZero() && now.Sub(c.at) < ttl {
		return false
	}
	return c.errAt.IsZero() || now.Sub(c.errAt) >= packagesRetryTTL
}

// expire заставляет следующее чтение статуса спросить opkg заново.
func (c *packagesCache) expire() { c.at, c.errAt = time.Time{}, time.Time{} }

// installedPackages отдаёт кэш zapret-пакетов и, если он устарел, запускает
// opkg в фоне: статус его не ждёт (на MIPS это секунды), а свежий ответ
// приходит инвалидацией routing.zapretStatus.
func (s *Service) installedPackages() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.packages.due(s.currentTime()) {
		s.packages.inFlight = true
		s.probes.Add(1)
		go s.probePackages()
	}
	return maps.Clone(s.packages.found)
}

func (s *Service) probePackages() {
	defer s.probes.Done()
	out, err := s.opkgListInstalled(context.Background())
	s.mu.Lock()
	c := &s.packages
	c.inFlight = false
	var changed bool
	if err != nil {
		msg := fmt.Sprintf("opkg list-installed: %v", err)
		changed = c.err != msg
		c.err, c.errAt = msg, s.currentTime()
	} else {
		found := parseZapretPackages(out)
		changed = c.err != "" || !maps.Equal(found, c.found)
		*c = packagesCache{found: found, at: s.currentTime()}
	}
	pub := s.pub
	s.mu.Unlock()
	if changed {
		events.PublishInvalidatedTo(pub, events.ResourceRoutingZapretStatus, "zapret-packages")
	}
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
