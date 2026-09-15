package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Идентификатор kernel-туннеля и номер его интерфейса — одно и то же N,
// поэтому занятые ИДЕНТИФИКАТОРЫ уходят в пул отдельным вето. Легаси NativeWG
// на awg12 номер OpkgTun12 не занимает, но ключ хранилища держит: выдать
// такой номер значит получить ErrAlreadyExists на записи (#891).
func TestIdentifierHolders(t *testing.T) {
	got := identifierHolders([]storage.AWGTunnel{
		{ID: "awg10", Name: "Amsterdam", Backend: "kernel"},
		{ID: "awg12", Name: "Work", Backend: "nativewg"},
		{ID: "awg13"}, // легаси без имени и бэкенда
		{ID: "wdttraw-home", Backend: "wdtt-raw"}, // номер живёт в своей подсистеме
		{ID: "awgm5", Backend: "kernel"},          // OS 4.x, NDMS-имени нет
		{ID: "awg-5", Backend: "kernel"},          // Atoi принял бы как −5
	})

	want := map[int]string{
		10: "туннель «Amsterdam»",
		12: "системный туннель «Work»",
		13: "туннель awg13",
	}
	if len(got) != len(want) {
		t.Fatalf("вето = %v, ждали %v", got, want)
	}
	for idx, name := range want {
		if got[idx].String() != name {
			t.Errorf("держатель %d = %q, want %q", idx, got[idx], name)
		}
	}
}

// Номер, свободный в пуле, но занятый идентификатором, выдавать нельзя.
func TestKernelIDSkipsTakenIdentifier(t *testing.T) {
	svc, _, _ := serviceForImport(t)
	// awg10 — первый номер обхода. Занимаем его записью nativewg: номер
	// OpkgTun10 она не занимает, а ключ хранилища держит.
	if err := svc.store.Create(&storage.AWGTunnel{ID: "awg10", Name: "Work", Backend: "nativewg"}); err != nil {
		t.Fatal(err)
	}

	id, res, err := svc.kernelID(context.Background(), "новый")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if id == "awg10" {
		t.Fatal("выдан номер, чей идентификатор занят: Create вернёт ErrAlreadyExists")
	}
	if id != "awg11" {
		t.Fatalf("id = %q, ждали awg11", id)
	}
}

// Битый JSON не должен запирать выдачу навсегда: прощающее чтение зовётся
// первым и уносит повреждённую запись в карантин, после чего строгое читает
// уже вычищенный список.
func TestKernelIDQuarantinesCorruptRecordFirst(t *testing.T) {
	svc, dir, _ := serviceForImport(t)
	if err := svc.store.Create(&storage.AWGTunnel{ID: "awg10", Name: "целая"}); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(bad, []byte("{нет"), 0o600); err != nil {
		t.Fatal(err)
	}

	id, res, err := svc.kernelID(context.Background(), "новый")
	if err != nil {
		t.Fatalf("битая запись заперла выдачу: %v", err)
	}
	defer res.Close()
	if id == "" {
		t.Fatal("пустой идентификатор")
	}
	if _, err := os.Stat(bad + ".corrupt"); err != nil {
		t.Errorf("повреждённая запись обязана уйти в карантин: %v", err)
	}
}

// На OS 4.x интерфейсов OpkgTun нет вовсе: номер там ничей, идентификатор
// выдаёт хранилище, резервация пустая. Предикат спрашивается в МОМЕНТ ВЫЗОВА —
// определение версии ОС best-effort, и зафиксированный на старте ответ «это
// 4.x» пережил бы саму 4.x.
func TestKernelIDOnOS4GoesToStore(t *testing.T) {
	svc, _, _ := serviceForImport(t)
	is5 := false
	svc.SetOpkgTunPool(svc.opkgPool, func() bool { return is5 })

	id, res, err := svc.kernelID(context.Background(), "новый")
	if err != nil {
		t.Fatal(err)
	}
	res.Close() // nil-резервация закрывается безопасно
	if !strings.HasPrefix(id, "awgm") {
		t.Fatalf("id = %q, на 4.x ждали awgm*", id)
	}

	// Тот же сервис после «прогрева» отвечает уже по-пятёрочному.
	is5 = true
	id, res, err = svc.kernelID(context.Background(), "новый")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if !strings.HasPrefix(id, "awg") || strings.HasPrefix(id, "awgm") {
		t.Fatalf("id = %q, после прогрева ждали номер пула", id)
	}
}

