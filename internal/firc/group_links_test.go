package firc

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

var (
	chainA = []string{"eth3", "tunvless0"}
	chainB = []string{"eth3", "tunvless1"}
	checkA = GroupCheck{Mode: "url", URL: "https://a.example/204"}
)

func chainGroup(id string, chain []string, check *GroupCheck) Group {
	return Group{ID: id, Name: id, Interface: GroupInterface(chain), Enable: true, Check: check}
}

func groupCheckOf(t *testing.T, groups []Group, id string) GroupCheck {
	t.Helper()
	for _, g := range groups {
		if g.ID == id {
			return normalizeGroupCheck(g.Check)
		}
	}
	t.Fatalf("group %s not found in %+v", id, groups)
	return GroupCheck{}
}

func savedGroups(t *testing.T, op *Operator) []Group {
	t.Helper()
	groups, err := op.LoadGroupsFile()
	if err != nil {
		t.Fatal(err)
	}
	return groups
}

// writeLegacyExt кладёт конфиг прежней версии: поля связей в нём нет.
func writeLegacyExt(t *testing.T, op *Operator, ext ExtConfig) {
	t.Helper()
	raw, err := json.Marshal(ext)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "groupLinks")
	raw, _ = json.Marshal(doc)
	if err := os.WriteFile(op.extConfigPath(), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncGroups_CheckInheritedByLinkOnly(t *testing.T) {
	ext := ExtConfig{
		ProxyGroups: []ProxyGroup{{ID: "pg", Interfaces: chainA, Check: &checkA}},
		GroupLinks:  map[string]string{"linked": "pg", "moved": "pg", "gone": "deleted"},
	}
	own := GroupCheck{Mode: "url", URL: "https://own.example/204"}
	got, _ := syncGroupsWithExt([]Group{
		chainGroup("linked", chainA, nil),
		chainGroup("stranger", chainA, &own),
		chainGroup("moved", chainB, &own),
		chainGroup("gone", chainA, &own),
	}, ext)
	if c := groupCheckOf(t, got, "linked"); c != checkA {
		t.Errorf("linked: %+v", c)
	}
	for _, id := range []string{"stranger", "moved", "gone"} {
		if c := groupCheckOf(t, got, id); c != own {
			t.Errorf("%s must keep its own check, got %+v", id, c)
		}
	}
}

func TestMigrateGroupLinks(t *testing.T) {
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	writeLegacyExt(t, op, ExtConfig{
		ProxyGroups: []ProxyGroup{
			{ID: "pgA", Name: "A", Interfaces: chainA, Check: &checkA},
			{ID: "pgB1", Name: "B1", Interfaces: chainB, Check: &checkA},
			{ID: "pgB2", Name: "B2", Interfaces: chainB},
			{ID: "pgC", Name: "C", Interfaces: []string{"eth3", "tunvless2"}},
			{ID: "pgOne", Name: "One", Interfaces: []string{"eth3"}, Check: &GroupCheck{Mode: "none"}},
		},
		GlobalTarget: "pg:pgA",
	})
	groups := []Group{
		chainGroup("same", chainA, &checkA),
		chainGroup("other_check", chainA, &GroupCheck{Mode: "url", URL: "https://own.example/204"}),
		chainGroup("reordered", []string{"tunvless0", "eth3"}, &checkA),
		chainGroup("two_pgs", chainB, &checkA),
		chainGroup("no_pg_check", []string{"eth3", "tunvless2"}, nil),
		chainGroup("single", []string{"eth3"}, nil),
		chainGroup(globalGroupID, chainA, &checkA),
		chainGroup(KeenDNSGroupID, chainA, &checkA), // без прокси-группы в настройках облака
	}
	if err := op.SaveGroupsFile(groups); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"same": "pgA", globalGroupID: "pgA"}
	if got := op.LoadExtConfigWithStatus(context.Background()).GroupLinks; !maps.Equal(got, want) {
		t.Fatalf("links = %v, want %v", got, want)
	}

	// Однократно: позже совпавшая группа чужой остаётся.
	groups[1].Check = &checkA
	if err := op.SaveGroupsFile(groups); err != nil {
		t.Fatal(err)
	}
	if got := op.LoadExtConfigWithStatus(context.Background()).GroupLinks; !maps.Equal(got, want) {
		t.Fatalf("second pass: links = %v, want %v", got, want)
	}
}

