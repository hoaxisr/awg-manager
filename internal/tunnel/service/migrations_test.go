package service

import (
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Бутовые миграции записей туннелей не имели покрытия ВОВСЕ (проверено грепом
// на 2026-08-29), а бегут они на каждом старте и правят пользовательские
// данные. K10 переписал их с «снимок List → Save» на «снимок как гейт →
// Update с перепроверкой по свежей записи», так что базовое поведение стоит
// закрепить хотя бы на счастливом пути.
//
// Перепроверка по свежей записи здесь НЕ пинится: чтобы свежая запись
// разошлась со снимком, нужна параллельная правка ровно между List и Update,
// а вклиниться туда нечем — `s.store` конкретного типа, шва нет. Заводить
// ради этого прод-шов план не просил.
func newMigrationService(t *testing.T) (*ServiceImpl, *storage.AWGTunnelStore) {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	return &ServiceImpl{store: store}, store
}

func TestMigrateISPInterfaceNone_ClearsSentinel(t *testing.T) {
	svc, store := newMigrationService(t)
	if err := store.Create(&storage.AWGTunnel{ID: "awg10", ISPInterface: "none"}); err != nil {
		t.Fatalf("сид: %v", err)
	}
	if err := store.Create(&storage.AWGTunnel{ID: "awg11", ISPInterface: "eth3"}); err != nil {
		t.Fatalf("сид: %v", err)
	}

	svc.MigrateISPInterfaceNone()

	migrated, err := store.Get("awg10")
	if err != nil {
		t.Fatal(err)
	}
	if migrated.ISPInterface != "" {
		t.Errorf(`ISPInterface = %q, want "" (сентинел "none" не снят)`, migrated.ISPInterface)
	}
	untouched, err := store.Get("awg11")
	if err != nil {
		t.Fatal(err)
	}
	if untouched.ISPInterface != "eth3" {
		t.Errorf("чужой ISPInterface затронут: %q", untouched.ISPInterface)
	}
}

func TestMigrateEmptyBackend_FillsKernel(t *testing.T) {
	svc, store := newMigrationService(t)
	if err := store.Create(&storage.AWGTunnel{ID: "awg10"}); err != nil {
		t.Fatalf("сид: %v", err)
	}
	if err := store.Create(&storage.AWGTunnel{ID: "awg11", Backend: "nativewg"}); err != nil {
		t.Fatalf("сид: %v", err)
	}

	svc.MigrateEmptyBackend()

	migrated, err := store.Get("awg10")
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Backend != "kernel" {
		t.Errorf("Backend = %q, want kernel", migrated.Backend)
	}
	untouched, err := store.Get("awg11")
	if err != nil {
		t.Fatal(err)
	}
	if untouched.Backend != "nativewg" {
		t.Errorf("чужой Backend переписан: %q", untouched.Backend)
	}
}
