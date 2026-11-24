package firc

import (
	"errors"
	"testing"
)

// Конец операции рантайма — подсказка firc с любым исходом: без неё вкладка,
// не державшая опрос хода, так и показывала бы «устанавливается».
func TestOnDemandInstaller_OnFinishedFiresOnSuccessAndFailure(t *testing.T) {
	i := NewOnDemandInstaller("aarch64-3.10", FircBinarySpec{Version: "0.4.0"})
	calls := 0
	i.SetOnFinished(func() {
		// Подписчик зовётся вне i.mu: статус из него читается без дедлока.
		i.mu.Lock()
		busy := i.busy
		i.mu.Unlock()
		if busy {
			t.Error("onFinished saw busy=true")
		}
		calls++
	})
	if err := i.reserve("install"); err != nil {
		t.Fatal(err)
	}
	i.finish(nil)
	if err := i.reserve("upgrade"); err != nil {
		t.Fatal(err)
	}
	i.finish(errors.New("boom"))
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}