func TestMigrateGroupLinks_SkippedCases(t *testing.T) {
	ext := ExtConfig{ProxyGroups: []ProxyGroup{{ID: "pgA", Name: "A", Interfaces: chainA, Check: &checkA}}}

	t.Run("новая установка", func(t *testing.T) {
		op := NewOperator(Options{ConfigDir: t.TempDir()})
		if err := op.SaveGroupsFile([]Group{chainGroup("g", chainA, &checkA)}); err != nil {
			t.Fatal(err)
		}
		if err := op.SaveExtConfig(ext); err != nil {
			t.Fatal(err)
		}
		if got := op.LoadExtConfigWithStatus(context.Background()).GroupLinks; got == nil || len(got) != 0 {
			t.Fatalf("links = %v, want empty", got)
		}
	})

	t.Run("группы не читаются — повтор позже", func(t *testing.T) {
		op := NewOperator(Options{ConfigDir: t.TempDir()})
		writeLegacyExt(t, op, ext)
		if err := os.WriteFile(op.GroupsPath(), []byte("groups: [:"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Примирение до удачной миграции связей не заводит: пустой набор отменил бы её.
		if err := op.refreshGroupLinks(op.LoadExtConfig()); err != nil {
			t.Fatal(err)
		}
		if got := op.LoadExtConfigWithStatus(context.Background()).GroupLinks; got != nil {
			t.Fatalf("links = %v, want nil", got)
		}
		if err := op.SaveGroupsFile([]Group{chainGroup("g", chainA, &checkA)}); err != nil {
			t.Fatal(err)
		}
		if got := op.LoadExtConfigWithStatus(context.Background()).GroupLinks; got["g"] != "pgA" {
			t.Fatalf("links = %v, want g→pgA", got)
		}
	})

	t.Run("битый конфиг не перезаписывается", func(t *testing.T) {
		op := NewOperator(Options{ConfigDir: t.TempDir()})
		if err := os.WriteFile(op.extConfigPath(), []byte("{broken"), 0o644); err != nil {
			t.Fatal(err)
		}
		op.LoadExtConfigWithStatus(context.Background())
		if raw, _ := os.ReadFile(op.extConfigPath()); string(raw) != "{broken" {
			t.Fatalf("broken config overwritten: %s", raw)
		}
	})
}

func TestStageGroupsOfflineWithLinks(t *testing.T) {
	newOp := func(t *testing.T) *Operator {
		op := NewOperator(Options{ConfigDir: t.TempDir()})
		if err := op.SaveExtConfig(ExtConfig{ProxyGroups: []ProxyGroup{
			{ID: "pgA", Name: "A", Interfaces: chainA, Check: &checkA},
			{ID: "pgB", Name: "B", Interfaces: chainB},
		}}); err != nil {
			t.Fatal(err)
		}
		return op
	}
	links := func(op *Operator) map[string]string { return op.LoadExtConfig().GroupLinks }

	op := newOp(t)
	groups, err := op.StageGroupsOfflineWithLinks(
		[]Group{chainGroup("g1", chainA, nil), chainGroup("g2", chainA, nil), chainGroup("g3", chainB, nil)},
		map[string]string{"g1": "pgA", "g3": "pgB", "g2": "unknown", "absent": "pgA", KeenDNSGroupID: "pgA"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"g1": "pgA", "g3": "pgB"}; !maps.Equal(links(op), want) {
		t.Fatalf("links = %v, want %v", links(op), want)
	}
	if groupCheckOf(t, groups, "g1") != checkA || groupCheckOf(t, groups, "g2").Mode != "none" {
		t.Fatalf("check must follow the link only: %+v", groups)
	}
	if raw, _ := os.ReadFile(op.GroupsPath()); strings.Contains(string(raw), "pgA") {
		t.Fatalf("groups.yaml must not carry links: %s", raw)
	}

	// Клиент без связей (старая вкладка, автолист): цепочка прежняя — связь жива.
	groups[2].Interface = GroupInterface(chainA)
	if _, err := op.StageGroupsOffline(groups[:3]); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"g1": "pgA"}; !maps.Equal(links(op), want) {
		t.Fatalf("after chain change: links = %v, want %v", links(op), want)
	}

	// Пустой набор снимает связи, своя проверка группы остаётся.
	if _, err := op.StageGroupsOfflineWithLinks(groups, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if len(links(op)) != 0 || groupCheckOf(t, savedGroups(t, op), "g1") != checkA {
		t.Fatalf("unlink: links=%v groups=%+v", links(op), savedGroups(t, op))
	}

	// Удалённая группа уносит связь.
	if _, err := op.ApplyGroupsWithLinks(context.Background(), groups, map[string]string{"g1": "pgA"}); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"g1": "pgA"}; !maps.Equal(links(op), want) {
		t.Fatalf("fircd stopped: links = %v, want %v", links(op), want)
	}
	if _, err := op.StageGroupsOffline(groups[1:]); err != nil {
		t.Fatal(err)
	}
	if len(links(op)) != 0 {
		t.Fatalf("deleted group: links = %v", links(op))
	}
}

// Сохранение групп до первого чтения ext: связи выводятся по прежним группам.
func TestMigrateGroupLinks_OnGroupsSave(t *testing.T) {
	for name, save := range map[string]func(*Operator, []Group) ([]Group, error){
		"apply": func(op *Operator, g []Group) ([]Group, error) { return op.ApplyGroups(context.Background(), g) },
		"stage": func(op *Operator, g []Group) ([]Group, error) { return op.StageGroupsOffline(g) },
	} {
		t.Run(name, func(t *testing.T) {
			op := NewOperator(Options{ConfigDir: t.TempDir()})
			writeLegacyExt(t, op, ExtConfig{ProxyGroups: []ProxyGroup{{ID: "pgA", Name: "A", Interfaces: chainA, Check: &checkA}}})
			if err := op.SaveGroupsFile([]Group{chainGroup("g1", chainA, &checkA), chainGroup("g2", chainA, &checkA)}); err != nil {
				t.Fatal(err)
			}
			if _, err := save(op, []Group{chainGroup("g1", chainA, &checkA), chainGroup("g2", chainB, nil), chainGroup("new", chainA, &checkA)}); err != nil {
				t.Fatal(err)
			}
			if got, want := op.LoadExtConfig().GroupLinks, map[string]string{"g1": "pgA"}; !maps.Equal(got, want) {
				t.Fatalf("links = %v, want %v", got, want)
			}
		})
	}
}

func TestExtConfigSave_OwnsNoGroupLinks(t *testing.T) {
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	ext := ExtConfig{ProxyGroups: []ProxyGroup{{ID: "pgA", Name: "A", Interfaces: chainA, Check: &checkA}}}
	if err := op.SaveExtConfig(ext); err != nil {
		t.Fatal(err)
	}
	if _, err := op.StageGroupsOfflineWithLinks([]Group{chainGroup("g1", chainA, nil), chainGroup("g2", chainA, nil)},
		map[string]string{"g1": "pgA"}); err != nil {
		t.Fatal(err)
	}

	// Присланное с конфигом (устаревшая вкладка) связи не меняет; новая проверка — меняет.
	ext.GroupLinks = map[string]string{"g2": "pgA"}
	next := GroupCheck{Mode: "url", URL: "https://next.example/204"}
	ext.ProxyGroups[0].Check = &next
	got, err := op.StageExtConfigOffline(context.Background(), ext)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"g1": "pgA"}; !maps.Equal(got.GroupLinks, want) || !maps.Equal(op.LoadExtConfig().GroupLinks, want) {
		t.Fatalf("links: answer=%v saved=%v, want %v", got.GroupLinks, op.LoadExtConfig().GroupLinks, want)
	}
	groups := savedGroups(t, op)
	if groupCheckOf(t, groups, "g1") != next || groupCheckOf(t, groups, "g2").Mode != "none" {
		t.Fatalf("groups = %+v", groups)
	}

	// Удалённая прокси-группа уносит связь, проверка группы остаётся.
	ext.ProxyGroups = nil
	if _, err := op.StageExtConfigOffline(context.Background(), ext); err != nil {
		t.Fatal(err)
	}
	if got := op.LoadExtConfig().GroupLinks; len(got) != 0 {
		t.Fatalf("deleted proxy group: links = %v", got)
	}
	if c := groupCheckOf(t, savedGroups(t, op), "g1"); c != next {
		t.Fatalf("check = %+v", c)
	}
}

// Прокси-группа без check: проверка, унаследованная от прежней, уходит, своя остаётся.
func TestSpecialGroups_InheritedCheckLeavesWithProxyGroup(t *testing.T) {
	own := GroupCheck{Mode: "url", URL: "https://own.example/204"}
	for name, target := range map[string]func(ext *ExtConfig, pgID string) string{
		"KeenDNS": func(ext *ExtConfig, pgID string) string {
			ext.KeenDNSCloud = KeenDNSCloudConfig{Enabled: true, ProxyGroupID: pgID, Interfaces: chainB}
			return KeenDNSGroupID
		},
		"Global": func(ext *ExtConfig, pgID string) string {
			ext.GlobalTarget = "pg:" + pgID
			return globalGroupID
		},
	} {
		for _, legacy := range []bool{false, true} {
			t.Run(name+map[bool]string{true: ", конфиг прежней версии"}[legacy], func(t *testing.T) {
				ctx := context.Background()
				op := NewOperator(Options{ConfigDir: t.TempDir()})
				ext := ExtConfig{ProxyGroups: []ProxyGroup{
					{ID: "with", Name: "With", Interfaces: chainA, Check: &checkA},
					{ID: "without", Name: "Without", Interfaces: chainB},
				}}
				id := target(&ext, "with")
				if _, err := op.StageExtConfigOffline(ctx, ext); err != nil {
					t.Fatal(err)
				}
				if c := groupCheckOf(t, savedGroups(t, op), id); c != checkA {
					t.Fatalf("inherited check = %+v", c)
				}
				if legacy {
					writeLegacyExt(t, op, ext)
				}

				target(&ext, "without")
				if _, err := op.StageExtConfigOffline(ctx, ext); err != nil {
					t.Fatal(err)
				}
				if c := groupCheckOf(t, savedGroups(t, op), id); c.Mode != "none" || c.URL != "" {
					t.Fatalf("inherited check must go, got %+v", c)
				}

				// Своя проверка, заданная после, переживает и сохранения, и смену прокси-группы.
				groups := savedGroups(t, op)
				for i := range groups {
					if groups[i].ID == id {
						groups[i].Check = &own
					}
				}
				// Связь KeenDNS и Global клиент не задаёт.
				if _, err := op.StageGroupsOfflineWithLinks(groups, map[string]string{id: "with"}); err != nil {
					t.Fatal(err)
				}
				ext.ProxyGroups = append(ext.ProxyGroups, ProxyGroup{ID: "third", Name: "Third", Interfaces: []string{"eth3", "tunvless2"}})
				target(&ext, "third")
				if _, err := op.StageExtConfigOffline(ctx, ext); err != nil {
					t.Fatal(err)
				}
				if c := groupCheckOf(t, savedGroups(t, op), id); c != own {
					t.Fatalf("own check must stay, got %+v", c)
				}
			})
		}
	}
}

// Работающий fircd: связь пишется только после применения, и сорванное применение
// не превращает унаследованную проверку в свою.
func TestGroupLinks_RunningFircd(t *testing.T) {
	resetKeenCloudTestHooks(t)
	InvalidateOutputRoutingCache()
	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	SetExtIpsetRunner(ipsetSt.runner)
	ctx := context.Background()
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	var (
		mu   sync.Mutex
		live []Group
	)
	srv := startMockRunningFircServer(t, op, &mu, &live, iptSt)
	defer srv.Close()
	liveCheck := func(id string) GroupCheck {
		mu.Lock()
		defer mu.Unlock()
		return groupCheckOf(t, live, id)
	}

	ext := ExtConfig{GlobalTarget: "pg:with", ProxyGroups: []ProxyGroup{
		{ID: "with", Name: "With", Interfaces: chainA, Check: &checkA},
		{ID: "without", Name: "Without", Interfaces: chainB},
	}}
	if _, err := op.ApplyExtConfig(ctx, ext); err != nil {
		t.Fatal(err)
	}
	groups, err := op.ApplyGroupsWithLinks(ctx, append(cloneGroups(live), chainGroup("g1", chainA, nil), chainGroup("g2", chainA, nil)),
		map[string]string{"g1": "with"})
	if err != nil {
		t.Fatal(err)
	}
	if groupCheckOf(t, groups, "g1") != checkA || liveCheck("g2").Mode != "none" {
		t.Fatalf("groups = %+v", groups)
	}
	want := map[string]string{"g1": "with", globalGroupID: "with"}
	if got := op.LoadExtConfig().GroupLinks; !maps.Equal(got, want) {
		t.Fatalf("links = %v, want %v", got, want)
	}

	ext.GlobalTarget = "pg:without"
	op.groupsSaveFaultHook = func([]Group) error { return errors.New("disk full") }
	if _, err := op.ApplyExtConfig(ctx, ext); err == nil {
		t.Fatal("expected apply failure")
	}
	if got := op.LoadExtConfig().GroupLinks; !maps.Equal(got, want) || liveCheck(globalGroupID) != checkA {
		t.Fatalf("failed apply: links = %v, check = %+v", got, liveCheck(globalGroupID))
	}
	op.groupsSaveFaultHook = nil
	if _, err := op.ApplyGroups(ctx, cloneGroups(live)); err != nil {
		t.Fatal(err)
	}
	if c := liveCheck(globalGroupID); c.Mode != "none" {
		t.Fatalf("Global keeps the inherited check: %+v", c)
	}
	delete(want, globalGroupID)
	if got := op.LoadExtConfig().GroupLinks; !maps.Equal(got, want) {
		t.Fatalf("links = %v, want %v", got, want)
	}

	// Группы уже верны, а связи ещё не записаны: примирение дописывает.
	ext.GlobalTarget = "pg:with"
	ext.GroupLinks = map[string]string{"g2": "with"}
	answer, err := op.ApplyExtConfig(ctx, ext)
	if err != nil {
		t.Fatal(err)
	}
	want[globalGroupID] = "with"
	if !maps.Equal(answer.GroupLinks, want) {
		t.Fatalf("answer links = %v, want %v", answer.GroupLinks, want)
	}
	if err := op.updateGroupLinks(func(map[string]string) map[string]string { return map[string]string{"g1": "with"} }); err != nil {
		t.Fatal(err)
	}
	if err := op.ReconcileExt(ctx); err != nil {
		t.Fatal(err)
	}
	if got := op.LoadExtConfig().GroupLinks; got[globalGroupID] != "with" {
		t.Fatalf("reconcile must settle links, got %v", got)
	}
	if err := op.updateGroupLinks(func(map[string]string) map[string]string { return map[string]string{"g1": "with"} }); err != nil {
		t.Fatal(err)
	}
	if err := op.reconcileExtOnStartLocked(ctx); err != nil {
		t.Fatal(err)
	}
	if got := op.LoadExtConfig().GroupLinks; got[globalGroupID] != "with" {
		t.Fatalf("start reconcile must settle links, got %v", got)
	}
}

func groupIfaceOf(t *testing.T, groups []Group, id string) []string {
	t.Helper()
	for _, g := range groups {
		if g.ID == id {
			return []string(g.Interface)
		}
	}
	t.Fatalf("group %s not found in %+v", id, groups)
	return nil
}

func TestProxyGroupChainEditPropagatesToLinkedGroups_Offline(t *testing.T) {
	ctx := context.Background()
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	own := GroupCheck{Mode: "url", URL: "https://own.example/204"}
	ext := ExtConfig{ProxyGroups: []ProxyGroup{
		{ID: "pgA", Name: "A", Interfaces: chainA, Check: &checkA},
		{ID: "pgNoCheck", Name: "NoCheck", Interfaces: chainB},
	}}
	if err := op.SaveExtConfig(ext); err != nil {
		t.Fatal(err)
	}
	if _, err := op.StageGroupsOfflineWithLinks([]Group{
		chainGroup("linked", chainA, nil),
		chainGroup("unlinked", chainA, &own),
		chainGroup("diverged", chainB, &own),
		chainGroup("nocheck_linked", chainB, &own),
	}, map[string]string{
		"linked":         "pgA",
		"diverged":       "pgA",
		"nocheck_linked": "pgNoCheck",
	}); err != nil {
		t.Fatal(err)
	}

	// Смена порядка и расширение цепочки в прокси-группе обновляют только связанные группы с прежней цепочкой.
	reordered := []string{"tunvless0", "eth3", "tunvless2"}
	reorderedB := []string{"tunvless1", "eth3"}
	nextCheck := GroupCheck{Mode: "url", URL: "https://next.example/204"}
	ext.ProxyGroups[0].Interfaces = reordered
	ext.ProxyGroups[0].Check = &nextCheck
	ext.ProxyGroups[1].Interfaces = reorderedB
	if _, err := op.StageExtConfigOffline(ctx, ext); err != nil {
		t.Fatal(err)
	}

	groups := savedGroups(t, op)
	if got := groupIfaceOf(t, groups, "linked"); !slices.Equal(got, reordered) {
		t.Fatalf("linked iface = %v, want %v", got, reordered)
	}
	if c := groupCheckOf(t, groups, "linked"); c != nextCheck {
		t.Fatalf("linked check = %+v, want %+v", c, nextCheck)
	}
	if got := groupIfaceOf(t, groups, "unlinked"); !slices.Equal(got, chainA) {
		t.Fatalf("unlinked iface = %v, want %v", got, chainA)
	}
	if got := groupIfaceOf(t, groups, "diverged"); !slices.Equal(got, chainB) || groupCheckOf(t, groups, "diverged") != own {
		t.Fatalf("diverged must stay untouched, iface=%v check=%+v", got, groupCheckOf(t, groups, "diverged"))
	}
	if got := groupIfaceOf(t, groups, "nocheck_linked"); !slices.Equal(got, reorderedB) || groupCheckOf(t, groups, "nocheck_linked") != own {
		t.Fatalf("nocheck_linked iface=%v check=%+v", got, groupCheckOf(t, groups, "nocheck_linked"))
	}
	if got := op.LoadExtConfig().GroupLinks; got["linked"] != "pgA" || got["nocheck_linked"] != "pgNoCheck" {
		t.Fatalf("links must remain intact, got %v", got)
	}

	// Сужение цепочки до одного интерфейса отключает check, но сохраняет связь;
	// последующее расширение снова подтягивает цепочку и проверку.
	single := []string{"tunvless0"}
	ext.ProxyGroups[0].Interfaces = single
	ext.ProxyGroups[0].Check = &GroupCheck{Mode: "none"}
	if _, err := op.StageExtConfigOffline(ctx, ext); err != nil {
		t.Fatal(err)
	}
	groups = savedGroups(t, op)
	if got := groupIfaceOf(t, groups, "linked"); !slices.Equal(got, single) {
		t.Fatalf("shrunk linked iface = %v, want %v", got, single)
	}
	if c := groupCheckOf(t, groups, "linked"); c.Mode != "none" {
		t.Fatalf("shrunk linked check = %+v, want mode none", c)
	}
	if got := op.LoadExtConfig().GroupLinks; got["linked"] != "pgA" {
		t.Fatalf("shrunk linked lost link: %v", got)
	}

	grown := []string{"tunvless0", "tunvless2"}
	ext.ProxyGroups[0].Interfaces = grown
	ext.ProxyGroups[0].Check = &checkA
	if _, err := op.StageExtConfigOffline(ctx, ext); err != nil {
		t.Fatal(err)
	}
	groups = savedGroups(t, op)
	if got := groupIfaceOf(t, groups, "linked"); !slices.Equal(got, grown) {
		t.Fatalf("regrown linked iface = %v, want %v", got, grown)
	}
	if c := groupCheckOf(t, groups, "linked"); c != checkA {
		t.Fatalf("regrown linked check = %+v, want %+v", c, checkA)
	}
}

func TestProxyGroupChainEditPropagatesToLinkedGroups_RunningFircd(t *testing.T) {
	resetKeenCloudTestHooks(t)
	InvalidateOutputRoutingCache()
	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	SetExtIpsetRunner(ipsetSt.runner)

	ctx := context.Background()
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	var (
		mu   sync.Mutex
		live []Group
	)
	srv := startMockRunningFircServer(t, op, &mu, &live, iptSt)
	defer srv.Close()

	ext := ExtConfig{ProxyGroups: []ProxyGroup{
		{ID: "pg", Name: "PG", Interfaces: chainA, Check: &checkA},
	}}
	if _, err := op.ApplyExtConfig(ctx, ext); err != nil {
		t.Fatal(err)
	}
	if _, err := op.ApplyGroupsWithLinks(ctx, []Group{
		chainGroup("g1", chainA, nil),
		chainGroup("g2", chainA, nil),
	}, map[string]string{"g1": "pg"}); err != nil {
		t.Fatal(err)
	}

	reordered := []string{"tunvless0", "eth3"}
	ext.ProxyGroups[0].Interfaces = reordered
	answer, err := op.ApplyExtConfig(ctx, ext)
	if err != nil {
		t.Fatal(err)
	}
	if answer.GroupLinks["g1"] != "pg" || op.LoadExtConfig().GroupLinks["g1"] != "pg" {
		t.Fatalf("link must stay after reorder: answer=%v disk=%v", answer.GroupLinks, op.LoadExtConfig().GroupLinks)
	}

	mu.Lock()
	liveSnap := cloneGroups(live)
	mu.Unlock()
	if got := groupIfaceOf(t, liveSnap, "g1"); !slices.Equal(got, reordered) {
		t.Fatalf("live g1 iface = %v, want %v", got, reordered)
	}
	if got := groupIfaceOf(t, liveSnap, "g2"); !slices.Equal(got, chainA) {
		t.Fatalf("live unlinked g2 iface = %v, want %v", got, chainA)
	}
	if got := groupIfaceOf(t, savedGroups(t, op), "g1"); !slices.Equal(got, reordered) {
		t.Fatalf("disk g1 iface = %v, want %v", got, reordered)
	}

	// Временный сбой сохранения откатывает ProxyGroups в ext, поэтому повторный ApplyExtConfig сходится.
	reorderedAgain := []string{"eth3", "tunvless2"}
	ext.ProxyGroups[0].Interfaces = reorderedAgain
	op.groupsSaveFaultHook = func([]Group) error { return errors.New("transient disk error") }
	if _, err := op.ApplyExtConfig(ctx, ext); err == nil {
		t.Fatal("expected ApplyExtConfig to fail on groupsSaveFaultHook")
	}
	if got := op.LoadExtConfig().ProxyGroups[0].Interfaces; !slices.Equal(got, reordered) {
		t.Fatalf("ProxyGroups must roll back on failed sync, got %v want %v", got, reordered)
	}
	op.groupsSaveFaultHook = nil
	if _, err := op.ApplyExtConfig(ctx, ext); err != nil {
		t.Fatalf("retry ApplyExtConfig: %v", err)
	}
	mu.Lock()
	liveSnap = cloneGroups(live)
	mu.Unlock()
	if got := groupIfaceOf(t, liveSnap, "g1"); !slices.Equal(got, reorderedAgain) {
		t.Fatalf("live g1 iface after retry = %v, want %v", got, reorderedAgain)
	}
	if got := op.LoadExtConfig().GroupLinks["g1"]; got != "pg" {
		t.Fatalf("link lost after retry: %q", got)
	}

	// При ManuallyDisabled (остановленный пользователем FIRC) правка прокси-группы всё равно обновляет groups.yaml.
	srv.Close()
	if err := op.StopByUser(ctx); err != nil {
		t.Fatalf("StopByUser: %v", err)
	}
	disabledOrder := []string{"tunvless2", "eth3"}
	ext.ProxyGroups[0].Interfaces = disabledOrder
	if _, err := op.ApplyExtConfig(ctx, ext); err != nil {
		t.Fatalf("ApplyExtConfig while ManuallyDisabled: %v", err)
	}
	if got := groupIfaceOf(t, savedGroups(t, op), "g1"); !slices.Equal(got, disabledOrder) {
		t.Fatalf("disk g1 iface while ManuallyDisabled = %v, want %v", got, disabledOrder)
	}
	if got := op.LoadExtConfig().GroupLinks["g1"]; got != "pg" {
		t.Fatalf("link lost while ManuallyDisabled: %q", got)
	}

	// Под recovery_required ApplyExtConfig не записывает awgm-ext.json.
	if err := op.SetRecoveryRequired("test lock"); err != nil {
		t.Fatalf("SetRecoveryRequired: %v", err)
	}
	ext.ProxyGroups[0].Interfaces = []string{"eth3"}
	if _, err := op.ApplyExtConfig(ctx, ext); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired, got %v", err)
	}
	if got := op.LoadExtConfig().ProxyGroups[0].Interfaces; !slices.Equal(got, disabledOrder) {
		t.Fatalf("recovery_required must not mutate ext.json, got %v", got)
	}
}

