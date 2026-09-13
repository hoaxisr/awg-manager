package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/storage"
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
	// Манифест на месте — иначе отказ придёт раньше и про него, а проверяется
	// здесь именно отсутствие settings.json.
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(validManifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}
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

// PeekManifest — то, чем UI подписывает «что вы собираетесь восстановить»:
// тип и версия формата плюс версия приложения, снявшего бэкап. Пустой ответ
// показал бы пользователю чужой архив как свой.
func TestPeekManifest_ReadsExportedHeader(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Export(dataDir, "9.9.9-fixture", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}

	m, err := PeekManifest(buf.Bytes())
	if err != nil {
		t.Fatalf("PeekManifest: %v", err)
	}
	// Литералы, а не прод-константы: манифест — формат файла, который читают
	// прошлые и будущие сборки, и переименование константы обязано быть
	// осознанным изменением формата, а не молча зелёным тестом.
	if m.Type != "awg-manager-full-backup" {
		t.Errorf("Type = %q, want %q", m.Type, "awg-manager-full-backup")
	}
	if m.Version != 1 {
		t.Errorf("Version = %d, want %d", m.Version, 1)
	}
	if m.AppVersion != "9.9.9-fixture" {
		t.Errorf("AppVersion = %q, want %q", m.AppVersion, "9.9.9-fixture")
	}
}

// Архив без манифеста — чужой tar.gz, а не наш бэкап: отказ обязан быть
// внятным, а не «пустым манифестом с нулевыми полями».
func TestPeekManifest_RejectsArchiveWithoutManifest(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := PeekManifest(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "manifest not found") {
		t.Fatalf("err = %v, want manifest not found", err)
	}
}

// Маркер post-restore — one-shot: он переводит следующий старт в холодную
// загрузку с восстановленного диска. Не снятый маркер загонял бы демон в
// холодный старт на каждом запуске навсегда.
func TestPostRestoreMarker_ConsumedOnce(t *testing.T) {
	dir := t.TempDir()
	if HasPostRestoreMarker(dir) {
		t.Fatal("маркер есть на чистом каталоге")
	}
	if err := WritePostRestoreMarker(dir); err != nil {
		t.Fatalf("WritePostRestoreMarker: %v", err)
	}
	if !HasPostRestoreMarker(dir) {
		t.Fatal("маркер не виден после записи")
	}
	if !ConsumePostRestoreMarker(dir) {
		t.Fatal("первый Consume не увидел маркер")
	}
	path := filepath.Join(dir, "run", PostRestoreMarkerName)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("файл маркера остался на диске: err=%v", err)
	}
	if ConsumePostRestoreMarker(dir) {
		t.Fatal("второй Consume отдал true — маркер не одноразовый")
	}
	if HasPostRestoreMarker(dir) {
		t.Fatal("маркер виден после Consume")
	}
}

// tarNames отдаёт имена всех записей архива — проверяем состав по реальному
// выходу Export, а не по предикату shouldSkip.
func tarNames(t *testing.T, archive []byte) []string {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gr.Close()
	var names []string
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		names = append(names, hdr.Name)
	}
}

// Секрет устройства в бэкап не едет: архив уходит в поддержку и в облако.
// settings.json проверяется тем же тестом намеренно — иначе shouldSkip,
// отсеивающий вообще всё, оставил бы тест зелёным.
func TestExportSkipsDeviceKey(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewDeviceCipher(dataDir).Encrypt("vpn://test-key-export"); err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, storage.DeviceKeyFile)); err != nil {
		t.Fatalf("секрет не создан, тест бессмысленен: %v", err)
	}

	var buf bytes.Buffer
	if err := Export(dataDir, "2.18.2", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}

	names := tarNames(t, buf.Bytes())
	settingsFound := false
	for _, name := range names {
		if name == storage.DeviceKeyFile {
			t.Fatalf("секрет устройства попал в архив: %v", names)
		}
		if name == "settings.json" {
			settingsFound = true
		}
	}
	if !settingsFound {
		t.Fatalf("settings.json не попал в архив: %v", names)
	}
}

