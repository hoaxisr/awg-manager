package hydraroute

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func setupRuleFiles(t *testing.T) (svc *Service, domainPath, ipPath string) {
	t.Helper()
	dir := t.TempDir()
	domainPath = filepath.Join(dir, "domain.conf")
	ipPath = filepath.Join(dir, "ip.list")

	origDomain := domainConfPath
	origIP := ipListPath
	domainConfPath = domainPath
	ipListPath = ipPath
	t.Cleanup(func() {
		domainConfPath = origDomain
		ipListPath = origIP
	})

	setupEmptyConf(t) // mutateRules дописывает ForceInterface в hrneo.conf

	svc = &Service{}
	svc.SetStatusForTest(true)
	stopRestartTimerOnCleanup(t, svc) // mutateRules планирует neo restart (F410)
	return svc, domainPath, ipPath
}

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(data)
}

func writeRuleFiles(t *testing.T, domainPath, ipPath, d, i string) {
	t.Helper()
	if err := os.WriteFile(domainPath, []byte(d), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ipPath, []byte(i), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseGroupRef(t *testing.T) {
	ref, err := ParseGroupRef("abc123:x:y:2")
	if err != nil {
		t.Fatal(err)
	}
	if ref != (GroupRef{Revision: "abc123", Target: "x:y", Index: 2}) {
		t.Fatalf("ref = %+v", ref)
	}
	if ref.String() != "abc123:x:y:2" {
		t.Fatalf("String() = %q", ref.String())
	}
	for _, bad := range []string{"", "Youtube", "rev:2", "rev::2", "rev:P:-1", "rev:P:x"} {
		if _, err := ParseGroupRef(bad); err == nil {
			t.Errorf("ParseGroupRef(%q) accepted", bad)
		}
	}
}

func TestListRuleSet_ReadsHRwebFiles(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	writeRuleFiles(t, dp, ip, "##\ntelegram.org,t.me/Belka", "##\n/Belka\n91.108.4.0/22")
	rs, err := svc.ListRuleSet()
	if err != nil {
		t.Fatal(err)
	}
	g := rs.Target("Belka").Groups[0]
	if g.Description != "" || len(g.Domains) != 2 || g.Subnets[0] != "91.108.4.0/22" {
		t.Fatalf("group = %+v", g)
	}
}

func TestCreateGroup_AppendsAndAllowsEmptyAndDuplicateNames(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	if _, err := svc.CreateGroup("P", Group{Domains: []string{"a.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateGroup("P", Group{Description: "X", Domains: []string{"b.com"}}); err != nil {
		t.Fatal(err)
	}
	// CIDR-группа с тем же описанием, что у доменной группы "X": формат
	// склеивает их по общему `##X` (linkGroups HRweb), и после записи это
	// одна группа №1. Позиция берётся из перечитанного документа.
	ref, err := svc.CreateGroup("P", Group{Description: "X", Subnets: []string{"1.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Index != 1 || ref.Target != "P" {
		t.Fatalf("ref = %+v", ref)
	}
	d, i := readFileOrEmpty(t, dp), readFileOrEmpty(t, ip)
	if d != "a.com/P\n##X\nb.com/P" || i != "##X\n/P\n1.0.0.0/8" {
		t.Fatalf("files:\n%q\n%q", d, i)
	}
	if rev, _ := svc.RulesRevision(); rev != ref.Revision {
		t.Fatalf("returned ref revision %q != files %q", ref.Revision, rev)
	}
}

func TestMutations_StaleRevisionRejectedFilesUntouched(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	writeRuleFiles(t, dp, ip, "a.com/P", "")
	stale := GroupRef{Revision: "000000000000", Target: "P", Index: 0}
	checks := map[string]error{
		"update":  func() error { _, err := svc.UpdateGroup(stale, "P", Group{Domains: []string{"b.com"}}); return err }(),
		"delete":  svc.DeleteGroups([]GroupRef{stale}),
		"enabled": svc.SetGroupEnabled(stale, false),
		"target":  svc.DeleteTarget("P", stale.Revision),
	}
	for name, err := range checks {
		if !errors.Is(err, ErrRulesChanged) {
			t.Errorf("%s: err = %v, want ErrRulesChanged", name, err)
		}
	}
	if readFileOrEmpty(t, dp) != "a.com/P" {
		t.Fatal("domain.conf changed on a rejected mutation")
	}
}

func TestUpdateGroup_KeepsFlagsMovesTarget(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	writeRuleFiles(t, dp, ip, "##Tg\n#t.me/P\nz.com/P", "##Tg\n/P\n91.108.4.0/22")
	rs, _ := svc.ListRuleSet()
	ref := GroupRef{Revision: rs.Revision, Target: "P", Index: 0}
	newRef, err := svc.UpdateGroup(ref, "P", Group{Description: "Tg", Domains: []string{"t.me", "x.me"}, Subnets: []string{"91.108.4.0/22"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFileOrEmpty(t, dp); got != "##Tg\n#t.me,x.me/P\nz.com/P" {
		t.Fatalf("flags lost: %q", got)
	}
	// перенос в другую цель: группа уходит в конец новой цели
	movedRef, err := svc.UpdateGroup(newRef, "Q", Group{Domains: []string{"t.me"}})
	if err != nil {
		t.Fatal(err)
	}
	if movedRef.Target != "Q" || movedRef.Index != 0 {
		t.Fatalf("moved ref = %+v", movedRef)
	}
	if got := readFileOrEmpty(t, dp); got != "z.com/P\n#t.me/Q" {
		t.Fatalf("after move: %q", got)
	}
}

func TestUpdateGroup_NewHalfInheritsGroupState(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	writeRuleFiles(t, dp, ip, "#a.com/P", "")
	rs, _ := svc.ListRuleSet()
	_, err := svc.UpdateGroup(GroupRef{rs.Revision, "P", 0}, "P", Group{Domains: []string{"a.com"}, Subnets: []string{"1.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFileOrEmpty(t, ip); got != "##\n#/P\n1.0.0.0/8" {
		t.Fatalf("new CIDR half of a disabled group must be disabled: %q", got)
	}
}

func TestDeleteGroups_OneSnapshot(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	writeRuleFiles(t, dp, ip, "a.com/P\nb.com/P\nc.com/P", "")
	rs, _ := svc.ListRuleSet()
	err := svc.DeleteGroups([]GroupRef{{rs.Revision, "P", 0}, {rs.Revision, "P", 2}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFileOrEmpty(t, dp); got != "b.com/P" {
		t.Fatalf("got %q, want only the middle group left", got)
	}
}

func TestDeleteGroups_MixedRevisionsRejected(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	writeRuleFiles(t, dp, ip, "a.com/P\nb.com/P", "")
	rs, _ := svc.ListRuleSet()
	err := svc.DeleteGroups([]GroupRef{{rs.Revision, "P", 0}, {"000000000000", "P", 1}})
	if !errors.Is(err, ErrRulesChanged) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetGroupEnabled_BothHalves(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	writeRuleFiles(t, dp, ip, "##A\na.com/P", "##A\n/P\n1.0.0.0/8")
	rs, _ := svc.ListRuleSet()
	if err := svc.SetGroupEnabled(GroupRef{rs.Revision, "P", 0}, false); err != nil {
		t.Fatal(err)
	}
	if readFileOrEmpty(t, dp) != "##A\n#a.com/P" || readFileOrEmpty(t, ip) != "##A\n#/P\n1.0.0.0/8" {
		t.Fatalf("files:\n%q\n%q", readFileOrEmpty(t, dp), readFileOrEmpty(t, ip))
	}
}

func TestDeleteTarget_GroupsAndPolicyOrder(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	writeRuleFiles(t, dp, ip, "a.com/P\nb.com/Q", "/P\n1.0.0.0/8")
	if err := WritePolicyOrderOnly([]string{"Q", "P", "Empty"}); err != nil {
		t.Fatal(err)
	}
	rev, _ := svc.RulesRevision()
	if err := svc.DeleteTarget("P", rev); err != nil {
		t.Fatal(err)
	}
	if readFileOrEmpty(t, dp) != "b.com/Q" || readFileOrEmpty(t, ip) != "" {
		t.Fatal("P groups left in files")
	}
	cfg, err := ReadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.PolicyOrder, []string{"Q", "Empty"}) {
		t.Fatalf("PolicyOrder = %v", cfg.PolicyOrder)
	}
	// пустая цель (только в PolicyOrder) тоже удаляется
	rev, _ = svc.RulesRevision()
	if err := svc.DeleteTarget("Empty", rev); err != nil {
		t.Fatal(err)
	}
	cfg, _ = ReadConfig()
	if !reflect.DeepEqual(cfg.PolicyOrder, []string{"Q"}) {
		t.Fatalf("PolicyOrder = %v", cfg.PolicyOrder)
	}
}

func TestGroupValidation(t *testing.T) {
	svc, _, _ := setupRuleFiles(t)
	bad := []struct {
		target string
		g      Group
	}{
		{"", Group{Domains: []string{"a.com"}}},
		{"P", Group{}},
		{"P", Group{Description: "impossible to use", Domains: []string{"a.com"}}},
		{"P", Group{Description: "a\nb", Domains: []string{"a.com"}}},
		{"P", Group{Domains: []string{"a.com/x"}}},
		{"P", Group{Domains: []string{"a.com,b.com"}}},
		{"P", Group{Domains: []string{"#a.com"}}},
		{"P", Group{Subnets: []string{"1.0.0.0/8\n2.0.0.0/8"}}},
		{"P", Group{Subnets: []string{"/Q"}}},
		{"P", Group{Subnets: []string{"   "}}},
		{"P", Group{Subnets: []string{" 1.0.0.0/8"}}},
		{"P", Group{Domains: []string{" a.com"}}},
		{"a/b", Group{Domains: []string{"a.com"}}},
		{"#P", Group{Domains: []string{"a.com"}}},
		{"P", Group{Domains: []string{"_dmarc.a.com"}}},
		{"P", Group{Subnets: []string{"1.2.3.4"}}},
	}
	for i, tc := range bad {
		if _, err := svc.CreateGroup(tc.target, tc.g); err == nil {
			t.Errorf("case %d accepted: %q %+v", i, tc.target, tc.g)
		}
	}
}

// Ожидания сняты прогоном JS из /opt/bin/hrweb 1.68.0 (save → classifyLine +
// InputValidator.isValidToken), а не выведены вручную.
func TestHRwebTokenError(t *testing.T) {
	for _, tc := range []struct {
		token string
		want  error // nil — принят
	}{
		{"a.com", nil},
		{"Example.COM", nil},
		{"xn--80ak6aa92e.com", nil},
		{"sub-1.a.com", nil},
		{"ru", nil},
		{"geosite:category-ads-all", nil},
		{"geoip:ru", nil},
		{"geoip:ru-x", nil},
		{"1.2.3.0/24", nil},
		{"2a00:1450::/32", nil},
		{"10.0.0.1-10.0.0.9", nil},
		{"::/0", errHRwebChars},
		{"_dmarc.a.com", errHRwebChars},
		{"a_b", errHRwebChars},
		{"*.a.com", errHRwebChars},
		{".a.com", errHRwebChars},
		{"a.com.", errHRwebChars},
		{"a..com", errHRwebChars},
		{"a-.com", errHRwebChars},
		{"-a.com", errHRwebChars},
		{"geosite:google@cn", errHRwebChars},
		{"geosite:", errHRwebChars},
		{"a b.com", errHRwebChars},
		{"a.com:", errHRwebChars},
		{"1.2.3.0/24-x", errHRwebChars},
		{"1.2.3.0//24", errHRwebChars},
		{"1.2.3.4", errHRwebMask},
		{"2a00::1", errHRwebMask},
		{"full:a.com", errHRwebMask},
	} {
		err := hrwebTokenError(tc.token)
		if (err == nil) != (tc.want == nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%q: got %v, want %v", tc.token, err, tc.want)
		}
	}
}

func TestRuleWrites_KeepOversizedServiceSection(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	writeRuleFiles(t, dp, ip, "", "##impossible to use\n#/Too-big-geoip-tag\ngeoip:cn")
	if _, err := svc.CreateGroup("P", Group{Subnets: []string{"1.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	want := "/P\n1.0.0.0/8\n##impossible to use\n#/Too-big-geoip-tag\ngeoip:cn"
	if got := readFileOrEmpty(t, ip); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGroupAt_NegativeIndexRejected(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	writeRuleFiles(t, dp, ip, "a.com/P", "")
	rev, _ := svc.RulesRevision()
	err := svc.SetGroupEnabled(GroupRef{Revision: rev, Target: "P", Index: -1}, false)
	if err == nil || errors.Is(err, ErrRulesChanged) {
		t.Fatalf("err = %v", err)
	}
	if readFileOrEmpty(t, dp) != "a.com/P" {
		t.Fatal("domain.conf changed")
	}
}

// Мутация без изменений не пишет файлы и не дёргает рестарт демона.
func TestMutateRules_NoChangeNoWrite(t *testing.T) {
	svc, dp, ip := setupRuleFiles(t)
	writeRuleFiles(t, dp, ip, "a.com/P", "/P\n1.0.0.0/8")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, p := range []string{dp, ip} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	rev, _ := svc.RulesRevision()
	if err := svc.SetGroupEnabled(GroupRef{Revision: rev, Target: "P", Index: 0}, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dp, ip} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !st.ModTime().Equal(old) {
			t.Errorf("%s rewritten", filepath.Base(p))
		}
	}
	if readFileOrEmpty(t, dp) != "a.com/P" || readFileOrEmpty(t, ip) != "/P\n1.0.0.0/8" {
		t.Fatal("contents changed")
	}
	if svc.restartTimer != nil {
		t.Fatal("restart scheduled for a no-op")
	}
}
