package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// newRecords кладёт записи ЧЕРЕЗ хранилище: нормализация и инварианты те же,
// что у прода, и фикстура не может застыть в форме, которой store уже не
// отдаёт. Перенесено из reconcile_test.go — сам reconcile снят вместе со
// старым движком.
func newRecords(t *testing.T, dataDir string, recs ...instancestore.Record) *instancestore.Store {
	t.Helper()
	store := instancestore.New(dataDir)
	if _, err := store.Replace(func(st *instancestore.State) error {
		st.Records = recs
		return nil
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	return store
}

func TestExportRestoreRoundtrip(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	if err := os.MkdirAll(filepath.Join(dataDir, "tunnels"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "tunnels", "awg1.json"), []byte(`{"id":"awg1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "run", "wdtt.pid"), []byte("123"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Export(dataDir, "2.16.0", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}

	target := filepath.Join(root, "restored")
	if err := Restore(target, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "settings.json")); err != nil {
		t.Fatalf("settings.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "tunnels", "awg1.json")); err != nil {
		t.Fatalf("tunnel missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "run", "wdtt.pid")); !os.IsNotExist(err) {
		t.Fatalf("run/ should be skipped, got err=%v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, "restored.pre-restore-*"))
	if len(matches) != 0 {
		t.Fatalf("fresh restore should not create pre-restore dir, got %v", matches)
	}

	// Second restore replaces existing dir and keeps pre-restore snapshot.
	if err := Restore(target, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Restore again: %v", err)
	}
	matches, _ = filepath.Glob(filepath.Join(root, "restored.pre-restore-*"))
	if len(matches) != 1 {
		t.Fatalf("expected one pre-restore dir after overwrite, got %v", matches)
	}
}

func TestValidateStagingRejectsBadArchive(t *testing.T) {
	dir := t.TempDir()
	if err := validateStaging(dir); err == nil || !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("expected settings.json error, got %v", err)
	}
}

func TestExtractRejectsPathTraversal(t *testing.T) {
	dest := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "../escape.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()

	if err := extractArchive(bytes.NewReader(buf.Bytes()), dest); err == nil {
		t.Fatal("expected path traversal error")
	}
}

// Каждое восстановление оставляло полный каталог данных на /opt; ротации не
// было. Держим только последнюю копию.
func TestRestorePrunesOlderPreRestoreCopies(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "awg-manager.pre-restore-20200101-000000")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Export(dataDir, "2.16.3", &buf); err != nil {
		t.Fatal(err)
	}
	if err := Restore(dataDir, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("старая копия не удалена: %v", err)
	}
	kept, err := filepath.Glob(filepath.Join(root, "awg-manager.pre-restore-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Fatalf("ожидали ровно одну копию, получили %v", kept)
	}
}

// Хранилище прокси-инстансов обязано и уезжать в архив, и возвращаться из
// него: бэкап, молча не взявший файл, обнаруживается только тогда, когда он
// уже понадобился. Проверяется СОСТАВ записи, а не факт наличия файла —
// пустой или обрезанный proxy-instances.json прошёл бы проверку по имени.
func TestExportRestoreKeepsProxyInstances(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}
	newRecords(t, dataDir,
		instancestore.Record{ID: "wd1", Kind: instancestore.KindWdttClient, Name: "Нидерланды",
			WdttClient: &roles.WdttClientConfig{Mode: "wg", Listen: "127.0.0.1:9001",
				Peer: "1.2.3.4:56000", Password: "p", VKHashes: "h"}},
	)

	var buf bytes.Buffer
	if err := Export(dataDir, "2.17.3", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !archiveHasEntry(t, buf.Bytes(), "proxy-instances.json") {
		t.Fatal("proxy-instances.json не попал в архив")
	}

	target := filepath.Join(root, "restored")
	if err := Restore(target, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	st, err := instancestore.New(target).Load()
	if err != nil {
		t.Fatalf("восстановленное хранилище не читается: %v", err)
	}
	if len(st.Records) != 1 {
		t.Fatalf("записей после восстановления %d, ждали 1", len(st.Records))
	}
	rec := st.Records[0]
	if rec.Key() != "wdtt-client:wd1" || rec.Name != "Нидерланды" {
		t.Fatalf("запись после восстановления = %+v", rec)
	}
	cfg, err := rec.WdttClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:9001" || cfg.Peer != "1.2.3.4:56000" {
		t.Fatalf("конфиг после восстановления = %+v", cfg)
	}
}

func archiveHasEntry(t *testing.T, archive []byte, name string) bool {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == name {
			return true
		}
	}
}
