package bypassset

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	// SetName is the ipset name used for the AWGM bypass filter.
	SetName = "AWGM-BYPASS"

	// SetMaxElem is the maximum number of entries in the ipset.
	// hash:net on Keenetic kernels supports up to ~1M entries; 262144
	// is a safe ceiling that covers all realistic rule-set sizes without
	// consuming excessive kernel memory.
	SetMaxElem = 262144

	// ipsetCtlTimeout — явный таймаут одиночных управляющих команд ipset
	// (create/flush/swap/destroy/list -t). Дефолтные 30 с sysexec тесны для
	// наборов с maxelem=262144 на нагруженном MIPS-роутере; сами команды
	// конечны, поэтому щедрый потолок безопасен.
	ipsetCtlTimeout = 120 * time.Second
)

// runIpsetCtl запускает управляющую команду ipset с ipsetCtlTimeout вместо
// дефолтного таймаута sysexec.
func runIpsetCtl(ctx context.Context, bin string, args ...string) (*sysexec.Result, error) {
	return sysexec.RunWithOptions(ctx, bin, args, sysexec.Options{Timeout: ipsetCtlTimeout})
}

// ipsetBin returns the path to ipset, or an error if not available.
func ipsetBin() (string, error) {
	p := IPSetBinary()
	if p == "" {
		return "", ErrIPSetNotAvailable
	}
	return p, nil
}

// CreateSet creates the AWGM-BYPASS ipset (hash:net) if it does not
// already exist. Idempotent — "set with the same name already exists" is
// silently ignored.
func CreateSet(ctx context.Context) error {
	bin, err := ipsetBin()
	if err != nil {
		return err
	}
	res, err := runIpsetCtl(ctx, bin,
		"create", SetName, "hash:net",
		"maxelem", fmt.Sprintf("%d", SetMaxElem),
		"family", "inet",
	)
	if err != nil {
		// "set with the same name already exists" → idempotent success
		combined := ""
		if res != nil {
			combined = res.Stdout + res.Stderr
		}
		if strings.Contains(combined, "already exists") {
			return nil
		}
		return sysexec.FormatError(res, fmt.Errorf("ipset create: %w", err))
	}
	return nil
}

// DestroySet removes the AWGM-BYPASS ipset. Idempotent — "set does not
// exist" is silently ignored (set was never created or already cleaned up).
func DestroySet(ctx context.Context) error {
	return DestroyNamedSet(ctx, SetName)
}

// DestroyNamedSet removes an arbitrary AWGM-owned ipset (live or staging),
// with the same idempotent "does not exist" handling as DestroySet.
func DestroyNamedSet(ctx context.Context, name string) error {
	bin, err := ipsetBin()
	if err != nil {
		return err
	}
	res, err := runIpsetCtl(ctx, bin, "destroy", name)
	if err != nil {
		combined := ""
		if res != nil {
			combined = res.Stdout + res.Stderr
		}
		if strings.Contains(combined, "does not exist") || strings.Contains(combined, "not found") {
			return nil
		}
		return sysexec.FormatError(res, fmt.Errorf("ipset destroy %s: %w", name, err))
	}
	return nil
}

// SetExists reports whether the AWGM-BYPASS ipset currently exists in
// the kernel. Uses `ipset list -name` which is fast (no entry output), но
// идёт тем же медленным kernel-путём, что и `list -t`, — дефолтные 30 с
// sysexec на нагруженном роутере давали ложное «набора нет», поэтому
// команда выполняется через runIpsetCtl с ipsetCtlTimeout.
func SetExists(ctx context.Context) bool {
	bin, err := ipsetBin()
	if err != nil {
		return false
	}
	res, err := runIpsetCtl(ctx, bin, "list", "-name")
	if err != nil || res == nil {
		return false
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(line) == SetName {
			return true
		}
	}
	return false
}

// EntryCount returns the number of entries in the AWGM-BYPASS ipset,
// or 0 if the set does not exist or the count cannot be determined.
func EntryCount(ctx context.Context) int {
	n, _ := EntryCountChecked(ctx)
	return n
}

// EntryCountChecked — как EntryCount, но различает подтверждённый ноль и
// «счётчик получить не удалось» (команда ipset не выполнилась, ctx мёртв,
// вывод не разобран): ok=false во втором случае. Нужен вызывающим, для
// которых 0 и «неизвестно» — разные исходы: итоговое сообщение пересборки
// после успешного swap не должно выдавать сбой счётчика за «ipset пустой —
// весь трафик в WAN».
//
// Сначала пробует поле "Number of entries" из `ipset list -t` (protocol 7,
// дёшево — только header). Ядра Keenetic работают на kernel protocol 6, где
// это поле НЕ печатается ни в терсе, ни в полном list; там счётчик снимается
// подсчётом `add`-строк из `ipset save` (дороже — дампит набор, но статус
// запрашивают редко, не в горячем пути).
func EntryCountChecked(ctx context.Context) (n int, ok bool) {
	bin, err := ipsetBin()
	if err != nil {
		return 0, false
	}
	res, err := runIpsetCtl(ctx, bin, "list", SetName, "-t")
	if err != nil || res == nil {
		return 0, false // ошибка команды / несуществующий набор
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		k, v, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found || strings.TrimSpace(k) != "Number of entries" {
			continue
		}
		parsed, perr := strconv.Atoi(strings.TrimSpace(v))
		if perr != nil {
			return 0, false
		}
		return parsed, true
	}
	// Набор существует (list -t не ошибся), но поле счётчика отсутствует —
	// protocol 6. Считаем члены через save.
	return countViaSave(ctx, bin)
}

// countViaSave считает записи набора как число `add`-строк в `ipset save`.
// Fallback для kernel protocol 6, где `list -t` не печатает счётчик.
func countViaSave(ctx context.Context, bin string) (n int, ok bool) {
	res, err := runIpsetCtl(ctx, bin, "save", SetName)
	if err != nil || res == nil {
		return 0, false
	}
	count := 0
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "add ") {
			count++
		}
	}
	return count, true
}

// NormalizeEntry canonicalises a CIDR or bare IPv4 address for ipset.
// Returns "" for anything that is not a valid IPv4 address or CIDR.
// IPv6 is not supported — sing-box TProxy on Keenetic is IPv4-only.
func NormalizeEntry(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Try CIDR first.
	if _, ipnet, err := net.ParseCIDR(raw); err == nil {
		if ipnet.IP.To4() == nil {
			return "" // IPv6 — skip
		}
		return ipnet.String() // canonical form (e.g. "10.0.0.0/8")
	}
	// Try bare IP.
	if ip := net.ParseIP(raw); ip != nil {
		if ip.To4() == nil {
			return "" // IPv6 — skip
		}
		return ip.To4().String() + "/32"
	}
	return ""
}
