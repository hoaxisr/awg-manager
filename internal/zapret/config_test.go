package zapret

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadOptBlockMultiline(t *testing.T) {
	// Реальный формат zapret2: многострочное присваивание, каждый
	// профиль на своей строке, --new в конце всех кроме последнего.
	cfg := `# comment before
NFQWS2_ENABLE=1
NFQWS2_OPT="
--filter-tcp=80 --lua-desync=multisplit --new
--filter-tcp=443 --lua-desync=fake:blob=tls_clienthello_www_google_com --new
--filter-udp=443 --lua-desync=fake:blob=quic5
"
QNUM=200
IFACE_WAN=ppp0
`
	blk, ok := readOptBlockFromString(cfg, optVarV2)
	if !ok {
		t.Fatal("блок не найден")
	}
	if !blk.multiline {
		t.Error("ожидался многострочный блок")
	}
	want := "\n--filter-tcp=80 --lua-desync=multisplit --new\n" +
		"--filter-tcp=443 --lua-desync=fake:blob=tls_clienthello_www_google_com --new\n" +
		"--filter-udp=443 --lua-desync=fake:blob=quic5\n"
	if blk.value != want {
		t.Errorf("значение блока:\n%q\nожидалось:\n%q", blk.value, want)
	}
	// Всё после блока обязано сохраниться — иначе мы затрём настройки,
	// которых не знаем.
	tail := cfg[blk.end:]
	if !strings.Contains(tail, "QNUM=200") || !strings.Contains(tail, "IFACE_WAN=ppp0") {
		t.Errorf("хвост конфига потерян: %q", tail)
	}
}

func TestReadOptBlockSingleLine(t *testing.T) {
	cfg := `NFQWS_OPT="--filter-tcp=443 --dpi-desync=fake --dpi-desync-split-pos=1"
DISABLE_IPV6=1
`
	blk, ok := readOptBlockFromString(cfg, optVarV1)
	if !ok {
		t.Fatal("блок не найден")
	}
	if blk.multiline {
		t.Error("однострочный блок помечен как многострочный")
	}
	if blk.value != "--filter-tcp=443 --dpi-desync=fake --dpi-desync-split-pos=1" {
		t.Errorf("значение: %q", blk.value)
	}
}

func TestReadOptBlockQuotedValueWithTrailingComment(t *testing.T) {
	// Кавычка внутри значения не должна завершать блок — иначе хвост
	// стратегии молча уедет в следующий ключ.
	cfg := "NFQWS2_OPT=\"--comment=\"important\" --filter-tcp=443\"\nQNUM=200\n"
	blk, ok := readOptBlockFromString(cfg, optVarV2)
	if !ok {
		t.Fatal("блок не найден")
	}
	if strings.Contains(blk.value, "QNUM") {
		t.Errorf("хвост попал в значение блока: %q", blk.value)
	}
}

func TestSplitProfilesPreservesOrder(t *testing.T) {
	value := "\n--filter-tcp=80 --lua-desync=multisplit --new\n" +
		"--filter-tcp=443 --lua-desync=fake --new\n" +
		"--filter-udp=443 --lua-desync=fake:blob=quic5\n"
	got := splitProfiles(value)
	if len(got) != 3 {
		t.Fatalf("профилей: %d, ожидалось 3: %q", len(got), got)
	}
	// Порядок = приоритет: первый профиль выигрывает при совпадении.
	if !strings.Contains(got[0], "filter-tcp=80") {
		t.Errorf("порядок нарушен: %q", got)
	}
	if strings.Contains(got[2], "--new") {
		t.Errorf("в последнем профиле остался маркер --new: %q", got[2])
	}
}

