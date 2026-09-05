package api

import (
	"errors"
	"regexp"
	"testing"
)

func stubDateCommandOutput(t *testing.T, fn func() ([]byte, error)) {
	t.Helper()
	old := dateCommandOutput
	dateCommandOutput = fn
	t.Cleanup(func() { dateCommandOutput = old })
}

// Имя файла отчёта берётся у системного `date` — только он применяет DST-хвост
// /etc/TZ. Шов пинует именно этот ответ, целиком и без переформатирования.
func TestDiagnosticsFilenameTimestamp_UsesDateOutput(t *testing.T) {
	stubDateCommandOutput(t, func() ([]byte, error) { return []byte("2026-09-04_12-00-00\n"), nil })
	if got := diagnosticsFilenameTimestamp(); got != "2026-09-04_12-00-00" {
		t.Fatalf("got %q, want 2026-09-04_12-00-00", got)
	}
}

// Отказ форка не должен оставлять имя пустым: фолбэк даёт ту же форму.
func TestDiagnosticsFilenameTimestamp_FallsBackOnError(t *testing.T) {
	stubDateCommandOutput(t, func() ([]byte, error) { return nil, errors.New("no date") })
	got := diagnosticsFilenameTimestamp()
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}$`).MatchString(got) {
		t.Fatalf("фолбэк дал %q — форма имени файла не совпадает с форматом `date`", got)
	}
}

// Пустой ответ `date` равносилен отказу — иначе имя файла осталось бы пустым.
func TestDiagnosticsFilenameTimestamp_FallsBackOnBlankOutput(t *testing.T) {
	stubDateCommandOutput(t, func() ([]byte, error) { return []byte("  \n"), nil })
	got := diagnosticsFilenameTimestamp()
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}$`).MatchString(got) {
		t.Fatalf("на пустом выводе `date` получили %q", got)
	}
}
