package opkg

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Долгий opkg update держит мьютекс до пяти минут. Чтения не должны молча
// стоять в этой очереди: страница висит без единого признака жизни.
func TestReadsDoNotQueueBehindLongOperation(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "opkg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{Bin: bin}

	c.mu.Lock() // имитируем идущий update
	defer c.mu.Unlock()

	cases := []struct {
		name string
		call func() error
	}{
		{"ListInstalled", func() error { _, err := c.ListInstalled(); return err }},
		{"ListUpgradable", func() error { _, err := c.ListUpgradable(); return err }},
		{"Search", func() error { _, err := c.Search("curl"); return err }},
		{"ListAvailable", func() error { _, _, err := c.ListAvailable("", 0, 50); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- tc.call() }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrBusy) {
					t.Errorf("ошибка = %v, ожидалась ErrBusy", err)
				}
			case <-time.After(time.Second):
				t.Error("чтение встало в очередь за долгой операцией")
			}
		})
	}
}
