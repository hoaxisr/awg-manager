package firewall

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newHookTestManager(t *testing.T) *ManagerImpl {
	t.Helper()
	m := New(true, false, nil) // kernel backend: mssClamp on, ndms off
	dir := t.TempDir()
	m.hookPath = filepath.Join(dir, "netfilter.d", "51-awgm-tunnel-fw.sh")
	m.listPath = filepath.Join(dir, "tunnel-fw.list")
	return m
}

func TestHookState_AddPutsIfaceAndInstallsHook(t *testing.T) {
	m := newHookTestManager(t)
	if err := m.syncHookState("awgm0", true); err != nil {
		t.Fatalf("sync: %v", err)
	}
	list, err := os.ReadFile(m.listPath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := strings.TrimSpace(string(list)); got != "awgm0 mss" {
		t.Fatalf("list content %q", got)
	}
	hook, err := os.ReadFile(m.hookPath)
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	for _, want := range []string{"#!/bin/sh", "MASQUERADE", m.listPath, "TCPMSS"} {
		if !strings.Contains(string(hook), want) {
			t.Fatalf("hook missing %q", want)
		}
	}
	info, _ := os.Stat(m.hookPath)
	if info.Mode()&0o111 == 0 {
		t.Fatal("hook not executable")
	}
}

func TestHookState_RemoveDropsIface(t *testing.T) {
	m := newHookTestManager(t)
	_ = m.syncHookState("awgm0", true)
	_ = m.syncHookState("awgm1", true)
	if err := m.syncHookState("awgm0", false); err != nil {
		t.Fatalf("sync: %v", err)
	}
	list, _ := os.ReadFile(m.listPath)
	if got := strings.TrimSpace(string(list)); got != "awgm1 mss" {
		t.Fatalf("list content %q", got)
	}
}

// Одновременный старт двух туннелей на общем менеджере: без сериализации
// read-modify-write списка один из интерфейсов теряется.
func TestHookState_ConcurrentAddsKeepBoth(t *testing.T) {
	m := newHookTestManager(t)
	var wg sync.WaitGroup
	for _, iface := range []string{"awgm0", "awgm1"} {
		wg.Add(1)
		go func(iface string) {
			defer wg.Done()
			if err := m.syncHookState(iface, true); err != nil {
				t.Errorf("sync %s: %v", iface, err)
			}
		}(iface)
	}
	wg.Wait()
	list, err := os.ReadFile(m.listPath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := strings.TrimSpace(string(list)); got != "awgm0 mss\nawgm1 mss" {
		t.Fatalf("list content %q", got)
	}
}

func TestHookState_NDMSManagedSkips(t *testing.T) {
	m := newHookTestManager(t)
	m.ndmsManaged = true
	if err := m.syncHookState("awgm0", true); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, err := os.Stat(m.listPath); !os.IsNotExist(err) {
		t.Fatal("ndmsManaged must not write hook state")
	}
}
