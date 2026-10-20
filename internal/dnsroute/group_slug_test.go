package dnsroute

// F694: имя NDMS-группы списка хранится в самом списке (DomainList.GroupSlug),
// уникально среди списков и не меняется при переименовании.

import (
	"context"
	"testing"
)

func slugsOf(lists []DomainList) []string {
	out := make([]string, 0, len(lists))
	for _, l := range lists {
		out = append(out, l.GroupSlug)
	}
	return out
}

func TestMigrateGroupSlugs(t *testing.T) {
	data := &StoreData{Lists: []DomainList{
		{ID: "list_1", Name: "YouTube"},
		{ID: "list_2", Name: "youtube"},
		{ID: "list_3", Name: "Other"},
	}}
	if !migrateGroupSlugs(data) {
		t.Fatal("первый вызов: изменений нет")
	}
	assertSeq(t, "слаги", slugsOf(data.Lists), "youtube", "youtube_2", "other")

	if migrateGroupSlugs(data) {
		t.Error("повторный вызов вернул true")
	}
	assertSeq(t, "слаги после повтора", slugsOf(data.Lists), "youtube", "youtube_2", "other")

	t.Run("заданный слаг занят и не трогается", func(t *testing.T) {
		data := &StoreData{Lists: []DomainList{
			{ID: "list_1", Name: "YouTube"},
			{ID: "list_2", Name: "x", GroupSlug: "youtube"},
		}}
		if !migrateGroupSlugs(data) {
			t.Fatal("изменений нет")
		}
		assertSeq(t, "слаги", slugsOf(data.Lists), "youtube_1", "youtube")
	})
}

// Суффикс коллидирующего списка не должен занимать нынешнее имя группы
// более позднего списка без коллизии: «youtube 2» уже владеет youtube_2_p1.
func TestMigrateGroupSlugs_SuffixDoesNotStealLaterBase(t *testing.T) {
	data := &StoreData{Lists: []DomainList{
		{ID: "list_1", Name: "YouTube", Domains: []string{"a.example"}},
		{ID: "list_2", Name: "youtube", Domains: []string{"b.example"}},
		{ID: "list_3", Name: "youtube 2", Domains: []string{"c.example"}},
	}}
	migrateGroupSlugs(data)
	assertSeq(t, "слаги", slugsOf(data.Lists), "youtube", "youtube_2_2", "youtube_2")
}

// Пустой список группы на роутере не имеет (buildTargetState его пропускает)
// и не отнимает основу у непустого, который группой владеет.
func TestMigrateGroupSlugs_EmptyListYieldsBase(t *testing.T) {
	data := &StoreData{Lists: []DomainList{
		{ID: "list_1", Name: "YouTube"},
		{ID: "list_2", Name: "youtube", Subnets: []string{"198.51.100.0/24"}},
	}}
	migrateGroupSlugs(data)
	assertSeq(t, "слаги", slugsOf(data.Lists), "youtube_1", "youtube")
}

func TestStoreLoadMigratesGroupSlugs(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := store.Save(&StoreData{Lists: []DomainList{
		{ID: "list_1", Name: "YouTube"},
		{ID: "list_2", Name: "youtube"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).Load(); err != nil {
		t.Fatal(err)
	}
	// Миграция записана на диск: свежий Load видит слаги из файла.
	data, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	assertSeq(t, "слаги", slugsOf(data.Lists), "youtube", "youtube_2")
}

func TestCreateAssignsUniqueGroupSlug(t *testing.T) {
	svc := newServiceWithRouter(t, newFakeRouter())
	a := mustCreate(t, svc, "YouTube", "Wireguard1")
	b := mustCreate(t, svc, "youtube", "Wireguard2")
	assertSeq(t, "слаги", []string{a.GroupSlug, b.GroupSlug}, "youtube", "youtube_2")
	assertSeq(t, "слаги в хранилище", slugsOf(svc.store.GetCached().Lists), "youtube", "youtube_2")
}

func TestCreateIgnoresIncomingGroupSlug(t *testing.T) {
	svc := newServiceWithRouter(t, newFakeRouter())
	l, err := svc.Create(context.Background(), DomainList{
		Name:          "first",
		ManualDomains: []string{"first.example"},
		GroupSlug:     "forged",
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.GroupSlug != "first" {
		t.Errorf("GroupSlug = %q, ожидался first", l.GroupSlug)
	}
}

func TestCreateBatchAssignsUniqueGroupSlug(t *testing.T) {
	svc := newServiceWithRouter(t, newFakeRouter())
	_, err := svc.CreateBatch(context.Background(), []DomainList{
		{Name: "YouTube", ManualDomains: []string{"a.example"}, Routes: routes("Wireguard1")},
		{Name: "youtube", ManualDomains: []string{"b.example"}, Routes: routes("Wireguard2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSeq(t, "слаги", slugsOf(svc.store.GetCached().Lists), "youtube", "youtube_2")
}

// Переименование не трогает группу: ни сноса, ни создания, позиция цела.
func TestUpdateRenameKeepsGroupSlug(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	l := mustCreate(t, svc, "first", "Wireguard1")
	mustCreate(t, svc, "second", "Wireguard2")
	before := waitQuiet(t, r)

	upd := *l
	upd.Name = "renamed"
	upd.GroupSlug = "forged" // пришедшее значение игнорируется
	got, err := svc.Update(context.Background(), upd)
	if err != nil {
		t.Fatal(err)
	}
	if got.GroupSlug != "first" {
		t.Errorf("GroupSlug = %q, ожидался first", got.GroupSlug)
	}
	if s := svc.store.GetCached().Lists[0].GroupSlug; s != "first" {
		t.Errorf("GroupSlug в хранилище = %q, ожидался first", s)
	}
	if n := waitQuiet(t, r) - before; n != 0 {
		t.Errorf("переименование сделало %d POST-ов", n)
	}
	assertSeq(t, "порядок групп", r.groupOrder(), "first_p1", "second_p1")
}

// F694: «YouTube» и «youtube» — две группы, каждая со своим составом и
// туннелем; повторная сверка ничего не трогает.
func TestF694_CollidingNamesGetSeparateGroups(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	ctx := context.Background()
	for _, l := range []DomainList{
		{Name: "YouTube", ManualDomains: []string{"198.51.100.0/24"}, Routes: routes("Wireguard1")},
		{Name: "youtube", ManualDomains: []string{"203.0.113.0/24"}, Routes: routes("Wireguard2")},
	} {
		if _, err := svc.Create(ctx, l); err != nil {
			t.Fatal(err)
		}
	}

	waitQuiet(t, r)
	r.mu.Lock()
	for name, want := range map[string]string{
		"youtube_p1":   "198.51.100.0/24",
		"youtube_2_p1": "203.0.113.0/24",
	} {
		g := r.groups[name]
		if g == nil {
			t.Errorf("группы %s нет", name)
			continue
		}
		assertSeq(t, name+" include", g.includes, want)
	}
	r.mu.Unlock()
	assertSeq(t, "youtube", r.ifaceOrder("youtube_p"), "Wireguard1")
	assertSeq(t, "youtube_2", r.ifaceOrder("youtube_2_p"), "Wireguard2")

	before := waitQuiet(t, r)
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := waitQuiet(t, r) - before; n != 0 {
		t.Errorf("повторная сверка сделала %d POST-ов", n)
	}
}
