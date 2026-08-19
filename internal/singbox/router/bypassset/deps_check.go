// Package bypassset owns the AWGM-BYPASS ipset: creation, population and the
// availability checks for the userspace tooling it needs (ipset binary,
// conntrack). Traffic whose destination IP is listed in the set bypasses
// sing-box entirely (RETURN → WAN).
package bypassset

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// ipsetBinaryPaths lists candidate absolute paths for the ipset binary,
// searched in order. Entware installs to /opt/sbin; system may have
// /usr/sbin or /sbin.
var ipsetBinaryPaths = []string{
	"/opt/sbin/ipset",
	"/usr/sbin/ipset",
	"/sbin/ipset",
}

// healthLog — журнал приложения для вердиктов health-check
// бинаря ipset. nil-safe: до/без подключения через SetHealthLogger вердикты
// просто не журналируются (следующий скан после подключения зажурналирует
// актуальный сбой заново — Warn пишется на каждый скан, не однократно).
var healthLog *logging.ScopedLogger

// SetHealthLogger подключает журнал для health-check ipset. Вызывается один
// раз из wiring; пишется под тем же мьютексом, под которым publishIPSetScan
// журналирует вердикты, — безопасно и при уже работающих фоновых вызовах.
func SetHealthLogger(l *logging.ScopedLogger) {
	ipsetHealth.mu.Lock()
	defer ipsetHealth.mu.Unlock()
	healthLog = l
}

// ipsetProbeTimeout ограничивает пробу `ipset version`. Сломанный бинарь
// (exit 127) падает мгновенно, здоровый отвечает миллисекунды; таймаут —
// только чтобы фоновая горутина скана не жила вечно на зависшем носителе
// /opt. Сам таймаут вердикта «битый» НЕ выносит (см. classifyIPSetProbe):
// на нагруженном MIPS-роутере (идущая пересборка, cold start) форк может
// легально не уложиться и в куда большие лимиты — см. ipsetCtlTimeout.
const ipsetProbeTimeout = 5 * time.Second

const (
	// ipsetHealthyTTL — как долго доверять найденному рабочему бинарю,
	// прежде чем перепроверить (opkg upgrade может сломать его на лету).
	ipsetHealthyTTL = 5 * time.Minute
	// ipsetBrokenTTL — как долго кэшировать вердикт «рабочего бинаря нет»:
	// короче, чтобы ручная починка (переустановка пакета по SSH) подхватилась
	// без рестарта демона.
	ipsetBrokenTTL = 30 * time.Second
)

// classifyIPSetProbe выносит вердикт по результату запуска `<path> version`.
// «Битый» — ТОЛЬКО exec-класс сбоев: fork/exec-ошибка (EACCES, ENOEXEC…)
// либо exit 126/127 (так завершается ld.so при битой установке Entware:
// «error while loading shared libraries: libc.so»). Всё остальное — НЕ
// вердикт о бинаре и трактуется как «исполняется»:
//   - таймаут: загруженный роутер (проба ходит тем же медленным путём, что
//     и обычные команды ipset — см. ipsetCtlTimeout про «дефолтные 30 с
//     тесны»); ложный «битый» здесь ронял бы идущую пересборку;
//   - ненулевой exit ≠126/127: ipset ≥7 на `version` делает netlink-запрос
//     протокола к ядру и выходит с кодом 1, если ip_set ещё не загружен
//     (ранний бут) — бинарь при этом полностью рабочий.
func classifyIPSetProbe(res *sysexec.Result, err error) (broken bool, detail string) {
	if err == nil {
		return false, ""
	}
	if errors.Is(err, sysexec.ErrTimeout) {
		return false, ""
	}
	if res != nil && (res.ExitCode == 126 || res.ExitCode == 127) {
		return true, sysexec.FormatError(res, err).Error()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, ""
	}
	// Процесс не стартовал вовсе — бинарь не исполняется.
	return true, err.Error()
}

