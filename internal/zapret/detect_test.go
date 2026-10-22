package zapret

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupInstall собирает правдоподобное дерево zapret2 в t.TempDir() и
// перенаправляет на него пакетные пути.
func setupInstall(t *testing.T, opts ...func(base string)) string {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "zapret2")

	for _, d := range []string{"ipset", "nfq2", filepath.Join("init.d", "sysv")} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := `NFQWS2_ENABLE=1
NFQWS2_OPT="
--filter-tcp=80 --lua-desync=multisplit --new
--filter-tcp=443 --lua-desync=fake:blob=fake_default_tls
"
QNUM=200
`
	if err := os.WriteFile(filepath.Join(base, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	// Демон.
	bin := filepath.Join(base, "nfq2", "nfqws2")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Init-скрипт.
	init := filepath.Join(base, "init.d", "sysv", "zapret2")
	if err := os.WriteFile(init, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Пользовательский список.
	if err := os.WriteFile(filepath.Join(base, "ipset", "zapret-hosts-user.txt"),
		[]byte("youtube.com\nrt.com\n# комментарий\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, opt := range opts {
		opt(base)
	}

	prevOpt, prevInit := optRoot, initDirs
	t.Cleanup(func() { optRoot, initDirs = prevOpt, prevInit })

	// Каталог установки теперь находится поиском по optRoot, поэтому
	// тесту достаточно подсунуть корень с одним «zapret» внутри.
	optRoot = filepath.Join(root, "opt")
	if err := os.MkdirAll(optRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(base, filepath.Join(optRoot, "zapret2")); err != nil {
		t.Fatal(err)
	}
	base = filepath.Join(optRoot, "zapret2")
	initDirs = []string{filepath.Join(base, "init.d", "sysv")}
	return base
}

func TestResolveLayoutFindsZapret2(t *testing.T) {
	base := setupInstall(t)
	l, err := (&Service{}).resolveLayout()
	if err != nil {
		t.Fatal(err)
	}
	if l.optVar != optVarV2 {
		t.Errorf("переменная опций: %q, ожидалась %q", l.optVar, optVarV2)
	}
	if l.binary == "" {
		t.Error("бинарь демона не найден")
	}
	if l.init == "" {
		t.Error("init-скрипт не найден")
	}
	if filepath.Base(l.ipset) != "ipset" {
		t.Errorf("каталог списков: %q", l.ipset)
	}
	_ = base
}

func TestDiscoverLayoutsIgnoresDirWithoutConfig(t *testing.T) {
	// Каталог без config — не zapret: нечего показывать и нечего править.
	root := t.TempDir()
	bare := filepath.Join(root, "zapret2")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	prev := optRoot
	t.Cleanup(func() { optRoot = prev })
	optRoot = root

	if len(discoverLayouts()) != 0 {
		t.Error("каталог без config принят за установленный zapret")
	}
}

func TestDiscoverLayoutsRejectsUnknownOptVar(t *testing.T) {
	// config есть, но ни NFQWS_OPT, ни NFQWS2_OPT — формат не наш.
	// Лучше показать «не установлен», чем предложить редактировать то,
	// о чём мы ничего не знаем.
	root := t.TempDir()
	base := filepath.Join(root, "zapret2")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "config"), []byte("SOMETHING_ELSE=\"--x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := optRoot
	t.Cleanup(func() { optRoot = prev })
	optRoot = root

	if len(discoverLayouts()) != 0 {
		t.Error("конфиг с чужой переменной принят за zapret")
	}
}

func TestParseZapretPackagesMatchesKeyword(t *testing.T) {
	// Реальный вывод `opkg list-installed`. Пакет zapret в репозитории
	// Entware отсутствует, поэтому в списке всегда будет локальный .ipk
	// с именем, которое мы угадать не можем — отсюда поиск по слову.
	out := strings.Join([]string{
		"base-files - 1484-k3.4-1",
		"busybox - 1.36.1-1",
		"curl - 8.11.1-1",
		"zapret22 - 2.0.1-p86.16",
		"zapret-gui-keenetic - 1.4.2-1",
		"iptables - 1.8.9-1",
	}, "\n")

	found := parseZapretPackages(out)
	if len(found) != 2 {
		t.Fatalf("найдено %d пакетов, ожидалось 2: %v", len(found), found)
	}
	if found["zapret22"] != "2.0.1-p86.16" {
		t.Errorf("версия zapret22: %q", found["zapret22"])
	}
	if _, ok := found["curl"]; ok {
		t.Error("посторонний пакет принят за zapret")
	}
}

func TestParseZapretPackagesEmptyOutput(t *testing.T) {
	if got := parseZapretPackages(""); len(got) != 0 {
		t.Errorf("пустой вывод дал %v", got)
	}
	// Мусорные строки без «имя - версия» не должны ронять разбор.
	if got := parseZapretPackages("Collected errors:\n * unknown opkg\n"); len(got) != 0 {
		t.Errorf("мусорные строки дали %v", got)
	}
}

func TestServicePackagesTTLasymmetric(t *testing.T) {
	now := time.Now()
	probe := func(svc *Service) map[string]string {
		svc.installedPackages()
		svc.probes.Wait()
		return svc.installedPackages()
	}

	// Наличие меняется редко, а list-installed стоит около секунды на MIPS:
	// через 11 минут пакет ещё в кэше.
	var present int
	svc := &Service{
		now: func() time.Time { return now },
		opkgListInstalled: func(context.Context) (string, error) {
			present++
			return "zapret22 - 2.0.1\n", nil
		},
	}
	if got := probe(svc); got["zapret22"] != "2.0.1" {
		t.Fatalf("пакет не найден: %v", got)
	}
	now = now.Add(packagesTTLAbsent + time.Minute)
	probe(svc)
	if present != 1 {
		t.Errorf("opkg вызван %d раз, ожидался 1 (кэш при наличии)", present)
	}

	// Отсутствие переспрашиваем через 10 минут, но не раньше.
	var absentCalls int
	absent := &Service{
		now: func() time.Time { return now },
		opkgListInstalled: func(context.Context) (string, error) {
			absentCalls++
			return "", nil
		},
	}
	probe(absent)
	probe(absent)
	if absentCalls != 1 {
		t.Errorf("opkg вызван %d раз до истечения, ожидался 1", absentCalls)
	}
	now = now.Add(packagesTTLAbsent + time.Minute)
	probe(absent)
	if absentCalls != 2 {
		t.Errorf("opkg вызван %d раз, ожидался 2 (отсутствие переспрашиваем)", absentCalls)
	}
}

func TestServiceRecheckInvalidatesCaches(t *testing.T) {
	setupInstall(t)
	var calls int
	svc := &Service{
		now: func() time.Time { return time.Now() },
		opkgListInstalled: func(context.Context) (string, error) {
			calls++
			return "zapret22 - 2.0.1\n", nil
		},
		runVersion: func(context.Context, string) (string, error) { return "2.1.0", nil },
	}
	svc.GetStatus()
	svc.probes.Wait()
	before := calls
	svc.Recheck()
	svc.probes.Wait()
	if calls == before {
		t.Error("Recheck не сбросил кэш пакетов — после управления демоном пакет не перечитается")
	}
}

func TestParseVersionOutput(t *testing.T) {
	cases := map[string]string{
		"github version 2.1.0 (abc1234) lua_compat_ver 3": "2.1.0",
		// Дату в self-built выводе версией считать нельзя: номер дня
		// без точки — не версия.
		"self-built version Sep  9 2026 12:00:00 lua_compat_ver 2": "",
		"v1.71.4":         "1.71.4",
		"no version here": "",
	}
	for out, want := range cases {
		if got := parseVersionOutput(out); got != want {
			t.Errorf("parseVersionOutput(%q) = %q, ожидалось %q", out, got, want)
		}
	}
}

func TestBinaryFingerprintChangesWithFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nfqws2")
	if err := os.WriteFile(p, []byte("a"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := binaryFingerprint(p)
	if first == "" {
		t.Fatal("отпечаток пуст")
	}
	// Новый размер → новый отпечаток → кэш версии сбрасывается.
	if err := os.WriteFile(p, []byte("aaaa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if second := binaryFingerprint(p); second == first {
		t.Error("отпечаток не изменился после замены бинаря")
	}
	if got := binaryFingerprint(filepath.Join(dir, "нет")); got != "" {
		t.Errorf("несуществующий файл дал отпечаток %q", got)
	}
}

func TestServiceStatusNotInstalled(t *testing.T) {
	root := t.TempDir()
	prev := optRoot
	t.Cleanup(func() { optRoot = prev })
	optRoot = root

	s := NewService(t.TempDir(), nil)
	st := s.GetStatus()
	if st.Installed {
		t.Error("zapret отмечен установленным при пустой файловой системе")
	}
	if st.ProcessState != StateNotInstalled {
		t.Errorf("processState: %q", st.ProcessState)
	}
}

func TestGetHostlistsClassifiesEntries(t *testing.T) {
	base := setupInstall(t)
	l, err := (&Service{}).resolveLayout()
	if err != nil {
		t.Fatal(err)
	}
	// Список, где только адреса: доменов 0, IP 2. UI обязан на этом
	// предупредить — демон в MODE_FILTER=hostlist их игнорирует.
	if err := os.WriteFile(filepath.Join(base, "ipset", "zapret-hosts-user-exclude.txt"),
		[]byte("127.0.0.0/8\n10.0.0.0/8\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	infos := listHostlists(l)
	if len(infos) == 0 {
		t.Fatal("списки не отданы")
	}
	byID := map[HostlistID]HostlistInfo{}
	for _, h := range infos {
		byID[h.ID] = h
	}
	user := byID[HostlistUser]
	if !user.Exists || user.Entries != 2 {
		t.Errorf("пользовательский список: exists=%v entries=%d (в файле комментарий и пустая строка)", user.Exists, user.Entries)
	}
	if !user.Editable {
		t.Error("пользовательский список помечен как нередактируемый")
	}
	excl := byID[HostlistExclude]
	if excl.Entries != 2 || excl.IPs != 2 || excl.Domains != 0 {
		t.Errorf("исключения разобраны неверно: %+v", excl)
	}
	if len(excl.Warnings) != 1 || excl.Warnings[0].Code != "hostlist_ip_only" {
		t.Errorf("неверное предупреждение о списке из одних IP: %+v", excl.Warnings)
	}
	if main := byID[HostlistMain]; main.Editable {
		t.Error("генерируемый список помечен как редактируемый")
	}
}

func TestGetHostlistsReturnsCodedCountWarning(t *testing.T) {
	base := setupInstall(t)
	path := filepath.Join(base, "ipset", "zapret-hosts-user.txt")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := (&Service{}).resolveLayout()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range listHostlists(l) {
		if info.ID != HostlistUser {
			continue
		}
		if len(info.Warnings) != 1 || info.Warnings[0].Code != "hostlist_count_failed" || info.Warnings[0].Detail == "" {
			t.Fatalf("unexpected count warning: %+v", info.Warnings)
		}
		return
	}
	t.Fatal("user hostlist not found")
}

func TestHostlistRoundTripPreservesMode(t *testing.T) {
	setupInstall(t)
	l, err := (&Service{}).resolveLayout()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.ipset, "zapret-hosts-user.txt")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Service{dataDir: t.TempDir()}).writeHostlist(l, HostlistUser, "example.com", Revision(path, string(before))); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Перевод строки в конце обязателен: без него zapret не увидит
	// последнюю запись.
	if string(data) != "example.com\n" {
		t.Errorf("содержимое: %q", data)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("права изменены: %o", st.Mode().Perm())
	}
}

func TestWriteHostlistRejectsGenerated(t *testing.T) {
	setupInstall(t)
	l, err := (&Service{}).resolveLayout()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Service{dataDir: t.TempDir()}).writeHostlist(l, HostlistMain, "example.com", missingRevision(filepath.Join(l.ipset, "zapret-hosts.txt"))); err == nil {
		t.Error("запись в генерируемый список должна отклоняться")
	}
}

func TestWriteHostlistRejectsNUL(t *testing.T) {
	setupInstall(t)
	l, err := (&Service{}).resolveLayout()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Service{dataDir: t.TempDir()}).writeHostlist(l, HostlistUser, "ok.example\x00bad", userListRevision(t, l)); err == nil {
		t.Error("NUL-байт должен отклоняться")
	}
}

func userListRevision(t *testing.T, l layout) string {
	t.Helper()
	path := filepath.Join(l.ipset, "zapret-hosts-user.txt")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return Revision(path, string(body))
}
