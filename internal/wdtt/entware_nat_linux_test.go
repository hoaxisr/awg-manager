//go:build linux

package wdtt

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMasqueradeMatchArgs(t *testing.T) {
	plan := entwareNATPlan{Iface: "wdttraw0", CIDR: "10.70.0.0/16"}
	full := masqueradeMatchArgs(plan, "full", "eth3")
	wantFull := []string{"-s", "10.70.0.0/16", "!", "-o", "wdttraw0",
		"-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE"}
	if !reflect.DeepEqual(full, wantFull) {
		t.Fatalf("full: %v, want %v", full, wantFull)
	}
	inet := masqueradeMatchArgs(plan, "internet-only", "eth3")
	wantInet := []string{"-s", "10.70.0.0/16", "-o", "eth3",
		"-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE"}
	if !reflect.DeepEqual(inet, wantInet) {
		t.Fatalf("internet-only: %v, want %v", inet, wantInet)
	}
	// internet-only без известного WAN — деградация в full-форму, не в слепой -o ""
	if got := masqueradeMatchArgs(plan, "internet-only", ""); !reflect.DeepEqual(got, wantFull) {
		t.Fatalf("internet-only без WAN: %v, want %v", got, wantFull)
	}
}

func TestWdttNetfilterHookScript(t *testing.T) {
	spec := wdttNetfilterSpec{
		ForwardIfaces: []string{"wdttraw0", "opkgtun17"},
		DNS: []wdttDNSSpec{
			{Iface: "wdttraw0", Gateway: "10.70.66.1"},
			{Iface: "opkgtun17", Gateway: "10.66.0.1"},
		},
		Masq:          []entwareNATPlan{{Iface: "wdttraw0", CIDR: "10.70.0.0/16"}},
		MasqMode:      "full",
		RawPolicyMark: "0xffffaaf",
	}
	script := wdttNetfilterHookScript(spec)
	for _, want := range []string{
		`case "$table" in`,
		`-I FORWARD 1 -i "wdttraw0" -j ACCEPT`,
		`-I INPUT 1 -i "wdttraw0" -p udp --dport 53 -j ACCEPT`,
		`-t nat -I PREROUTING 1 -i "wdttraw0" -p udp --dport 53 -j DNAT --to-destination 10.70.66.1:53`,
		`-t nat -I PREROUTING 1 -i "opkgtun17" -p tcp --dport 53 -j DNAT --to-destination 10.66.0.1:53`,
		`-s 10.70.0.0/16 ! -o wdttraw0`,
		// mangle: пара CONNMARK+MARK вставляется только если ОБА правила
		// отсутствуют (M1) — независимая довставка при частичном состоянии
		// инвертирует итоговый порядок (F3).
		`if ! run -t mangle -C PREROUTING -i "wdttraw0" -j CONNMARK --save-mark --nfmask 0xffffffff --ctmask 0xffffffff && ! run -t mangle -C PREROUTING -i "wdttraw0" -j MARK --set-xmark 0xffffaaf/0xffffffff; then`,
		`-t mangle -I PREROUTING 1 -i "wdttraw0" -j CONNMARK --save-mark`,
		`-t mangle -I PREROUTING 1 -i "wdttraw0" -j MARK --set-xmark 0xffffaaf/0xffffffff`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("в скрипте нет %q:\n%s", want, script)
		}
	}
	// порядок ВСТАВКИ (не проверок) в mangle: CONNMARK раньше MARK (оба -I 1 → итог MARK@1)
	connInsIdx := strings.Index(script, `-t mangle -I PREROUTING 1 -i "wdttraw0" -j CONNMARK`)
	markInsIdx := strings.Index(script, `-t mangle -I PREROUTING 1 -i "wdttraw0" -j MARK`)
	if connInsIdx < 0 || markInsIdx < 0 {
		t.Fatalf("не нашли строки вставки CONNMARK/MARK:\n%s", script)
	}
	if connInsIdx > markInsIdx {
		t.Fatal("mangle: CONNMARK должен вставляться раньше MARK")
	}
	// без метки — mangle-секция пустая
	spec.RawPolicyMark = ""
	if s2 := wdttNetfilterHookScript(spec); strings.Contains(s2, "-t mangle") {
		t.Fatal("mangle-правила без метки политики")
	}
}

