package exitreg

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
)

// fakeMirror записывает, что зеркалу поручили, и умеет отказывать.
// owned — записи, которые уборка вправе удалить (в проде это выдача
// хранилища, отфильтрованная по нашему бэкенду).
type fakeMirror struct {
	mu      sync.Mutex
	ensured []ExitDecl
	swept   []map[string]bool
	owned   []string
	err     error
}

func (f *fakeMirror) Ensure(d ExitDecl) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, d)
	return f.err
}

func (f *fakeMirror) Sweep(declared map[string]bool) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.swept = append(f.swept, declared)
	var removed []string
	for _, id := range f.owned {
		if !declared[id] {
			removed = append(removed, id)
		}
	}
	return removed, f.err
}

func (f *fakeMirror) Owned() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.owned), f.err
}

func (f *fakeMirror) sweeps() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.swept)
}

func (f *fakeMirror) lastSwept() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.swept[len(f.swept)-1]
}

// recJournal — журнал приложения на время теста. Форма интерфейса та же, что
// у instance.Journal (instance.go:14-17), поэтому в проде ему удовлетворяет
// *logging.ScopedLogger как есть.
type recJournal struct {
	mu   sync.Mutex
	rows []string
}

func (j *recJournal) Info(action, target, _ string) { j.add("info:" + action + ":" + target) }
func (j *recJournal) Warn(action, target, _ string) { j.add("warn:" + action + ":" + target) }

func (j *recJournal) add(s string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.rows = append(j.rows, s)
}

func (j *recJournal) has(row string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Contains(j.rows, row)
}

// newReg — реестр с фейковым журналом; журнал возвращается тем тестам,
// которым он нужен.
func newReg(m Mirror) (*Registry, *recJournal) {
	j := &recJournal{}
	return New(m, j), j
}

// seededReg — реестр с ОТКРЫТЫМ гейтом: посев подтверждён. Тесты про уборку
// начинают отсюда, тесты про сам гейт — с newReg.
func seededReg(m Mirror) (*Registry, *recJournal) {
	r, j := newReg(m)
	if err := r.MarkSeeded(1); err != nil {
		panic(err)
	}
	return r, j
}

func decl(id, ndms, kernel string) ExitDecl {
	return ExitDecl{ID: id, InstanceID: strings.TrimPrefix(id, "wdttraw-"),
		Name: "Германия", NDMSName: ndms, KernelIface: kernel, Peer: "1.2.3.4:56000", Enabled: true}
}

func TestDeclaredExitIsResolvableBeforeAnyObservation(t *testing.T) {
	// §5: правило, указывающее на выключенный или лежачий выход, обязано
	// РАЗРЕШАТЬСЯ в имя. Ни одного Ensure ещё не было.
	r, _ := newReg(&fakeMirror{})
	if err := r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")}); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Lookup("wdttraw-de")
	if !ok {
		t.Fatal("объявленный выход обязан находиться до первой реконсиляции")
	}
	if got.NDMSName != "OpkgTun18" || got.KernelIface != "opkgtun18" {
		t.Fatalf("имена не доехали: %+v", got)
	}
	if got.Ready {
		t.Fatal("ненаблюдённый выход не может быть Ready")
	}
}