func TestSplitProfilesAtEverySeparator(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        []string
	}{
		{"inline", "--filter-tcp=80 --new --filter-tcp=443 --new --filter-udp=443", []string{"--filter-tcp=80", "--filter-tcp=443", "--filter-udp=443"}},
		{"mixed lines", "--filter-tcp=80\n--lua-desync=fake --new --filter-tcp=443\n--lua-desync=multisplit", []string{"--filter-tcp=80 --lua-desync=fake", "--filter-tcp=443 --lua-desync=multisplit"}},
		{"leading marker", "--new\n--filter-tcp=80", []string{"--filter-tcp=80"}},
		{"empty profiles", "--new --filter-tcp=80 --new --new\n--new --filter-tcp=443 --new", []string{"--filter-tcp=80", "--filter-tcp=443"}},
		{"token boundary", "--filter-tcp=80 --newline=1 --new --filter-tcp=443", []string{"--filter-tcp=80 --newline=1", "--filter-tcp=443"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := splitProfiles(tc.value)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("profiles = %q, want %q", got, tc.want)
			}
			if again := splitProfiles(serializeProfiles(got)); !slices.Equal(again, tc.want) {
				t.Fatalf("round trip = %q, want %q", again, tc.want)
			}
		})
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	value := "\n--filter-tcp=80 --new\n--filter-tcp=443 --new\n--filter-udp=443\n"
	items := splitProfiles(value)
	again := splitProfiles(serializeProfiles(items))
	if len(again) != len(items) {
		t.Fatalf("после round-trip %d профилей, было %d", len(again), len(items))
	}
	for i := range items {
		if items[i] != again[i] {
			t.Errorf("профиль %d изменился:\n%q\n%q", i, items[i], again[i])
		}
	}
}

func TestParseStrategiesExtractsFilters(t *testing.T) {
	raw := `--name=rkn --filter-tcp=443,2053 --filter-l7=tls ` +
		`--hostlist=/opt/zapret2/ipset/zapret-hosts-user.txt ` +
		`--in-range=-s5556 --out-range=-n4 --payload=tls_client_hello ` +
		`--lua-desync=circular:fails=3:time=60:key=rkn_tcp:nld=2 ` +
		`--lua-desync=fake:blob=fake_default_tls:repeats=4:strategy=1`
	s := parseStrategy(0, raw)

	if s.Name != "rkn" {
		t.Errorf("имя: %q", s.Name)
	}
	if s.FilterTCP != "443,2053" || s.FilterL7 != "tls" {
		t.Errorf("фильтры: tcp=%q l7=%q", s.FilterTCP, s.FilterL7)
	}
	if s.InRange != "-s5556" || s.OutRange != "-n4" {
		t.Errorf("диапазоны: %q %q", s.InRange, s.OutRange)
	}
	if len(s.LuaDesync) != 2 {
		t.Fatalf("вызовов lua: %d", len(s.LuaDesync))
	}
	if s.LuaDesync[0].Function != "circular" || s.LuaDesync[0].Key != "rkn_tcp" {
		t.Errorf("circular разобран неверно: %+v", s.LuaDesync[0])
	}
	if s.LuaDesync[1].Slot != "1" {
		t.Errorf("slot: %q", s.LuaDesync[1].Slot)
	}
}

func TestStrategyWarnings(t *testing.T) {
	t.Run("filter без обхода", func(t *testing.T) {
		s := parseStrategy(0, "--filter-tcp=443 --filter-l7=tls")
		s.Warnings = strategyWarnings(s, true)
		if !hasWarning(s, "strategy_no_lua_desync") {
			t.Errorf("нет предупреждения о пустом профиле: %v", s.Warnings)
		}
	})
	t.Run("circular без key", func(t *testing.T) {
		s := parseStrategy(0, "--filter-tcp=443 --lua-desync=circular:fails=3")
		s.Warnings = strategyWarnings(s, true)
		if !hasWarning(s, "circular_missing_key") {
			t.Errorf("нет предупреждения про key: %v", s.Warnings)
		}
	})
	t.Run("hostlist count error has a localized code and detail", func(t *testing.T) {
		s := Strategy{Hostlists: []StrategyHostlist{{countErr: "permission denied"}}}
		warnings := strategyWarnings(s, false)
		if len(warnings) != 1 || warnings[0].Code != "hostlist_count_failed" || warnings[0].Detail != "permission denied" {
			t.Fatalf("unexpected warning payload: %+v", warnings)
		}
	})
}

