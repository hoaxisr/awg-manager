package zapret

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGetStrategiesParsesOrder(t *testing.T) {
	setupInstall(t)
	svc := NewService(t.TempDir(), nil)

	got, err := svc.GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	if got.OptVar != optVarV2 {
		t.Errorf("переменная: %q", got.OptVar)
	}
	if len(got.Items) != 2 {
		t.Fatalf("стратегий: %d, ожидалось 2", len(got.Items))
	}
	// Порядок в файле = приоритет: первый профиль выигрывает при
	// совпадении фильтров, поэтому индекс обязан совпадать с позицией.
	if !strings.Contains(got.Items[0].Raw, "filter-tcp=80") || got.Items[0].Index != 0 {
		t.Errorf("первая стратегия: %+v", got.Items[0])
	}
	if !strings.Contains(got.Items[1].Raw, "filter-tcp=443") || got.Items[1].Index != 1 {
		t.Errorf("вторая стратегия: %+v", got.Items[1])
	}
}

func TestGetStrategiesParseWarningsAreCoded(t *testing.T) {
	base := setupInstall(t)
	svc := NewService(t.TempDir(), nil)
	configPath := filepath.Join(base, "config")
	if err := os.WriteFile(configPath, []byte("NFQWS2_OPT=\"--new\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ParseWarnings) != 1 || got.ParseWarnings[0].Code != "strategies_empty" || got.ParseWarnings[0].Detail != optVarV2 {
		t.Fatalf("unexpected empty-strategy warning: %+v", got.ParseWarnings)
	}

	profiles := strings.Repeat("--filter-tcp=443 --lua-desync=fake --new\n", maxProfiles+1)
	if err := os.WriteFile(configPath, []byte("NFQWS2_OPT=\""+profiles+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = svc.GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ParseWarnings) != 1 || got.ParseWarnings[0].Code != "strategies_limit_reached" || got.ParseWarnings[0].Detail != "64" {
		t.Fatalf("unexpected strategy-limit warning: %+v", got.ParseWarnings)
	}
}

func TestReorderStrategiesWritesOrder(t *testing.T) {
	base := setupInstall(t)
	svc := NewService(t.TempDir(), nil)

	cur, err := svc.GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	// Переворачиваем порядок и сохраняем.
	if _, err := svc.ReorderStrategies(1, 0, cur.Revision); err != nil {
		t.Fatal(err)
	}

	after, err := svc.GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 2 {
		t.Fatalf("после записи %d стратегий", len(after.Items))
	}
	if !strings.Contains(after.Items[0].Raw, "filter-tcp=443") {
		t.Errorf("порядок не изменился: %+v", after.Items)
	}
	for i, it := range after.Items {
		if it.Index != i {
			t.Errorf("индекс %d = %d, индекс обязан быть позицией", i, it.Index)
		}
	}
	// Ключи вне блока опций не должны пострадать.
	data, err := os.ReadFile(filepath.Join(base, "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"NFQWS2_ENABLE=1", "QNUM=200"} {
		if !strings.Contains(string(data), key) {
			t.Errorf("ключ %s потерян", key)
		}
	}
}

func TestReorderStrategiesMovesCard(t *testing.T) {
	setupInstall(t)
	svc := NewService(t.TempDir(), nil)
	cur, err := svc.GetStrategies()
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.ReorderStrategies(0, 1, cur.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Items[0].Raw, "filter-tcp=443") {
		t.Errorf("карточка не переехала: %+v", got.Items[0])
	}

	if _, err := svc.ReorderStrategies(5, 0, got.Revision); err == nil {
		t.Error("выход за границы должен отклоняться")
	}
}

func TestInlineStrategiesSaveAndReorderPreserveBoundaries(t *testing.T) {
	base := setupInstall(t)
	config := filepath.Join(base, "config")
	original := "NFQWS2_ENABLE=1\nNFQWS2_OPT=\"--filter-tcp=80 --lua-desync=fake --new --filter-tcp=443 --lua-desync=multisplit --new --filter-udp=443 --lua-desync=fake\"\nQNUM=200\n"
	if err := os.WriteFile(config, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewService(t.TempDir(), nil)
	cur, err := svc.GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	if len(cur.Items) != 3 {
		t.Fatalf("inline config has %d profiles, want 3", len(cur.Items))
	}
	saved, err := svc.ReorderStrategies(0, 0, cur.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Items) != 3 || saved.Items[0].FilterTCP != "80" || saved.Items[1].FilterTCP != "443" || saved.Items[2].FilterUDP != "443" {
		t.Fatalf("save changed profile boundaries: %+v", saved.Items)
	}
	reordered, err := svc.ReorderStrategies(2, 0, saved.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(reordered.Items) != 3 || reordered.Items[0].FilterUDP != "443" || reordered.Items[1].FilterTCP != "80" || reordered.Items[2].FilterTCP != "443" {
		t.Fatalf("reorder changed profile boundaries: %+v", reordered.Items)
	}
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "--new") != 2 || !strings.Contains(string(raw), "NFQWS2_ENABLE=1\n") || !strings.HasSuffix(string(raw), "\nQNUM=200\n") {
		t.Fatalf("save/reorder damaged config: %s", raw)
	}
}

func TestGetStrategiesNotInstalled(t *testing.T) {
	root := t.TempDir()
	prev := optRoot
	t.Cleanup(func() { optRoot = prev })
	optRoot = root

	if _, err := NewService(t.TempDir(), nil).GetStrategies(); err != ErrNotInstalled {
		t.Errorf("ожидалась ErrNotInstalled, получено %v", err)
	}
}

func TestGetStrategiesHydratesHostlistRefs(t *testing.T) {
	base := setupInstall(t)
	// Переписываем конфиг так, чтобы профиль ссылался на реальный файл
	// списка: без этого UI показал бы «файл не найден» для существующего.
	cfg := `NFQWS2_OPT="
--filter-tcp=443 --filter-l7=tls --hostlist=` + filepath.Join(base, "ipset", "zapret-hosts-user.txt") +
		` --lua-desync=fake:blob=fake_default_tls
"
`
	if err := os.WriteFile(filepath.Join(base, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := NewService(t.TempDir(), nil).GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("стратегий: %d", len(got.Items))
	}
	refs := got.Items[0].Hostlists
	if len(refs) != 1 {
		t.Fatalf("ссылок на списки: %d", len(refs))
	}
	if !refs[0].Exists {
		t.Errorf("существующий список помечен как отсутствующий: %+v", refs[0])
	}
	if refs[0].Entries != 2 {
		t.Errorf("записей в списке: %d, ожидалось 2", refs[0].Entries)
	}
}

func TestHostlistPlaceholderIsExpanded(t *testing.T) {
	// Реальный конфиг с роутера пишет <HOSTLIST>, а не путь: init-скрипт
	// разворачивает его в несколько списков при старте. Если не развернуть,
	// карточка показала бы «файла нет» на заведомо рабочей стратегии.
	base := setupInstall(t)
	if err := os.WriteFile(filepath.Join(base, "ipset", "zapret-hosts.txt"),
		[]byte("blocked.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := `NFQWS2_OPT="
--filter-tcp=443 <HOSTLIST> --payload=tls_client_hello --lua-desync=fake:blob=tls_google:repeats=6
"
`
	if err := os.WriteFile(filepath.Join(base, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := NewService(t.TempDir(), nil).GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("стратегий: %d", len(got.Items))
	}
	refs := got.Items[0].Hostlists
	if len(refs) != 2 {
		t.Fatalf("плейсхолдер развёрнут в %d включающих списков, ожидалось 2 (user, main): %+v", len(refs), refs)
	}
	excl := got.Items[0].HostlistExcludes
	if len(excl) != 1 || filepath.Base(excl[0].Path) != "zapret-hosts-user-exclude.txt" {
		t.Fatalf("исключение из плейсхолдера: %+v", excl)
	}
	auto := got.Items[0].HostlistAuto
	if len(auto) != 1 || filepath.Base(auto[0].Path) != "zapret-hosts-auto.txt" {
		t.Fatalf("автосписок из плейсхолдера: %+v", auto)
	}
	for _, r := range slices.Concat(refs, excl, auto) {
		if r.Path == "<HOSTLIST>" || !strings.HasPrefix(r.Path, "/") {
			t.Errorf("остался неразвёрнутый плейсхолдер: %+v", r)
		}
		if r.FromPlaceholder != "<HOSTLIST>" {
			t.Errorf("не помечено происхождение из плейсхолдера: %+v", r)
		}
	}
	// Пользовательский список обязан быть найден и посчитан.
	var userEntries int
	for _, r := range refs {
		if filepath.Base(r.Path) == "zapret-hosts-user.txt" {
			userEntries = r.Entries
			if !r.Exists {
				t.Error("пользовательский список помечен как отсутствующий")
			}
		}
	}
	if userEntries != 2 {
		t.Errorf("в пользовательском списке %d записей, ожидалось 2", userEntries)
	}
}

func TestDetectPrefersRunningInstall(t *testing.T) {
	// Реальный случай на роутере: в /opt лежали /opt/zapret (апрель),
	// /opt/zapret2 (сентябрь, протухшая копия) и /opt/zapret22 (октябрь,
	// рабочая). Перечисление каталогов по именам выбирало протухшую, и
	// интерфейс правил не тот конфиг.
	root := t.TempDir()
	mk := func(name string, opts string) string {
		base := filepath.Join(root, name)
		for _, d := range []string{"ipset", filepath.Join("nfq2"), filepath.Join("init.d", "sysv")} {
			if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cfg := "NFQWS2_OPT=\"\n" + opts + "\n\"\n"
		if err := os.WriteFile(filepath.Join(base, "config"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "nfq2", "nfqws2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "init.d", "sysv", "zapret2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return base
	}
	mk("zapret", "--filter-tcp=80 --lua-desync=OLD_ORIGINAL")
	mk("zapret2", "--filter-tcp=80 --lua-desync=STALE_COPY")
	active := mk("zapret22", "--filter-tcp=80 --lua-desync=ACTIVE")
	// Каталог без config — не установка (у нас так лежит /opt/zapret2-backups).
	_ = os.MkdirAll(filepath.Join(root, "zapret2-backups"), 0o755)

	prevOpt := optRoot
	t.Cleanup(func() { optRoot = prevOpt })
	optRoot = root

	// The resolver uses process ownership to select the running installation.
	prevProcesses := nfqProcessesHook
	t.Cleanup(func() { nfqProcessesHook = prevProcesses })
	nfqProcessesHook = func() []nfqProc {
		return []nfqProc{{pid: 42, exe: filepath.Join(active, "nfq2", "nfqws2")}}
	}

	bases := discoverBasesRaw()
	if len(bases) != 3 {
		t.Fatalf("найдено баз: %d (%v), ожидалось 3 — бэкапы без config должны отсеяться", len(bases), bases)
	}
	l, err := (&Service{}).resolveLayout()
	if err != nil {
		t.Fatal(err)
	}
	if l.base != active {
		t.Errorf("выбрана база %q вместо работающей %q", l.base, active)
	}
	got, err := NewService(t.TempDir(), nil).GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || !strings.Contains(got.Items[0].Raw, "ACTIVE") {
		t.Errorf("прочитан не тот конфиг: %+v", got.Items)
	}
}

func TestBaseOfExe(t *testing.T) {
	cases := map[string]string{
		"/opt/zapret22/nfq2/nfqws2": "/opt/zapret22",
		"/opt/zapret2/nfq2/nfqws2":  "/opt/zapret2",
		"/opt/zapret/nfq/nfqws":     "/opt/zapret",
		"/opt/zapret22/nfqws2":      "/opt/zapret22",
		"/usr/local/bin/nfqws2":     "",
		"":                          "",
		"/opt/singbox/sing-box":     "",
	}
	for exe, want := range cases {
		if got := baseOfExe(exe); got != want {
			t.Errorf("baseOfExe(%q) = %q, ожидалось %q", exe, got, want)
		}
	}
}

func TestIsNFQWSName(t *testing.T) {
	for _, n := range []string{"nfqws2", "nfqws", "dvtws2", "tpws"} {
		if !isNFQWSName(n) {
			t.Errorf("%q должен распознаваться как демон zapret", n)
		}
	}
	for _, n := range []string{"sing-box", "sshd", "sh", "curl"} {
		if isNFQWSName(n) {
			t.Errorf("%q не должен распознаваться как демон zapret", n)
		}
	}
}

func TestDetectOptVarFollowsV1Mode(t *testing.T) {
	// zapret v1 держит опции в трёх переменных, и какая рабочая —
	// задаёт MODE. Если всегда брать NFQWS_OPT, у установки в режиме
	// tpws мы бы показывали и правили не тот блок.
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "v1 nfqws",
			body: "MODE=nfqws\nNFQWS_OPT=\"--filter-tcp=443 --dpi-desync=fake\"\n",
			want: optVarV1,
		},
		{
			name: "v1 tpws",
			body: "MODE=tpws\nTPWS_OPT=\"--filter-tcp=443 --methodeol\"\nNFQWS_OPT=\"--filter-tcp=443 --dpi-desync=fake\"\n",
			want: optVarTPWS,
		},
		{
			name: "v1 tpws-socks",
			body: "MODE=tpws-socks\nTPWS_SOCKS_OPT=\"--methodeol\"\n",
			want: optVarTPWSSocks,
		},
		{
			// MODE может отсутствовать — тогда берём первую непустую.
			name: "v1 без MODE",
			body: "TPWS_OPT=\"--methodeol\"\n",
			want: optVarTPWS,
		},
		{
			name: "v2 всегда NFQWS2_OPT",
			body: "MODE=nfqws\nNFQWS2_OPT=\"--filter-tcp=443 --lua-desync=fake\"\n",
			want: optVarV2,
		},
		{
			// Режим задан, но его переменная пуста (частая situation с
			// только что установленным zapret): откатываемся на первую
			// непустую, чтобы показать хоть что-то.
			name: "v1 MODE без своей переменной",
			body: "MODE=tpws\nTPWS_OPT=\"\"\nNFQWS_OPT=\"--filter-tcp=443\"\n",
			want: optVarV1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := detectOptVar(p); got != tc.want {
				t.Errorf("detectOptVar = %q, ожидалось %q", got, tc.want)
			}
		})
	}
}

func TestInitScriptPrefersWiredSymlink(t *testing.T) {
	// На роутере с тремя установками рабочий init-скрипт был симлинком
	// S90-zapret -> /opt/zapret22/init.d/sysv/zapret22 в /opt/etc/init.d,
	// а у протухших копий лежали собственные sysv/zapret2. Поиск сначала
	// внутри базы поднимал не тот: перезапуск по кнопке дёргал бы чужой
	// демон.
	base := setupInstall(t)

	wired := filepath.Join(base, "init.d", "sysv", "zapret2")
	// Кладём «включённый» скрипт в общий каталог init.d.
	globalDir := filepath.Join(base, "..", "etc-init.d")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(globalDir, "S90-zapret")
	if err := os.Symlink(wired, link); err != nil {
		t.Fatal(err)
	}

	prevInit := initDirs
	t.Cleanup(func() { initDirs = prevInit })
	initDirs = []string{globalDir}

	got := findInitScript(base)
	if got != link {
		t.Errorf("найден %q, ожидался включённый скрипт %q", got, link)
	}
}

func TestControlRejectsUnknownAction(t *testing.T) {
	base := setupInstall(t)
	err := NewService(t.TempDir(), nil).Control("переустановить", base)
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("неизвестное действие должно отклоняться: %v", err)
	}
}

func TestControlWithoutInitScript(t *testing.T) {
	base := setupInstall(t, func(base string) {
		_ = os.Remove(filepath.Join(base, "init.d", "sysv", "zapret2"))
	})
	err := NewService(t.TempDir(), nil).Control("restart", base)
	if err == nil {
		t.Fatal("ожидалась ошибка при отсутствии init-скрипта")
	}
	if !strings.Contains(err.Error(), "init script") {
		t.Errorf("ошибка неинформативна: %v", err)
	}
}

func TestLooksLikeIP(t *testing.T) {
	ips := []string{"1.2.3.4", "10.0.0.0/8", "2001:db8::1", "::1"}
	domains := []string{"youtube.com", "rt.com", "xn--80ak6aa92e.com", "1.2.3.4.example.com"}
	for _, s := range ips {
		if !looksLikeIP(s) {
			t.Errorf("%q должно считаться адресом", s)
		}
	}
	for _, s := range domains {
		if looksLikeIP(s) {
			t.Errorf("%q не должно считаться адресом", s)
		}
	}
}
