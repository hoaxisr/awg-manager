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

	"github.com/hoaxisr/awg-manager/internal/backup"
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
