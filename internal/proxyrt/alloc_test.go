package proxyrt

import (
	"errors"
	"sync"
	"testing"
)

func TestAllocIndexPrefersPinnedWhenFree(t *testing.T) {
	a := NewAllocator(IndexRange{Min: 17, Max: 49})

	got, err := a.AllocIndex(23, map[int]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != 23 {
		t.Fatalf("индекс %d, ожидали закреплённый 23: на имя OpkgTun23 ссылаются permit'ы пользователя", got)
	}
}

func TestAllocIndexSkipsTaken(t *testing.T) {
	a := NewAllocator(IndexRange{Min: 17, Max: 49})

	got, err := a.AllocIndex(0, map[int]bool{17: true, 18: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != 19 {
		t.Fatalf("индекс %d, ожидали 19", got)
	}
}

func TestAllocIndexPinnedButTakenFallsBack(t *testing.T) {
	a := NewAllocator(IndexRange{Min: 17, Max: 49})

	got, err := a.AllocIndex(17, map[int]bool{17: true})
	if err != nil {
		t.Fatal(err)
	}
	if got == 17 {
		t.Fatal("занятый закреплённый индекс переиспользовать нельзя")
	}
}

func TestAllocIndexExhausted(t *testing.T) {
	a := NewAllocator(IndexRange{Min: 17, Max: 18})

	if _, err := a.AllocIndex(0, map[int]bool{17: true, 18: true}); !errors.Is(err, ErrNoFreeIndex) {
		t.Fatalf("ошибка %v, ожидали ErrNoFreeIndex", err)
	}
}

func TestAllocIndexReleaseReturnsToPool(t *testing.T) {
	a := NewAllocator(IndexRange{Min: 17, Max: 17})

	if _, err := a.AllocIndex(0, map[int]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AllocIndex(0, map[int]bool{}); !errors.Is(err, ErrNoFreeIndex) {
		t.Fatal("занятый номер не должен выдаваться дважды")
	}
	a.Release(17)
	if _, err := a.AllocIndex(0, map[int]bool{}); err != nil {
		t.Fatalf("после освобождения номер обязан выдаваться: %v", err)
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			idx, err := a.AllocIndex(0, map[int]bool{})
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
