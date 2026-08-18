package proxyrt

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// noIndex — разборщик «номер неизвестен»: ресурс рассматривается только по
// declared, как было до появления консультации с аллокатором.
func noIndex(string) (int, bool) { return 0, false }

type fakeScanner struct {
	out []OwnedResource
	err error
}

func (f fakeScanner) Scan(context.Context, []string) ([]OwnedResource, error) {
	return f.out, f.err
}

type fakeRemover struct {
	mu      sync.Mutex
	removed []string
	delay   time.Duration
	err     error
	entered chan struct{} // сигнал «я внутри Remove», до задержки
}

func (f *fakeRemover) Remove(_ context.Context, r OwnedResource) error {
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.removed = append(f.removed, r.Name)
	f.mu.Unlock()
	return f.err
}

func TestSweepRemovesOnlyUndeclared(t *testing.T) {
	sc := fakeScanner{out: []OwnedResource{
		{Label: "AWGM WDTT client", Name: "OpkgTun18"},
		{Label: "AWGM WDTT client", Name: "OpkgTun19"},
		{Label: "AWGM WDTT", Name: "OpkgTun20"},
	}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}),
		[]string{"AWGM WDTT client", "AWGM WDTT"}, noIndex)

	removed, err := sw.Sweep(context.Background(), map[string]bool{"OpkgTun18": true, "OpkgTun20": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "OpkgTun19" {
		t.Fatalf("удалено %v, ожидали только OpkgTun19", removed)
	}
}

func TestSweepNeverRemovesDeclared(t *testing.T) {
	// Объявленный ресурс не удаляется никогда: ни по «процесс не бежит», ни по
	// таймеру. Выключенный инстанс продолжает объявлять свои ресурсы.
	sc := fakeScanner{out: []OwnedResource{{Label: "AWGM WDTT client", Name: "OpkgTun18"}}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}), []string{"AWGM WDTT client"}, noIndex)

	removed, err := sw.Sweep(context.Background(), map[string]bool{"OpkgTun18": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("удалено %v, ожидали пусто", removed)
	}
}

func TestSweepFailedScanRemovesNothing(t *testing.T) {
	// «Не знаем» не равно «наш и лишний». Скан упал — не сносим ничего.
	sc := fakeScanner{err: errors.New("rci недоступен")}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}), []string{"AWGM WDTT client"}, noIndex)

	removed, err := sw.Sweep(context.Background(), map[string]bool{})
	if err == nil {
		t.Fatal("ожидали ошибку скана")
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if len(removed) != 0 || len(rm.removed) != 0 {
		t.Fatalf("при упавшем скане удалять нельзя, удалено %v", rm.removed)
	}
}

func TestSweepReportsRemoveError(t *testing.T) {
	sc := fakeScanner{out: []OwnedResource{{Label: "AWGM WDTT client", Name: "OpkgTun19"}}}
	rm := &fakeRemover{err: errors.New("rci отказал")}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}), []string{"AWGM WDTT client"}, noIndex)

	removed, err := sw.Sweep(context.Background(), map[string]bool{})
	if err == nil {
		t.Fatal("ошибка сноса обязана доехать наружу, а не проглотиться")
	}
	if len(removed) != 0 {
		t.Fatalf("неудавшийся снос не должен попадать в список удалённых: %v", removed)
	}
}

func TestSweepDoesNotHoldAllocatorLockDuringRemoval(t *testing.T) {
	// Снос — это RCI-вызовы на секунды. Держать на них лок аллокатора значит
	// остановить выделение номеров всем инстансам.
	alloc := NewAllocator(IndexRange{Min: 17, Max: 49})
	sc := fakeScanner{out: []OwnedResource{{Label: "L", Name: "OpkgTun19"}}}
	rm := &fakeRemover{delay: 150 * time.Millisecond, entered: make(chan struct{}, 1)}
	sw := NewSweeper(sc, rm, alloc, []string{"L"}, noIndex)

	done := make(chan struct{})
	go func() {
		defer close(done)
		sw.Sweep(context.Background(), map[string]bool{})
	}()

	// Ждём сигнал «снос начался», а не спим наугад: сон дал бы ложный зелёный,
	// если планировщик задержит старт горутины дольше сна.
	<-rm.entered
	start := time.Now()
	if _, err := alloc.AllocIndex("inst1", 0, map[int]bool{}); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited > 100*time.Millisecond {
		t.Fatalf("выделение номера ждало лок %v — уборщик держит его на время сносов", waited)
	}
	<-done
}

func TestSweepFindsOrphanByLabelPrefix(t *testing.T) {
	// Сканер отдаёт ФАКТИЧЕСКОЕ описание ресурса, а не константу-метку: у
	// клиента описание — это метка плюс имя инстанса (roles.ClientDescription),
	// и другого текста в NDMS попросту нет. Сверка точным равенством на этом
	// месте молча переставала находить клиентские сироты — ровно тот мёртвый
	// скан, который план и чинит, только этажом выше.
	sc := fakeScanner{out: []OwnedResource{
		{Label: "AWGM WDTT Raw Client: мой инстанс", Name: "OpkgTun19"},
	}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}),
		[]string{"AWGM WDTT Raw Client"}, noIndex)

	removed, err := sw.Sweep(context.Background(), map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "OpkgTun19" {
		t.Fatalf("сирота с описанием-хвостом не найдена: %v", removed)
	}
}

