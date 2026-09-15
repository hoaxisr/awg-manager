package proxyrt

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestAllocPortPrefersPinnedWhenFree(t *testing.T) {
	a := NewAllocator(PortRange{Min: 9000, Max: 9200})

	got, err := a.AllocPort("inst1", 9006, true, map[int]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got != 9006 {
		t.Fatalf("порт %d, ожидали закреплённый 9006: порт стоит в ссылке абонента, и переезд рвёт соединение", got)
	}
}

func TestAllocPortSkipsTaken(t *testing.T) {
	a := NewAllocator(PortRange{Min: 9000, Max: 9200})

	got, err := a.AllocPort("inst1", 0, false, map[int]bool{9000: true, 9001: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != 9002 {
		t.Fatalf("порт %d, ожидали 9002", got)
	}
}

func TestAllocPortPinnedButTakenFallsBack(t *testing.T) {
	a := NewAllocator(PortRange{Min: 9000, Max: 9200})

	got, err := a.AllocPort("inst1", 9000, true, map[int]bool{9000: true})
	if err != nil {
		t.Fatal(err)
	}
	if got == 9000 {
		t.Fatal("занятый закреплённый порт переиспользовать нельзя")
	}
}

func TestAllocPortExhausted(t *testing.T) {
	a := NewAllocator(PortRange{Min: 9000, Max: 9001})

	if _, err := a.AllocPort("inst1", 0, false, map[int]bool{9000: true, 9001: true}); !errors.Is(err, ErrNoFreePort) {
		t.Fatalf("ошибка %v, ожидали ErrNoFreePort", err)
	}
}

func TestAllocPortReleaseReturnsToPool(t *testing.T) {
	a := NewAllocator(PortRange{Min: 9000, Max: 9000})

	if _, err := a.AllocPort("inst1", 0, false, map[int]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AllocPort("inst2", 0, false, map[int]bool{}); !errors.Is(err, ErrNoFreePort) {
		t.Fatal("занятый порт не должен выдаваться дважды")
	}
	a.Release("inst1")
	if _, err := a.AllocPort("inst2", 0, false, map[int]bool{}); err != nil {
		t.Fatalf("после освобождения порт обязан выдаваться: %v", err)
	}
}

func TestAllocPortIsIdempotentForSameOwner(t *testing.T) {
	// Повторный проход реконсиляции не должен менять порт: он стоит в ссылке
	// абонента снаружи, и переезд рвёт соединение.
	a := NewAllocator(PortRange{Min: 9000, Max: 9200})

	first, err := a.AllocPort("inst1", 9006, true, map[int]bool{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.AllocPort("inst1", 9006, true, map[int]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("порт сменился с %d на %d при повторном выделении тому же владельцу", first, second)
	}
}

func TestAllocPortDoesNotGiveOthersHeldPort(t *testing.T) {
	a := NewAllocator(PortRange{Min: 9000, Max: 9200})
	mine, _ := a.AllocPort("inst1", 9000, true, map[int]bool{})
	other, err := a.AllocPort("inst2", 9000, true, map[int]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if other == mine {
		t.Fatalf("порт %d выдан двум владельцам", mine)
	}
}

func TestAllocPortReleaseIsByOwnerNotByPort(t *testing.T) {
	// Release по владельцу, а не по порту: иначе остаётся способ освободить
	// чужой порт. Владельцы — РАЗНЫЕ ключи одного инстанса, как в проде
	// (`key/wg`, `key/raw`, `key/listen`): за каждым ровно один порт.
	a := NewAllocator(PortRange{Min: 9000, Max: 9001})

	if _, err := a.AllocPort("inst1/wg", 9000, true, map[int]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AllocPort("inst1/raw", 9001, true, map[int]bool{}); err != nil {
		t.Fatal(err)
	}
	a.Release("inst2") // чужой владелец ничего не освобождает
	if _, err := a.AllocPort("inst3", 0, false, map[int]bool{}); !errors.Is(err, ErrNoFreePort) {
		t.Fatal("Release чужого владельца не должен освобождать порты")
	}

	a.Release("inst1/wg")
	if _, err := a.AllocPort("inst3", 9000, true, map[int]bool{}); err != nil {
		t.Fatalf("после освобождения владельца порт 9000 обязан выдаваться: %v", err)
	}
	// Освобождён ровно один владелец: порт второго остаётся за ним.
	if _, err := a.AllocPort("inst4", 9001, true, map[int]bool{}); !errors.Is(err, ErrNoFreePort) {
		t.Fatal("Release одного ключа не должен освобождать порт другого")
	}
}

// За владельцем остаётся РОВНО ОДИН порт: переезжая, он отдаёт прежний.
// Иначе порт, с которого владелец ушёл (listen-порт, занятый чужим
// туннелем), висел бы за ним до удаления инстанса или рестарта демона —
// недоступный другим и уже никому не нужный.
func TestAllocPortMoveReleasesPreviousPort(t *testing.T) {
	a := NewAllocator(PortRange{Min: 9000, Max: 9001})

	first, err := a.AllocPort("inst1/listen", 9000, true, map[int]bool{})
	if err != nil || first != 9000 {
		t.Fatalf("первый порт: %d, %v", first, err)
	}
	// 9000 занят снаружи — владелец переезжает на 9001.
	moved, err := a.AllocPort("inst1/listen", 9000, true, map[int]bool{9000: true})
	if err != nil || moved != 9001 {
		t.Fatalf("переезд: %d, %v", moved, err)
	}
	// 9000 обязан снова выдаваться: прежний захват снят вместе с переездом.
	if got, err := a.AllocPort("inst2", 9000, true, map[int]bool{}); err != nil || got != 9000 {
		t.Fatalf("прежний порт не освобождён: %d, %v", got, err)
	}
}

func TestAllocPortOwnPortInTakenBreaksPinning(t *testing.T) {
	// Документирует цену нарушения контракта taken: собственный порт,
	// попавший в taken, читается как чужой, и закрепление ломается.
	// Это не желаемое поведение, а зафиксированное следствие.
	a := NewAllocator(PortRange{Min: 9000, Max: 9200})

	got, err := a.AllocPort("inst1", 9006, true, map[int]bool{9006: true})
	if err != nil {
		t.Fatal(err)
	}
	if got == 9006 {
		t.Fatal("контракт taken изменился — обнови докстроку и этот тест")
	}
}

func TestAllocPortConcurrentGivesDistinct(t *testing.T) {
	// Два воркера одновременно видят «порт свободен». Без общего лока оба
	// взяли бы одинаковый, и второй инстанс не поднялся бы: адрес занят.
	a := NewAllocator(PortRange{Min: 9000, Max: 9200})

	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[int]bool{}

	for i := 0; i < n; i++ {
		owner := fmt.Sprintf("inst%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			idx, err := a.AllocPort(owner, 0, false, map[int]bool{})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			if seen[idx] {
				t.Errorf("порт %d выдан дважды", idx)
			}
			seen[idx] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
}

// Закреплённый порт ВНЕ диапазона обязан подменяться на свободный, а не
// выдаваться как есть: порт приходит из конфига снаружи, а валидатор ссылки
// абонента (roles/linkres) порт вне пула отвергает — инстанс с ним не поднялся
// бы вовсе.
func TestAllocPortRefusesPinOutsideRange(t *testing.T) {
	a := NewAllocator(PortRange{Min: 9000, Max: 9001})

	got, err := a.AllocPort("inst1", 80, true, map[int]bool{})
	if err != nil {
		t.Fatalf("выдача: %v", err)
	}
	if got == 80 {
		t.Fatal("выдан закреплённый порт вне диапазона: валидатор отвергнет его, инстанс не поднимется")
	}
	if got < 9000 || got > 9001 {
		t.Fatalf("порт %d вне диапазона 9000..9001", got)
	}
}
