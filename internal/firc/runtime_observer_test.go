package firc

import "testing"

// Сторож тикает раз в 3 с; подсказка обязана уходить только на смену, иначе
// это тот же опрос, перенесённый на шину.
func TestNoteRuntimeState_FiresOnlyOnChange(t *testing.T) {
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	calls := 0
	op.SetOnRuntimeChange(func() { calls++ })
	step := func(running bool, kdErr string, want int, what string) {
		t.Helper()
		op.noteRuntimeState(running, kdErr)
		if calls != want {
			t.Fatalf("%s: %d calls, want %d", what, calls, want)
		}
	}

	step(true, "", 0, "first observation is a baseline")
	step(true, "", 0, "unchanged")
	step(false, "", 0, "single failed ping is not a crash")
	step(true, "", 0, "recovered before confirmation")
	step(false, "", 0, "first failure again")
	step(false, "", 1, "second consecutive failure = down")
	step(false, "", 1, "still down")
	step(true, "", 2, "back up")
	step(true, "dial 10.0.0.1:41234: timeout", 3, "keendns error appeared")
	step(true, "dial 10.0.0.1:51877: timeout", 3, "only error text changed")
	step(true, "", 4, "keendns error cleared")
}