func TestEnsureCarriesReadinessOnly(t *testing.T) {
	r, _ := newReg(&fakeMirror{})
	_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")})

	if err := r.Ensure(linkres.ExitInfo{ID: "wdttraw-de", NDMSName: "OpkgTun18",
		KernelIface: "opkgtun18", Ready: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Lookup("wdttraw-de"); !got.Ready {
		t.Fatal("готовность не доехала")
	}
	// Повторное объявление тем же составом готовность НЕ гасит.
	_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")})
	if got, _ := r.Lookup("wdttraw-de"); !got.Ready {
		t.Fatal("объявление погасило готовность на неизменившихся именах")
	}
}

func TestRenamedExitLosesReadinessUntilObserved(t *testing.T) {
	r, _ := newReg(&fakeMirror{})
	_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")})
	_ = r.Ensure(linkres.ExitInfo{ID: "wdttraw-de", NDMSName: "OpkgTun18",
		KernelIface: "opkgtun18", Ready: true})

	// Перепиновка индекса: прежняя готовность относилась к другому интерфейсу.
	_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun19", "opkgtun19")})
	got, _ := r.Lookup("wdttraw-de")
	if got.Ready {
		t.Fatal("готовность обязана погаснуть при смене имён")
	}
	if got.KernelIface != "opkgtun19" {
		t.Fatalf("новое имя не доехало: %+v", got)
	}
}

func TestStaleObservationCannotRelightReadiness(t *testing.T) {
	// Ресурс наблюдал СТАРЫЙ интерфейс и доложил уже после перепиновки.
	// Без сверки имён такое наблюдение зажгло бы Ready на записи нового
	// интерфейса, отменив исключение из правила 2 обычной гонкой.
	r, _ := newReg(&fakeMirror{})
	_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")})
	_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun19", "opkgtun19")})

	err := r.Ensure(linkres.ExitInfo{ID: "wdttraw-de", NDMSName: "OpkgTun18",
		KernelIface: "opkgtun18", Ready: true})
	if err == nil || !strings.Contains(err.Error(), "OpkgTun18") {
		t.Fatalf("наблюдение чужого интерфейса обязано быть отказом с именами: %v", err)
	}
	if got, _ := r.Lookup("wdttraw-de"); got.Ready {
		t.Fatal("запоздалое наблюдение старого интерфейса зажгло готовность нового")
	}
}

func TestUndeclaredExitIsRemovedAndSwept(t *testing.T) {
	m := &fakeMirror{owned: []string{"wdttraw-de", "wdttraw-nl"}}
	r, j := seededReg(m)
	_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18"),
		decl("wdttraw-nl", "OpkgTun19", "opkgtun19")})
	_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")})

	if _, ok := r.Lookup("wdttraw-nl"); ok {
		t.Fatal("снятый с объявления выход обязан исчезнуть")
	}
	last := m.lastSwept()
	if last["wdttraw-nl"] || !last["wdttraw-de"] {
		t.Fatalf("зеркалу передана неверная ведомость: %v", last)
	}
	// В9: каждый снос назван в журнале приложения — id и причина.
	if !j.has("info:exit-mirror-removed:wdttraw-nl") {
		t.Fatal("удаление зеркальной записи обязано попасть в журнал")
	}
}

func TestSweepIsBlockedUntilSeedIsConfirmed(t *testing.T) {
	// В9/G7. До подтверждения посева уборка не зовётся ВООБЩЕ: объявления
	// принимаются, зеркальные записи обновляются, удаление заперто.
	m := &fakeMirror{owned: []string{"wdttraw-de", "wdttraw-nl"}}
	r, j := newReg(m)

	if err := r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")}); err != nil {
		t.Fatal(err)
	}
	if n := m.sweeps(); n != 0 {
		t.Fatalf("до посева уборка не имеет права зваться: %d вызовов", n)
	}
	if len(m.ensured) != 1 {
		t.Fatal("гейт запирает удаление, а не объявление: зеркало обязано получить Ensure")
	}
	if !j.has("warn:sweep-blocked:exitreg") {
		t.Fatal("запертая уборка обязана быть видна в журнале, а не молчать")
	}

	if err := r.MarkSeeded(2); err != nil {
		t.Fatalf("посев с непустым результатом обязан открыть гейт: %v", err)
	}
	if err := r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")}); err != nil {
		t.Fatal(err)
	}
	if m.sweeps() != 1 {
		t.Fatal("после подтверждённого посева уборка обязана пойти")
	}
}

func TestEmptySeedKeepsGateClosedWhenRecordsExist(t *testing.T) {
	// Ключевой случай: конфиги не прочитались, посев доложил ноль. Для уборки
	// это неотличимо от «инстансов больше нет» — и она снесла бы ВСЁ.
	m := &fakeMirror{owned: []string{"wdttraw-de"}}
	r, _ := newReg(m)

	err := r.MarkSeeded(0)
	if err == nil {
		t.Fatal("пустой посев при живых зеркальных записях обязан быть отказом с причиной")
	}
	// Именно id, а не только число (правка редакции 5, закрывает 18а): выход
	// из запертого гейта — удалить карточку-призрак руками, и без id её не найти.
	if !strings.Contains(err.Error(), "wdttraw-de") {
		t.Fatalf("причина обязана назвать записи по id: %v", err)
	}
	if err := r.SetDeclared(nil); err != nil {
		t.Fatal(err)
	}
	if n := m.sweeps(); n != 0 {
		t.Fatalf("гейт обязан остаться закрытым: %d уборок", n)
	}
}

func TestEmptySeedKeepsGateClosedWhenStoreCannotBeListed(t *testing.T) {
	// Fail-open ровно того гейта, ради которого он заведён: не перечислился
	// каталог — owned пуст, и «нечего терять» становится неотличимо от
	// «неизвестно, что там». Ошибка Owned обязана запирать гейт, а не
	// открывать его по пустому списку.
	m := &fakeMirror{err: errors.New("диск")}
	r, _ := newReg(m)

	if err := r.MarkSeeded(0); err == nil {
		t.Fatal("нечитаемое хранилище при пустом посеве обязано быть отказом")
	}
	if err := r.SetDeclared(nil); err != nil {
		t.Fatal(err)
	}
	if n := m.sweeps(); n != 0 {
		t.Fatalf("гейт обязан остаться закрытым: %d уборок", n)
	}
}