// Восстановление СВОЕГО бэкапа на СВОЁМ роутере не должно ронять ключ
// подписки: секрета в архиве нет по построению, поэтому Restore переносит
// существующий из отложенного каталога.
func TestRestoreCarriesDeviceKey(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}
	token, err := storage.NewDeviceCipher(dataDir).Encrypt("vpn://test-key-restore")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	var buf bytes.Buffer
	if err := Export(dataDir, "2.18.2", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := Restore(dataDir, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	info, err := os.Stat(filepath.Join(dataDir, storage.DeviceKeyFile))
	if err != nil {
		t.Fatalf("секрет не перенесён: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("права перенесённого секрета %o, want 600", perm)
	}
	got, err := storage.NewDeviceCipher(dataDir).Decrypt(token)
	if err != nil {
		t.Fatalf("Decrypt после Restore: %v", err)
	}
	if got != "vpn://test-key-restore" {
		t.Fatalf("Decrypt = %q", got)
	}
}

// Восстановление в каталог, где секрета не было: переносить нечего, и это не
// повод уронить восстановление.
func TestRestoreWithoutDeviceKeySucceeds(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Export(source, "2.18.2", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}

	target := filepath.Join(root, "awg-manager")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "settings.json"), []byte(`{"version":31}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Restore(target, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "settings.json")); err != nil {
		t.Fatalf("данные не восстановлены: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, storage.DeviceKeyFile)); !os.IsNotExist(err) {
		t.Fatalf("секрет взялся из ниоткуда: %v", err)
	}
}

// forgedArchive собирает tar.gz руками — так, как его собрал бы отправитель
// чужого бэкапа. Через Export такой архив не получить: Export секрет не
// кладёт, и проверять обратное направление нечем.
func forgedArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("Write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// validManifestJSON — манифест ровно того вида, что кладёт Export. Литералы,
// а не прод-константы: подделыватель архива берёт манифест из настоящего
// бэкапа, и переименование константы не должно молча чинить такой тест.
const validManifestJSON = `{"version":1,"type":"awg-manager-full-backup"}`

// settingsWithKey/keyFromSettings — settings.json ровно в той роли, в какой
// он участвует в задаче: носитель шифротекста ключа подписки.
func settingsWithKey(t *testing.T, dir, token string) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"amneziaSubscriptionKey": token})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func keyFromSettings(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	return m["amneziaSubscriptionKey"]
}

// Секрет устройства не приезжает ИЗ архива: файл, который принципиально не
// кладут в бэкап, принципиально же нельзя из бэкапа и доставать. Иначе
// «вот готовый бэкап, восстановите у себя» отдаёт автору архива секрет
// устройства, а с ним — всё, что пользователь потом зашифрует.
func TestRestoreIgnoresDeviceKeyFromArchive(t *testing.T) {
	foreign := strings.Repeat("A", 32)
	archive := forgedArchive(t, map[string]string{
		ManifestName:          validManifestJSON,
		"settings.json":       `{"version":32}`,
		storage.DeviceKeyFile: foreign,
	})

	// Свежая установка: каталога данных ещё нет (первый запуск, переезд,
	// после opkg remove). Переносить нечего — значит в каталоге не должно
	// оказаться и архивного секрета.
	t.Run("каталога данных не было", func(t *testing.T) {
		root := t.TempDir()
		dataDir := filepath.Join(root, "awg-manager")
		if err := Restore(dataDir, bytes.NewReader(archive)); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, "settings.json")); err != nil {
			t.Fatalf("данные не восстановлены: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, storage.DeviceKeyFile)); !os.IsNotExist(err) {
			t.Fatalf("секрет из чужого архива приехал: err=%v", err)
		}
	})

	// Свой секрет уже есть: он обязан пережить восстановление байт в байт,
	// а не быть подменённым архивным.
	t.Run("свой секрет уже был", func(t *testing.T) {
		root := t.TempDir()
		dataDir := filepath.Join(root, "awg-manager")
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":31}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.NewDeviceCipher(dataDir).Encrypt("vpn://test-key-own"); err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		own, err := os.ReadFile(filepath.Join(dataDir, storage.DeviceKeyFile))
		if err != nil {
			t.Fatal(err)
		}

		if err := Restore(dataDir, bytes.NewReader(archive)); err != nil {
			t.Fatalf("Restore: %v", err)
		}

		after, err := os.ReadFile(filepath.Join(dataDir, storage.DeviceKeyFile))
		if err != nil {
			t.Fatalf("свой секрет пропал: %v", err)
		}
		if !bytes.Equal(after, own) {
			t.Fatalf("секрет подменён архивным (архивный=%v)", bytes.Equal(after, []byte(foreign)))
		}
	})
}

// Отложенная копия <dir>.pre-restore-* оставляется на диске как путь ручного
// отката, значит она обязана быть самодостаточной: шифротекст в её
// settings.json должен читаться лежащим рядом секретом. Перенос секрета
// (os.Rename) опустошал её — откат возвращал нерасшифровываемый ключ.
func TestRestoreKeepsRollbackCopySelfSufficient(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	token, err := storage.NewDeviceCipher(dataDir).Encrypt("vpn://test-key-rollback")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	settingsWithKey(t, dataDir, token)

	var buf bytes.Buffer
	if err := Export(dataDir, "2.18.2", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := Restore(dataDir, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(root, "awg-manager.pre-restore-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("ожидали одну откатную копию, получили %v", matches)
	}
	previous := matches[0]
	if _, err := os.Stat(filepath.Join(previous, storage.DeviceKeyFile)); err != nil {
		t.Fatalf("в откатной копии нет секрета: %v", err)
	}
	got, err := storage.NewDeviceCipher(previous).Decrypt(keyFromSettings(t, previous))
	if err != nil {
		t.Fatalf("ключ подписки в откатной копии не расшифровывается: %v", err)
	}
	if got != "vpn://test-key-rollback" {
		t.Fatalf("Decrypt в откатной копии = %q", got)
	}
	// Восстановленный каталог при этом секрет тоже видит — иначе «откат
	// работает» куплено ценой сломанного основного пути.
	if _, err := storage.NewDeviceCipher(dataDir).Decrypt(keyFromSettings(t, dataDir)); err != nil {
		t.Fatalf("ключ подписки после восстановления не расшифровывается: %v", err)
	}
}

// Карантинная копия секрета (.device-key.corrupt, куда QuarantineCorrupt
// уносит файл негодной длины) несёт настоящий секрет и в архив не едет.
// settings.json проверяется тем же тестом намеренно: без этого shouldSkip,
// отсеивающий вообще всё, оставил бы тест зелёным.
func TestExportSkipsQuarantinedDeviceKey(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}
	corrupt := storage.DeviceKeyFile + ".corrupt"
	if err := os.WriteFile(filepath.Join(dataDir, corrupt), []byte(strings.Repeat("K", 33)), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Export(dataDir, "2.18.2", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}

	names := tarNames(t, buf.Bytes())
	settingsFound := false
	for _, name := range names {
		if name == corrupt {
			t.Fatalf("карантинная копия секрета попала в архив: %v", names)
		}
		if name == "settings.json" {
			settingsFound = true
		}
	}
	if !settingsFound {
		t.Fatalf("settings.json не попал в архив: %v", names)
	}
}

// Восстановление ЧУЖОГО бэкапа: свой секрет на месте, а шифротекст пришёл
// чужой. Причина обязана быть различимой — «ключ непригоден»
// (ErrDeviceCiphertext), а не «ключа нет» (ErrDeviceKeyMissing): от этого
// зависит, сотрёт вызывающий сохранённый ключ или оставит его с
// признаком usable:false.
func TestRestoreForeignBackupYieldsUnusableKey(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "foreign")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	foreignToken, err := storage.NewDeviceCipher(foreign).Encrypt("vpn://test-key-foreign")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	settingsWithKey(t, foreign, foreignToken)
	var buf bytes.Buffer
	if err := Export(foreign, "2.18.2", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}

	dataDir := filepath.Join(root, "awg-manager")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ownToken, err := storage.NewDeviceCipher(dataDir).Encrypt("vpn://test-key-own")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	settingsWithKey(t, dataDir, ownToken)
	own, err := os.ReadFile(filepath.Join(dataDir, storage.DeviceKeyFile))
	if err != nil {
		t.Fatal(err)
	}

	if err := Restore(dataDir, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	token := keyFromSettings(t, dataDir)
	if token != foreignToken {
		t.Fatalf("в settings.json не чужой шифротекст: %q", token)
	}
	_, err = storage.NewDeviceCipher(dataDir).Decrypt(token)
	if !errors.Is(err, storage.ErrDeviceCiphertext) {
		t.Fatalf("Decrypt = %v, ждали %v", err, storage.ErrDeviceCiphertext)
	}
	if errors.Is(err, storage.ErrDeviceKeyMissing) {
		t.Fatalf("Decrypt отдал «ключа нет» вместо «ключ непригоден»: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dataDir, storage.DeviceKeyFile))
	if err != nil {
		t.Fatalf("свой секрет не пережил восстановление: %v", err)
	}
	if !bytes.Equal(after, own) {
		t.Fatal("секрет после восстановления чужого бэкапа не свой")
	}
}

// Ведущий слэш обходил фильтр распаковки целиком: filepath.Clean оставляет
// "/.device-key" как есть, shouldSkip сравнивает с относительными именами и не
// срабатывает, а filepath.Join(destDir, "/.device-key") кладёт файл ровно
// туда, куда его и хотели положить. Тем же путём проходило всё прочее
// отсеиваемое — run/, locks/, .lock. Проверяется на реальном пути (Restore),
// а не на extractArchive: обходили именно восстановление.
func TestRestoreRejectsAbsoluteNamesInArchive(t *testing.T) {
	cases := map[string]string{
		"секрет устройства": "/" + storage.DeviceKeyFile,
		"рантайм-каталог":   "/run/x",
		"корень архива":     ".",
	}
	for title, name := range cases {
		t.Run(title, func(t *testing.T) {
			archive := forgedArchive(t, map[string]string{
				ManifestName:    validManifestJSON,
				"settings.json": `{"version":32}`,
				name:            strings.Repeat("B", 32),
			})
			root := t.TempDir()
			dataDir := filepath.Join(root, "awg-manager")

			err := Restore(dataDir, bytes.NewReader(archive))
			if err == nil {
				t.Fatalf("архив с записью %q принят", name)
			}
			// Только для абсолютных имён: имя корня (".") содержится в любом
			// пути, и проверка «не пересказывает» на нём ловила бы точку в
			// тексте сообщения, а не пересказ архива.
			if strings.HasPrefix(name, "/") && strings.Contains(err.Error(), name) {
				t.Errorf("ошибка пересказывает содержимое архива: %v", err)
			}
			if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
				t.Fatalf("каталог данных создан отвергнутым архивом: err=%v", err)
			}
		})
	}
}

// Отложенная копия <dir>.pre-restore-* — путь ручного отката, поэтому её
// секрет обязан быть отдельным файлом, а не вторым именем того же inode.
// Жёсткая ссылка давала «оба имени видят секрет» сразу после Restore (что и
// проверял TestRestoreKeepsRollbackCopySelfSufficient), но правка файла НА
// МЕСТЕ портила обе копии разом: карантин уносил имя из dataDir, заводил там
// новый секрет, а в откатной копии оставался мусор.
func TestRestoreRollbackCopyIsIndependentOfDataDir(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	token, err := storage.NewDeviceCipher(dataDir).Encrypt("vpn://test-key-independent")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	settingsWithKey(t, dataDir, token)
	original, err := os.ReadFile(filepath.Join(dataDir, storage.DeviceKeyFile))
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Export(dataDir, "2.18.2", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := Restore(dataDir, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(root, "awg-manager.pre-restore-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("ожидали одну откатную копию, получили %v", matches)
	}
	previous := matches[0]

	// Правка НА МЕСТЕ (без O_TRUNC и без временного файла) — ровно то, что
	// видит второе имя жёсткой ссылки и не видит копия.
	overwriteInPlace(t, filepath.Join(dataDir, storage.DeviceKeyFile), strings.Repeat("X", len(original)))
	if got := mustRead(t, filepath.Join(previous, storage.DeviceKeyFile)); !bytes.Equal(got, original) {
		t.Fatalf("правка секрета в каталоге данных видна в откатной копии: копии не независимы")
	}

	overwriteInPlace(t, filepath.Join(previous, storage.DeviceKeyFile), strings.Repeat("Y", len(original)))
	if got := mustRead(t, filepath.Join(dataDir, storage.DeviceKeyFile)); !bytes.Equal(got, []byte(strings.Repeat("X", len(original)))) {
		t.Fatalf("правка секрета в откатной копии видна в каталоге данных: копии не независимы")
	}
}

func overwriteInPlace(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("открыть %s: %v", path, err)
	}
	if _, err := f.Write([]byte(body)); err != nil {
		f.Close()
		t.Fatalf("запись %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение %s: %v", path, err)
	}
	return raw
}

// Перенос секрета в восстановленный каталог не имеет права затереть уже
// лежащий там секрет: свой секрет старше архива, и именно им зашифровано всё,
// что пользователь сохранит дальше.
func TestCarryDeviceKeyKeepsExistingTarget(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "from")
	to := filepath.Join(root, "to")
	for _, dir := range []string{from, to} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(from, storage.DeviceKeyFile), []byte(strings.Repeat("A", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	own := []byte(strings.Repeat("B", 32))
	if err := os.WriteFile(filepath.Join(to, storage.DeviceKeyFile), own, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := carryDeviceKey(from, to); err == nil {
		t.Fatal("перенос поверх существующего секрета прошёл молча")
	}
	if got := mustRead(t, filepath.Join(to, storage.DeviceKeyFile)); !bytes.Equal(got, own) {
		t.Fatal("свой секрет затёрт переносом")
	}
}

// Перенос в каталог без секрета: копия байт в байт и права 0600 — секрет не
// имеет права стать доступным на чтение кому-то ещё.
func TestCarryDeviceKeyCopiesBytesAndMode(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "from")
	to := filepath.Join(root, "to")
	for _, dir := range []string{from, to} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := []byte(strings.Repeat("S", 32))
	if err := os.WriteFile(filepath.Join(from, storage.DeviceKeyFile), secret, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := carryDeviceKey(from, to); err != nil {
		t.Fatalf("carryDeviceKey: %v", err)
	}
	if got := mustRead(t, filepath.Join(to, storage.DeviceKeyFile)); !bytes.Equal(got, secret) {
		t.Fatalf("скопированы не те байты: %q", got)
	}
	info, err := os.Stat(filepath.Join(to, storage.DeviceKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("права копии %o, want 600", perm)
	}
}

// Тип и версия архива — то немногое, что отличает наш бэкап от чужого tar.gz
// до распаковки настроек. Манифест кладёт сам Export с самой первой версии
// фичи, поэтому его отсутствие — не совместимость, а подлог или чужой файл.
func TestRestoreRejectsBadManifest(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"чужой тип": {
			files: map[string]string{
				ManifestName:    `{"version":1,"type":"some-other-tool-backup"}`,
				"settings.json": `{"version":32}`,
			},
			want: "тип архива",
		},
		"версия из будущего": {
			files: map[string]string{
				ManifestName:    `{"version":2,"type":"awg-manager-full-backup"}`,
				"settings.json": `{"version":32}`,
			},
			want: "версия архива",
		},
		"пустой манифест": {
			files: map[string]string{
				ManifestName:    `{}`,
				"settings.json": `{"version":32}`,
			},
			want: "тип архива",
		},
		"без манифеста": {
			files: map[string]string{
				"settings.json": `{"version":32}`,
			},
			want: ManifestName,
		},
	}
	for title, tc := range cases {
		t.Run(title, func(t *testing.T) {
			root := t.TempDir()
			dataDir := filepath.Join(root, "awg-manager")
			err := Restore(dataDir, bytes.NewReader(forgedArchive(t, tc.files)))
			if err == nil {
				t.Fatal("архив принят")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, ждали упоминание %q", err, tc.want)
			}
			if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
				t.Fatalf("каталог данных создан отвергнутым архивом: err=%v", err)
			}
		})
	}
}

// Архив, снятый нашим же Export, обязан проходить те же проверки — иначе
// «отказ по умолчанию» закрывает и нормальный путь.
func TestRestoreAcceptsOwnExport(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Export(source, "2.18.2", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := Restore(filepath.Join(root, "awg-manager"), bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Restore своего же архива: %v", err)
	}
}

// Отсеиваемое имя, использованное как каталог: на выгрузке весь такой
// подкаталог отрезает filepath.SkipDir, а при распаковке предикат смотрел
// только на имя целиком. Так из чужого архива в каталоге данных появлялся
// КАТАЛОГ ".device-key" — свой секрет после этого не прочитать и не записать,
// то есть ключ подписки терялся навсегда.
func TestRestoreSkipsFilteredNamesUsedAsDirectories(t *testing.T) {
	for _, name := range []string{
		storage.DeviceKeyFile + "/x",
		"settings.json.lock/x",
		"tunnels.tmp/x",
		// Отсеиваемое имя глубже первого уровня: все случаи выше ловятся и
		// проверкой одного лишь верхнего сегмента, а предикат обязан
		// смотреть на ВСЕХ предков. Верхний сегмент ("tunnels") здесь —
		// обычный каталог данных, и появиться он может только вместе с
		// распакованной записью.
		"tunnels/awg1.json.tmp/x",
	} {
		t.Run(name, func(t *testing.T) {
			archive := forgedArchive(t, map[string]string{
				ManifestName:    validManifestJSON,
				"settings.json": `{"version":32}`,
				name:            "payload",
			})
			root := t.TempDir()
			dataDir := filepath.Join(root, "awg-manager")
			if err := Restore(dataDir, bytes.NewReader(archive)); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			top := strings.SplitN(name, "/", 2)[0]
			if _, err := os.Stat(filepath.Join(dataDir, top)); !os.IsNotExist(err) {
				t.Fatalf("%q приехал из архива: err=%v", top, err)
			}
		})
	}
}

// Т3. settings.json, присланный КАТАЛОГОМ, — не резервная копия. Запись вида
// "settings.json/x" проходила проверку «путь существует», приезжала в каталог
// данных, и дальше SettingsStore.Load получал EISDIR — не IsNotExist, — то
// есть панель не поднималась вовсе. Тот же класс, что каталог ".device-key".
func TestRestoreRejectsSettingsAsDirectory(t *testing.T) {
	archive := forgedArchive(t, map[string]string{
		ManifestName:      validManifestJSON,
		"settings.json/x": `{"version":32}`,
	})
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")

	err := Restore(dataDir, bytes.NewReader(archive))
	if err == nil {
		t.Fatal("архив с каталогом вместо settings.json принят")
	}
	if !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("err = %v, ждали упоминание settings.json", err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("каталог данных создан отвергнутым архивом: err=%v", err)
	}
}

// Т4. Имя временного файла секрета обязано начинаться с ".device-key.": из
// архива его держит ровно этот префикс (shouldSkip), а не отдельное правило.
// Осиротевший после сбоя питания временный файл — это ЖИВОЙ секрет установки,
// и уехать в архив, который пользователь шлёт в поддержку и кладёт в облако,
// он не имеет права.
//
// Имя берётся у самого storage, а не повторяется здесь литералом: inotify
// показывает, что реально создано в каталоге, поэтому смена префикса в
// storage красит этот тест, а не переезжает вместе с ним.
func TestExportSkipsDeviceKeyTempFile(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}

	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		t.Fatalf("inotify_init1: %v", err)
	}
	defer syscall.Close(fd)
	if _, err := syscall.InotifyAddWatch(fd, dataDir, syscall.IN_CREATE); err != nil {
		t.Fatalf("inotify_add_watch: %v", err)
	}
	secret := bytes.Repeat([]byte("k"), 32)
	if err := storage.PublishDeviceKey(dataDir, secret); err != nil {
		t.Fatalf("PublishDeviceKey: %v", err)
	}
	tempName := ""
	for _, name := range inotifyCreated(t, fd) {
		if name != storage.DeviceKeyFile {
			tempName = name
		}
	}
	if tempName == "" {
		t.Fatal("временный файл секрета не замечен — проверка не состоялась")
	}

	// Сбой питания между записью и получением целевого имени: временный файл
	// остаётся в каталоге данных, и его застаёт выгрузка.
	if err := os.WriteFile(filepath.Join(dataDir, tempName), secret, 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Export(dataDir, "2.18.2", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	names := tarNames(t, buf.Bytes())
	settingsFound := false
	for _, name := range names {
		if name == tempName {
			t.Fatalf("временный файл секрета %q попал в архив: %v", tempName, names)
		}
		if name == "settings.json" {
			settingsFound = true
		}
	}
	if !settingsFound {
		t.Fatalf("settings.json не попал в архив: %v", names)
	}
}

// inotifyCreated вычитывает очередь событий целиком и отдаёт имена созданных
// записей. События кладутся в очередь внутри самой операции над каталогом,
// поэтому к возврату вызова они уже там: ждать нечего, от времени проверка не
// зависит. Тест линуксовый по построению; проект работает только на Linux.
func inotifyCreated(t *testing.T, fd int) []string {
	t.Helper()
	const header = 16 // int32 wd + uint32 mask + uint32 cookie + uint32 len
	var out []string
	buf := make([]byte, 16*1024)
	for {
		n, err := syscall.Read(fd, buf)
		if err == syscall.EAGAIN {
			return out
		}
		if err != nil {
			t.Fatalf("чтение очереди inotify: %v", err)
		}
		for off := 0; off+header <= n; {
			nameLen := int(binary.NativeEndian.Uint32(buf[off+12:]))
			name := string(bytes.SplitN(buf[off+header:off+header+nameLen], []byte{0}, 2)[0])
			out = append(out, name)
			off += header + nameLen
		}
	}
}

// Т1. Отказ записи во время переноса секрета не оставляет под целевым именем
// ни пустого, ни короткого файла. Прежняя, вторая реализация записи жила
// здесь же (O_CREATE|O_EXCL прямо на целевом имени + Write): после EFBIG под
// именем оставался файл нулевой длины, и ближайшее шифрование уносило эту
// пустышку в карантин, сообщая пользователю, что прежний ключ подписки
// расшифровать больше нечем, — из-за ВРЕМЕННОЙ нехватки места, после которой
// повтор ещё мог сработать. Проверяется путь, который менялся: сам перенос,
// а не соседний storage (там свойство держалось и до правки).
func TestCarryDeviceKeyWriteFailureLeavesNothing(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "from")
	to := filepath.Join(root, "to")
	for _, dir := range []string{from, to} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := bytes.Repeat([]byte("s"), storage.DeviceKeyLen)
	if err := os.WriteFile(filepath.Join(from, storage.DeviceKeyFile), secret, 0o600); err != nil {
		t.Fatal(err)
	}

	allow := forbidFileWrites(t)
	err := carryDeviceKey(from, to)
	allow()

	if err == nil {
		t.Fatal("перенос прошёл при запрете записи — отказ не смоделирован, проверка не состоялась")
	}
	entries, readErr := os.ReadDir(to)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		info, statErr := e.Info()
		size := int64(-1)
		if statErr == nil {
			size = info.Size()
		}
		t.Fatalf("после отказа записи в целевом каталоге остался %s (%d байт): %v", e.Name(), size, err)
	}
}

// forbidFileWrites запрещает процессу писать в обычные файлы: RLIMIT_FSIZE=0
// разрешает создать файл, но любая запись в него отдаёт EFBIG — так же, как
// при кончившемся месте на флеше. Лимит процессный и снимается возвращённой
// функцией сразу после проверяемого вызова; на stdout тестового процесса он
// не влияет — это канал, а не обычный файл. SIGXFSZ, который ядро шлёт вместе
// с EFBIG, перехватывается, чтобы тестовый процесс не умер от него, и
// перехват снимается ПОСЛЕ возврата лимита: в обратном порядке любая запись,
// попавшая в зазор, убила бы тестовый бинарь. Близнец живёт в
// internal/storage (devicecipher_test.go): помощник тестовый и в обоих
// пакетах неэкспортируемый.
func forbidFileWrites(t *testing.T) func() {
	t.Helper()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGXFSZ)
	var saved syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &saved); err != nil {
		t.Fatalf("getrlimit: %v", err)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 0, Max: saved.Max}); err != nil {
		t.Fatalf("setrlimit: %v", err)
	}
	done := false
	allow := func() {
		if done {
			return
		}
		done = true
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &saved); err != nil {
			t.Fatalf("вернуть RLIMIT_FSIZE: %v", err)
		}
		signal.Stop(sig)
	}
	t.Cleanup(allow)
	return allow
}

// Т3. Секрет негодной длины из откатного каталога не публикуется под целевым
// именем. Байты там берутся из файла как есть, и опубликованная пустышка (или
// раздувшийся файл) прожила бы до ближайшего шифрования, которое унесло бы её
// в карантин со словами «ключ подписки расшифровать больше нечем». Отказ
// переноса молчит по построению — восстановление он не отменяет, — поэтому
// проверяется не ошибка, а то, что под именем ничего не появилось.
func TestRestoreDoesNotCarryBadLengthDeviceKey(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{"пустой", 0},
		{"раздувшийся", storage.DeviceKeyLen + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dataDir := filepath.Join(root, "awg-manager")
			if err := os.MkdirAll(dataDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := Export(dataDir, "2.18.2", &buf); err != nil {
				t.Fatalf("Export: %v", err)
			}
			// Секрет кладётся после выгрузки нарочно: в архив он всё равно не
			// едет, а в откатном каталоге оказаться обязан.
			if err := os.WriteFile(filepath.Join(dataDir, storage.DeviceKeyFile), bytes.Repeat([]byte("x"), tc.size), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := Restore(dataDir, bytes.NewReader(buf.Bytes())); err != nil {
				t.Fatalf("Restore: %v", err)
			}

			info, err := os.Stat(filepath.Join(dataDir, storage.DeviceKeyFile))
			if err == nil {
				t.Fatalf("негодный секрет (%d байт) опубликован под целевым именем: %d байт", tc.size, info.Size())
			}
			if !os.IsNotExist(err) {
				t.Fatalf("под именем секрета: %v", err)
			}
		})
	}
}
