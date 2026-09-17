package presets

import "testing"

// Вшитые пресеты неизменны, а разбирались заново на КАЖДЫЙ GET /api/presets.
// Правка элемента у одного вызывающего не должна доезжать до следующего:
// кэш отдаёт копию внешнего среза.
func TestLoadBuiltins_CallerCannotCorruptCache(t *testing.T) {
	first, err := LoadBuiltins()
	if err != nil {
		t.Fatalf("первый вызов: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("встроенных пресетов нет — тест ничего не проверяет")
	}
	wantID := first[0].ID

	first[0].ID = "испорчено-вызывающим"

	second, err := LoadBuiltins()
	if err != nil {
		t.Fatalf("второй вызов: %v", err)
	}
	if second[0].ID != wantID {
		t.Errorf("ID=%q, ожидался %q — правка вызывающего протекла в кэш", second[0].ID, wantID)
	}
}

// Разбор идёт один раз: второй вызов обязан вернуть тот же состав.
func TestLoadBuiltins_StableAcrossCalls(t *testing.T) {
	a, err := LoadBuiltins()
	if err != nil {
		t.Fatalf("вызов: %v", err)
	}
	b, err := LoadBuiltins()
	if err != nil {
		t.Fatalf("вызов: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("составы разошлись: %d против %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatalf("позиция %d: %q против %q", i, a[i].ID, b[i].ID)
		}
		if a[i].Origin != OriginBuiltin {
			t.Errorf("позиция %d: Origin=%q, ожидался builtin", i, a[i].Origin)
		}
	}
}
