package exitreg

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeStore struct {
	m       map[string]storage.AWGTunnel
	saves   int
	deleted []string
	getErr  error // «запись есть, но не читается»
}

func newFakeStore() *fakeStore { return &fakeStore{m: map[string]storage.AWGTunnel{}} }

func (f *fakeStore) Get(id string) (*storage.AWGTunnel, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	t, ok := f.m[id]
	if !ok {
		// ФАКТ: настоящий AWGTunnelStore.Get отдаёт обычную ошибку без
		// sentinel'а (awg_store.go:88-114). Фейк повторяет эту форму —
		// именно она заставляет зеркало спрашивать Exists.
		return nil, fmt.Errorf("tunnel not found: %s", id)
	}
	cp := t
	return &cp, nil
}

func (f *fakeStore) Exists(id string) bool { _, ok := f.m[id]; return ok }

func (f *fakeStore) Save(t *storage.AWGTunnel) error {
	f.saves++
	f.m[t.ID] = *t
	return nil
}

func (f *fakeStore) Delete(id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.m, id)
	return nil
}

func (f *fakeStore) List() ([]storage.AWGTunnel, error) {
	out := make([]storage.AWGTunnel, 0, len(f.m))
	for _, t := range f.m {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

type fakePub struct{ published []string }

func (p *fakePub) Publish(eventType string, _ any) { p.published = append(p.published, eventType) }

func TestMirrorCreatesRecordFromDeclaration(t *testing.T) {
	st, pub := newFakeStore(), &fakePub{}
	m := NewStoreMirror(st, pub)

	if err := m.Ensure(decl("wdttraw-de", "OpkgTun18", "opkgtun18")); err != nil {
		t.Fatal(err)
	}
	rec := st.m["wdttraw-de"]
	if rec.Backend != backendWdttRaw || rec.WdttClientID != "de" {
		t.Fatalf("идентичность записи: %+v", rec)
	}
	if rec.RawNdmsIface != "OpkgTun18" || rec.RawKernelIface != "opkgtun18" {
		t.Fatalf("имена интерфейсов: %+v", rec)
	}
	if rec.Name != "Германия wdtt" || rec.Type != "awg" {
		t.Fatalf("имя/тип: %+v", rec)
	}
	if rec.Peer.Endpoint != "1.2.3.4:56000" || len(rec.Peer.AllowedIPs) != 1 {
		t.Fatalf("peer: %+v", rec)
	}
	if !rec.Enabled {
		t.Fatalf("Enabled — поле владения зеркала (В7), намерение обязано доехать: %+v", rec)
	}
	if rec.ConnectivityCheck == nil || rec.CreatedAt == "" {
		t.Fatalf("дефолты создания: %+v", rec)
	}
	if rec.Interface.Address != "" {
		t.Fatalf("адрес — объявленный резидуал (В2), зеркало его не пишет: %+v", rec)
	}
	if len(pub.published) == 0 {
		t.Fatal("создание записи обязано инвалидировать список туннелей")
	}
}

func TestMirrorWritesIntentBothWays(t *testing.T) {
	// M2: пара к предыдущей проверке. Вместе они запирают Enabled с обеих
	// сторон — мутация «не писать rec.Enabled» умирает и на создании, и на
	// обновлении, а не проходит зелёной.
	st, pub := newFakeStore(), &fakePub{}
	m := NewStoreMirror(st, pub)
	d := decl("wdttraw-de", "OpkgTun18", "opkgtun18") // Enabled: true
	if err := m.Ensure(d); err != nil {
		t.Fatal(err)
	}
	if !st.m["wdttraw-de"].Enabled {
		t.Fatal("намерение «включён» не доехало на создании")
	}

	d.Enabled = false
	if err := m.Ensure(d); err != nil {
		t.Fatal(err)
	}
	if st.m["wdttraw-de"].Enabled {
		t.Fatal("намерение «выключен» не доехало на обновлении: Enabled пишется ВСЕГДА")
	}
}

func TestMirrorPreservesUserFields(t *testing.T) {
	st, pub := newFakeStore(), &fakePub{}
	m := NewStoreMirror(st, pub)
	_ = m.Ensure(decl("wdttraw-de", "OpkgTun18", "opkgtun18"))

	// Пользователь правит своё через API туннелей.
	rec := st.m["wdttraw-de"]
	rec.PingCheck = &storage.TunnelPingCheck{Enabled: true}
	rec.DefaultRoute = true
	rec.ISPInterface = "ISP2"
	rec.Interface.Address = "10.8.0.5/32"
	rec.CreatedAt = "2020-01-01T00:00:00Z"
	st.m["wdttraw-de"] = rec

	// Перепиновка индекса — обновление записи.
	if err := m.Ensure(decl("wdttraw-de", "OpkgTun19", "opkgtun19")); err != nil {
		t.Fatal(err)
	}
	got := st.m["wdttraw-de"]
	if got.RawKernelIface != "opkgtun19" {
		t.Fatalf("пин не обновился: %+v", got)
	}
	if got.PingCheck == nil || !got.PingCheck.Enabled || !got.DefaultRoute || got.ISPInterface != "ISP2" {
		t.Fatalf("пользовательские поля затёрты: %+v", got)
	}
	if got.Interface.Address != "10.8.0.5/32" {
		t.Fatalf("адрес обязан пережить обновление нетронутым: %+v", got)
	}
	if got.CreatedAt != "2020-01-01T00:00:00Z" {
		t.Fatalf("CreatedAt перезаписан: %+v", got)
	}
}

func TestMirrorRefusesUnreadableRecord(t *testing.T) {
	// C3. Запись ЕСТЬ, но не читается — битый JSON, ошибка чтения, права.
	// Пересобрать её с дефолтами значит стереть PingCheck/Address/ISPInterface,
	// а различить эти три случая в storage нечем: sentinel'а нет.
	// Гарантия локальна (см. «Граница гарантии» в шапке задачи): настоящий
	// List() карантинит битый JSON в <id>.json.corrupt, после чего Exists
	// даёт false — тест закрывает поведение самого Ensure, не судьбу файла.
	st, pub := newFakeStore(), &fakePub{}
	st.m["wdttraw-de"] = storage.AWGTunnel{ID: "wdttraw-de", Backend: backendWdttRaw}
	st.getErr = errors.New("parse tunnel JSON: unexpected end of input")
	m := NewStoreMirror(st, pub)

	before := st.saves
	err := m.Ensure(decl("wdttraw-de", "OpkgTun18", "opkgtun18"))
	if err == nil {
		t.Fatal("нечитаемая запись обязана быть отказом, а не пересборкой с дефолтами")
	}
	// M5: причина обязана доехать целиком — иначе %w сложился в %!w(<nil>).
	if !errors.Is(err, st.getErr) {
		t.Fatalf("причина отказа обязана нести исходную ошибку: %v", err)
	}
	if st.saves != before {
		t.Fatal("на нечитаемой записи писатель не имеет права на Save")
	}
	if len(st.deleted) != 0 {
		t.Fatal("нечитаемая запись не удаляется: она не осиротела, она не прочиталась")
	}
}

func TestMirrorIdempotent(t *testing.T) {
	st, pub := newFakeStore(), &fakePub{}
	m := NewStoreMirror(st, pub)
	_ = m.Ensure(decl("wdttraw-de", "OpkgTun18", "opkgtun18"))
	before, evBefore := st.saves, len(pub.published)

	_ = m.Ensure(decl("wdttraw-de", "OpkgTun18", "opkgtun18"))
	if st.saves != before {
		t.Fatal("повтор без изменений обязан не писать файл")
	}
	if len(pub.published) != evBefore {
		t.Fatal("повтор без изменений обязан не публиковать событие")
	}
}

func TestOwnedListsOnlyOurRecords(t *testing.T) {
	// Гейт посева спрашивает именно этот список (задача 2): «есть ли у уборки
	// что терять». Чужие записи в нём появиться не могут ни при каких условиях.
	st, pub := newFakeStore(), &fakePub{}
	m := NewStoreMirror(st, pub)
	_ = m.Ensure(decl("wdttraw-de", "OpkgTun18", "opkgtun18"))
	st.m["awg10"] = storage.AWGTunnel{ID: "awg10", Backend: "kernel"}
	st.m["nwg0"] = storage.AWGTunnel{ID: "nwg0", Backend: "nativewg"}

	owned, err := m.Owned()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(owned, []string{"wdttraw-de"}) {
		t.Fatalf("Owned обязан перечислять только наши записи: %v", owned)
	}
}

func TestSweepDeletesOnlyOwnUndeclared(t *testing.T) {
	st, pub := newFakeStore(), &fakePub{}
	m := NewStoreMirror(st, pub)
	_ = m.Ensure(decl("wdttraw-de", "OpkgTun18", "opkgtun18"))
	_ = m.Ensure(decl("wdttraw-nl", "OpkgTun19", "opkgtun19"))
	st.m["awg10"] = storage.AWGTunnel{ID: "awg10", Backend: "kernel"}
	st.m["nwg0"] = storage.AWGTunnel{ID: "nwg0", Backend: "nativewg"}

	removed, err := m.Sweep(map[string]bool{"wdttraw-de": true})
	if err != nil {
		t.Fatal(err)
	}
	// Список удалённых возвращается вызывающему: журнал сноса ведёт реестр,
	// который знает причину.
	if !slices.Equal(removed, []string{"wdttraw-nl"}) {
		t.Fatalf("Sweep обязан назвать, что именно снёс: %v", removed)
	}
	if !slices.Equal(st.deleted, []string{"wdttraw-nl"}) {
		t.Fatalf("удалено не то: %v", st.deleted)
	}
	for _, id := range []string{"wdttraw-de", "awg10", "nwg0"} {
		if _, ok := st.m[id]; !ok {
			t.Fatalf("тронуто чужое или объявленное: %s", id)
		}
	}
}

func TestSweepWithoutChangesIsSilent(t *testing.T) {
	st, pub := newFakeStore(), &fakePub{}
	m := NewStoreMirror(st, pub)
	_ = m.Ensure(decl("wdttraw-de", "OpkgTun18", "opkgtun18"))
	n := len(pub.published)
	removed, err := m.Sweep(map[string]bool{"wdttraw-de": true})
	if err != nil || len(removed) != 0 {
		t.Fatalf("уборка не имела права ничего снести: %v, %v", removed, err)
	}
	if len(pub.published) != n {
		t.Fatal("уборка, которая ничего не тронула, не публикует событие")
	}
}

// failingStore — хранилище, отказывающее по требованию. Отдельным типом
// поверх fakeStore, а не полями в нём: створки отказа (I5) проверяются
// здесь, а счётчики и содержимое остаются те же.
type failingStore struct {
	*fakeStore
	listErr   error
	saveErr   error
	deleteErr map[string]error
}

func (f *failingStore) List() ([]storage.AWGTunnel, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.fakeStore.List()
}

func (f *failingStore) Save(t *storage.AWGTunnel) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.fakeStore.Save(t)
}

func (f *failingStore) Delete(id string) error {
	if err := f.deleteErr[id]; err != nil {
		return err
	}
	return f.fakeStore.Delete(id)
}

func TestOwnedAndSweepRefuseOnUnlistableStore(t *testing.T) {
	// Створка I5: ошибка List — отказ, ни одна запись не тронута. Пустой
	// список вместо отказа опаснее самой ошибки: гейт посева спрашивает
	// Owned ровно затем, чтобы узнать, есть ли уборке что терять, и на
	// (nil, nil) он открылся бы при нечитаемом хранилище.
	st := &failingStore{fakeStore: newFakeStore(), listErr: errors.New("read dir: i/o error")}
	st.m["wdttraw-de"] = storage.AWGTunnel{ID: "wdttraw-de", Backend: backendWdttRaw}
	m := NewStoreMirror(st, &fakePub{})

	if _, err := m.Owned(); !errors.Is(err, st.listErr) {
		t.Fatalf("Owned обязан отдать причину: %v", err)
	}
	removed, err := m.Sweep(map[string]bool{})
	if !errors.Is(err, st.listErr) {
		t.Fatalf("Sweep обязан отдать причину: %v", err)
	}
	if len(removed) != 0 || len(st.deleted) != 0 {
		t.Fatalf("на нечитаемом хранилище уборка не трогает ничего: %v / %v", removed, st.deleted)
	}
}

func TestSweepSurvivesFailedDelete(t *testing.T) {
	// Створка I5: отказ по одной записи не отменяет остальные и не теряется.
	// Снесённым обязан считаться только тот, кого правда снесли: список
	// уходит в журнал реестра.
	st := &failingStore{fakeStore: newFakeStore(),
		deleteErr: map[string]error{"wdttraw-nl": errors.New("permission denied")}}
	m := NewStoreMirror(st, &fakePub{})
	_ = m.Ensure(decl("wdttraw-nl", "OpkgTun19", "opkgtun19"))
	_ = m.Ensure(decl("wdttraw-fi", "OpkgTun20", "opkgtun20"))

	removed, err := m.Sweep(map[string]bool{})
	if err == nil {
		t.Fatal("неудавшееся удаление обязано доехать до вызывающего")
	}
	if !slices.Equal(removed, []string{"wdttraw-fi"}) {
		t.Fatalf("снесённым числится не тот, кого снесли: %v", removed)
	}
	if _, ok := st.m["wdttraw-nl"]; !ok {
		t.Fatal("запись, удалить которую не вышло, обязана остаться на месте")
	}
}

func TestEnsureSurfacesSaveFailure(t *testing.T) {
	// Отказ записи обязан быть виден: по нему реестр решает, доехало
	// объявление до диска или нет. Инвалидация несостоявшейся записи —
	// ложь фронту, поэтому события тоже нет.
	st := &failingStore{fakeStore: newFakeStore(), saveErr: errors.New("write tunnel: no space left")}
	pub := &fakePub{}
	m := NewStoreMirror(st, pub)

	if err := m.Ensure(decl("wdttraw-de", "OpkgTun18", "opkgtun18")); !errors.Is(err, st.saveErr) {
		t.Fatalf("отказ хранилища обязан доехать: %v", err)
	}
	if len(pub.published) != 0 {
		t.Fatal("запись не состоялась — инвалидировать нечего")
	}
}

func TestMirrorWorksWithoutPublisher(t *testing.T) {
	// Издатель необязателен по конструкции (обязательно только хранилище) —
	// значит, ни один путь не имеет права разыменовать его вслепую.
	m := NewStoreMirror(newFakeStore(), nil)
	if err := m.Ensure(decl("wdttraw-de", "OpkgTun18", "opkgtun18")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Sweep(map[string]bool{}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreMirrorSatisfiesPorts(t *testing.T) {
	// Два обещания задачи, проверяемые компилятором: зеркало — это Mirror
	// реестра, а настоящий стор удовлетворяет узкому TunnelStore КАК ЕСТЬ,
	// без единой правки internal/storage. Дрейф сигнатуры там всплывёт
	// здесь, а не в проводке (задача 7).
	var _ Mirror = (*StoreMirror)(nil)
	var _ TunnelStore = (*storage.AWGTunnelStore)(nil)
}
