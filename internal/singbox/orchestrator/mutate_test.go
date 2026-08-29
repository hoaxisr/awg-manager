package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newMutateOrch — оркестратор с одним зарегистрированным AlwaysOn-слотом и
// путём к его активному файлу. fakeProc обязателен: применённая мутация
// планирует debounce-reload, и выстреливший таймер идёт в o.proc.
func newMutateOrch(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	dir := t.TempDir()
	o := newFakeOrch(t, dir, &fakeProc{running: true})
	if err := o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return o, filepath.Join(dir, "00-base.json")
}

func mutateReloadScheduled(o *Orchestrator) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.reloadTimer != nil || o.pendingReload
}

// Мутатор видит текущее содержимое слота, результат ложится на диск и
// планирует reload — как обычный Save.
func TestMutate_AppliesAndSchedulesReload(t *testing.T) {
	o, path := newMutateOrch(t)
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var seen []byte
	sawExists := false
	if err := o.Mutate(SlotBase, func(cur []byte, exists bool) ([]byte, error) {
		seen, sawExists = cur, exists
		return []byte(`{"a":2}`), nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	if string(seen) != `{"a":1}` || !sawExists {
		t.Errorf("мутатор получил (%q, exists=%v), want (`{\"a\":1}`, true)", seen, sawExists)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"a":2}` {
		t.Errorf("на диске %q, want `{\"a\":2}`", data)
	}
	if !mutateReloadScheduled(o) {
		t.Error("применённая мутация обязана планировать reload")
	}
}

// nil от мутатора — «менять нечего»: ни записи, ни reload.
func TestMutate_NilResultDoesNotWrite(t *testing.T) {
	o, path := newMutateOrch(t)
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := o.Mutate(SlotBase, func(cur []byte, exists bool) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"a":1}` {
		t.Errorf("файл тронут при nil-результате: %q", data)
	}
	if mutateReloadScheduled(o) {
		t.Error("nil-результат не должен планировать reload")
	}
}

// Байт-в-байт тот же результат reload не планирует — зеркало гейта Save.
func TestMutate_IdenticalBytesDoNotScheduleReload(t *testing.T) {
	o, path := newMutateOrch(t)
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := o.Mutate(SlotBase, func(cur []byte, exists bool) ([]byte, error) {
		return append([]byte(nil), cur...), nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	if mutateReloadScheduled(o) {
		t.Error("неизменившийся слот не должен планировать reload")
	}
}

// Файла слота нет — мутатор зовётся с exists=false и пустым cur, а его
// результат создаёт файл.
func TestMutate_MissingFileReportsNotExists(t *testing.T) {
	o, path := newMutateOrch(t)

	sawExists := true
	var seen []byte
	if err := o.Mutate(SlotBase, func(cur []byte, exists bool) ([]byte, error) {
		seen, sawExists = cur, exists
		return []byte(`{"a":1}`), nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	if sawExists || len(seen) != 0 {
		t.Errorf("мутатор получил (%q, exists=%v), want (пусто, false)", seen, sawExists)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файл не создан: %v", err)
	}
	if string(data) != `{"a":1}` {
		t.Errorf("на диске %q, want `{\"a\":1}`", data)
	}
}

// Ошибка мутатора отменяет запись целиком.
func TestMutate_MutatorErrorSkipsWrite(t *testing.T) {
	o, path := newMutateOrch(t)
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")

	err := o.Mutate(SlotBase, func(cur []byte, exists bool) ([]byte, error) {
		return []byte(`{"a":2}`), boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Mutate = %v, want %v", err, boom)
	}

	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(data) != `{"a":1}` {
		t.Errorf("файл переписан при ошибке мутатора: %q", data)
	}
}
