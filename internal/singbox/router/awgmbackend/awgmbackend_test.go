package awgmbackend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAvailableRejectsForeignModel(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "MODEL"), "KN-1812\n")

	b := New(dir, func() string { return "KN-1810" })
	ok, why := b.Available()
	if ok {
		t.Fatal("бандл собран под KN-1812, модель KN-1810 — должно быть недоступно")
	}
	if !strings.Contains(why, "KN-1812") || !strings.Contains(why, "KN-1810") {
		t.Fatalf("причина должна называть обе модели, получили: %s", why)
	}
}

func TestAvailableWithoutBundle(t *testing.T) {
	b := New(t.TempDir(), func() string { return "KN-1812" })
	ok, why := b.Available()
	if ok {
		t.Fatal("пустой каталог — бандла нет, доступности быть не должно")
	}
	if !strings.Contains(why, "не установлен") {
		t.Fatalf("причина должна говорить, что бандл не установлен, получили: %s", why)
	}
}

func TestAvailableNamesMissingModule(t *testing.T) {
	b := newTestBundle(t)
	if err := os.Remove(filepath.Join(b.dir, "modules", "xt_AWGMTPROXY.ko")); err != nil {
		t.Fatal(err)
	}

	ok, why := b.Available()
	if ok {
		t.Fatal("без xt_AWGMTPROXY.ko бандл неполон — доступности быть не должно")
	}
	if !strings.Contains(why, "xt_AWGMTPROXY.ko") {
		t.Fatalf("причина должна называть недостающий модуль, получили: %s", why)
	}
}

func TestAvailableRequiresBinaryAndModules(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "MODEL"), "KN-1812\n")

	b := New(dir, func() string { return "KN-1812" })
	if ok, _ := b.Available(); ok {
		t.Fatal("без бинаря и модулей доступности быть не должно")
	}

	mustWrite(t, filepath.Join(dir, "sbin", "iptables"), "#!/bin/sh\n")
	mustWrite(t, filepath.Join(dir, "modules", "iptable_awgm.ko"), "\x7fELF")
	mustWrite(t, filepath.Join(dir, "modules", "xt_AWGMTPROXY.ko"), "\x7fELF")
	mustWrite(t, filepath.Join(dir, "modules", "xt_AWGMPPE.ko"), "\x7fELF")

	if ok, why := b.Available(); !ok {
		t.Fatalf("полный бандл должен быть доступен, получили: %s", why)
	}
}

// Модель роутера приходит из кешированной информации NDMS и на старте демона
// бывает ещё пустой. Захваченная один раз, она держала бы бэкенд недоступным
// до перезапуска демона; ленивый резолв самоисцеляется, как только NDMS ответит.
func TestAvailableResolvesModelLazily(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "MODEL"), "KN-1812\n")
	mustWrite(t, filepath.Join(dir, "sbin", "iptables"), "#!/bin/sh\n")
	for _, m := range requiredModules {
		mustWrite(t, filepath.Join(dir, "modules", m), "\x7fELF")
	}
	model := ""
	b := New(dir, func() string { return model })

	ok, why := b.Available()
	if ok {
		t.Fatal("пока модель не известна, доступности быть не должно")
	}
	if !strings.Contains(why, "модель роутера ещё не известна") {
		t.Fatalf("причина должна объяснять, что модель ещё не известна, получили: %s", why)
	}

	model = "KN-1812"
	if ok, why := b.Available(); !ok {
		t.Fatalf("ответ NDMS обязан сделать бэкенд доступным без перезапуска демона: %s", why)
	}
}

func TestLoadIsIdempotent(t *testing.T) {
	b := newTestBundle(t)
	already := map[string]bool{"iptable_awgm": true}
	var inserted []string

	b.loaded = func(name string) bool { return already[name] }
	b.insmod = func(_ context.Context, path string) error {
		inserted = append(inserted, filepath.Base(path))
		already[strings.TrimSuffix(filepath.Base(path), ".ko")] = true
		return nil
	}

	if err := b.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range inserted {
		if name == "iptable_awgm.ko" {
			t.Fatal("уже загруженный модуль не должен грузиться повторно")
		}
	}

	inserted = nil
	if err := b.Load(context.Background()); err != nil {
		t.Fatalf("повторный Load: %v", err)
	}
	if len(inserted) != 0 {
		t.Fatalf("второй Load должен быть no-op, вставлено: %v", inserted)
	}
}

func TestLoadPartialFailureIsTotalFailure(t *testing.T) {
	b := newTestBundle(t)
	// Стаб отражает реальность /proc/modules: удачный insmod поднимает модуль,
	// неудачный — нет.
	already := map[string]bool{}

	b.loaded = func(name string) bool { return already[name] }
	b.insmod = func(_ context.Context, path string) error {
		if strings.Contains(path, "xt_AWGMPPE") {
			return errors.New("Unknown symbol nf_conntrack_in")
		}
		already[strings.TrimSuffix(filepath.Base(path), ".ko")] = true
		return nil
	}

	err := b.Load(context.Background())
	if err == nil {
		t.Fatal("частичная загрузка обязана быть отказом целиком")
	}
	if !strings.Contains(err.Error(), "xt_AWGMPPE") {
		t.Fatalf("ошибка должна называть модуль, получили: %v", err)
	}
	if !strings.Contains(err.Error(), "nf_conntrack_in") {
		t.Fatalf("ошибка должна нести причину из ядра, получили: %v", err)
	}
}

