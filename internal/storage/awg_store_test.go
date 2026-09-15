package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/opkgtun"
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

// Ветка OS4 проверяется через чистую функцию: раньше её покрывал только
// глобальный фолбэк osdetect, а он теперь отдаёт 5.x (см. следующий тест).
func TestNextAvailableIDOS4Kernel(t *testing.T) {
	tunnels := []AWGTunnel{
		{ID: "awgm0", Name: "zero"},
		{ID: "awgm2", Name: "two"},
		{ID: "awg10", Name: "os5-style"},
	}

	got, err := nextAvailableID(tunnels, "kernel", false, mipsOpkgTunCeiling)
	if err != nil {
		t.Fatalf("nextAvailableID() error = %v", err)
	}
	if got != "awgm1" {
		t.Fatalf("nextAvailableID() = %q, want awgm1", got)
	}
}

// Версия ОС неизвестна — считаем 5.x. Фолбэк развёрнут осознанно
// (osdetect.Get): оператор OS5 на четвёрке падает громко, оператор OS4 на
// пятёрке ломает тихо. До этой ветки вообще не должно доходить — версию
// добывают два независимых канала, RCI и ndmc.
func TestAWGTunnelStoreNextAvailableIDUnknownOSAssumesOS5(t *testing.T) {
	ndmsinfo.Reset()
	t.Cleanup(ndmsinfo.Reset)

	store, _ := newTestAWGStore(t)

	if err := store.Create(&AWGTunnel{ID: "awgm0", Name: "zero"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&AWGTunnel{ID: "awg10", Name: "os5-style"}); err != nil {
		t.Fatal(err)
	}

	// Kernel на 5.x сюда больше не приходит — номер ему выдаёт пул. Признак
	// того, что фолбэк остался пятёркой, теперь именно отказ: на четвёрке
	// вернулось бы имя awgm*.
	if got, err := store.NextAvailableID("kernel"); err == nil {
		t.Fatalf("NextAvailableID() = %q, ожидали отказ: на 5.x kernel идёт в пул", got)
	}
	// nativewg на 5.x по-прежнему обслуживается здесь. Различает версии ОС
	// ПРЕФИКС, а не номер: на четвёрке вернулось бы awgm*. Сам номер зависит
	// от потолка архитектуры и проверяется таблицей nextAvailableID.
	got, err := store.NextAvailableID("nativewg")
	if err != nil {
		t.Fatalf("NextAvailableID(nativewg) error = %v", err)
	}
	if !strings.HasPrefix(got, "awg") || strings.HasPrefix(got, "awgm") {
		t.Fatalf("NextAvailableID(nativewg) = %q, ждали имя пятёрки awg<N>", got)
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

// Потолки берутся у КАНОНИЧЕСКОЙ карты по имени архитектуры, а не числом и не
// от runtime.GOARCH: числом это была бы копия карты, а от runtime.GOARCH тест
// зависел бы от машины, на которой его запустили.
var (
	mipsOpkgTunCeiling = opkgtun.Ceiling("mipsle")
	armOpkgTunCeiling  = opkgtun.Ceiling("arm64")
)

func TestNextAvailableIDOS5NativeWG(t *testing.T) {
	kernelFull := []string{"awg10", "awg11", "awg12", "awg13", "awg14", "awg15", "awg16"}
	tests := []struct {
		name    string
		tunnels []AWGTunnel
		ceiling int
		want    string
	}{
		{"empty store", nil, mipsOpkgTunCeiling, "awg20"},
		// Сам баг: kernel-диапазон полностью занят, NativeWG всё равно
		// получает собственный ID awg20 (раньше — ошибка общего лимита 7).
		{"kernel range full", awgTunnelsFromIDs(kernelFull), mipsOpkgTunCeiling, "awg20"},
		{"skips occupied", awgTunnelsFromIDs(
			[]string{"awg20", "awg21"}, "awg20", "awg21"), mipsOpkgTunCeiling, "awg22"},
		{"gap reused", awgTunnelsFromIDs(
			[]string{"awg20", "awg22"}, "awg20", "awg22"), mipsOpkgTunCeiling, "awg21"},
		// Диапазон не ограничен сверху десятью ID: awg20..awg30 заняты → awg31.
		{"beyond ten ids", awgTunnelsFromIDs([]string{
			"awg20", "awg21", "awg22", "awg23", "awg24", "awg25",
			"awg26", "awg27", "awg28", "awg29", "awg30",
		}), mipsOpkgTunCeiling, "awg31"},
		// Легаси NativeWG на awg12 не мешает выдаче нового диапазона.
		{"legacy nwg id untouched", awgTunnelsFromIDs(
			[]string{"awg12"}, "awg12"), mipsOpkgTunCeiling, "awg20"},
		// arm: потолок OpkgTun 49, и kernel туда заходит — диапазон NativeWG
		// обязан начаться ВЫШЕ него, иначе два выбирающих спорят за один ключ.
		{"arm: выше потолка OpkgTun", nil, armOpkgTunCeiling, "awg50"},
		// Легаси NativeWG, осевший в kernel-диапазоне до разведения, свой ключ
		// сохраняет — миграции нет, а вето пула его пропускает.
		{"arm: легаси в kernel-диапазоне", awgTunnelsFromIDs(
			[]string{"awg20"}, "awg20"), armOpkgTunCeiling, "awg50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nextAvailableID(tt.tunnels, "nativewg", true, tt.ceiling)
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
	// Пустой/неизвестный backend — это kernel, и на 5.x он идёт в пул, а не
	// сюда. Отказ, а не «запасной» номер: запасной разошёлся бы с пулом.
	for _, backend := range []string{"", "kernel", "unknown"} {
		if got, err := nextAvailableID(nil, backend, true, mipsOpkgTunCeiling); err == nil {
			t.Fatalf("nextAvailableID(%q) = %q, ожидали отказ", backend, got)
		}
	}
	// OS4: backend не различается — nativewg тоже получает awgm*.
	got, err := nextAvailableID(awgTunnelsFromIDs([]string{"awgm0"}), "nativewg", false, 0)
	if err != nil {
		t.Fatalf("nextAvailableID(OS4) error = %v", err)
	}
	if got != "awgm1" {
		t.Fatalf("nextAvailableID(OS4, nativewg) = %q, want awgm1", got)
	}
}

// Идентификатор awg<N> разбирается тем же разбором, что и имена интерфейсов:
// собственный принимал бы «awg-5» как −5, потому что это принимает Atoi, а
// ручка создания такой идентификатор пропускает.
func TestAWGIdentifierNum(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"awg10", 10, true},
		{"awg007", 7, true},
		{"awg0", 0, true},
		{"awg-5", 0, false},
		{"awg+5", 0, false},
		{"awgm5", 0, false},
		{"awg", 0, false},
		{"wdttraw-home", 0, false},
		{"awg99999999999999999999", 0, false},
	}
	for _, c := range cases {
		got, ok := AWGIdentifierNum(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("AWGIdentifierNum(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
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

// Несущий инвариант F317: пространство идентификаторов NativeWG не пересекается
// с номерами OpkgTun НИ НА ОДНОЙ архитектуре.
//
// Пересечение — не чересполосица, а гонка двух выбирающих за один ключ
// хранилища: номер kernel-туннеля выдаёт пул и держит резервацией до записи, а
// этот перебор видит только диск и открытой резервации не видит. Проигравший
// получает «tunnel already exists» без ретрая.
//
// Потолок берётся у канонической карты, а не числом: копий карты быть не должно.
// Потолок ЭТОЙ сборки спрашивается тем же CeilingForHost, которым проводка
// строит пул, — иначе страж сверял бы карту саму с собой, а разойтись могли бы
// пул и пол.
func TestNWGFloorNeverOverlapsOpkgTunRange(t *testing.T) {
	for _, goarch := range []string{"mips", "mipsle", "mips64", "mips64le", "arm", "arm64", "amd64"} {
		ceiling := opkgtun.Ceiling(goarch)
		if got := nwgFloor(ceiling); got <= ceiling {
			t.Errorf("%s: пол NativeWG = %d, потолок OpkgTun = %d — диапазоны пересекаются",
				goarch, got, ceiling)
		}
	}
	host := opkgtun.CeilingForHost()
	if got := nwgFloor(host); got <= host {
		t.Errorf("этой сборки: пол NativeWG = %d, потолок пула = %d", got, host)
	}
}
