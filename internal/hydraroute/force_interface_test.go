package hydraroute

import (
	"os"
	"testing"
)

func readTestConf(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(hrConfPath)
	if err != nil {
		t.Fatalf("read conf: %v", err)
	}
	return string(data)
}

func ruleSetWith(ifaces ...string) RuleSet {
	var rs RuleSet
	for _, iface := range ifaces {
		rs.Targets = append(rs.Targets, Target{Name: iface, Groups: []Group{{Domains: []string{"a.com"}}}})
	}
	return rs
}

// Шаблон пишется только для семейств, которые реально стоят целью,
// а не для всех трёх; чужая цель (политика, сторонний интерфейс) не в счёт.
func TestSyncForceInterface_AddsUsedFamiliesOnly(t *testing.T) {
	setupTestConf(t, "log=off\nDirectRouteEnabled=true\n")
	changed, err := syncForceInterface(ruleSetWith("awgm0", "opkgtun3", "awgm12", "HydraRoute", "nwg0", "t2s"))
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := "log=off\nDirectRouteEnabled=true\nForceInterface=awgmX,opkgtunX\n"
	if got := readTestConf(t); got != want {
		t.Fatalf("conf:\n%s\nwant:\n%s", got, want)
	}
}

// Записи пользователя и их порядок сохраняются, наше дописывается в хвост.
func TestSyncForceInterface_KeepsUserEntries(t *testing.T) {
	setupTestConf(t, "forceinterface=wg9, t2sX\nlog=off\n")
	changed, err := syncForceInterface(ruleSetWith("t2s1", "awgm0"))
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := "forceinterface=wg9,t2sX,awgmX\nlog=off\n"
	if got := readTestConf(t); got != want {
		t.Fatalf("conf:\n%s\nwant:\n%s", got, want)
	}
}

// HR Neo складывает несколько строк ключа: при сведении в одну значения
// второй строки не теряются.
func TestSyncForceInterface_MergesDuplicateLines(t *testing.T) {
	setupTestConf(t, "ForceInterface=wg9\nlog=off\nForceInterface=awgmX,tun7\n")
	changed, err := syncForceInterface(ruleSetWith("awgm0"))
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := "ForceInterface=wg9,awgmX,tun7\nlog=off\n"
	if got := readTestConf(t); got != want {
		t.Fatalf("conf:\n%s\nwant:\n%s", got, want)
	}
}

// Всё нужное уже есть — файл не трогается.
func TestSyncForceInterface_NoChangeNoWrite(t *testing.T) {
	setupTestConf(t, "ForceInterface=opkgtunX,awgmX\n")
	before, err := os.Stat(hrConfPath)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := syncForceInterface(ruleSetWith("awgm0", "opkgtun1"))
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	after, err := os.Stat(hrConfPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || readTestConf(t) != "ForceInterface=opkgtunX,awgmX\n" {
		t.Fatal("conf rewritten without change")
	}

	changed, err = syncForceInterface(ruleSetWith("HydraRoute"))
	if err != nil || changed {
		t.Fatalf("no own targets: changed=%v err=%v", changed, err)
	}
}

// Выключенное правило HR Neo не читает — его цель шаблон не добавляет.
func TestSyncForceInterface_IgnoresDisabledRules(t *testing.T) {
	setupTestConf(t, "log=off\n")
	rs := RuleSet{Targets: []Target{
		{Name: "awgm0", Groups: []Group{{Domains: []string{"a.com"}}}},
		{Name: "opkgtun0", Groups: []Group{{Domains: []string{"a.com"}, DomainsDisabled: true}}},
	}}
	changed, err := syncForceInterface(rs)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if got := readTestConf(t); got != "log=off\nForceInterface=awgmX\n" {
		t.Fatalf("conf = %q", got)
	}
}

// Правило из панели с целью-нашим интерфейсом сразу попадает в ForceInterface:
// без этого HR Neo на следующем старте снова завёл бы пустую политику.
func TestCreateGroup_SyncsForceInterface(t *testing.T) {
	svc, _, _ := setupRuleFiles(t)
	if _, err := svc.CreateGroup("awgm0", Group{Description: "NL", Domains: []string{"example.com"}}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if got := readTestConf(t); got != "ForceInterface=awgmX\n" {
		t.Fatalf("conf = %q", got)
	}
}