func hasWarning(s Strategy, code string) bool {
	for _, w := range s.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestValidateProfilesRejectsGarbage(t *testing.T) {
	if err := validateProfiles([]string{"--filter-tcp=443 --new"}); err != nil {
		t.Errorf("валидный набор отклонён: %v", err)
	}
	if err := validateProfiles(nil); err == nil {
		t.Error("пустой набор должен отклоняться")
	}
	if err := validateProfiles([]string{"не параметр"}); err == nil {
		t.Error("строка без параметров должна отклоняться")
	}
	if err := validateProfiles([]string{"--filter-tcp=443\x00"}); err == nil {
		t.Error("NUL in a profile must be rejected")
	}
}

func TestWriteOptBlockPreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	original := `NFQWS2_ENABLE=1
NFQWS2_OPT="
--filter-tcp=80 --lua-desync=multisplit --new
--filter-tcp=443 --lua-desync=fake
"
QNUM=200
IFACE_WAN=ppp0
DISABLE_IPV6=1
`
	if err := os.WriteFile(cfg, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	body := serializeProfiles([]string{
		"--filter-tcp=443 --lua-desync=fake",
		"--filter-tcp=80 --lua-desync=multisplit",
	})
	if err := writeOptBlock(cfg, optVarV2, body, fileMode(cfg)); err != nil {
		t.Fatal(err)
	}

	out, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Каждый ключ вне блока обязан выжить: мы их не понимаем и не
	// имеем права затирать (NFQWS2_ENABLE, QNUM, IFACE_WAN…).
	for _, key := range []string{"NFQWS2_ENABLE=1", "QNUM=200", "IFACE_WAN=ppp0", "DISABLE_IPV6=1"} {
		if !strings.Contains(string(out), key) {
			t.Errorf("ключ %s потерян:\n%s", key, out)
		}
	}
	// Права не меняем.
	st, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("права изменены: %o", st.Mode().Perm())
	}

	// И перечитать должны получить ровно то, что записали, в том же
	// порядке — иначе карточки и файл разойдутся.
	blk, ok := readOptBlock(cfg, optVarV2)
	if !ok {
		t.Fatal("после записи блок не читается")
	}
	items := splitProfiles(blk.value)
	if len(items) != 2 || !strings.Contains(items[0], "filter-tcp=443") {
		t.Errorf("после round-trip: %q", items)
	}
}

func TestDetectOptVarPrefersNonEmpty(t *testing.T) {
	// z2k-конфиг может содержать обе переменные: NFQWS_OPT от старого
	// zapret в комментариях, NFQWS2_OPT рабочую.
	cfg := `# NFQWS_OPT="--filter-tcp=80"
NFQWS2_OPT="
--filter-tcp=443
"
`
	if got := firstNonEmptyAssignment(cfg); got != optVarV2 {
		t.Errorf("выбрана %q, ожидалась %q", got, optVarV2)
	}
}

func firstNonEmptyAssignment(body string) string {
	for _, v := range []string{optVarV2, optVarV1} {
		if findAssignment(body, v) != "" {
			return v
		}
	}
	return ""
}

func TestFindAssignmentIgnoresCommentedLine(t *testing.T) {
	body := `# NFQWS2_OPT="--filter-tcp=80"`
	if findAssignment(body, optVarV2) != "" {
		t.Error("закомментированная переменная принята за заданную")
	}
}

