package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

// Контракт слота эксперт-редактора: зарегистрирован, файл 90-user.json
// (последний СРЕДИ СОДЕРЖАТЕЛЬНЫХ — массивы конкатенируются после всех),
// не AlwaysOn (можно «припарковать» через SetEnabled).
//
// Единственный слот ниже — 99-defaults: он несёт только скаляры-дефолты и
// лежит последним намеренно, чтобы проигрывать всем по first-file-wins.
// Массивов в нём нет, поэтому порядок конкатенации user-слота он не рушит.
func TestKnownSlots_UserSlotRegisteredLast(t *testing.T) {
	slots := KnownSlots()
	idx := -1
	for i, m := range slots {
		if m.Slot == SlotUser {
			idx = i
			if m.Filename != "90-user.json" {
				t.Errorf("user slot filename = %q, want 90-user.json", m.Filename)
			}
			if m.AlwaysOn {
				t.Error("user slot must not be AlwaysOn")
			}
		}
	}
	if idx < 0 {
		t.Fatal("SlotUser not in KnownSlots")
	}
	for i, m := range slots {
		if i == idx || m.Slot == SlotDefaults {
			continue
		}
		if m.Filename >= "90-user.json" {
			t.Errorf("slot %s file %q sorts after 90-user.json — user slot must merge last", m.Slot, m.Filename)
		}
	}
}

// 99-defaults обязан лежать ПОСЛЕ всех: в first-file-wins это и есть
// «дефолт, который проигрывает пассивно». Уедет выше — начнёт затенять
// скаляры режимных слотов, и вернётся весь класс с примирением 00-base.
func TestKnownSlots_DefaultsRegisteredLastOfAll(t *testing.T) {
	slots := KnownSlots()
	var defaults SlotMeta
	for _, m := range slots {
		if m.Slot == SlotDefaults {
			defaults = m
		}
	}
	if defaults.Slot == "" {
		t.Fatal("SlotDefaults not in KnownSlots")
	}
	if defaults.Filename != "99-defaults.json" {
		t.Errorf("defaults filename = %q, want 99-defaults.json", defaults.Filename)
	}
	if !defaults.AlwaysOn {
		t.Error("defaults slot must be AlwaysOn — дефолты обязаны действовать в любом режиме")
	}
	if defaults.HasContent != nil {
		t.Error("defaults slot must not gate hasActiveWork — сам по себе демона не поднимает")
	}
	for _, m := range slots {
		if m.Slot != SlotDefaults && m.Filename >= defaults.Filename {
			t.Errorf("slot %s file %q sorts at/after %q — defaults must merge last",
				m.Slot, m.Filename, defaults.Filename)
		}
	}
}

// setupUserOrch регистрирует awg (источник тегов) + user и включает оба.
func setupUserOrch(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	dir := t.TempDir()
	o := New(dir, nil)
	for _, meta := range KnownSlots() {
		if meta.Slot == SlotAwg || meta.Slot == SlotUser {
			if err := o.Register(meta); err != nil {
				t.Fatalf("register %s: %v", meta.Slot, err)
			}
		}
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	o.enabled[SlotUser] = true
	return o, dir
}

// Правки пользователя не мутируем молча: висячая ссылка в селекторе
// 90-user.json переживает prune байт-в-байт (системный слот в тех же
// условиях был бы переписан — это покрывает существующий контракт prune).
func TestPruneDanglingSelectorRefs_SkipsUserSlot(t *testing.T) {
	o, dir := setupUserOrch(t)
	userJSON := []byte(`{
  "outbounds": [
    {"type": "selector", "tag": "my-select", "outbounds": ["direct", "ghost-tag"], "default": "ghost-tag"}
  ]
}`)
	userPath := filepath.Join(dir, "90-user.json")
	if err := os.WriteFile(userPath, userJSON, 0644); err != nil {
		t.Fatal(err)
	}

	o.mu.Lock()
	logs := o.pruneDanglingSelectorRefsLocked()
	o.mu.Unlock()

	got, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("read after prune: %v", err)
	}
	if string(got) != string(userJSON) {
		t.Errorf("user slot mutated by prune:\nwant %s\ngot  %s\nlogs: %v", userJSON, got, logs)
	}
}

// Тот же конфиг в СИСТЕМНОМ слоте prune чинит — контроль, что скип выше
// объясняется именно исключением user-слота, а не пассивностью prune.
func TestPruneDanglingSelectorRefs_StillPrunesSystemSlot(t *testing.T) {
	o, dir := setupUserOrch(t)
	sysJSON := []byte(`{
  "outbounds": [
    {"type": "selector", "tag": "sys-select", "outbounds": ["direct", "ghost-tag"]}
  ]
}`)
	sysPath := filepath.Join(dir, "15-awg.json")
	if err := os.WriteFile(sysPath, sysJSON, 0644); err != nil {
		t.Fatal(err)
	}

	o.mu.Lock()
	logs := o.pruneDanglingSelectorRefsLocked()
	o.mu.Unlock()

	got, err := os.ReadFile(sysPath)
	if err != nil {
		t.Fatalf("read after prune: %v", err)
	}
	if string(got) == string(sysJSON) {
		t.Errorf("system slot NOT pruned (logs: %v):\n%s", logs, got)
	}
}