// probeIPSetBinary запускает `<path> version` и классифицирует исход.
// Подменяется в тестах.
var probeIPSetBinary = func(path string) (broken bool, detail string) {
	res, err := sysexec.RunWithOptions(context.Background(), path, []string{"version"},
		sysexec.Options{Timeout: ipsetProbeTimeout})
	return classifyIPSetProbe(res, err)
}

// ipsetHealth кэширует вердикт поиска рабочего бинаря ipset.
var ipsetHealth = struct {
	mu      sync.Mutex
	path    string    // проверенный рабочий путь ("" — нет)
	checked time.Time // момент вердикта (zero — кэш пуст)
	probing bool      // скан уже идёт (второй не запускаем)
	broken  bool      // в последнем скане встречен существующий, но неисполнимый кандидат
	// state — исход последнего скана по каждому кандидату ("ok"/"broken"/
	// "absent"): переходы broken→ok и broken→absent журналируются Info.
	state map[string]string
}{state: map[string]string{}}

// ipsetCandidateResult — исход проверки одного кандидата в скане.
type ipsetCandidateResult struct {
	path   string
	status string // "ok" | "broken" | "absent"
	detail string
}

// scanIPSetCandidates проверяет кандидатов по порядку и останавливается на
// первом рабочем. Выполняется ВНЕ мьютекса кэша: stat и форк на зависшем
// /opt (USB-носитель) не должны блокировать остальных потребителей ipset.
func scanIPSetCandidates() (chosen string, results []ipsetCandidateResult) {
	for _, p := range ipsetBinaryPaths {
		if _, err := os.Stat(p); err != nil {
			results = append(results, ipsetCandidateResult{path: p, status: "absent"})
			continue
		}
		if broken, detail := probeIPSetBinary(p); broken {
			results = append(results, ipsetCandidateResult{path: p, status: "broken", detail: detail})
			continue
		}
		results = append(results, ipsetCandidateResult{path: p, status: "ok"})
		chosen = p
		break
	}
	return chosen, results
}

// publishIPSetScan фиксирует вердикт скана и журналирует его. Вызывается под
// ipsetHealth.mu. Warn о битом кандидате пишется на КАЖДЫЙ скан — коалесцер
// журнала складывает идентичные повторы в одну запись с ×N и держит её
// живой, пока сбой актуален (одноразовый Warn выпадал бы из кольцевого
// буфера за часы, и хронический сбой снова становился бы невидимым).
func publishIPSetScan(chosen string, results []ipsetCandidateResult, now time.Time) {
	broken := false
	for _, r := range results {
		prev := ipsetHealth.state[r.path]
		switch r.status {
		case "broken":
			broken = true
			healthLog.Warn("ipset-health", r.path,
				fmt.Sprintf("ipset binary is present but not executable (%s) — candidate skipped; reinstall it: opkg --force-reinstall install ipset", r.detail))
		case "ok":
			if prev == "broken" {
				healthLog.Info("ipset-health", r.path, "ipset binary is runnable again")
			}
		case "absent":
			if prev == "broken" {
				healthLog.Info("ipset-health", r.path, "broken ipset binary is no longer present")
			}
		}
		ipsetHealth.state[r.path] = r.status
	}
	ipsetHealth.path = chosen
	ipsetHealth.broken = broken
	ipsetHealth.checked = now
	ipsetHealth.probing = false
}