func TestProxyGroupChainEdit_RollsBackOnGroupsLoadErrorAndRespectsLegacyRecovery(t *testing.T) {
	ctx := context.Background()
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	ext := ExtConfig{ProxyGroups: []ProxyGroup{
		{ID: "pg", Name: "PG", Interfaces: chainA, Check: &checkA},
	}}
	if err := op.SaveExtConfig(ext); err != nil {
		t.Fatal(err)
	}
	if _, err := op.StageGroupsOfflineWithLinks([]Group{chainGroup("g1", chainA, nil)}, map[string]string{"g1": "pg"}); err != nil {
		t.Fatal(err)
	}
	validGroupsYAML, err := os.ReadFile(op.GroupsPath())
	if err != nil {
		t.Fatal(err)
	}

	// Битый groups.yaml имитирует ошибку чтения при StageExtConfigOffline: ProxyGroups откатывается,
	// и повторная попытка после восстановления файла доносит новый порядок.
	reordered := []string{"tunvless0", "eth3"}
	ext.ProxyGroups[0].Interfaces = reordered
	if err := os.WriteFile(op.GroupsPath(), []byte("groups: [:"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := op.StageExtConfigOffline(ctx, ext); err == nil {
		t.Fatal("expected StageExtConfigOffline to fail on broken groups.yaml")
	}
	if got := op.LoadExtConfig().ProxyGroups[0].Interfaces; !slices.Equal(got, chainA) {
		t.Fatalf("StageExtConfigOffline must roll back ProxyGroups on LoadGroupsFile error, got %v", got)
	}

	// То же под ManuallyDisabled() в ApplyExtConfig: ошибка не проглатывается, ProxyGroups откатывается.
	if err := op.StopByUser(ctx); err != nil {
		t.Fatalf("StopByUser: %v", err)
	}
	if _, err := op.ApplyExtConfig(ctx, ext); err == nil {
		t.Fatal("expected ApplyExtConfig while ManuallyDisabled to propagate LoadGroupsFile error")
	}
	if got := op.LoadExtConfig().ProxyGroups[0].Interfaces; !slices.Equal(got, chainA) {
		t.Fatalf("ApplyExtConfig(ManuallyDisabled) must roll back ProxyGroups, got %v", got)
	}

	// После восстановления groups.yaml повторная попытка доносит новый порядок и сохраняет связь.
	if err := os.WriteFile(op.GroupsPath(), validGroupsYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := op.ApplyExtConfig(ctx, ext); err != nil {
		t.Fatalf("retry ApplyExtConfig: %v", err)
	}
	if got := groupIfaceOf(t, savedGroups(t, op), "g1"); !slices.Equal(got, reordered) {
		t.Fatalf("g1 iface after retry = %v, want %v", got, reordered)
	}

	// На legacy ext.json (без groupLinks) под recovery_required migrateGroupLinks не должен писать диск.
	opLegacy := NewOperator(Options{ConfigDir: t.TempDir()})
	writeLegacyExt(t, opLegacy, ExtConfig{ProxyGroups: []ProxyGroup{{ID: "pg", Name: "PG", Interfaces: chainA, Check: &checkA}}})
	if err := opLegacy.SaveGroupsFile([]Group{chainGroup("g1", chainA, &checkA)}); err != nil {
		t.Fatal(err)
	}
	beforeRaw, err := os.ReadFile(opLegacy.extConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := opLegacy.SetRecoveryRequired("legacy lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := opLegacy.ApplyExtConfig(ctx, ext); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("ApplyExtConfig legacy recovery err = %v", err)
	}
	if _, err := opLegacy.StageExtConfigOffline(ctx, ext); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("StageExtConfigOffline legacy recovery err = %v", err)
	}
	afterRaw, err := os.ReadFile(opLegacy.extConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(afterRaw) != string(beforeRaw) {
		t.Fatalf("recovery_required mutated legacy ext.json:\nbefore: %s\nafter: %s", beforeRaw, afterRaw)
	}

	// restoreProxyGroups не должен перезаписывать битый awgm-ext.json обнулённой структурой.
	opCorrupt := NewOperator(Options{ConfigDir: t.TempDir()})
	if err := os.WriteFile(opCorrupt.extConfigPath(), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	opCorrupt.restoreProxyGroups([]ProxyGroup{{ID: "pg", Name: "PG", Interfaces: chainA}})
	if raw, _ := os.ReadFile(opCorrupt.extConfigPath()); string(raw) != "{broken" {
		t.Fatalf("restoreProxyGroups overwrote corrupt ext.json: %s", raw)
	}
}
