package proxyrt

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestAllocIndexPrefersPinnedWhenFree(t *testing.T) {
	a := NewAllocator(IndexRange{Min: 17, Max: 49})

	got, err := a.AllocIndex("inst1", 23, map[int]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != 23 {
		t.Fatalf("индекс %d, ожидали закреплённый 23: на имя OpkgTun23 ссылаются permit'ы пользователя", got)
	}
}

func TestAllocIndexSkipsTaken(t *testing.T) {
	a := NewAllocator(IndexRange{Min: 17, Max: 49})

	got, err := a.AllocIndex("inst1", 0, map[int]bool{17: true, 18: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != 19 {
		t.Fatalf("индекс %d, ожидали 19", got)
	}
}

func TestAllocIndexPinnedButTakenFallsBack(t *testing.T) {
	a := NewAllocator(IndexRange{Min: 17, Max: 49})

	got, err := a.AllocIndex("inst1", 17, map[int]bool{17: true})
	if err != nil {
		t.Fatal(err)
	}
	if got == 17 {
		t.Fatal("занятый закреплённый индекс переиспользовать нельзя")
	}
}

func TestAllocIndexExhausted(t *testing.T) {
	a := NewAllocator(IndexRange{Min: 17, Max: 18})

	if _, err := a.AllocIndex("inst1", 0, map[int]bool{17: true, 18: true}); !errors.Is(err, ErrNoFreeIndex) {
		t.Fatalf("ошибка %v, ожидали ErrNoFreeIndex", err)
	}
}

func TestAllocIndexReleaseReturnsToPool(t *testing.T) {
	a := NewAllocator(IndexRange{Min: 17, Max: 17})

	if _, err := a.AllocIndex("inst1", 0, map[int]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AllocIndex("inst2", 0, map[int]bool{}); !errors.Is(err, ErrNoFreeIndex) {
		t.Fatal("занятый номер не должен выдаваться дважды")
	}
	a.Release("inst1")
	if _, err := a.AllocIndex("inst2", 0, map[int]bool{}); err != nil {
		t.Fatalf("после освобождения номер обязан выдаваться: %v", err)
	}
}

func TestAllocIndexIsIdempotentForSameOwner(t *testing.T) {
	// Повторный проход реконсиляции не должен менять номер интерфейса:
	// на имя OpkgTunN ссылаются permit'ы пользователя в политиках.
	a := NewAllocator(IndexRange{Min: 17, Max: 49})

	first, err := a.AllocIndex("inst1", 23, map[int]bool{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.AllocIndex("inst1", 23, map[int]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("номер сменился с %d на %d при повторном выделении тому же владельцу", first, second)
	}
}

func TestAllocIndexDoesNotGiveOthersHeldNumber(t *testing.T) {
	a := NewAllocator(IndexRange{Min: 17, Max: 49})
	mine, _ := a.AllocIndex("inst1", 17, map[int]bool{})
	other, err := a.AllocIndex("inst2", 17, map[int]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if other == mine {
		t.Fatalf("номер %d выдан двум владельцам", mine)
	}
}

func TestAllocIndexReleaseFreesAllOwnerNumbers(t *testing.T) {
	// Release по владельцу, а не по номеру: иначе остаётся способ освободить
	// чужой номер. Владелец мог удержать несколько номеров.
	a := NewAllocator(IndexRange{Min: 17, Max: 18})

	if _, err := a.AllocIndex("inst1", 17, map[int]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AllocIndex("inst1", 18, map[int]bool{}); err != nil {
		t.Fatal(err)
	}
	a.Release("inst2") // чужой владелец ничего не освобождает
	if _, err := a.AllocIndex("inst3", 0, map[int]bool{}); !errors.Is(err, ErrNoFreeIndex) {
		t.Fatal("Release чужого владельца не должен освобождать номера")
	}

	a.Release("inst1")
	// Освободиться обязаны ОБА номера владельца, а не первый попавшийся:
	// два разных новых владельца должны получить номер каждый.
	if _, err := a.AllocIndex("inst3", 17, map[int]bool{}); err != nil {
		t.Fatalf("после освобождения владельца номер 17 обязан выдаваться: %v", err)
	}
	if _, err := a.AllocIndex("inst4", 18, map[int]bool{}); err != nil {
		t.Fatalf("Release освободил только часть номеров владельца: 18 занят, %v", err)
	}
}

func TestAllocIndexOwnNumberInTakenBreaksPinning(t *testing.T) {
	// Документирует цену нарушения контракта taken: собственный номер,
	// попавший в taken, читается как чужой, и закрепление ломается.
	// Это не желаемое поведение, а зафиксированное следствие.
	a := NewAllocator(IndexRange{Min: 17, Max: 49})

	got, err := a.AllocIndex("inst1", 23, map[int]bool{23: true})
	if err != nil {
		t.Fatal(err)
	}
	if got == 23 {
		t.Fatal("контракт taken изменился — обнови докстроку и этот тест")
	}
}

func TestAllocIndexConcurrentGivesDistinct(t *testing.T) {
	// Два воркера одновременно видят «номер свободен». Без общего лока оба
	// взяли бы одинаковый и столкнулись бы на создании интерфейса.
	a := NewAllocator(IndexRange{Min: 17, Max: 49})

	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[int]bool{}

	for i := 0; i < n; i++ {
		owner := fmt.Sprintf("inst%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			idx, err := a.AllocIndex(owner, 0, map[int]bool{})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			if seen[idx] {
				t.Errorf("индекс %d выдан дважды", idx)
			}
			seen[idx] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
}