// IPSetBinary returns the path to a WORKING ipset binary, or "" when no
// candidate both exists and executes. Существующий, но неисполнимый бинарь
// (битый Entware-пакет, exit 127 от ld.so) пропускается с Warn в журнал —
// иначе он затеняет рабочий системный и валит каждую команду.
//
// Вердикт кэшируется; протухший освежается ФОНОВОЙ горутиной (stale-while-
// revalidate): вызывающие — status-хендлеры, чанки пересборки, reconcile —
// получают прежний вердикт мгновенно и никогда не платят за пробу. Синхронно
// сканирует только самый первый вызов (на буте ensureBypassSetExists
// должен видеть реальность, а не пустой плейсхолдер); конкуренты первого
// вызова до публикации вердикта получают "" — бут-последовательность
// однопоточна, а фоновые потребители самовосстанавливаются на своих тиках.
func IPSetBinary() string {
	now := time.Now()
	ipsetHealth.mu.Lock()
	ttl := ipsetBrokenTTL
	if ipsetHealth.path != "" {
		ttl = ipsetHealthyTTL
	}
	if !ipsetHealth.checked.IsZero() && now.Sub(ipsetHealth.checked) < ttl {
		p := ipsetHealth.path
		ipsetHealth.mu.Unlock()
		return p
	}
	if ipsetHealth.probing {
		p := ipsetHealth.path
		ipsetHealth.mu.Unlock()
		return p
	}
	ipsetHealth.probing = true
	first := ipsetHealth.checked.IsZero()
	ipsetHealth.mu.Unlock()

	if first {
		chosen, results := scanIPSetCandidates()
		ipsetHealth.mu.Lock()
		publishIPSetScan(chosen, results, now)
		p := ipsetHealth.path
		ipsetHealth.mu.Unlock()
		return p
	}

	go func() {
		chosen, results := scanIPSetCandidates()
		ipsetHealth.mu.Lock()
		publishIPSetScan(chosen, results, time.Now())
		ipsetHealth.mu.Unlock()
	}()

	ipsetHealth.mu.Lock()
	p := ipsetHealth.path
	ipsetHealth.mu.Unlock()
	return p
}

// IsIPSetAvailable reports whether a working ipset binary is present on the
// router (existence AND executability — see IPSetBinary).
func IsIPSetAvailable() bool {
	return IPSetBinary() != ""
}

// RecheckIPSet сбрасывает кэш health-check'а: следующий вызов IPSetBinary
// синхронно перепроверит кандидатов. Используется install-флоу, чтобы
// решение «уже установлено?» принималось по текущему состоянию бинаря, а не
// по вердикту пятиминутной давности (бинарь мог сломаться или исчезнуть).
func RecheckIPSet() { resetIPSetCache() }

// xtSetModuleName is the kernel module name for iptables ipset matching.
const xtSetModuleName = "xt_set"

// isModuleLoaded checks /proc/modules for the given module name.
// Identical to the helper in iptables.go; duplicated to keep the
// bypassset package self-contained and avoid an internal import cycle.
func isModuleLoaded(name string) bool {
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return false
	}
	prefix := name + " "
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// EnsureXtSetModule attempts to load xt_set.ko via insmod. Soft-fail by
// design: if the module is built into the kernel there is no .ko file but
// it still works; a hard error here would be a false positive. The caller
// (builder) logs the warning and continues — the real failure surfaces at
// iptables-restore COMMIT time with a concrete message.
func EnsureXtSetModule(ctx context.Context) error {
	if isModuleLoaded(xtSetModuleName) {
		return nil
	}
	kernel := osdetect.KernelRelease()
	if kernel == "" {
		return nil // soft-fail: can't determine kernel version
	}
	path := filepath.Join("/lib/modules", kernel, xtSetModuleName+".ko")
	if _, err := os.Stat(path); err != nil {
		return nil // soft-fail: .ko absent → assume built-in or unavailable
	}
	_, err := sysexec.Run(ctx, "insmod", path)
	return err
}

