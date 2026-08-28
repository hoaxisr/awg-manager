package iptables

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

const (
	Binary          = "/opt/sbin/iptables"
	RestoreBinary   = "/opt/sbin/iptables-restore"
	MaxRestoreTries = 3
	RetryBaseWait   = time.Second
)

// Run executes iptables and surfaces its stderr on failure. Голый err даёт
// только «exit status 1», по которому нельзя отличить занятый xtables-лок от
// отсутствующего модуля или отвергнутой ядром спеки — тот же довод, что уже
// записан у RestoreNoflush ниже.
func Run(ctx context.Context, args ...string) error {
	comment := usesCommentMatch(args)
	if comment {
		ensureCommentModuleFn(ctx)
	}
	full := append([]string{"-w"}, args...)
	var res *exec.Result
	var err error
	for attempt := 0; attempt < MaxRestoreTries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * RetryBaseWait)
		}
		res, err = exec.Run(ctx, Binary, full...)
		if err == nil {
			return nil
		}
		if !lockBusy(res) {
			break
		}
		// Занятый лок ретраибелен: NDM переписывает таблицы пачками (18-21 раз
		// на flap интерфейса), и `-w` версии 1.4.21 ждёт не всегда —
		// «Resource temporarily unavailable», exit 4 (стенд 2026-08-28).
	}
	if comment && !commentModuleLoaded() {
		return fmt.Errorf("%w (модуль ядра xt_comment не загружен)", exec.FormatError(res, err))
	}
	return exec.FormatError(res, err)
}

// lockBusy — отказ из-за занятого xtables-лока, а не из-за спеки правила.
func lockBusy(res *exec.Result) bool {
	if res == nil {
		return false
	}
	return strings.Contains(res.Stderr, "Resource temporarily unavailable") ||
		strings.Contains(res.Stderr, "xtables lock")
}

// xt_comment грузится лениво здесь, в единственной точке, через которую
// проходят все наши правила. Причина: на части прошивок (Keenetic/Netcraze
// OS 5.x EA, NC-1812) NDMS сам `-m comment` нигде не использует, поэтому
// модуль не подгружается на старте; если ещё и sing-box не установлен —
// его router-преflight тоже не отрабатывает, и первое же правило с нашим
// маркером ядро отвергает: «iptables: No chain/target/match by that name»
// (issue #666; тот же корень, что #130 для sb-router). Место выбрано не у
// вызывающих — их уже трое (WDTT NAT, WDTT LAN, listen-firewall), и ровно
// «забыли позвать preflight» и есть баг.
//
// Soft-fail: отсутствующий .ko означает либо вкомпилен в ядро, либо реально
// нет — вердикт в обоих случаях выносит сам iptables, а не мы.
var (
	commentMu     sync.Mutex
	commentLoaded bool
)

// ensureCommentModuleFn — точка подмены для тестов.
var ensureCommentModuleFn = ensureCommentModule

func usesCommentMatch(args []string) bool {
	for i, a := range args {
		if a == "comment" && i > 0 && args[i-1] == "-m" {
			return true
		}
	}
	return false
}

func commentModuleLoaded() bool {
	commentMu.Lock()
	defer commentMu.Unlock()
	return commentLoaded
}

func ensureCommentModule(ctx context.Context) {
	commentMu.Lock()
	defer commentMu.Unlock()
	if commentLoaded {
		return
	}
	if moduleLoaded("xt_comment") {
		commentLoaded = true
		return
	}
	kernel := osdetect.KernelRelease()
	if kernel == "" {
		return
	}
	path := filepath.Join("/lib/modules", kernel, "xt_comment.ko")
	if _, err := os.Stat(path); err != nil {
		return
	}
	if _, err := exec.Run(ctx, "insmod", path); err == nil {
		commentLoaded = true
	}
}

func moduleLoaded(name string) bool {
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, name+" ") {
			return true
		}
	}
	return false
}

// RunOutput runs iptables and returns its stdout. Used by read-only queries
// (e.g. `-S PREROUTING`) where the caller needs to inspect the rule listing,
// not just the exit code.
func RunOutput(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"-w"}, args...)
	res, err := exec.Run(ctx, Binary, full...)
	if res == nil {
		return "", err
	}
	if err != nil {
		return res.Stdout, exec.FormatError(res, err)
	}
	return res.Stdout, nil
}

func RestoreNoflush(ctx context.Context, input string) error {
	var lastErr error
	var lastResult *exec.Result
	for attempt := 0; attempt < MaxRestoreTries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * RetryBaseWait)
		}
		result, err := exec.RunWithOptions(ctx, RestoreBinary, []string{"--noflush"}, exec.Options{
			Stdin: strings.NewReader(input),
		})
		if err == nil {
			return nil
		}
		lastErr = err
		lastResult = result
	}
	// Surface stderr (e.g. `iptables-restore: line N failed`) so the
	// caller's log entry actually tells us where the kernel rejected the
	// batch. Without FormatError, we get only "exit status 1" which is
	// useless for diagnosing parse vs. commit failures.
	return fmt.Errorf("iptables-restore --noflush: %w", exec.FormatError(lastResult, lastErr))
}