func TestEmptySeedOnCleanStoreOpensGate(t *testing.T) {
	// Чистая установка: инстансов нет, зеркальных записей нет, терять нечего.
	// Не открой мы гейт здесь — у нового пользователя первая же пара
	// «создал клиента → удалил клиента» оставила бы карточку-призрак.
	m := &fakeMirror{}
	r, _ := newReg(m)

	if err := r.MarkSeeded(0); err != nil {
		t.Fatalf("пустой посев при пустом хранилище обязан открыть гейт: %v", err)
	}
	if err := r.SetDeclared(nil); err != nil {
		t.Fatal(err)
	}
	if m.sweeps() != 1 {
		t.Fatal("уборка обязана пойти")
	}
}

func TestSeedMarkIsMonotonic(t *testing.T) {
	m := &fakeMirror{owned: []string{"wdttraw-de"}}
	r, _ := newReg(m)
	if err := r.MarkSeeded(3); err != nil {
		t.Fatal(err)
	}
	// Второй посев с нулём не имеет права закрыть уже открытый гейт: снять
	// отметку нельзя по построению.
	_ = r.MarkSeeded(0)
	if err := r.SetDeclared(nil); err != nil {
		t.Fatal(err)
	}
	if m.sweeps() != 1 {
		t.Fatal("отметка о посеве обязана быть монотонной")
	}
}

func TestEnsureOnUndeclaredIsVerdict(t *testing.T) {
	// G3: наблюдение не создаёт идентичность. Иначе в реестре заводится выход
	// без имени инстанса, а реконсиляция в полёте воскрешает только что снятое.
	r, _ := newReg(&fakeMirror{})
	err := r.Ensure(linkres.ExitInfo{ID: "wdttraw-ghost", NDMSName: "OpkgTun18"})
	if err == nil || !strings.Contains(err.Error(), "wdttraw-ghost") {
		t.Fatalf("причина обязана называть выход: %v", err)
	}
	if _, ok := r.Lookup("wdttraw-ghost"); ok {
		t.Fatal("необъявленный выход не может попасть в реестр")
	}
}

func TestSetDeclaredRejectsIncompleteDeclBeforeTouchingAnything(t *testing.T) {
	// Конфиг приходит нормализованным (план 5). Пустое имя интерфейса —
	// дефект писателя, и чинить его молча значит завести второго
	// нормализатора; в старом коде две копии нормализовали по-разному.
	//
	// Fail-closed: отказ наступает ДО касания памяти и зеркала, поэтому
	// один битый конфиг не сносит зеркальные записи остальных.
	m := &fakeMirror{}
	r, _ := seededReg(m)
	_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")})

	err := r.SetDeclared([]ExitDecl{
		decl("wdttraw-de", "OpkgTun18", "opkgtun18"),
		{ID: "wdttraw-nl", InstanceID: "nl", NDMSName: "OpkgTun19"}, // нет kernel-имени
	})
	if err == nil {
		t.Fatal("объявление без kernel-имени обязано быть отклонено")
	}
	if _, ok := r.Lookup("wdttraw-de"); !ok {
		t.Fatal("отклонённая ведомость не имеет права трогать память")
	}
	// Память не тронута и в другую сторону: битый выход в неё не попал.
	// Без этой проверки страж «валидация после обновления памяти» остаётся
	// зелёным — прежняя ведомость тоже содержала wdttraw-de, и одного его
	// присутствия мало, чтобы отличить нетронутую память от перезаписанной.
	if _, ok := r.Lookup("wdttraw-nl"); ok {
		t.Fatal("битый выход не имеет права попасть в память")
	}
	if n := m.sweeps(); n != 1 {
		t.Fatalf("отклонённая ведомость не имеет права доезжать до зеркала: %d уборок", n)
	}
}

func TestSetDeclaredRejectsDuplicateID(t *testing.T) {
	// Дубликат ID в ведомости — реальный случай, а не теория: RawTunnelID
	// усекает безопасную часть id до 20 символов (roles/wdttclient/role.go:41-43),
	// и два длинных id инстансов могут схлопнуться в один ExitID. Принять
	// такую ведомость молча — в память ляжет последний, а зеркало получит
	// два Ensure одной записи. Fail-closed, как и любой невалидный ExitDecl:
	// отказ до касания памяти и зеркала.
	m := &fakeMirror{}
	r, _ := seededReg(m)
	_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")})

	err := r.SetDeclared([]ExitDecl{
		decl("wdttraw-de", "OpkgTun18", "opkgtun18"),
		decl("wdttraw-de", "OpkgTun19", "opkgtun19"),
	})
	if err == nil || !strings.Contains(err.Error(), "wdttraw-de") {
		t.Fatalf("дубликат id в ведомости обязан быть отказом, называющим выход: %v", err)
	}
	if _, ok := r.Lookup("wdttraw-de"); !ok {
		t.Fatal("отклонённая ведомость не имеет права трогать память")
	}
	if n := m.sweeps(); n != 1 {
		t.Fatalf("отклонённая ведомость не имеет права доезжать до зеркала: %d уборок", n)
	}
}