// InstallIPSet runs `opkg install ipset` and streams output lines to
// progressFn (called once per line, nil = silent). Returns the first
// non-zero exit error, or nil on success.
func InstallIPSet(ctx context.Context, progressFn func(line string)) error {
	opkg, err := findOpkg()
	if err != nil {
		return err
	}
	args := []string{"install", "ipset"}
	ipsetHealth.mu.Lock()
	brokenDetected := ipsetHealth.broken
	ipsetHealth.mu.Unlock()
	if brokenDetected {
		// Пакет установлен, но бинарь не исполняется (битая установка
		// Entware) — голый `opkg install` для установленного пакета no-op,
		// лечит только переустановка.
		args = []string{"--force-reinstall", "install", "ipset"}
	}
	// We use RunWithOptions with a long timeout — opkg downloads packages
	// and can take 30-60s on slow WAN.
	res, err := sysexec.RunWithOptions(ctx, opkg, args,
		sysexec.Options{Timeout: 120e9}) // 120s
	if res != nil && progressFn != nil {
		combined := res.Stdout
		if res.Stderr != "" {
			combined += res.Stderr
		}
		for _, line := range strings.Split(combined, "\n") {
			if l := strings.TrimSpace(line); l != "" {
				progressFn(l)
			}
		}
	}
	if err != nil {
		return sysexec.FormatError(res, err)
	}
	// Invalidate the cached result so subsequent IsIPSetAvailable calls
	// reflect the newly installed binary.
	resetIPSetCache()

	// Best-effort: also install conntrack so a routing change takes effect
	// immediately (existing flows get evicted) instead of waiting for old
	// connections to expire. Failure here is non-fatal — the bypass guard
	// works without it, just with delayed effect on established flows.
	if !IsConntrackAvailable() {
		_ = InstallConntrackTools(ctx, progressFn)
	}
	return nil
}

// conntrackBinaryPaths lists candidate absolute paths for the conntrack
// binary (from the conntrack-tools package), searched in preference order.
var conntrackBinaryPaths = []string{
	"/opt/sbin/conntrack",
	"/usr/sbin/conntrack",
	"/sbin/conntrack",
}

// ConntrackBinary returns the path to the conntrack binary, or "" when not
// found. The conntrack package is optional — without it the bypass guard
// still applies to NEW connections, it just can't evict already-tracked ones
// (changes take effect only once the old flow expires).
func ConntrackBinary() string {
	for _, p := range conntrackBinaryPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// IsConntrackAvailable reports whether the conntrack binary is present.
func IsConntrackAvailable() bool { return ConntrackBinary() != "" }

// InstallConntrackTools installs the conntrack userspace binary via opkg.
// Keenetic Entware ships it as package "conntrack"; some feeds use
// "conntrack-tools" instead — we try both.
func InstallConntrackTools(ctx context.Context, progressFn func(line string)) error {
	opkg, err := findOpkg()
	if err != nil {
		return err
	}
	for _, pkg := range []string{"conntrack", "conntrack-tools"} {
		if err := opkgInstall(ctx, opkg, pkg, progressFn); err == nil {
			return nil
		}
	}
	return fmt.Errorf("opkg install conntrack: package not found in feed")
}

func opkgInstall(ctx context.Context, opkg, pkg string, progressFn func(line string)) error {
	res, err := sysexec.RunWithOptions(ctx, opkg, []string{"install", pkg},
		sysexec.Options{Timeout: 120e9})
	if res != nil && progressFn != nil {
		combined := res.Stdout
		if res.Stderr != "" {
			combined += res.Stderr
		}
		for _, line := range strings.Split(combined, "\n") {
			if l := strings.TrimSpace(line); l != "" {
				progressFn(l)
			}
		}
	}
	if err != nil {
		return sysexec.FormatError(res, err)
	}
	return nil
}

// resetIPSetCache сбрасывает кэш health-check'а — следующий IPSetBinary
// перепробует кандидатов немедленно (синхронно), а не по TTL. state
// сознательно не чистится: восстановление после переустановки журналируется
// переходом broken→ok («runnable again»). probing не трогаем — идущий скан
// корректно опубликует свой результат.
func resetIPSetCache() {
	ipsetHealth.mu.Lock()
	defer ipsetHealth.mu.Unlock()
	ipsetHealth.path = ""
	ipsetHealth.checked = time.Time{}
}

// findOpkg returns the absolute path to opkg, or an error if not found.
func findOpkg() (string, error) {
	for _, p := range []string{"/opt/bin/opkg", "/usr/bin/opkg", "/bin/opkg"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", ErrOpkgNotFound
}
