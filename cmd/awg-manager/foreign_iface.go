package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/hoaxisr/awg-manager/internal/api"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
)

// foreignIfaces — отметка «Сторонний интерфейс» (issue #935, spec.md §2–4).
type foreignIfaces struct {
	settings *storage.SettingsStore
	pool     *opkgtun.Pool
	// ndmsNames — системные имена ядра, известные NDMS. Ошибка — отказ
	// (500), а не «неизвестен»: иначе отметили бы интерфейс роутера.
	ndmsNames func(ctx context.Context) (map[string]bool, error)
	// boundBy — имена ядра, к которым привязан direct-выход роутера. Снятие
	// отметки с такого имени отказывается: следующий Enable роутера вырезал
	// бы выход как автоуправляемый (stripAutoManagedDirect) и записал на диск.
	boundBy func(ctx context.Context) (map[string]bool, error)
	orphans func(ctx context.Context) ([]external.OrphanIface, error)
	sysNet  string
}

func rejectForeign(format string, a ...any) error {
	return fmt.Errorf("%w: %s", api.ErrForeignIfaceRejected, fmt.Sprintf(format, a...))
}

// canonicalForeign — opkgtunN в каноническом написании; остальное как есть.
func canonicalForeign(name string) (string, int, bool) {
	if idx, ok := opkgtun.IndexOf(name); ok {
		return fmt.Sprintf("opkgtun%d", idx), idx, true
	}
	return name, 0, false
}

// isPanelIfaceName — имена, которые создают панель и прошивка: awgm*, nwg*,
// t2s*, proxy* и opkgtun*, не разобранный opkgtun.IndexOf (разобранный идёт
// через пул). Короче списка router.IsAutoManagedIface намеренно: wg*/awg* — имена
// userland-программ (wireguard-go, amneziawg-go), их обязаны отмечать —
// отметка и спасает такой direct-выход от stripAutoManagedDirect.
func isPanelIfaceName(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"opkgtun", "awgm", "nwg", "t2s", "proxy"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// isFirmwareServiceIface — ezcfg*: служебный TUN прошивки (F514).
func isFirmwareServiceIface(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "ezcfg")
}

// Mark — отказы по границе (spec §4): имя; номер OpkgTun с ключевым
// держателем; имя панели (isPanelIfaceName — сюда же tun sing-box: режимы
// роутера живут на opkgtun*); служебный ezcfg*; существующий не-TUN;
// интерфейс ядра, известный NDMS.
func (f *foreignIfaces) Mark(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return rejectForeign("пустое имя")
	case len(name) > 15:
		return rejectForeign("имя интерфейса ядра длиннее 15 символов")
	case name == "." || name == "..",
		strings.ContainsAny(name, "/:"),
		strings.ContainsFunc(name, unicode.IsSpace):
		// Правила ядра (dev_valid_name) плюс «:» — разделитель алиасов.
		return rejectForeign("недопустимое имя интерфейса ядра: пробелы, «/», «:», «.» и «..» запрещены")
	}
	if canon, idx, ok := canonicalForeign(name); ok {
		err := f.pool.ClaimIfFree(ctx, opkgtun.ForeignHolder(canon), idx, func() error {
			return f.settings.MarkForeignInterface(canon)
		})
		if errors.Is(err, opkgtun.ErrClaimed) || errors.Is(err, opkgtun.ErrOutOfRange) {
			return rejectForeign("%v", err)
		}
		return err
	}
	if isPanelIfaceName(name) {
		return rejectForeign("%s — имя интерфейсов панели", name)
	}
	if isFirmwareServiceIface(name) {
		return rejectForeign("%s — служебный интерфейс прошивки", name)
	}
	// Существующий интерфейс ядра без tun_flags — не TUN userland-программы
	// (lo, dummy0, tunl0, порты и радио роутера). Отсутствующий отмечается:
	// программа поднимет его позже.
	if _, err := os.Stat(filepath.Join(f.sysNet, name)); err == nil {
		if _, err := os.Stat(filepath.Join(f.sysNet, name, "tun_flags")); err != nil {
			return rejectForeign("%s — не TUN-интерфейс программы", name)
		}
	}
	known, err := f.ndmsNames(ctx)
	if err != nil {
		return fmt.Errorf("интерфейсы NDMS: %w", err)
	}
	if known[name] {
		return rejectForeign("%s — интерфейс роутера, он и так доступен как выход", name)
	}
	return f.settings.MarkForeignInterface(name)
}

func (f *foreignIfaces) Unmark(ctx context.Context, name string) error {
	canon, _, _ := canonicalForeign(strings.TrimSpace(name))
	bound, err := f.boundBy(ctx)
	if err != nil {
		return fmt.Errorf("выходы sing-box: %w", err)
	}
	if bound[canon] {
		return rejectForeign("%s привязан выходом sing-box — сначала удалите или перепривяжите выход", canon)
	}
	return f.settings.UnmarkForeignInterface(canon)
}

// Candidates — сироты пула OpkgTun и живые TUN-интерфейсы ядра, не известные
// NDMS и не наши; уже отмеченные не предлагаются. orphanIfaces отдаёт кэш с
// TTL 15 с — кандидат может отставать на это время, это приемлемо.
func (f *foreignIfaces) Candidates(ctx context.Context) ([]api.ForeignIfaceCandidate, error) {
	marked := f.settings.GetForeignInterfaces()
	out := []api.ForeignIfaceCandidate{}
	orphans, err := f.orphans(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range orphans {
		if slices.Contains(marked, o.Iface) {
			continue
		}
		label := o.Description
		if label == "" {
			label = o.Iface
		}
		out = append(out, api.ForeignIfaceCandidate{Name: o.Iface, Label: label, Kind: "opkgtun", Up: o.KernelDevice})
	}
	known, err := f.ndmsNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("интерфейсы NDMS: %w", err)
	}
	entries, err := os.ReadDir(f.sysNet)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		n := e.Name()
		if known[n] || isPanelIfaceName(n) || isFirmwareServiceIface(n) || slices.Contains(marked, n) {
			continue
		}
		if _, err := os.Stat(filepath.Join(f.sysNet, n, "tun_flags")); err != nil {
			continue // не TUN: userland-программы поднимают именно TUN
		}
		out = append(out, api.ForeignIfaceCandidate{Name: n, Label: n, Kind: "kernel", Up: sysCarrier(f.sysNet, n)})
	}
	return out, nil
}

// sysCarrier — есть ли несущая у интерфейса ядра (файл carrier = "1").
func sysCarrier(sysNet, name string) bool {
	b, err := os.ReadFile(filepath.Join(sysNet, name, "carrier"))
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// ndmsSystemNames — имена ядра всех интерфейсов, известных NDMS. Берутся из
// ListAll: `interface-name` в списке — эхо id или метка (стенд 5.02.A.11:
// Bridge0 → "Home"), настоящее имя (br0) даёт только резолвер, а ListAll
// разрешает его пакетом и кэширует.
func ndmsSystemNames(store *ndmsquery.InterfaceStore) func(context.Context) (map[string]bool, error) {
	return func(ctx context.Context) (map[string]bool, error) {
		all, err := store.ListAll(ctx)
		if err != nil {
			return nil, err
		}
		out := make(map[string]bool, len(all))
		for _, i := range all {
			out[i.Name] = true
		}
		return out, nil
	}
}