func TestWriteOptBlockExactReplacement(t *testing.T) {
	for _, initial := range []string{
		"BEFORE=1\nNFQWS2_OPT=\"--filter-tcp=80\"\nAFTER=2\n",
		"BEFORE=1\nNFQWS2_OPT=\"\n--filter-tcp=80\n\"\nAFTER=2\n",
		"BEFORE=1\nNFQWS2_OPT=--filter-tcp=80\nAFTER=2\n",
	} {
		path := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
			t.Fatal(err)
		}
		if err := writeOptBlock(path, optVarV2, "--filter-tcp=443", 0600); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "BEFORE=1\nNFQWS2_OPT=\"--filter-tcp=443\"\nAFTER=2\n"
		if strings.Contains(initial, "OPT=\"\n") {
			want = "BEFORE=1\nNFQWS2_OPT=\"\n--filter-tcp=443\n\"\nAFTER=2\n"
		}
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestStrategyWriteDoesNotExecuteShell(t *testing.T) {
	for _, payload := range []string{
		`$(touch "$MARKER")`,
		"`touch \"$MARKER\"`",
		`"; touch "$MARKER"; #`,
		`${MISSING:-$(touch "$MARKER")}`,
		`\"$(touch "$MARKER")`,
		`\\$(touch "$MARKER")`,
	} {
		t.Run(payload, func(t *testing.T) {
			dir := t.TempDir()
			config := filepath.Join(dir, "config")
			marker := filepath.Join(dir, "executed")
			if err := os.WriteFile(config, []byte("NFQWS2_OPT=\"--filter-tcp=80\"\nAFTER=preserved\n"), 0600); err != nil {
				t.Fatal(err)
			}
			profile := "--filter-tcp=443 --comment=" + payload
			if err := validateProfiles([]string{profile}); err != nil {
				t.Fatal(err)
			}
			if err := writeOptBlock(config, optVarV2, profile, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", "-c", `. "$1"; printf '%s\n%s' "$NFQWS2_OPT" "$AFTER"`, "sh", config)
			cmd.Env = append(os.Environ(), "MARKER="+marker)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("source fixture: %v: %s", err, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("strategy executed shell command: %v", err)
			}
			if !strings.Contains(string(out), "touch") || !strings.HasSuffix(string(out), "\npreserved") {
				t.Fatalf("lost option text or following assignment: %q", out)
			}
			first, _ := os.ReadFile(config)
			blk, ok := readOptBlock(config, optVarV2)
			if !ok {
				t.Fatal("saved assignment cannot be read")
			}
			if err := writeOptBlock(config, optVarV2, blk.value, 0600); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(config)
			if string(first) != string(second) {
				t.Fatalf("repeated save changes escapes: %q -> %q", first, second)
			}
		})
	}
}

func TestStrategyWritePreservesSimpleVariables(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(config, []byte("BASE=/safe/base\nNFQWS2_OPT=\"\n--filter-tcp=80\n\"\nAFTER=preserved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := `--filter-tcp=443 --hostlist=$BASE/user.txt --blob=${BASE}/fake.bin --comment=\$BASE --name="quoted"`
	if err := writeOptBlock(config, optVarV2, profile, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", "-c", `. "$1"; printf '%s' "$NFQWS2_OPT"`, "sh", config).CombinedOutput()
	if err != nil {
		t.Fatalf("source fixture: %v: %s", err, out)
	}
	want := "\n--filter-tcp=443 --hostlist=/safe/base/user.txt --blob=/safe/base/fake.bin --comment=$BASE --name=\"quoted\"\n"
	if string(out) != want {
		t.Fatalf("source yielded %q, want %q", out, want)
	}
}

func TestReadOptBlockSkipsEscapedQuoteAtLineEnd(t *testing.T) {
	body := "NFQWS2_OPT=\"\n--comment=\\\"quoted\\\"\n--filter-tcp=443\n\"\nAFTER=preserved\n"
	blk, ok := readOptBlockFromString(body, optVarV2)
	if !ok || !strings.Contains(blk.value, "--filter-tcp=443") || strings.Contains(blk.value, "AFTER=") {
		t.Fatalf("assignment parsed incorrectly: %+v, %v", blk, ok)
	}
}