// Резервация держит номер до записи: пока она открыта, второй проситель его не
// получает. Без этого между выбором и Create номер уводит соседняя подсистема.
func TestKernelIDReservationHoldsNumberUntilClosed(t *testing.T) {
	svc, _, _ := serviceForImport(t)

	first, res1, err := svc.kernelID(context.Background(), "первый")
	if err != nil {
		t.Fatal(err)
	}
	second, res2, err := svc.kernelID(context.Background(), "второй")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Close()
	if first == second {
		t.Fatalf("оба получили %s: резервация номер не держит", first)
	}
	res1.Close()
}

// Пул не подключён — отказ, а не «запасной» номер из хранилища: запасной
// разошёлся бы с пулом и выдал занятое.
func TestKernelIDRefusesWithoutPool(t *testing.T) {
	svc, _, _ := serviceForImport(t)
	svc.opkgPool = nil

	if _, _, err := svc.kernelID(context.Background(), "новый"); err == nil {
		t.Fatal("ждали отказ: пул не подключён")
	}
}

// Страж порядка локов: Reserve НЕ зовётся из-под dir-lock хранилища. Лок
// нереентрантен, и вложенный захват даёт таймаут-отказ 5 с на каждой выдаче.
//
// Источник занятости здесь делает то, что делает боевой, — читает хранилище.
// Если бы выдача шла из-под лока, это чтение встало бы на нём.
func TestKernelIDDoesNotReserveUnderStoreLock(t *testing.T) {
	svc, _, _ := serviceForImport(t)
	var reads int
	svc.SetOpkgTunPool(opkgtun.NewPool(16, opkgtun.Source{
		Name: "читающий хранилище",
		Read: func(context.Context) (opkgtun.Taken, error) {
			reads++
			if _, err := svc.store.ListStrict(); err != nil {
				return nil, err
			}
			// Запись в хранилище берёт тот же dir-lock, что и Update.
			return nil, svc.store.Create(&storage.AWGTunnel{
				ID: "awg15", Name: "проба лока", Backend: "kernel"})
		},
	}), func() bool { return true })

	_, res, err := svc.kernelID(context.Background(), "новый")
	if err != nil {
		t.Fatalf("выдача из-под лока хранилища: %v", err)
	}
	defer res.Close()
	if reads != 1 {
		t.Fatalf("источник прочитан %d раз, ждали один", reads)
	}
}

// Отказ источника занятости — это «не знаем», а не «всё свободно»: выдать
// номер по неполной картине значит получить коллизию.
func TestKernelIDFailsClosedOnOccupancyError(t *testing.T) {
	svc, _, _ := serviceForImport(t)
	svc.SetOpkgTunPool(opkgtun.NewPool(16, opkgtun.Source{
		Name: "недоступный",
		Read: func(context.Context) (opkgtun.Taken, error) {
			return nil, errors.New("RCI молчит")
		},
	}), func() bool { return true })

	if _, _, err := svc.kernelID(context.Background(), "новый"); err == nil {
		t.Fatal("ждали отказ: занятость не собрана")
	}
}

// Гонки: пока резервации ОТКРЫТЫ, параллельные выдачи не отдают один номер
// дважды. Закрытые закрываются в конце — номер после Close снова свободен, и
// проверять уникальность «за всё время» было бы проверкой другого свойства.
func TestKernelIDConcurrentIssuesAreUnique(t *testing.T) {
	svc, _, _ := serviceForImport(t)
	const n = 6
	var mu sync.Mutex
	seen := map[string]bool{}
	failures := 0
	var held []*opkgtun.Reservation
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, res, err := svc.kernelID(context.Background(), "x")
			if err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
				return
			}
			mu.Lock()
			held = append(held, res)
			if seen[id] {
				t.Errorf("идентификатор %s выдан дважды при открытых резервациях", id)
			}
			seen[id] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	for _, r := range held {
		r.Close()
	}
	// Отказы считаются: регресс, при котором пять выдач из шести отказывают,
	// иначе прошёл бы незамеченным — уникальность одной выдачи тривиальна.
	if failures != 0 {
		t.Fatalf("отказов выдачи: %d (пул на %d номеров, заявок %d)", failures, 15, n)
	}
	if len(seen) != n {
		t.Fatalf("уникальных выдач %d, ждали %d", len(seen), n)
	}
}
