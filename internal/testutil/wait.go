package testutil

import (
	"testing"
	"time"
)

// WaitFor ждёт события, а не срока: минута — потолок на случай, когда событие
// не наступит никогда, и от загрузки машины исход не зависит. Где нет настоящего
// ввода-вывода, вместо ожиданий — testing/synctest.
func WaitFor(t testing.TB, cond func() bool, msg string) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
	}
}
