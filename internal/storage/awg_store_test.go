package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

func newTestAWGStore(t *testing.T) (*AWGTunnelStore, string) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "tunnels")
	lockDir := filepath.Join(t.TempDir(), "locks")
	return NewAWGTunnelStoreWithLockDir(dataDir, lockDir), dataDir
}

func TestAWGTunnelStoreListMissingDirReturnsEmptySlice(t *testing.T) {
	store, _ := newTestAWGStore(t)

	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got == nil {
		t.Fatal("List() returned nil slice, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("List() len = %d, want 0", len(got))
	}
}

func TestAWGTunnelStoreCreateDefaultsTypeAndDoesNotEscapeHTML(t *testing.T) {
	store, dataDir := newTestAWGStore(t)

	tun := &AWGTunnel{
		ID:   "awg1",
		Name: "test",
		Interface: AWGInterface{
			AWGObfuscation: AWGObfuscation{I1: "<sig>"},
		},
	}

	if err := store.Create(tun); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if tun.Type != "awg" {
		t.Fatalf("Create() mutated Type = %q, want awg", tun.Type)
	}

	raw, err := os.ReadFile(filepath.Join(dataDir, "awg1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"type": "awg"`)) {
		t.Fatalf("saved JSON does not contain default type: %s", raw)
	}
	if bytes.Contains(raw, []byte(`\u003c`)) || bytes.Contains(raw, []byte(`\u003e`)) {
		t.Fatalf("saved JSON escaped HTML markers: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`<sig>`)) {
		t.Fatalf("saved JSON does not preserve raw signature marker: %s", raw)
	}
}

func TestAWGTunnelStoreGetBackfillsLegacyDefaults(t *testing.T) {
	store, dataDir := newTestAWGStore(t)

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{
		"id": "legacy",
		"name": "legacy"
	}`)
	if err := os.WriteFile(filepath.Join(dataDir, "legacy.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get("legacy")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Type != "awg" {
		t.Fatalf("Type = %q, want awg", got.Type)
	}
	if !got.DefaultRoute {
		t.Fatal("DefaultRoute = false, want true for legacy tunnel")
	}
	if !got.DefaultRouteSet {
		t.Fatal("DefaultRouteSet = false, want true for legacy tunnel")
	}
}

func TestAWGTunnelStoreGetMissingReturnsNotFoundError(t *testing.T) {
	store, _ := newTestAWGStore(t)

	got, err := store.Get("missing")
	if err == nil {
		t.Fatal("Get() error = nil, want error")
	}
	if got != nil {
		t.Fatalf("Get() tunnel = %#v, want nil", got)
	}
	if !strings.Contains(err.Error(), "tunnel not found: missing") {
		t.Fatalf("error = %q, want tunnel not found", err)
	}
}

func TestAWGTunnelStoreGetInvalidJSONReturnsParseError(t *testing.T) {
	store, dataDir := newTestAWGStore(t)

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "bad.json"), []byte(`{"id":`), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get("bad")
	if err == nil {
		t.Fatal("Get() error = nil, want parse error")
	}
	if got != nil {
		t.Fatalf("Get() tunnel = %#v, want nil", got)
	}
	if !strings.Contains(err.Error(), "parse tunnel JSON") {
		t.Fatalf("error = %q, want parse tunnel JSON", err)
	}
}

func TestAWGTunnelStoreListSkipsNonJSONDirsAndInvalidJSON(t *testing.T) {
	store, dataDir := newTestAWGStore(t)

	if err := os.MkdirAll(filepath.Join(dataDir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "note.txt"), []byte("ignore"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "bad.json"), []byte(`{"id":`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "ok.json"), []byte(`{"id":"ok","name":"ok"}`), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].ID != "ok" {
		t.Fatalf("List()[0].ID = %q, want ok", got[0].ID)
	}
	if got[0].Type != "awg" {
		t.Fatalf("List()[0].Type = %q, want awg", got[0].Type)
	}
	if !got[0].DefaultRoute || !got[0].DefaultRouteSet {
		t.Fatalf("legacy defaults not backfilled: %#v", got[0])
	}
}

func TestAWGTunnelStoreDeleteRemovesFile(t *testing.T) {
	store, dataDir := newTestAWGStore(t)

	if err := store.Create(&AWGTunnel{ID: "awg1", Name: "test"}); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete("awg1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, "awg1.json")); !os.IsNotExist(err) {
		t.Fatalf("file still exists or unexpected stat error: %v", err)
	}
}

func TestAWGTunnelStoreDeleteMissingReturnsNotFound(t *testing.T) {
	store, _ := newTestAWGStore(t)

	err := store.Delete("missing")
	if err == nil {
		t.Fatal("Delete() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "tunnel not found: missing") {
		t.Fatalf("error = %q, want tunnel not found", err)
	}
}

func TestAWGTunnelStoreExists(t *testing.T) {
	store, _ := newTestAWGStore(t)

	if store.Exists("awg1") {
		t.Fatal("Exists() = true before Create, want false")
	}

	if err := store.Create(&AWGTunnel{ID: "awg1", Name: "test"}); err != nil {
		t.Fatal(err)
	}

	if !store.Exists("awg1") {
		t.Fatal("Exists() = false after Create, want true")
	}
}

func TestAWGTunnelStoreNextAvailableIDOS4Fallback(t *testing.T) {
	ndmsinfo.Reset()
	t.Cleanup(ndmsinfo.Reset)

	store, _ := newTestAWGStore(t)

	if err := store.Create(&AWGTunnel{ID: "awgm0", Name: "zero"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&AWGTunnel{ID: "awgm2", Name: "two"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&AWGTunnel{ID: "awg10", Name: "os5-style"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.NextAvailableID(context.Background(), "kernel", occupancyOf())
	if err != nil {
		t.Fatalf("NextAvailableID() error = %v", err)
	}
	if got != "awgm1" {
		t.Fatalf("NextAvailableID() = %q, want awgm1", got)
	}
}

// awgTunnelsFromIDs builds a tunnel list from IDs; the IDs listed in
// nativewg get Backend "nativewg", the rest — "kernel".
func awgTunnelsFromIDs(ids []string, nativewg ...string) []AWGTunnel {
	nwgSet := make(map[string]bool, len(nativewg))
	for _, id := range nativewg {
		nwgSet[id] = true
	}
	out := make([]AWGTunnel, 0, len(ids))
	for _, id := range ids {
		t := AWGTunnel{ID: id, Name: id, Backend: "kernel"}
		if nwgSet[id] {
			t.Backend = "nativewg"
		}
		out = append(out, t)
	}
	return out
}

func TestNextAvailableIDOS5Kernel(t *testing.T) {
	tests := []struct {
		name    string
		tunnels []AWGTunnel
		want    string
	}{
		{"empty store", nil, "awg10"},
		{"first free", awgTunnelsFromIDs([]string{"awg10", "awg11"}), "awg12"},
		{"gap reused", awgTunnelsFromIDs([]string{"awg10", "awg12"}), "awg11"},
		// Легаси NativeWG-туннель на awg12 занимает номер в kernel-диапазоне —
		// kernel-аллокатор обязан его пропустить (без миграции).
		{"skips legacy nativewg id", awgTunnelsFromIDs(
			[]string{"awg10", "awg11", "awg12"}, "awg12"), "awg13"},
		// NativeWG-туннели нового диапазона (awg20+) kernel-диапазон не съедают.
		{"ignores nwg range ids", awgTunnelsFromIDs(
			[]string{"awg20", "awg21"}, "awg20", "awg21"), "awg10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nextAvailableID(tt.tunnels, "kernel", true, occupancyOf(), context.Background())
			if err != nil {
				t.Fatalf("nextAvailableID() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("nextAvailableID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNextAvailableIDOS5KernelExhaustion(t *testing.T) {
	// awg10..awg16 заняты — прошивочный потолок kernel-AWG (7 туннелей).
	// Занятость учитывается по ID независимо от backend: легаси NativeWG
	// на awg16 тоже съедает kernel-слот.
	ids := []string{"awg10", "awg11", "awg12", "awg13", "awg14", "awg15", "awg16"}
	for _, legacy := range []string{"", "awg16"} {
		tunnels := awgTunnelsFromIDs(ids, legacy)
		_, err := nextAvailableID(tunnels, "kernel", true, occupancyOf(), context.Background())
		if err == nil {
			t.Fatalf("nextAvailableID() error = nil, want exhaustion (legacy=%q)", legacy)
		}
		if err.Error() != "maximum number of tunnels reached (7)" {
			t.Fatalf("exhaustion message = %q, want byte-identical legacy message", err.Error())
		}
	}
}

func TestNextAvailableIDOS5NativeWG(t *testing.T) {
	kernelFull := []string{"awg10", "awg11", "awg12", "awg13", "awg14", "awg15", "awg16"}
	tests := []struct {
		name    string
		tunnels []AWGTunnel
		want    string
	}{
		{"empty store", nil, "awg20"},
		// Сам баг: kernel-диапазон полностью занят, NativeWG всё равно
		// получает собственный ID awg20 (раньше — ошибка общего лимита 7).
		{"kernel range full", awgTunnelsFromIDs(kernelFull), "awg20"},
		{"skips occupied", awgTunnelsFromIDs(
			[]string{"awg20", "awg21"}, "awg20", "awg21"), "awg22"},
		{"gap reused", awgTunnelsFromIDs(
			[]string{"awg20", "awg22"}, "awg20", "awg22"), "awg21"},
		// Диапазон не ограничен сверху десятью ID: awg20..awg30 заняты → awg31.
		{"beyond ten ids", awgTunnelsFromIDs([]string{
			"awg20", "awg21", "awg22", "awg23", "awg24", "awg25",
			"awg26", "awg27", "awg28", "awg29", "awg30",
		}), "awg31"},
		// Легаси NativeWG на awg12 не мешает выдаче нового диапазона.
		{"legacy nwg id untouched", awgTunnelsFromIDs(
			[]string{"awg12"}, "awg12"), "awg20"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nextAvailableID(tt.tunnels, "nativewg", true, nil, context.Background())
			if err != nil {
				t.Fatalf("nextAvailableID() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("nextAvailableID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNextAvailableIDBackendFallbacks(t *testing.T) {
	// Пустой/неизвестный backend трактуется как kernel (OS5).
	for _, backend := range []string{"", "kernel", "unknown"} {
		got, err := nextAvailableID(nil, backend, true, occupancyOf(), context.Background())
		if err != nil {
			t.Fatalf("nextAvailableID(%q) error = %v", backend, err)
		}
		if got != "awg10" {
			t.Fatalf("nextAvailableID(%q) = %q, want awg10", backend, got)
		}
	}
	// OS4: backend не различается — nativewg тоже получает awgm*.
	got, err := nextAvailableID(awgTunnelsFromIDs([]string{"awgm0"}), "nativewg", false, nil, context.Background())
	if err != nil {
		t.Fatalf("nextAvailableID(OS4) error = %v", err)
	}
	if got != "awgm1" {
		t.Fatalf("nextAvailableID(OS4, nativewg) = %q, want awgm1", got)
	}
}

// TestAWGTunnelStoreRefusesMalformedIDs — id подставляется в путь файла,
// поэтому хранилище само не пускает «../settings» ни в одном методе, а не
// доверяет вызывающему: REST валидирует, MCP и будущие вызовы — не факт.
func TestAWGTunnelStoreRefusesMalformedIDs(t *testing.T) {
	store, dataDir := newTestAWGStore(t)
	// A neighbour file the traversal would otherwise reach.
	secret := filepath.Join(filepath.Dir(dataDir), "settings.json")
	if err := os.WriteFile(secret, []byte(`{"id":"x","name":"leak"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../settings", "awg/1", "awg.json", "1abc", "a b"} {
		if _, err := store.Get(id); err == nil {
			t.Errorf("Get(%q) succeeded", id)
		}
		if store.Exists(id) {
			t.Errorf("Exists(%q) = true", id)
		}
		if err := store.Update(id, func(*AWGTunnel) error { return nil }); err == nil {
			t.Errorf("Update(%q) succeeded", id)
		}
		if err := store.Delete(id); err == nil {
			t.Errorf("Delete(%q) succeeded", id)
		}
		if err := store.Create(&AWGTunnel{ID: id, Name: "x"}); err == nil {
			t.Errorf("Create(%q) succeeded", id)
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("the neighbour file was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "..", "settings.json.json")); err == nil {
		t.Fatal("Create wrote outside the tunnel directory")
	}
}
