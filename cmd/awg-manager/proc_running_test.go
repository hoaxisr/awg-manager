package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcRunningIn(t *testing.T) {
	dir := t.TempDir()
	put := func(pid, comm string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, pid, "comm"), []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("1", "init")
	put("42", "tunvless")
	put("self", "fircd") // не PID — не процесс

	if procRunningIn(dir, "fircd") {
		t.Error("fircd: нет процесса, а ответ «запущен» — установка рантайма упадёт")
	}
	if !procRunningIn(dir, "tunvless") {
		t.Error("tunvless: процесс есть, а ответ «не запущен»")
	}
	if !procRunningIn(filepath.Join(dir, "missing"), "fircd") {
		t.Error("нечитаемый /proc обязан давать «запущен»")
	}
}