func TestMirrorFailureSurfaces(t *testing.T) {
	m := &fakeMirror{err: errors.New("диск")}
	r, _ := seededReg(m)
	if err := r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")}); err == nil {
		t.Fatal("отказ зеркала обязан доехать до вызывающего")
	}
	// Память при этом обновлена: резолв имён не должен зависеть от диска.
	if _, ok := r.Lookup("wdttraw-de"); !ok {
		t.Fatal("отказ зеркала не должен ронять резолв имён")
	}
}

// sweepFailMirror отказывает ТОЛЬКО на уборке: отказ Ensure приезжает в errs
// раньше и накрыл бы собой пропущенную ошибку Sweep.
type sweepFailMirror struct {
	fakeMirror
}

func (m *sweepFailMirror) Sweep(map[string]bool) ([]string, error) {
	return nil, errors.New("диск")
}

func TestSweepFailureSurfaces(t *testing.T) {
	// Молча не отработавшая уборка — это оставленные карточки-призраки, о
	// которых вызывающий не узнает ничего.
	r, _ := seededReg(&sweepFailMirror{})
	if err := r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")}); err == nil {
		t.Fatal("отказ уборки обязан доехать до вызывающего")
	}
}

func TestRegistrySatisfiesPort(t *testing.T) {
	r, _ := newReg(&fakeMirror{})
	var _ linkres.ExitRegistry = r
}

func TestConcurrentLookupAndDeclare(t *testing.T) {
	r, _ := seededReg(&fakeMirror{})
	_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.Lookup("wdttraw-de") }()
		go func() {
			defer wg.Done()
			_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")})
		}()
	}
	wg.Wait()
}

// gatedMirror задерживает ПЕРВЫЙ Ensure до сигнала теста — так проверяемое
// окно (память отдана, зеркало ещё не тронуто) открывается детерминированно,
// а не «иногда под -race».
type gatedMirror struct {
	mu      sync.Mutex
	log     []string
	held    atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (g *gatedMirror) add(s string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.log = append(g.log, s)
}

func (g *gatedMirror) calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.log)
}

func (g *gatedMirror) Ensure(d ExitDecl) error {
	if g.held.CompareAndSwap(false, true) {
		close(g.entered)
		<-g.release
	}
	g.add("ensure:" + d.ID)
	return nil
}

func (g *gatedMirror) Sweep(declared map[string]bool) ([]string, error) {
	g.add(fmt.Sprintf("sweep:%d", len(declared)))
	return nil, nil
}

func (g *gatedMirror) Owned() ([]string, error) { return nil, nil }

func TestSetDeclaredIsSerialized(t *testing.T) {
	// C4. Без сериализации всего тела пара SetDeclared укладывается так:
	// A считает дельту под r.mu (в памяти {de}), отпускает лок и входит в
	// зеркало; B считает свою дельту (в памяти пусто), проносится через
	// зеркало ПЕРВОЙ и убирает запись; A доводит свой Ensure и кладёт её
	// обратно. Итог: в памяти выхода нет, на диске он есть — и никакой
	// следующий вызов этого не исправит, потому что для реестра состояние
	// уже согласовано.
	m := &gatedMirror{entered: make(chan struct{}), release: make(chan struct{})}
	r, _ := seededReg(m)

	first := make(chan struct{})
	go func() {
		defer close(first)
		_ = r.SetDeclared([]ExitDecl{decl("wdttraw-de", "OpkgTun18", "opkgtun18")})
	}()
	<-m.entered // A внутри зеркала; лок памяти уже отпущен

	second := make(chan struct{})
	go func() {
		defer close(second)
		_ = r.SetDeclared(nil)
	}()

	select {
	case <-second:
		t.Fatal("вторая SetDeclared прошла, пока первая была в зеркале: ведомости укладываются в произвольном порядке")
	case <-time.After(200 * time.Millisecond):
	}

	close(m.release)
	<-first
	<-second

	want := []string{"ensure:wdttraw-de", "sweep:1", "sweep:0"}
	if got := m.calls(); !slices.Equal(got, want) {
		t.Fatalf("порядок обращений к зеркалу: %v, want %v", got, want)
	}
	if _, ok := r.Lookup("wdttraw-de"); ok {
		t.Fatal("память обязана совпасть с последней доехавшей ведомостью")
	}
}
