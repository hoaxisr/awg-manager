package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// F146: удаление инстанса убирает его сокет, журнал, pid и каталог состояния,
// не задевая файлы соседа с тем же impl/role.
func TestProxyRemoveRuntime(t *testing.T) {
	dir := t.TempDir()
	touch := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mine := []string{
		touch("freeturn-client-client-a.sock"),
		touch("freeturn-client-client-a.log"),
		touch("freeturn-client-client-a.pid"),
	}
	state := filepath.Join(dir, "freeturn-client-client-a.state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	touch("freeturn-client-client-a.state/client_config.json")
	other := touch("freeturn-client-client-b.sock")

	rm := proxyRemoveRuntime(dir)
	if err := rm(instancestore.Record{ID: "a", Kind: instancestore.KindFreeTurnClient}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, p := range append(mine, state) {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s остался", filepath.Base(p))
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("сосед задет: %v", err)
	}
	// Повторный вызов на пустом месте — не ошибка: pid Runner.Stop уже снял.
	if err := rm(instancestore.Record{ID: "a", Kind: instancestore.KindFreeTurnClient}); err != nil {
		t.Fatalf("повтор: %v", err)
	}
	if err := rm(instancestore.Record{ID: "a", Kind: instancestore.Kind("nope")}); err == nil {
		t.Fatal("неизвестная роль должна давать ошибку, а не молчать")
	}
}
