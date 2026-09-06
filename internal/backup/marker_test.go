package backup

import (
	"os"
	"path/filepath"
	"testing"
)

// Маркер, который не удалось снять (на его месте непустой каталог — os.Remove отказывает),
// всё равно считается существовавшим: холодный старт повторится, это безопасная сторона.
func TestPostRestoreMarker_ConsumeReportsExistedWhenRemoveFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "run", "post-restore", "busy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !ConsumePostRestoreMarker(dir) {
		t.Fatal("маркер есть (каталог) — Consume обязан вернуть true")
	}
	if !HasPostRestoreMarker(dir) {
		t.Fatal("несъёмный маркер обязан остаться видимым")
	}
}
