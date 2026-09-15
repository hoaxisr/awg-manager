package proxyrt

import (
	"context"
	"errors"
	"sync"
	"testing"
)

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
	err     error
}

func (f *fakeRemover) Remove(_ context.Context, r OwnedResource) error {
	f.mu.Lock()
	f.removed = append(f.removed, r.Name)
	f.mu.Unlock()
	return f.err
}

// declaring — ведомость-подстановка: отдаёт готовую карту и запоминает имена,
// с которыми её позвали.
func declaring(declared map[string]bool, gotNames *[]string) func([]string) (map[string]bool, error) {
	return func(found []string) (map[string]bool, error) {
		if gotNames != nil {
			*gotNames = found
		}
		return declared, nil
	}
}

func sweepAll(t *testing.T, sw *Sweeper, ctx context.Context, declared map[string]bool) ([]string, error) {
	t.Helper()
	return sw.Sweep(ctx, declaring(declared, nil))
}

func TestSweepRemovesOnlyUndeclared(t *testing.T) {
	sc := fakeScanner{out: []OwnedResource{
		{Label: "AWGM WDTT client", Name: "OpkgTun18"},
		{Label: "AWGM WDTT client", Name: "OpkgTun19"},
		{Label: "AWGM WDTT", Name: "OpkgTun20"},
	}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, []string{"AWGM WDTT client", "AWGM WDTT"})

	removed, err := sweepAll(t, sw, context.Background(),
		map[string]bool{"OpkgTun18": true, "OpkgTun20": true})
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
	sw := NewSweeper(sc, rm, []string{"AWGM WDTT client"})

	removed, err := sweepAll(t, sw, context.Background(), map[string]bool{"OpkgTun18": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("удалено %v, ожидали пусто", removed)
	}
}

func TestScanFailedRemovesNothing(t *testing.T) {
	// «Не знаем» не равно «наш и лишний». Скан упал — вызывающему нечего
	// сносить, и до SweepFound он не доходит.
	sc := fakeScanner{err: errors.New("rci недоступен")}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, []string{"AWGM WDTT client"})

	removed, err := sweepAll(t, sw, context.Background(), map[string]bool{})
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
	sw := NewSweeper(sc, rm, []string{"AWGM WDTT client"})

	removed, err := sweepAll(t, sw, context.Background(), map[string]bool{})
	if err == nil {
		t.Fatal("ошибка сноса обязана доехать наружу, а не проглотиться")
	}
	if len(removed) != 0 {
		t.Fatalf("неудавшийся снос не должен попадать в список удалённых: %v", removed)
	}
}