func TestSweepIgnoresForeignLabel(t *testing.T) {
	// Страховка от бага в сканере: цена ошибки — снесённый чужой интерфейс
	// роутера, поэтому метку проверяем сами, а не только доверяем сканеру.
	sc := fakeScanner{out: []OwnedResource{
		{Label: "AWGM WDTT client", Name: "OpkgTun19"},
		{Label: "Чужая метка", Name: "OpkgTun20"},
	}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}), []string{"AWGM WDTT client"}, noIndex)

	removed, err := sw.Sweep(context.Background(), map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "OpkgTun19" {
		t.Fatalf("удалено %v, ожидали только OpkgTun19", removed)
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if len(rm.removed) != 1 || rm.removed[0] != "OpkgTun19" {
		t.Fatalf("снос чужого ресурса: %v", rm.removed)
	}
}

func TestSweepStopsOnCanceledContext(t *testing.T) {
	// Отмена — не отказ уборки: пакет уже отделяет одно от другого в цикле и
	// воркере. Прекращаем сносы и не считаем это провалом.
	sc := fakeScanner{out: []OwnedResource{
		{Label: "L", Name: "OpkgTun19"},
		{Label: "L", Name: "OpkgTun20"},
	}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}), []string{"L"}, noIndex)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	removed, err := sw.Sweep(ctx, map[string]bool{})
	if err != nil {
		t.Fatalf("отмена не должна приезжать как отказ уборки: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("при отменённом контексте сносить нельзя, удалено %v", removed)
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if len(rm.removed) != 0 {
		t.Fatalf("Remove звался при отменённом контексте: %v", rm.removed)
	}
}

func TestSweepSpareResourceHeldByAllocator(t *testing.T) {
	// Инстанс получил номер и создал интерфейс, но объявиться ещё не успел:
	// declared его не содержит. Сносить нельзя — иначе уборщик уничтожает
	// только что созданный интерфейс.
	alloc := NewAllocator(IndexRange{Min: 17, Max: 49})
	if _, err := alloc.AllocIndex("inst1", 19, map[int]bool{}); err != nil {
		t.Fatal(err)
	}
	sc := fakeScanner{out: []OwnedResource{{Label: "L", Name: "OpkgTun19"}}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, alloc, []string{"L"}, func(n string) (int, bool) {
		if n == "OpkgTun19" {
			return 19, true
		}
		return 0, false
	})

	removed, err := sw.Sweep(context.Background(), map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("снесён закреплённый номер: %v", removed)
	}
}

func TestSweepRemovesUnheldResourceWithKnownIndex(t *testing.T) {
	// Обратная сторона предыдущего: номер разобран, но ни за кем не закреплён —
	// сирота, сносим. Иначе консультация с аллокатором выключила бы уборку.
	alloc := NewAllocator(IndexRange{Min: 17, Max: 49})
	sc := fakeScanner{out: []OwnedResource{{Label: "L", Name: "OpkgTun19"}}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, alloc, []string{"L"}, func(n string) (int, bool) {
		if n == "OpkgTun19" {
			return 19, true
		}
		return 0, false
	})

	removed, err := sw.Sweep(context.Background(), map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "OpkgTun19" {
		t.Fatalf("удалено %v, ожидали OpkgTun19: номер ни за кем не закреплён", removed)
	}
}

func TestSweepCanceledRemoveIsNotFailure(t *testing.T) {
	// Отмена, вернувшаяся из самого Remove, — не отказ уборки: прекращаем и
	// молчим, как и при отмене, замеченной до вызова.
	sc := fakeScanner{out: []OwnedResource{
		{Label: "L", Name: "OpkgTun19"},
		{Label: "L", Name: "OpkgTun20"},
	}}
	rm := &fakeRemover{err: context.Canceled}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}), []string{"L"}, noIndex)

	removed, err := sw.Sweep(context.Background(), map[string]bool{})
	if err != nil {
		t.Fatalf("отмена из Remove не должна приезжать как отказ уборки: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("отменённый снос не считается удалённым: %v", removed)
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if len(rm.removed) != 1 {
		t.Fatalf("после отмены сносы обязаны прекратиться, а не идти дальше: %v", rm.removed)
	}
}

func TestNewSweeperPanicsWithoutLabels(t *testing.T) {
	// Уборщик — единственный путь удаления. Конструктор без меток означал бы
	// вечное накопление сирот без единого сигнала: это ошибка программирования,
	// а не режим работы.
	defer func() {
		if recover() == nil {
			t.Fatal("ожидали панику на пустом списке меток")
		}
	}()
	NewSweeper(fakeScanner{}, &fakeRemover{}, NewAllocator(IndexRange{Min: 17, Max: 49}), nil, noIndex)
}

func TestNewSweeperPanicsWithoutIndexOf(t *testing.T) {
	// nil-разборщик компилируется и падает только на пути уборки — между
	// решением о сносе и сносами. Отказ обязан быть в конструкторе.
	defer func() {
		if recover() == nil {
			t.Fatal("ожидали панику на nil-разборщике имени")
		}
	}()
	NewSweeper(fakeScanner{}, &fakeRemover{}, NewAllocator(IndexRange{Min: 17, Max: 49}), []string{"L"}, nil)
}

func TestNewSweeperPanicsWithoutAllocator(t *testing.T) {
	// Та же болезнь: nil компилируется, а разыменование случается посреди Sweep,
	// на консультации held. Отказ обязан быть в конструкторе.
	defer func() {
		if recover() == nil {
			t.Fatal("ожидали панику на nil-аллокаторе")
		}
	}()
	NewSweeper(fakeScanner{}, &fakeRemover{}, nil, []string{"L"}, noIndex)
}