// Полный draft-цикл user-слота: SaveDraft → CheckMerged (ok и провал) →
// ApplyDraft → файл в active/, pending снят.
func TestUserSlot_DraftCycle(t *testing.T) {
	o, dir := setupUserOrch(t)

	good := []byte(`{"outbounds":[{"type":"direct","tag":"user-direct"}]}`)
	if err := o.SaveDraft(SlotUser, good); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if res, err := o.CheckMerged(SlotUser, good); err != nil || !res.Ok() {
		t.Fatalf("CheckMerged good: res=%v err=%v", res.Errors, err)
	}

	// Провал: правило ссылается на несуществующий outbound.
	bad := []byte(`{"route":{"rules":[{"outbound":"no-such-tag"}]}}`)
	res, err := o.CheckMerged(SlotUser, bad)
	if err != nil {
		t.Fatalf("CheckMerged bad: %v", err)
	}
	if res.Ok() {
		t.Fatal("CheckMerged bad: expected validation errors")
	}
	if res.Errors[0].Slot != SlotUser || res.Errors[0].Kind != "unknown-outbound" {
		t.Errorf("unexpected error attribution: %+v", res.Errors[0])
	}

	applyRes, err := o.ApplyDraft(SlotUser)
	if err != nil {
		t.Fatalf("ApplyDraft: %v", err)
	}
	if !applyRes.Ok() {
		t.Fatalf("ApplyDraft validation: %v", applyRes.Errors)
	}
	active, err := os.ReadFile(filepath.Join(dir, "90-user.json"))
	if err != nil {
		t.Fatalf("active file missing after apply: %v", err)
	}
	if string(active) != string(good) {
		t.Errorf("active content mismatch: %s", active)
	}
	if o.HasDraft(SlotUser) {
		t.Error("pending file survived apply")
	}
}

// Провал валидации на reload сохраняется в LastReloadValidation и
// сбрасывается после успешного reload (proc=nil — тестовый режим).
func TestLastReloadValidation_SetAndCleared(t *testing.T) {
	o, dir := setupUserOrch(t)
	userPath := filepath.Join(dir, "90-user.json")
	if err := os.WriteFile(userPath,
		[]byte(`{"route":{"rules":[{"outbound":"ghost-tag"}]}}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := o.Reload(); err == nil {
		t.Fatal("Reload: expected validation error")
	}
	v := o.LastReloadValidation()
	if v == nil || len(v.Errors) == 0 {
		t.Fatal("LastReloadValidation nil after failed reload")
	}
	if v.Errors[0].Slot != SlotUser {
		t.Errorf("failing slot = %s, want user", v.Errors[0].Slot)
	}

	if err := os.WriteFile(userPath, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := o.Reload(); err != nil {
		t.Fatalf("Reload after fix: %v", err)
	}
	if o.LastReloadValidation() != nil {
		t.Error("LastReloadValidation not cleared after successful reload")
	}
}

// Включённый, но содержательно пустой user-слот (90-user.json с «{}») не
// должен держать/запускать процесс sing-box; хотя бы один содержательный
// массив (outbounds/inbounds/dns.*/route.*) — уже работа для демона.
func TestHasActiveWork_UserSlot_EmptyVsContent(t *testing.T) {
	o, dir := setupUserOrch(t) // awg (AlwaysOn, без HasContent) + user (enabled)
	userPath := filepath.Join(dir, "90-user.json")

	// Слот включён, файла нет — не работа.
	if o.HasActiveWork() {
		t.Error("enabled user slot without a file must not be active work")
	}

	// Пустой объект — не работа.
	if err := os.WriteFile(userPath, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if o.HasActiveWork() {
		t.Error("enabled user slot with {} must not be active work")
	}

	// Пустые массивы — тоже не работа.
	if err := os.WriteFile(userPath, []byte(`{"outbounds":[],"route":{"rules":[]}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if o.HasActiveWork() {
		t.Error("enabled user slot with empty arrays must not be active work")
	}

	// Один outbound — работа есть.
	if err := os.WriteFile(userPath, []byte(`{"outbounds":[{"type":"direct","tag":"u-direct"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if !o.HasActiveWork() {
		t.Error("enabled user slot with an outbound must be active work")
	}

	// Выключенный слот — не работа независимо от содержимого.
	if err := o.SetEnabled(SlotUser, false); err != nil {
		t.Fatal(err)
	}
	if o.HasActiveWork() {
		t.Error("disabled user slot must not be active work")
	}
}