func TestLoadRejectsInsmodLyingAboutSuccess(t *testing.T) {
	// insmod вернул 0, но модуля в /proc/modules нет. Верим только ядру.
	b := newTestBundle(t)
	b.loaded = func(string) bool { return false }
	b.insmod = func(context.Context, string) error { return nil }

	err := b.Load(context.Background())
	if err == nil {
		t.Fatal("модуля нет в /proc/modules — Load обязан отказать, что бы ни вернул insmod")
	}
	if !strings.Contains(err.Error(), "iptable_awgm") {
		t.Fatalf("ошибка должна называть модуль, получили: %v", err)
	}
	if !strings.Contains(err.Error(), "/proc/modules") {
		t.Fatalf("причина должна объяснять, что модуль не появился в /proc/modules, получили: %v", err)
	}
}

// insmod настоящей причины не знает — её печатает ядро. Без неё в журнале
// остаётся бесполезное «invalid module format».
func TestLoadFailureCarriesKernelReason(t *testing.T) {
	b := newTestBundle(t)
	b.loaded = func(string) bool { return false }
	b.insmod = func(context.Context, string) error { return errors.New("invalid module format") }
	b.dmesg = func(context.Context) (string, error) {
		return strings.Join([]string{
			"[  12.000000] nf_conntrack: default automatic helper assignment",
			"[  99.111111] iptable_awgm: exports duplicate symbol nf_register_net_hooks",
			"[ 100.000000] br0: port 2 entered forwarding state",
		}, "\n"), nil
	}

	err := b.Load(context.Background())
	if err == nil {
		t.Fatal("Load обязан отказать")
	}
	if !strings.Contains(err.Error(), "duplicate symbol nf_register_net_hooks") {
		t.Fatalf("причина от ядра обязана доехать до текста ошибки, получили: %v", err)
	}
	if strings.Contains(err.Error(), "entered forwarding state") {
		t.Fatalf("посторонние строки ядра приклеивать нельзя, получили: %v", err)
	}
}

// Хвост ограничен: к ошибке идут последние релевантные строки, не весь буфер.
func TestKernelReasonIsBounded(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("[ %d.0] iptable_awgm: Unknown symbol sym%d (err 0)", i, i))
	}
	got := kernelReasonLines("iptable_awgm", strings.Join(lines, "\n"))
	if len(got) != kernelReasonMaxLines {
		t.Fatalf("ожидали %d строк, получили %d: %v", kernelReasonMaxLines, len(got), got)
	}
	if !strings.Contains(got[len(got)-1], "sym19") {
		t.Fatalf("брать надо ПОСЛЕДНИЕ строки, получили: %v", got)
	}
}

// Буфер ядра недоступен или релевантных строк нет — поведение прежнее.
func TestLoadFailureWithoutKernelReason(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dmesg func(context.Context) (string, error)
	}{
		// Оборванное чтение может отдать кусок вывода вместе с ошибкой —
		// доверять такому куску нельзя, ошибка перевешивает.
		{"недоступен", func(context.Context) (string, error) {
			return "[ 99.0] iptable_awgm: обрыв", errors.New("dmesg: not found")
		}},
		{"нет релевантных строк", func(context.Context) (string, error) {
			return "[ 100.0] br0: port 2 entered forwarding state", nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newTestBundle(t)
			b.loaded = func(string) bool { return false }
			b.insmod = func(context.Context, string) error { return errors.New("invalid module format") }
			b.dmesg = tc.dmesg

			err := b.Load(context.Background())
			if err == nil {
				t.Fatal("Load обязан отказать")
			}
			if !strings.Contains(err.Error(), "invalid module format") {
				t.Fatalf("исходная ошибка insmod обязана сохраниться, получили: %v", err)
			}
			if strings.Contains(err.Error(), "ядро:") {
				t.Fatalf("приклеивать нечего — добавки быть не должно, получили: %v", err)
			}
		})
	}
}

// На успешном пути буфер ядра не читаем.
func TestLoadDoesNotReadKernelLogOnSuccess(t *testing.T) {
	b := newTestBundle(t)
	already := map[string]bool{}
	b.loaded = func(name string) bool { return already[name] }
	b.insmod = func(_ context.Context, path string) error {
		already[strings.TrimSuffix(filepath.Base(path), ".ko")] = true
		return nil
	}
	reads := 0
	b.dmesg = func(context.Context) (string, error) {
		reads++
		return "", nil
	}

	if err := b.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reads != 0 {
		t.Fatalf("на успешном пути буфер ядра читать незачем, прочитан %d раз", reads)
	}
}

func TestWaitArgsPrependsWaitFlag(t *testing.T) {
	got := waitArgs([]string{"-t", "awgm"})
	if got[0] != "-w" {
		t.Fatalf("первым аргументом обязан идти -w, получили: %v", got)
	}
}

func newTestBundle(t *testing.T) *Backend {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "MODEL"), "KN-1812\n")
	mustWrite(t, filepath.Join(dir, "sbin", "iptables"), "#!/bin/sh\n")
	for _, m := range requiredModules {
		mustWrite(t, filepath.Join(dir, "modules", m), "\x7fELF")
	}
	b := New(dir, func() string { return "KN-1812" })
	b.dmesg = nil // тесты не читают буфер ядра машины, где идут
	return b
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