func TestWdttNetfilterHookScriptShellSyntax(t *testing.T) {
	spec := wdttNetfilterSpec{ForwardIfaces: []string{"wdttraw0"},
		DNS:  []wdttDNSSpec{{Iface: "wdttraw0", Gateway: "10.70.66.1"}},
		Masq: []entwareNATPlan{{Iface: "wdttraw0", CIDR: "10.70.0.0/16"}}, MasqMode: "full",
		RawPolicyMark: "0xffffaaf"}
	path := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(path, []byte(wdttNetfilterHookScript(spec)), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := osexec.Command("sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
}

// Холостой тик (сервер в конфиге есть, но не запущен) сносил entware-NAT
// вслепую каждые 15 с: ~18 форков iptables на тик вечно, хотя правила сами по
// себе не воскресают. Снос делается один раз на переход «ничего не работает»,
// повторный тик обязан быть пустым. Проверка белоящичная — своей точки
// наблюдения за iptables в пакете нет, а заводить её ради одного теста дороже
// самой правки.
func TestIdleNATSweepRunsOnce(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "")
	full, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	full.Servers = append(full.Servers, ServerInstance{ID: "srv1", Config: ndmsServerConfig()})
	if err := svc.store.Save(full); err != nil {
		t.Fatal(err)
	}

	svc.reconcileRunningServersNAT(context.Background())
	if !svc.natIdleSwept {
		t.Fatal("первый холостой тик обязан снести entware-NAT и взвести защёлку")
	}
	svc.reconcileRunningServersNAT(context.Background())
	if !svc.natIdleSwept {
		t.Fatal("защёлка сброшена без единого запущенного сервера")
	}
}

// Форма правила FORWARD accept обязана совпадать у всех трёх мест: вставка,
// проверка наличия и снос. С 2.17.0 они разъехались — вставка и проверка стали
// голыми, а снос остался на помеченной форме (`-m comment --comment AWGM_WDTT`)
// и потому не мог удалить ничего. Каждая остановка WDTT-сервера с тех пор
// оставляла в FORWARD вечное `-i wdttraw0 -j ACCEPT`: дыра в межсетевом экране,
// которую не закрывали ни остановка, ни удаление сервера.
func TestEntwareForwardDeleteMatchesInsertForm(t *testing.T) {
	specs := entwareForwardDeleteSpecs("-i", "wdttraw0")
	if len(specs) == 0 {
		t.Fatal("снос не знает ни одной формы правила")
	}
	insert := entwareForwardMatch("-i", "wdttraw0")
	found := false
	for _, s := range specs {
		if reflect.DeepEqual(s, insert) {
			found = true
		}
	}
	if !found {
		t.Fatalf("снос не покрывает форму вставки %v (знает только %v)", insert, specs)
	}
	// Помеченная форма версий ≤2.16.x обязана остаться в списке: иначе
	// апгрейд поверх них оставит их в FORWARD навсегда.
	legacy := []string{"-i", "wdttraw0", "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT"}
	for _, s := range specs {
		if reflect.DeepEqual(s, legacy) {
			return
		}
	}
	t.Fatalf("снос забыл помеченную форму старых версий: %v", specs)
}

// Хук netfilter.d восстанавливает те же правила после перезаписи таблиц NDM —
// и обязан ставить ровно ту форму, которую Go-код проверяет и сносит.
func TestEntwareForwardHookUsesSameForm(t *testing.T) {
	script := wdttNetfilterHookScript(wdttNetfilterSpec{ForwardIfaces: []string{"wdttraw0"}})
	match := strings.Join(entwareForwardMatch("-i", `"wdttraw0"`), " ")
	if !strings.Contains(script, "run -C FORWARD "+match) {
		t.Fatalf("проверка в хуке разошлась с формой Go-кода (%s):\n%s", match, script)
	}
	if !strings.Contains(script, "run -I FORWARD 1 "+match) {
		t.Fatalf("вставка в хуке разошлась с формой Go-кода (%s):\n%s", match, script)
	}
}
