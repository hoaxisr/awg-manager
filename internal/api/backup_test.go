package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/backup"
	"github.com/hoaxisr/awg-manager/internal/events"
)

// archiveOf — gzip-архив каталога с settings.json данного содержимого (через backup.Export).
func archiveOf(t *testing.T, settingsJSON string) []byte {
	t.Helper()
	src := filepath.Join(t.TempDir(), "awg-manager")
	if err := os.MkdirAll(filepath.Join(src, "tunnels"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := backup.Export(src, "9.9.9-fixture", &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func backupUpload(t *testing.T, filename string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(content)
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

type backupHarness struct {
	dataDir string
	events  []string
	h       *BackupHandler
}

func newBackupHarness(t *testing.T, quiesceErr error) *backupHarness {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":1,"marker":"BEFORE"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	bh := &backupHarness{dataDir: dataDir}
	quiesce := func(context.Context) error { bh.events = append(bh.events, "quiesce"); return quiesceErr }
	resume := func(context.Context) { bh.events = append(bh.events, "resume") }
	restart := func() {
		// Что лежит в dataDir в момент рестарта — и есть порядок restore→restart.
		b, _ := os.ReadFile(filepath.Join(dataDir, "settings.json"))
		if bytes.Contains(b, []byte("AFTER")) {
			bh.events = append(bh.events, "restart:restored")
		} else {
			bh.events = append(bh.events, "restart:stale")
		}
	}
	bh.h = NewBackupHandler(dataDir, "1.2.3", quiesce, resume, restart, nil)
	return bh
}

// Успешный импорт: quiesce → Restore (данные на диске уже новые) → restart; resume не зовётся.
func TestBackupImport_QuiesceRestoreRestartOrder(t *testing.T) {
	bh := newBackupHarness(t, nil)
	rr := httptest.NewRecorder()
	bh.h.Import(rr, backupUpload(t, "backup.tar.gz", archiveOf(t, `{"version":1,"marker":"AFTER"}`)))
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if want := []string{"quiesce", "restart:restored"}; !reflect.DeepEqual(bh.events, want) {
		t.Fatalf("порядок = %v, want %v", bh.events, want)
	}
	b, _ := os.ReadFile(filepath.Join(bh.dataDir, "settings.json"))
	if !bytes.Contains(b, []byte("AFTER")) {
		t.Fatalf("settings.json не заменён: %s", b)
	}
}

// Провал Restore (архив без settings.json): BACKUP_IMPORT_FAILED, resume позван, рестарта НЕТ,
// старые данные целы. Мутация трекера — снятый return после ошибки — делала рестарт и «успех».
func TestBackupImport_RestoreFailureResumesAndDoesNotRestart(t *testing.T) {
	bh := newBackupHarness(t, nil)
	var bad bytes.Buffer
	gz := gzip.NewWriter(&bad)
	gz.Write([]byte("not a tar"))
	gz.Close()
	rr := httptest.NewRecorder()
	bh.h.Import(rr, backupUpload(t, "backup.tgz", bad.Bytes()))
	if rr.Code == 200 || decodeJSONBody(t, rr)["code"] != "BACKUP_IMPORT_FAILED" {
		t.Fatalf("ожидался BACKUP_IMPORT_FAILED, got %d %s", rr.Code, rr.Body.String())
	}
	if want := []string{"quiesce", "resume"}; !reflect.DeepEqual(bh.events, want) {
		t.Fatalf("порядок = %v, want %v", bh.events, want)
	}
	b, _ := os.ReadFile(filepath.Join(bh.dataDir, "settings.json"))
	if !bytes.Contains(b, []byte("BEFORE")) {
		t.Fatalf("старые данные повреждены: %s", b)
	}
}

func TestBackupImport_RejectsBadFormatAndQuiesceFailure(t *testing.T) {
	bh := newBackupHarness(t, nil)
	rr := httptest.NewRecorder()
	bh.h.Import(rr, backupUpload(t, "backup.zip", []byte("zip")))
	if decodeJSONBody(t, rr)["code"] != "BACKUP_IMPORT_BAD_FORMAT" || len(bh.events) != 0 {
		t.Fatalf("плохое расширение: %s events=%v", rr.Body.String(), bh.events)
	}
	bq := newBackupHarness(t, errors.New("busy"))
	rr = httptest.NewRecorder()
	bq.h.Import(rr, backupUpload(t, "backup.tar.gz", archiveOf(t, `{"version":1}`)))
	if decodeJSONBody(t, rr)["code"] != "BACKUP_QUIESCE_FAILED" || !reflect.DeepEqual(bq.events, []string{"quiesce"}) {
		t.Fatalf("отказ quiesce: %s events=%v", rr.Body.String(), bq.events)
	}
}

// Export: GET → gzip-архив с settings.json, quiesce до и resume после.
func TestBackupExport_StreamsArchiveAndResumes(t *testing.T) {
	bh := newBackupHarness(t, nil)
	rr := httptest.NewRecorder()
	bh.h.Export(rr, httptest.NewRequest(http.MethodGet, "/api/backup/export", nil))
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("code=%d ct=%q", rr.Code, rr.Header().Get("Content-Type"))
	}
	if want := []string{"quiesce", "resume"}; !reflect.DeepEqual(bh.events, want) {
		t.Fatalf("порядок = %v, want %v", bh.events, want)
	}
	gr, err := gzip.NewReader(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(gr)
	if !bytes.Contains(raw, []byte("settings.json")) || !bytes.Contains(raw, []byte("BEFORE")) {
		t.Fatal("в архиве нет settings.json с текущим содержимым")
	}
	if rr := httptest.NewRecorder(); true {
		bh.h.Export(rr, httptest.NewRequest(http.MethodPost, "/api/backup/export", nil))
		if rr.Code != 405 {
			t.Fatalf("POST → 405, got %d", rr.Code)
		}
	}
}

// Откат на снимок идёт тем же путём, что импорт: quiesce → Restore → restart.
func TestBackupSnapshot_RestoreListDownloadDelete(t *testing.T) {
	bh := newBackupHarness(t, nil)
	if err := os.WriteFile(filepath.Join(bh.dataDir, "settings.json"), []byte(`{"version":1,"marker":"AFTER"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := backup.TakeUpdateSnapshot(bh.dataDir, "1.2.3", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bh.dataDir, "settings.json"), []byte(`{"version":1,"marker":"BROKEN"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	bh.h.ListSnapshots(rr, httptest.NewRequest(http.MethodGet, "/api/system/backup/snapshots", nil))
	if rr.Code != 200 || !bytes.Contains(rr.Body.Bytes(), []byte(snap.ID)) {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	bh.h.DownloadSnapshot(rr, httptest.NewRequest(http.MethodGet, "/api/system/backup/snapshots/download?id="+snap.ID, nil))
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("download: %d %s", rr.Code, rr.Header())
	}
	if m, err := backup.PeekManifest(rr.Body.Bytes()); err != nil || m.AppVersion != "1.2.3" {
		t.Fatalf("download: манифест %v %v", m, err)
	}

	rr = httptest.NewRecorder()
	bh.h.RestoreSnapshot(rr, httptest.NewRequest(http.MethodPost, "/api/system/backup/snapshots/restore?id="+snap.ID, nil))
	if rr.Code != 200 {
		t.Fatalf("restore: %d %s", rr.Code, rr.Body.String())
	}
	if want := []string{"quiesce", "restart:restored"}; !reflect.DeepEqual(bh.events, want) {
		t.Fatalf("порядок = %v, want %v", bh.events, want)
	}

	rr = httptest.NewRecorder()
	bh.h.DeleteSnapshot(rr, httptest.NewRequest(http.MethodPost, "/api/system/backup/snapshots/delete?id="+snap.ID, nil))
	if rr.Code != 200 || bytes.Contains(rr.Body.Bytes(), []byte(snap.ID)) {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
}

func TestBackupSnapshot_UnknownIDIsNotFound(t *testing.T) {
	bh := newBackupHarness(t, nil)
	for _, tc := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
		req  *http.Request
	}{
		{"download", bh.h.DownloadSnapshot, httptest.NewRequest(http.MethodGet, "/x?id=../settings.json", nil)},
		{"restore", bh.h.RestoreSnapshot, httptest.NewRequest(http.MethodPost, "/x?id=before-update-20260101-000000.tar.gz", nil)},
		{"delete", bh.h.DeleteSnapshot, httptest.NewRequest(http.MethodPost, "/x?id=settings.json", nil)},
	} {
		rr := httptest.NewRecorder()
		tc.call(rr, tc.req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s: code=%d body=%s", tc.name, rr.Code, rr.Body.String())
		}
	}
	if len(bh.events) != 0 {
		t.Fatalf("неизвестный снимок не должен останавливать службы: %v", bh.events)
	}
}

// Во время установки обновления восстановление (и из файла, и из снимка)
// отказывает до остановки служб: opkg с postinst и Restore боролись бы за
// одни данные.
func TestBackupRestore_RefusedWhileUpgrading(t *testing.T) {
	bh := newBackupHarness(t, nil)
	bh.h.SetUpgradeGuard(func() bool { return true })
	snap, err := backup.TakeUpdateSnapshot(bh.dataDir, "1.2.3", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	bh.h.Import(rr, backupUpload(t, "backup.tar.gz", archiveOf(t, `{"version":1,"marker":"AFTER"}`)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("import: code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	bh.h.RestoreSnapshot(rr, httptest.NewRequest(http.MethodPost, "/x?id="+snap.ID, nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("snapshot restore: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(bh.events) != 0 {
		t.Fatalf("службы не должны останавливаться: %v", bh.events)
	}
}

// Удаление снимка публикует подсказку инвалидации: другие вкладки обновят список.
func TestBackupSnapshot_DeletePublishesInvalidation(t *testing.T) {
	bh := newBackupHarness(t, nil)
	bus := events.NewBus()
	bh.h.SetEventBus(bus)
	_, ch, unsub := bus.Subscribe()
	defer unsub()
	snap, err := backup.TakeUpdateSnapshot(bh.dataDir, "1.2.3", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	bh.h.DeleteSnapshot(rr, httptest.NewRequest(http.MethodPost, "/x?id="+snap.ID, nil))
	if rr.Code != 200 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	select {
	case ev := <-ch:
		data, _ := ev.Data.(events.ResourceInvalidatedEvent)
		if ev.Type != events.EventResourceInvalidated || data.Resource != events.ResourceUpdateSnapshots {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("нет события об удалении снимка")
	}
}
