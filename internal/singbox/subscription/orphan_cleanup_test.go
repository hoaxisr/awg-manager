package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// The real Reload commits the slot before the injected store write failure.
type orphanCleanupMutator struct {
	*OperatorAdapter
	afterReload func()
}

func (m *orphanCleanupMutator) Reload(ctx context.Context) error {
	if err := m.OperatorAdapter.Reload(ctx); err != nil {
		return err
	}
	if hook := m.afterReload; hook != nil {
		m.afterReload = nil
		hook()
	}
	return nil
}

type orphanCleanupEnv struct {
	t        *testing.T
	cfgDir   string
	feedPath string
	orch     *orchestrator.Orchestrator
	mut      *orphanCleanupMutator
	store    *Store
	svc      *Service
}

func newOrphanCleanupEnv(t *testing.T) *orphanCleanupEnv {
	t.Helper()
	e := &orphanCleanupEnv{t: t, cfgDir: t.TempDir()}
	e.orch = orchestrator.NewWithAppliedPath(e.cfgDir, nil, filepath.Join(t.TempDir(), "applied.json"))
	t.Cleanup(e.orch.Close)
	// Пользовательский слот — для ссылок на сироту из чужого слота; выключен,
	// пока тест его не включит.
	for _, meta := range orchestrator.KnownSlots() {
		if meta.Slot == orchestrator.SlotUser || meta.Slot == orchestrator.SlotRouter {
			if err := e.orch.Register(meta); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := e.orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	e.mut = &orphanCleanupMutator{OperatorAdapter: NewOperatorAdapter(e.orch, nil, nil)}
	var err error
	e.store, err = NewStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	if err != nil {
		t.Fatal(err)
	}
	e.svc = NewService(e.store, e.mut)
	e.svc.SetNDMSProxyEnabled(func() bool { return false })
	e.feedPath = filepath.Join(fileSandbox(t, e.svc), "feed.txt")
	return e
}

func (e *orphanCleanupEnv) feed(hosts ...string) {
	e.t.Helper()
	var lines []string
	for _, host := range hosts {
		lines = append(lines, "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@"+host+":443?security=tls&sni=h#"+host)
	}
	if err := os.WriteFile(e.feedPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *orphanCleanupEnv) subscription(id string) *Subscription {
	e.t.Helper()
	sub, err := e.store.Get(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return sub
}

// Load with a fresh adapter so assertions cover persisted state, not just
// the original adapter's working copy.
func (e *orphanCleanupEnv) diskSlot() slotConfig {
	e.t.Helper()
	fresh := NewOperatorAdapter(e.orch, nil, nil)
	if err := fresh.LoadFromDisk(e.cfgDir); err != nil {
		e.t.Fatal(err)
	}
	return fresh.cfg
}

func (e *orphanCleanupEnv) workingSlot() []byte {
	e.t.Helper()
	e.mut.mu.Lock()
	defer e.mut.mu.Unlock()
	data, err := json.Marshal(e.mut.cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	return data
}

func orphanCleanupGroup(cfg slotConfig, tag string) (members []string, routes int) {
	for _, raw := range cfg.Outbounds {
		ob, _ := raw.(map[string]any)
		if outboundTag(ob) == tag {
			rawMembers, _ := ob["outbounds"].([]any)
			for _, member := range rawMembers {
				members = append(members, stringOf(member))
			}
		}
	}
	if rules, ok := cfg.Route["rules"].([]any); ok {
		for _, raw := range rules {
			rule, _ := raw.(map[string]any)
			if rule["outbound"] == tag {
				routes++
			}
		}
	}
	return members, routes
}

func orphanCleanupRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// An empty directory at the target makes AtomicWrite's rename fail even
// when tests run as root. Only the test's metadata file is obstructed;
// the real slot writes and validation still run normally.
func orphanCleanupBlockStore(t *testing.T, path string) func() {
	t.Helper()
	backup := path + ".before-failure"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	blocked, restored := false, false
	restore := func() {
		t.Helper()
		if restored {
			return
		}
		if blocked {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			blocked = false
		}
		if err := os.Rename(backup, path); err != nil {
			t.Fatal(err)
		}
		restored = true
	}
	t.Cleanup(restore)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	blocked = true
	return restore
}

func (e *orphanCleanupEnv) checkClean(id string) {
	e.t.Helper()
	sub := e.subscription(id)
	cfg := e.diskSlot()
	members, routes := orphanCleanupGroup(cfg, sub.SelectorTag)
	var tags []string
	for _, raw := range cfg.Outbounds {
		ob, _ := raw.(map[string]any)
		tags = append(tags, outboundTag(ob))
	}
	want := append([]string{sub.SelectorTag}, sub.MemberTags...)
	slices.Sort(tags)
	slices.Sort(want)
	if !slices.Equal(tags, want) || !slices.Equal(members, sub.MemberTags) || routes != 1 || len(sub.OrphanTags) != 0 {
		e.t.Errorf("cleanup: tags=%v, group=%v, routes=%d, stored members=%v, orphans=%v", tags, members, routes, sub.MemberTags, sub.OrphanTags)
	}
}

func TestDeleteOrphans_StaleStore(t *testing.T) {
	for _, mode := range []SubscriptionMode{ModeSelector, ModeURLTest, ModeFallback} {
		for _, tc := range []struct {
			name  string
			hosts []string
		}{
			{"full_rotation", []string{"b1.example", "b2.example"}},
			{"partial_rotation", []string{"a1.example", "b1.example"}},
		} {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				e := newOrphanCleanupEnv(t)
				ctx := context.Background()
				e.feed("c1.example")
				sub, err := e.svc.Create(ctx, CreateInput{Label: "cleanup", Path: e.feedPath, Enabled: true, Mode: mode})
				if err != nil {
					t.Fatal(err)
				}
				e.feed("a1.example", "a2.example")
				if _, err := e.svc.Refresh(ctx, sub.ID); err != nil {
					t.Fatal(err)
				}
				afterA := e.subscription(sub.ID)
				// C выпал из выдачи — обновление сняло его само (#1081).
				if len(afterA.OrphanTags) != 0 {
					t.Fatalf("orphans after refresh = %v, want none", afterA.OrphanTags)
				}
				beforeStore := orphanCleanupRead(t, e.store.path)
				e.feed(tc.hosts...)
				var restore func()
				e.mut.afterReload = func() { restore = orphanCleanupBlockStore(t, e.store.path) }
				_, refreshErr := e.svc.Refresh(ctx, sub.ID)
				if restore == nil {
					t.Fatalf("Refresh did not commit its slot: %v", refreshErr)
				}
				restore()
				var renameErr *os.LinkError
				if !errors.As(refreshErr, &renameErr) || renameErr.New != e.store.path {
					t.Fatalf("want metadata rename failure after Reload, got %v", refreshErr)
				}
				stale := e.subscription(sub.ID)
				if !slices.Equal(stale.MemberTags, afterA.MemberTags) || !slices.Equal(stale.OrphanTags, afterA.OrphanTags) {
					t.Fatal("failed store write changed membership")
				}
				beforeDisk := e.diskSlot()
				members, routes := orphanCleanupGroup(beforeDisk, sub.SelectorTag)
				newMembers := 0
				for _, member := range members {
					if !slices.Contains(stale.MemberTags, member) {
						newMembers++
					}
				}
				if len(members) != 2 || routes != 1 || newMembers == 0 {
					t.Fatalf("need a newer live group: members=%v, routes=%d, stored=%v", members, routes, stale.MemberTags)
				}
				beforeWorking := e.workingSlot()
				err = e.svc.DeleteOrphans(ctx, sub.ID)
				if !errors.Is(err, ErrValidation) {
					t.Errorf("DeleteOrphans = %v, want ErrValidation", err)
				}
				// Keep checking state even if the error assertion fails: on the
				// regression, nil concealed removal of live members and routes.
				if afterDisk := e.diskSlot(); !reflect.DeepEqual(afterDisk, beforeDisk) {
					afterMembers, afterRoutes := orphanCleanupGroup(afterDisk, sub.SelectorTag)
					t.Errorf("disk slot changed: group %v -> %v, routes %d -> %d", members, afterMembers, routes, afterRoutes)
				}
				if !bytes.Equal(e.workingSlot(), beforeWorking) {
					t.Error("working slot changed on refusal")
				}
				if !bytes.Equal(orphanCleanupRead(t, e.store.path), beforeStore) || !reflect.DeepEqual(e.subscription(sub.ID), stale) {
					t.Error("store changed on refusal")
				}
				e.mut.mu.Lock()
				pending := e.mut.pending != nil
				e.mut.mu.Unlock()
				if pending {
					t.Error("refusal left a staged batch")
				}
				// The suggested Refresh must restore agreement and permit cleanup.
				if _, err := e.svc.Refresh(ctx, sub.ID); err != nil {
					t.Fatal(err)
				}
				if err := e.svc.DeleteOrphans(ctx, sub.ID); err != nil {
					t.Fatal(err)
				}
				e.checkClean(sub.ID)
			})
		}
	}
}

func TestDeleteOrphans_ConsistentStoreAllGenerations(t *testing.T) {
	e := newOrphanCleanupEnv(t)
	ctx := context.Background()
	e.feed("c1.example")
	sub, err := e.svc.Create(ctx, CreateInput{Label: "cleanup", Path: e.feedPath, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"a1.example", "d1.example"} {
		e.feed(host)
		if _, err := e.svc.Refresh(ctx, sub.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.DeleteOrphans(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	e.checkClean(sub.ID)
}

func TestDeleteOrphans_ReferencedDefault(t *testing.T) {
	e := newOrphanCleanupEnv(t)
	ctx := context.Background()
	e.feed("c1.example")
	sub, err := e.svc.Create(ctx, CreateInput{Label: "cleanup", Path: e.feedPath, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	e.feed("a1.example")
	if _, err := e.svc.Refresh(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	// Synthetic inconsistent default: normally it belongs to outbounds.
	// Cover this reference field independently of the rotation scenarios.
	// Сироту обновление снимает само (#1081), поэтому кладём её в рабочий
	// слот явно.
	candidate := sub.SelectorTag + "-deadbeef"
	e.mut.mu.Lock()
	e.mut.cfg.Outbounds = append(e.mut.cfg.Outbounds, map[string]any{"type": "direct", "tag": candidate})
	for _, raw := range e.mut.cfg.Outbounds {
		ob, _ := raw.(map[string]any)
		if outboundTag(ob) == sub.SelectorTag {
			ob["default"] = candidate
		}
	}
	e.mut.mu.Unlock()
	beforeWorking, beforeDisk := e.workingSlot(), e.diskSlot()
	beforeStore, beforeSub := orphanCleanupRead(t, e.store.path), e.subscription(sub.ID)
	if err := e.svc.DeleteOrphans(ctx, sub.ID); !errors.Is(err, ErrValidation) {
		t.Errorf("DeleteOrphans = %v, want ErrValidation", err)
	}
	if !bytes.Equal(beforeWorking, e.workingSlot()) || !reflect.DeepEqual(beforeDisk, e.diskSlot()) {
		t.Error("slot changed on refusal")
	}
	if !bytes.Equal(beforeStore, orphanCleanupRead(t, e.store.path)) || !reflect.DeepEqual(beforeSub, e.subscription(sub.ID)) {
		t.Error("store changed on refusal")
	}
	e.mut.mu.Lock()
	pending := e.mut.pending != nil
	e.mut.mu.Unlock()
	if pending {
		t.Error("refusal left a staged batch")
	}
}

// Сирота, на которую ссылается другой слот, обновлением не снимается: иначе
// общая проверка падала бы на висячей ссылке на каждом тике (#1081).
// Несвязанная сирота того же обновления — снимается; ручная очистка ссылку
// тоже уважает.
func TestRefresh_OrphanReferencedElsewhereKept(t *testing.T) {
	user := `{"route":{"rules":[{"domain":["x.example"],"outbound":"KEPT"}]}}`
	e, kept, gone, err := orphanRefCase(t, "90-user.json", user, orchestrator.SlotUser)
	if err != nil {
		t.Fatalf("обновление упало на ссылке на сироту: %v", err)
	}
	tags := diskTags(e)
	if !slices.Contains(tags, kept) {
		t.Errorf("сирота со ссылкой из 90-user снята: слот %v", tags)
	}
	if slices.Contains(tags, gone) {
		t.Errorf("сирота без ссылок осталась: слот %v", tags)
	}
	id := e.store.List()[0].ID
	if got := e.subscription(id).OrphanTags; !slices.Equal(got, []string{kept}) {
		t.Errorf("OrphanTags = %v, want [%s]", got, kept)
	}
	if err := e.svc.DeleteOrphans(context.Background(), id); err != nil {
		t.Fatalf("DeleteOrphans: %v", err)
	}
	if !slices.Contains(diskTags(e), kept) {
		t.Error("DeleteOrphans снял сироту со ссылкой из 90-user")
	}
	if got := e.subscription(id).OrphanTags; !slices.Equal(got, []string{kept}) {
		t.Errorf("после очистки OrphanTags = %v, want [%s]", got, kept)
	}
}

// orphanRefCase: подписка c1+c2 → обновление на a1; ссылка на c1 лежит в
// файле другого слота по пути rel (относительно каталога конфигов).
func orphanRefCase(t *testing.T, rel, doc string, enable orchestrator.Slot) (e *orphanCleanupEnv, kept, gone string, refreshErr error) {
	t.Helper()
	e = newOrphanCleanupEnv(t)
	ctx := context.Background()
	e.feed("c1.example", "c2.example")
	sub, err := e.svc.Create(ctx, CreateInput{Label: "cleanup", Path: e.feedPath, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range e.subscription(sub.ID).Members {
		if strings.Contains(m.Server, "c1") {
			kept = m.Tag
		} else {
			gone = m.Tag
		}
	}
	path := filepath.Join(e.cfgDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(doc, "KEPT", kept)), 0o644); err != nil {
		t.Fatal(err)
	}
	if enable != "" {
		if err := e.orch.SetEnabled(enable, true); err != nil {
			t.Fatal(err)
		}
	}
	e.feed("a1.example")
	_, refreshErr = e.svc.Refresh(ctx, sub.ID)
	return e, kept, gone, refreshErr
}

func diskTags(e *orphanCleanupEnv) []string {
	var tags []string
	for _, raw := range e.diskSlot().Outbounds {
		ob, _ := raw.(map[string]any)
		tags = append(tags, outboundTag(ob))
	}
	return tags
}

// Ссылка из группы соседнего слота (не 90-user): валидатор виртуально
// вычищает висячих членов таких групп, а Reload — на диске, поэтому защита
// не может опираться на ошибку проверки (ревью #1081). Слот активен,
// черновик или выключен — сирота остаётся; несвязанная снимается.
func TestRefresh_OrphanReferencedFromOtherSlotStates(t *testing.T) {
	group := `{"outbounds":[{"type":"selector","tag":"grp","outbounds":["direct","KEPT"],"default":"KEPT"},{"type":"direct","tag":"direct"}]}`
	for _, tc := range []struct {
		name, rel string
		enable    orchestrator.Slot
	}{
		{"active", "20-router.json", orchestrator.SlotRouter},
		{"disabled", filepath.Join("disabled", "20-router.json"), ""},
		{"draft", filepath.Join("pending", "20-router.json"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, kept, gone, err := orphanRefCase(t, tc.rel, group, tc.enable)
			if err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			tags := diskTags(e)
			if !slices.Contains(tags, kept) {
				t.Errorf("сирота со ссылкой (%s) снята: слот %v", tc.name, tags)
			}
			if slices.Contains(tags, gone) {
				t.Errorf("сирота без ссылок осталась: слот %v", tags)
			}
			// Ручная очистка — та же проверка: член чужой группы не вычищается.
			id := e.store.List()[0].ID
			if err := e.svc.DeleteOrphans(context.Background(), id); err != nil {
				t.Fatalf("DeleteOrphans: %v", err)
			}
			if !slices.Contains(diskTags(e), kept) {
				t.Errorf("DeleteOrphans снял сироту со ссылкой (%s)", tc.name)
			}
			if tc.enable != "" {
				raw, err := os.ReadFile(filepath.Join(e.cfgDir, tc.rel))
				if err != nil || !strings.Contains(string(raw), kept) {
					t.Errorf("член группы вычищен из %s: %s", tc.rel, raw)
				}
			}
		})
	}
}

// Невалидный JSON в файле другого слота (черновик сохраняется без проверки)
// не выключает чистку: тег ищется в нём строкой — сирота с упоминанием
// остаётся, остальные снимаются.
func TestRefresh_InvalidJSONOtherSlotSearchedAsText(t *testing.T) {
	e, kept, gone, err := orphanRefCase(t, filepath.Join("pending", "20-router.json"), `{not json "KEPT"}`, "")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	tags := diskTags(e)
	if !slices.Contains(tags, kept) || slices.Contains(tags, gone) {
		t.Errorf("слот %v: ждали %s на месте и без %s", tags, kept, gone)
	}
}

// Файл другого слота не читается — ответ «кто ссылается» неполон: сирот не
// трогаем, обновление при этом проходит, а ручная очистка честно отказывает.
func TestRefresh_UnreadableOtherSlotKeepsOrphans(t *testing.T) {
	e, kept, gone, err := orphanRefCase(t, filepath.Join("disabled", "20-router.json", "x"), `{}`, "")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	tags := diskTags(e)
	if !slices.Contains(tags, kept) || !slices.Contains(tags, gone) {
		t.Errorf("слоты не прочитаны, а сироты сняты: слот %v", tags)
	}
	if err := e.svc.DeleteOrphans(context.Background(), e.store.List()[0].ID); !errors.Is(err, ErrValidation) {
		t.Errorf("DeleteOrphans при нечитаемых слотах = %v, want ErrValidation", err)
	}
	if after := diskTags(e); !slices.Equal(after, tags) {
		t.Errorf("отказ очистки изменил слот: %v -> %v", tags, after)
	}
}

// failRemoveMutator — RemoveOutbound отказывает на одном теге.
type failRemoveMutator struct {
	*fakeMutator
	fail string
}

func (m *failRemoveMutator) RemoveOutbound(tag string) error {
	if tag == m.fail {
		return errors.New("injected")
	}
	return m.fakeMutator.RemoveOutbound(tag)
}

// Не снятая сирота остаётся в учёте (kept), а не пропадает из обоих списков.
func TestRemoveUnreferencedOrphans_FailedRemovalKept(t *testing.T) {
	m := &failRemoveMutator{fakeMutator: &fakeMutator{}, fail: "sub-x-b"}
	s := &Service{mutator: m}
	removed, kept, err := s.removeUnreferencedOrphans([]string{"sub-x-a", "sub-x-b"})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || !slices.Equal(kept, []string{"sub-x-b"}) {
		t.Fatalf("removed=%d kept=%v, want 1 [sub-x-b]", removed, kept)
	}
}