func TestSweepFindsOrphanByLabelPrefix(t *testing.T) {
	// Сканер отдаёт ФАКТИЧЕСКОЕ описание ресурса, а не константу-метку: у
	// клиента описание — это метка плюс имя инстанса (roles.ClientDescription),
	// и другого текста в NDMS попросту нет. Сверка точным равенством на этом
	// месте молча переставала находить клиентские сироты.
	sc := fakeScanner{out: []OwnedResource{
		{Label: "AWGM WDTT Raw Client: мой инстанс", Name: "OpkgTun19"},
	}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, []string{"AWGM WDTT Raw Client"})

	removed, err := sweepAll(t, sw, context.Background(), map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "OpkgTun19" {
		t.Fatalf("сирота с описанием-хвостом не найдена: %v", removed)
	}
}

// Страховка от бага в сканере: цена ошибки — снесённый чужой интерфейс
// роутера, поэтому метку проверяет сам уборщик, а не только сканер. Проверки
// две, в двух разных функциях, и тесты на них РАЗНЫЕ: один упавший не должен
// скрывать состояние второго.
func TestSweepIgnoresForeignLabel(t *testing.T) {
	rm := &fakeRemover{}
	sw := NewSweeper(fakeScanner{out: []OwnedResource{
		{Label: "AWGM WDTT client", Name: "OpkgTun19"},
		{Label: "Чужая метка", Name: "OpkgTun20"},
	}}, rm, []string{"AWGM WDTT client"})

	var gotNames []string
	removed, err := sw.Sweep(context.Background(), declaring(map[string]bool{}, &gotNames))
	if err != nil {
		t.Fatal(err)
	}
	// Ведомость не должна и знать о чужом: иначе её автор однажды объявит его
	// своим, чтобы «спасти».
	if len(gotNames) != 1 || gotNames[0] != "OpkgTun19" {
		t.Fatalf("ведомость позвана с %v, ожидали только наше", gotNames)
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

// Отказ ведомости — то же «не знаем», что и упавший скан: не сносим ничего.
func TestSweepDeclarerFailureRemovesNothing(t *testing.T) {
	rm := &fakeRemover{}
	sw := NewSweeper(fakeScanner{out: []OwnedResource{{Label: "L", Name: "OpkgTun19"}}},
		rm, []string{"L"})

	removed, err := sw.Sweep(context.Background(), func([]string) (map[string]bool, error) {
		return nil, errors.New("стор не читается")
	})
	if err == nil {
		t.Fatal("отказ ведомости обязан доехать наружу")
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if len(removed) != 0 || len(rm.removed) != 0 {
		t.Fatalf("снесено %v при несобранной ведомости", rm.removed)
	}
}

// Отказ сноса на одном ресурсе не отменяет уборку остальных: одна недоступная
// запись RCI не должна оставлять весь остаток сирот до следующего боота.
func TestSweepContinuesAfterRemoveFailure(t *testing.T) {
	sc := fakeScanner{out: []OwnedResource{
		{Label: "L", Name: "OpkgTun19"},
		{Label: "L", Name: "OpkgTun20"},
	}}
	rm := &flakyRemover{failOn: "OpkgTun19"}
	sw := NewSweeper(sc, rm, []string{"L"})

	removed, err := sweepAll(t, sw, context.Background(), map[string]bool{})
	if err == nil {
		t.Fatal("отказ сноса обязан доехать наружу")
	}
	if len(removed) != 1 || removed[0] != "OpkgTun20" {
		t.Fatalf("снесено %v; отказ на одном не отменяет уборку остальных", removed)
	}
}

// flakyRemover отказывает на одном имени и сносит остальные.
type flakyRemover struct {
	failOn  string
	removed []string
}

func (f *flakyRemover) Remove(_ context.Context, r OwnedResource) error {
	if r.Name == f.failOn {
		return errors.New("rci отказал")
	}
	f.removed = append(f.removed, r.Name)
	return nil
}

func TestSweepStopsOnCanceledContext(t *testing.T) {
	// Отмена — не отказ уборки: пакет уже отделяет одно от другого в цикле и
	// воркере. Прекращаем сносы и не считаем это провалом.
	sc := fakeScanner{out: []OwnedResource{
		{Label: "L", Name: "OpkgTun19"},
		{Label: "L", Name: "OpkgTun20"},
	}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, []string{"L"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	removed, err := sw.Sweep(ctx, declaring(map[string]bool{}, nil))
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

func TestSweepCanceledRemoveIsNotFailure(t *testing.T) {
	// Отмена, вернувшаяся из самого Remove, — не отказ уборки: прекращаем и
	// молчим, как и при отмене, замеченной до вызова.
	sc := fakeScanner{out: []OwnedResource{
		{Label: "L", Name: "OpkgTun19"},
		{Label: "L", Name: "OpkgTun20"},
	}}
	rm := &fakeRemover{err: context.Canceled}
	sw := NewSweeper(sc, rm, []string{"L"})

	removed, err := sweepAll(t, sw, context.Background(), map[string]bool{})
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

func TestNewSweeperPanics(t *testing.T) {
	cases := []struct {
		name string
		call func()
	}{
		// Уборщик — единственный путь удаления. Конструктор без меток означал бы
		// вечное накопление сирот без единого сигнала.
		{"без меток", func() { NewSweeper(fakeScanner{}, &fakeRemover{}, nil) }},
		// nil компилируется и падает посреди уборки, а не при сборке движка.
		{"без сканера", func() { NewSweeper(nil, &fakeRemover{}, []string{"L"}) }},
		{"без сносчика", func() { NewSweeper(fakeScanner{}, nil, []string{"L"}) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("ожидали панику")
				}
			}()
			c.call()
		})
	}
}
