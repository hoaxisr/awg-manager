package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// ── фикстуры ─────────────────────────────────────────────────────

// downloadCall — один вызов загрузчика целиком: состав аргументов проверяется,
// а не только факт вызова.
type downloadCall struct {
	URL      string
	Dest     string
	MaxBytes int64
}

type fakeDownloader struct {
	mu      sync.Mutex
	payload map[string][]byte
	calls   []downloadCall
	// before — хук, исполняемый ДО записи файла: даёт заглянуть в статус
	// ровно во время установки.
	before func()
}

func (f *fakeDownloader) DownloadFile(_ context.Context, url, destPath string, maxBytes int64) error {
	f.mu.Lock()
	f.calls = append(f.calls, downloadCall{URL: url, Dest: destPath, MaxBytes: maxBytes})
	f.mu.Unlock()
	if f.before != nil {
		f.before()
	}
	body, ok := f.payload[url]
	if !ok {
		return fmt.Errorf("нет ассета %s", url)
	}
	return os.WriteFile(destPath, body, 0644)
}

// recorded отдаёт КОПИЮ: общий срез дал бы тесту подсматривать за фейком
// после проверки.
func (f *fakeDownloader) recorded() []downloadCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]downloadCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fixedClock — часы роутера с различимым значением.
func fixedClock() func() (time.Time, string) {
	return func() (time.Time, string) {
		return time.Date(2026, 8, 24, 15, 4, 5, 0, time.UTC), "MSK"
	}
}

// newTestService строит сервис с бинарями во временном каталоге: прод-пути
// /opt/bin в тесте недоступны.
func newTestService(t *testing.T, d Deps) *Service {
	t.Helper()
	if d.DataDir == "" {
		d.DataDir = t.TempDir()
	}
	if d.Clock == nil {
		d.Clock = fixedClock()
	}
	s := New(d)
	binDir := t.TempDir()
	for _, sub := range s.subs {
		sub.clientBin = filepath.Join(binDir, string(sub.name)+"-client")
		sub.serverBin = filepath.Join(binDir, string(sub.name)+"-server")
	}
	return s
}

// writeBin кладёт исполняемый файл и возвращает его SHA256.
func writeBin(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return sha256Hex([]byte(content))
}

func setSpecs(s *Service, name Subsystem, specs ArchSpecs) {
	s.subs[name].specs = &specs
}

func mustStatus(t *testing.T, s *Service, subsystem string) InstallStatus {
	t.Helper()
	st, err := s.Status(subsystem)
	if err != nil {
		t.Fatalf("Status(%q): %v", subsystem, err)
	}
	return st
}

// ── Binary ───────────────────────────────────────────────────────

// Пути бинарей — прод-литералы, а не производная от чего-либо: по ним
// собирается командная строка форка и уборка старого поколения.
func TestBinary_LiteralPaths(t *testing.T) {
	s := New(Deps{DataDir: t.TempDir()})
	want := map[instancestore.Kind]string{
		instancestore.KindWdttClient:     "/opt/bin/wdtt-client",
		instancestore.KindWdttServer:     "/opt/bin/wdtt-server",
		instancestore.KindFreeTurnClient: "/opt/bin/freeturn-client",
		instancestore.KindFreeTurnServer: "/opt/bin/freeturn-server",
	}
	for kind, path := range want {
		got, _ := s.Binary(kind)
		if got != path {
			t.Errorf("Binary(%s) = %q, want %q", kind, got, path)
		}
	}
	if path, present := s.Binary(instancestore.Kind("невиданная-роль")); path != "" || present {
		t.Errorf("неизвестная роль: got (%q,%v), want (\"\",false)", path, present)
	}
}

// Пин роли — сумма ИЗ ТАБЛИЦЫ сборки, а не с диска: ресурс process сверяет с
// ней binary_sha256, который сообщает сам процесс. Арка без закреплённой
// сборки и роль без сервера дают пусто — «сверять нечем», не «не совпало».
func TestPinnedSHA256(t *testing.T) {
	arm := New(Deps{Arch: "aarch64-3.10"})
	if got := arm.PinnedSHA256(instancestore.KindWdttClient); got != WdttEmbeddedBinaries["aarch64-3.10"].Client.SHA256 {
		t.Errorf("клиент wdtt: got %q", got)
	}
	if got := arm.PinnedSHA256(instancestore.KindWdttServer); got != WdttEmbeddedBinaries["aarch64-3.10"].Server.SHA256 {
		t.Errorf("сервер wdtt: got %q", got)
	}
	if got := arm.PinnedSHA256(instancestore.KindFreeTurnServer); got != FreeTurnEmbeddedBinaries["aarch64-3.10"].Server.SHA256 {
		t.Errorf("сервер freeturn: got %q", got)
	}
	// mipsel: сервера wdtt в пине нет — пусто.
	if got := New(Deps{Arch: "mipsel-3.4"}).PinnedSHA256(instancestore.KindWdttServer); got != "" {
		t.Errorf("сервер wdtt на mipsel: got %q, want пусто", got)
	}
	if got := New(Deps{Arch: "неведомая-арка"}).PinnedSHA256(instancestore.KindWdttClient); got != "" {
		t.Errorf("арка без пина: got %q, want пусто", got)
	}
	if got := arm.PinnedSHA256(instancestore.Kind("невиданная-роль")); got != "" {
		t.Errorf("неизвестная роль: got %q, want пусто", got)
	}
}

// Наличие — ФАКТ на диске: отсутствует, лежит неисполняемым, лежит каталогом.
func TestBinary_Presence(t *testing.T) {
	s := newTestService(t, Deps{})
	if _, present := s.Binary(instancestore.KindWdttClient); present {
		t.Error("отсутствующий бинарь объявлен установленным")
	}
	writeBin(t, s.subs[SubsystemWdtt].clientBin, "wdtt")
	path, present := s.Binary(instancestore.KindWdttClient)
	if !present {
		t.Errorf("исполняемый %s не найден", path)
	}
	// Неисполняемый файл — не бинарь: запуск упадёт на permission denied.
	if err := os.WriteFile(s.subs[SubsystemFreeTurn].clientBin, []byte("ft"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, present := s.Binary(instancestore.KindFreeTurnClient); present {
		t.Error("неисполняемый файл объявлен бинарём")
	}
	if err := os.MkdirAll(s.subs[SubsystemFreeTurn].serverBin, 0755); err != nil {
		t.Fatal(err)
	}
	if _, present := s.Binary(instancestore.KindFreeTurnServer); present {
		t.Error("каталог объявлен бинарём")
	}
}

// ── Status: все семь полей ───────────────────────────────────────

// Полная фикстура: каждое из семи полей в различимом значении, сравнение
// структуры целиком. Потеря любого поля видна здесь.
func TestStatus_AllSevenFields(t *testing.T) {
	clientBody, serverBody := "wdtt-client-9.9.9", "wdtt-server-9.9.9"
	s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
	sub := s.subs[SubsystemWdtt]
	setSpecs(s, SubsystemWdtt, ArchSpecs{
		Client: BinarySpec{Version: "9.9.9", URL: "http://mirror/client", SHA256: writeBin(t, sub.clientBin, clientBody), Size: int64(len(clientBody))},
		Server: BinarySpec{Version: "8.8.8", URL: "http://mirror/server", SHA256: writeBin(t, sub.serverBin, serverBody), Size: int64(len(serverBody))},
	})
	if err := sub.writeInstalledVersion("9.9.9+server-8.8.8"); err != nil {
		t.Fatal(err)
	}

	want := InstallStatus{
		ServerSupported:  true,
		InstallAvailable: true,
		InstallVersion:   "9.9.9+server-8.8.8",
		InstalledVersion: "9.9.9+server-8.8.8",
		UpdateAvailable:  false,
		Installing:       false,
		RouterClock:      "2026-08-24 15:04:05 MSK",
	}
	if got := mustStatus(t, s, "wdtt"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Status:\n got %+v\nwant %+v", got, want)
	}
}

// Вторая полная фикстура: ни пина, ни загрузчика, ни бинарей — все семь полей
// в противоположных значениях.
func TestStatus_NothingWired(t *testing.T) {
	s := newTestService(t, Deps{Arch: "неведомая-арка"})
	want := InstallStatus{RouterClock: "2026-08-24 15:04:05 MSK"}
	if got := mustStatus(t, s, "wdtt"); !reflect.DeepEqual(got, want) {
		t.Fatalf("wdtt:\n got %+v\nwant %+v", got, want)
	}
	if got := mustStatus(t, s, "freeturn"); !reflect.DeepEqual(got, want) {
		t.Fatalf("freeturn:\n got %+v\nwant %+v", got, want)
	}
}

// serverSupported — «сервер собирается под эту арку ИЛИ уже лежит на диске».
// Без второй половины роутер с руками установленным сервером получил бы
// спрятанную вкладку.
func TestStatus_ServerSupported(t *testing.T) {
	t.Run("пин без сервера и без бинаря", func(t *testing.T) {
		s := newTestService(t, Deps{Arch: "mipsel-3.4", Downloader: &fakeDownloader{}})
		if mustStatus(t, s, "wdtt").ServerSupported {
			t.Error("на арке без серверного пина сервер объявлен поддержанным")
		}
	})
	t.Run("пин без сервера, но бинарь лежит", func(t *testing.T) {
		s := newTestService(t, Deps{Arch: "mipsel-3.4", Downloader: &fakeDownloader{}})
		writeBin(t, s.subs[SubsystemWdtt].serverBin, "руками принесённый сервер")
		if !mustStatus(t, s, "wdtt").ServerSupported {
			t.Error("установленный руками сервер обязан считаться поддержанным")
		}
	})
	t.Run("серверный пин есть", func(t *testing.T) {
		s := newTestService(t, Deps{Arch: "aarch64-3.10", Downloader: &fakeDownloader{}})
		if !mustStatus(t, s, "wdtt").ServerSupported {
			t.Error("на арке с серверным пином сервер обязан быть поддержан")
		}
	})
}

func TestStatus_UnknownSubsystem(t *testing.T) {
	s := newTestService(t, Deps{})
	_, err := s.Status("singbox")
	if err == nil {
		t.Fatal("неизвестная подсистема обязана быть отказом, а не нулевым статусом")
	}
	if err.Error() != `неизвестная подсистема "singbox": ожидается wdtt или freeturn` {
		t.Fatalf("текст отказа: %q", err.Error())
	}
}

// ── Status: правила wdtt (перенос binaries_test.go) ──────────────

func TestStatus_WdttMatchingBinariesAreUpToDate(t *testing.T) {
	s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
	sub := s.subs[SubsystemWdtt]
	setSpecs(s, SubsystemWdtt, ArchSpecs{
		Client: BinarySpec{Version: "1.4.4-awgm", URL: "http://example/client", SHA256: writeBin(t, sub.clientBin, "client-1.4.4")},
		Server: BinarySpec{Version: "1.4.4-awgm", URL: "http://example/server", SHA256: writeBin(t, sub.serverBin, "server-1.4.4")},
	})
	if err := sub.writeInstalledVersion("1.4.4-awgm+server-1.4.4-awgm"); err != nil {
		t.Fatal(err)
	}

	st := mustStatus(t, s, "wdtt")
	if st.UpdateAvailable {
		t.Fatal("совпавшие с пином бинари не должны звать обновляться")
	}
	if st.InstalledVersion != "1.4.4-awgm+server-1.4.4-awgm" {
		t.Fatalf("installedVersion = %q", st.InstalledVersion)
	}
}

// Регрессия: сервер из протухшей сборки не знает -listen-raw и падает на
// flag.Parse, а wdtt-version.json объявлял его пином — UI показывал
// «актуально», и кнопки обновления человек не видел.
func TestStatus_WdttStaleBinaryOffersUpdate(t *testing.T) {
	s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
	sub := s.subs[SubsystemWdtt]
	clientSHA := writeBin(t, sub.clientBin, "client-1.4.4")
	writeBin(t, sub.serverBin, "server-old-without-listen-raw")
	setSpecs(s, SubsystemWdtt, ArchSpecs{
		Client: BinarySpec{Version: "1.4.4-awgm", URL: "http://example/client", SHA256: clientSHA},
		Server: BinarySpec{Version: "1.4.4-awgm", URL: "http://example/server", SHA256: "b639505b9952485bc16e9e3d43d6503975a878b0b18aba3fa5269953b61fd000"},
	})
	if err := sub.writeInstalledVersion("1.4.4-awgm+server-1.4.4-awgm"); err != nil {
		t.Fatal(err)
	}

	st := mustStatus(t, s, "wdtt")
	if !st.UpdateAvailable {
		t.Fatal("соврамший wdtt-version.json оставил роутер без обновления")
	}
	if st.InstalledVersion != "" {
		t.Fatalf("версия чужого бинаря выдана за установленную: %q", st.InstalledVersion)
	}
}

// mips/mipsel: сервера в пине нет — статус считается по одному клиенту.
func TestStatus_WdttClientOnlyArch(t *testing.T) {
	s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
	sub := s.subs[SubsystemWdtt]
	setSpecs(s, SubsystemWdtt, ArchSpecs{
		Client: BinarySpec{Version: "1.4.4-awgm", URL: "http://example/client", SHA256: writeBin(t, sub.clientBin, "client-1.4.4")},
	})
	if err := sub.writeInstalledVersion("1.4.4-awgm"); err != nil {
		t.Fatal(err)
	}

	st := mustStatus(t, s, "wdtt")
	if st.UpdateAvailable {
		t.Fatal("совпавший клиент на арке без сервера не должен звать обновляться")
	}
	if st.InstallVersion != "1.4.4-awgm" {
		t.Fatalf("составная метка на арке без сервера: %q", st.InstallVersion)
	}
}

// Бинарь пропал с диска — обновление обязано предлагаться, даже если
// version-файл цел.
func TestStatus_WdttMissingBinaryOffersUpdate(t *testing.T) {
	s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
	sub := s.subs[SubsystemWdtt]
	setSpecs(s, SubsystemWdtt, ArchSpecs{
		Client: BinarySpec{Version: "1.4.4-awgm", URL: "http://example/client", SHA256: strings.Repeat("a", 64)},
		Server: BinarySpec{Version: "1.4.4-awgm", URL: "http://example/server", SHA256: strings.Repeat("b", 64)},
	})
	if err := sub.writeInstalledVersion("1.4.4-awgm+server-1.4.4-awgm"); err != nil {
		t.Fatal(err)
	}

	st := mustStatus(t, s, "wdtt")
	if !st.UpdateAvailable {
		t.Fatal("без бинарей на диске обновление обязано предлагаться")
	}
	if st.InstalledVersion != "1.4.4-awgm+server-1.4.4-awgm" {
		t.Fatalf("installedVersion = %q", st.InstalledVersion)
	}
}

// ── Status: правила freeturn (перенос freeturn_test.go) ──────────

func TestStatus_FreeTurnPrefersSHAOverStaleVersionFile(t *testing.T) {
	s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
	sub := s.subs[SubsystemFreeTurn]
	setSpecs(s, SubsystemFreeTurn, ArchSpecs{
		Client: BinarySpec{Version: "2.1.1-1", URL: "https://x/client", SHA256: writeBin(t, sub.clientBin, "client-bin")},
		Server: BinarySpec{Version: "2.1.1-1", URL: "https://x/server", SHA256: writeBin(t, sub.serverBin, "server-bin")},
	})
	if err := sub.writeInstalledVersion("1.8.0-3"); err != nil {
		t.Fatal(err)
	}

	st := mustStatus(t, s, "freeturn")
	if st.InstalledVersion != "2.1.1-1" || st.UpdateAvailable {
		t.Fatalf("протухший version-файл не должен звать обновляться: %+v", st)
	}
}

func TestStatus_FreeTurnBundledWithoutVersionFile(t *testing.T) {
	s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
	sub := s.subs[SubsystemFreeTurn]
	setSpecs(s, SubsystemFreeTurn, ArchSpecs{
		Client: BinarySpec{Version: "2.1.1-1", URL: "https://x/client", SHA256: writeBin(t, sub.clientBin, "client-bin")},
		Server: BinarySpec{Version: "2.1.1-1", URL: "https://x/server", SHA256: writeBin(t, sub.serverBin, "server-bin")},
	})

	st := mustStatus(t, s, "freeturn")
	if st.InstalledVersion != "2.1.1-1" || st.UpdateAvailable {
		t.Fatalf("пин без version-файла: %+v", st)
	}
}

// Ревизия пересборки решает при равной semver-базе: "1.8.0-2" старее
// "1.8.0-3", и обновление обязано предлагаться.
func TestStatus_FreeTurnOlderRevisionOffersUpdate(t *testing.T) {
	s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
	sub := s.subs[SubsystemFreeTurn]
	// Бинари на месте, но не те, что в пине: версию берём из version-файла.
	writeBin(t, sub.clientBin, "client-old")
	writeBin(t, sub.serverBin, "server-old")
	setSpecs(s, SubsystemFreeTurn, ArchSpecs{
		Client: BinarySpec{Version: "1.8.0-3", URL: "https://x/client", SHA256: strings.Repeat("a", 64)},
		Server: BinarySpec{Version: "1.8.0-3", URL: "https://x/server", SHA256: strings.Repeat("b", 64)},
	})
	if err := sub.writeInstalledVersion("1.8.0-2"); err != nil {
		t.Fatal(err)
	}

	st := mustStatus(t, s, "freeturn")
	if st.InstalledVersion != "1.8.0-2" || !st.UpdateAvailable {
		t.Fatalf("старая ревизия обязана звать обновляться: %+v", st)
	}

	if err := sub.writeInstalledVersion("1.8.0-3"); err != nil {
		t.Fatal(err)
	}
	if st := mustStatus(t, s, "freeturn"); st.UpdateAvailable {
		t.Fatalf("та же ревизия не должна звать обновляться: %+v", st)
	}
}

// Неисполняемый бинарь чинится на месте (chmod +x), а не объявляется
// отсутствующим: файл приехал мимо установщика.
func TestStatus_FreeTurnChmodsNonExecutable(t *testing.T) {
	s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
	sub := s.subs[SubsystemFreeTurn]
	for _, p := range []string{sub.clientBin, sub.serverBin} {
		if err := os.WriteFile(p, []byte("bin"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	setSpecs(s, SubsystemFreeTurn, ArchSpecs{
		Client: BinarySpec{Version: "2.1.1-1", URL: "https://x/client", SHA256: strings.Repeat("a", 64)},
		Server: BinarySpec{Version: "2.1.1-1", URL: "https://x/server", SHA256: strings.Repeat("b", 64)},
	})
	if err := sub.writeInstalledVersion("2.1.1-1"); err != nil {
		t.Fatal(err)
	}

	if st := mustStatus(t, s, "freeturn"); st.UpdateAvailable {
		t.Fatalf("бинари на месте (после chmod) — обновление не нужно: %+v", st)
	}
	if !binaryPresent(sub.clientBin) {
		t.Error("бит исполнения не выставлен")
	}
}

// ── version-файл ─────────────────────────────────────────────────

// Старые wdtt-version.json писали версии клиента и сервера раздельно.
func TestReadInstalledVersion_LegacyWdttRecord(t *testing.T) {
	s := newTestService(t, Deps{})
	sub := s.subs[SubsystemWdtt]
	legacy := `{"clientVersion":"1.4.3-awgm","serverVersion":"1.4.2-awgm","clientPath":"/opt/bin/wdtt-client"}`
	if err := os.WriteFile(sub.versionPath, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	if got := sub.readInstalledVersion(); got != "1.4.3-awgm+server-1.4.2-awgm" {
		t.Fatalf("legacy-запись: %q", got)
	}
}

// Битый файл — «версия неизвестна», а не паника и не мусор наружу.
func TestReadInstalledVersion_BrokenFile(t *testing.T) {
	s := newTestService(t, Deps{})
	sub := s.subs[SubsystemFreeTurn]
	if err := os.WriteFile(sub.versionPath, []byte("{не json"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := sub.readInstalledVersion(); got != "" {
		t.Fatalf("битый файл дал версию %q", got)
	}
}

// Без каталога данных version-файла нет: относительный путь уехал бы в
// текущий каталог демона.
func TestVersionFile_NoDataDir(t *testing.T) {
	s := New(Deps{})
	sub := s.subs[SubsystemWdtt]
	if sub.versionPath != "" {
		t.Fatalf("без dataDir путь version-файла = %q", sub.versionPath)
	}
	if err := sub.writeInstalledVersion("1.0.0"); err != nil {
		t.Fatalf("запись без пути обязана быть no-op: %v", err)
	}
	if got := sub.readInstalledVersion(); got != "" {
		t.Fatalf("чтение без пути дало %q", got)
	}
}

// ── Install ──────────────────────────────────────────────────────

func TestInstall_DownloadsPinnedAssets(t *testing.T) {
	clientBody, serverBody := []byte("client-bin"), []byte("server-bin")
	dl := &fakeDownloader{payload: map[string][]byte{
		"https://x/client": clientBody,
		"https://x/server": serverBody,
	}}
	s := newTestService(t, Deps{Downloader: dl})
	sub := s.subs[SubsystemWdtt]
	setSpecs(s, SubsystemWdtt, ArchSpecs{
		Client: BinarySpec{Version: "1.4.4-awgm", URL: "https://x/client", SHA256: sha256Hex(clientBody), Size: int64(len(clientBody))},
		Server: BinarySpec{Version: "1.4.3-awgm", URL: "https://x/server", SHA256: sha256Hex(serverBody), Size: int64(len(serverBody))},
	})

	if err := s.Install(context.Background(), "wdtt"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Состав вызовов целиком: адрес пина, временный файл рядом с бинарём и
	// потолок передачи = размер пина + запас childproc.
	want := []downloadCall{
		{URL: "https://x/client", Dest: sub.clientBin + ".tmp", MaxBytes: int64(len(clientBody)) + 1<<20},
		{URL: "https://x/server", Dest: sub.serverBin + ".tmp", MaxBytes: int64(len(serverBody)) + 1<<20},
	}
	if got := dl.recorded(); !reflect.DeepEqual(got, want) {
		t.Fatalf("вызовы загрузчика:\n got %+v\nwant %+v", got, want)
	}
	if !binaryPresent(sub.clientBin) || !binaryPresent(sub.serverBin) {
		t.Fatal("бинари не активированы")
	}
	// Составная метка ушла в version-файл — иначе статус после установки
	// зовёт ставить снова.
	if got := sub.readInstalledVersion(); got != "1.4.4-awgm+server-1.4.3-awgm" {
		t.Fatalf("version-файл: %q", got)
	}
	if st := mustStatus(t, s, "wdtt"); st.UpdateAvailable || st.InstalledVersion != "1.4.4-awgm+server-1.4.3-awgm" {
		t.Fatalf("после установки: %+v", st)
	}
}

// На арке без серверного пина сервер НЕ качается: URL пустой, и загрузка
// упала бы на ровном месте.
func TestInstall_WdttClientOnlyArchSkipsServer(t *testing.T) {
	clientBody := []byte("client-bin")
	dl := &fakeDownloader{payload: map[string][]byte{"https://x/client": clientBody}}
	s := newTestService(t, Deps{Downloader: dl})
	sub := s.subs[SubsystemWdtt]
	setSpecs(s, SubsystemWdtt, ArchSpecs{
		Client: BinarySpec{Version: "1.4.4-awgm", URL: "https://x/client", SHA256: sha256Hex(clientBody), Size: int64(len(clientBody))},
	})

	if err := s.Install(context.Background(), "wdtt"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := []downloadCall{{URL: "https://x/client", Dest: sub.clientBin + ".tmp", MaxBytes: int64(len(clientBody)) + 1<<20}}
	if got := dl.recorded(); !reflect.DeepEqual(got, want) {
		t.Fatalf("вызовы загрузчика:\n got %+v\nwant %+v", got, want)
	}
}

func TestInstall_FreeTurnInstallsBothAndLabels(t *testing.T) {
	clientBody, serverBody := []byte("ft-client"), []byte("ft-server")
	dl := &fakeDownloader{payload: map[string][]byte{
		"https://x/ft-client": clientBody,
		"https://x/ft-server": serverBody,
	}}
	var infos []string
	s := newTestService(t, Deps{Downloader: dl, Info: func(m string) { infos = append(infos, m) }})
	sub := s.subs[SubsystemFreeTurn]
	setSpecs(s, SubsystemFreeTurn, ArchSpecs{
		Client: BinarySpec{Version: "2.1.1-1", URL: "https://x/ft-client", SHA256: sha256Hex(clientBody), Size: int64(len(clientBody))},
		Server: BinarySpec{Version: "2.1.1-1", URL: "https://x/ft-server", SHA256: sha256Hex(serverBody), Size: int64(len(serverBody))},
	})

	if err := s.Install(context.Background(), "freeturn"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// Метка freeturn — ОДНА версия на оба бинаря, без "+server-".
	if got := sub.readInstalledVersion(); got != "2.1.1-1" {
		t.Fatalf("version-файл: %q", got)
	}
	if len(infos) != 1 || !strings.Contains(infos[0], "freeturn v2.1.1-1 установлен") {
		t.Fatalf("журнал установки: %q", infos)
	}
}

// Установка недоступна: тексты отказов вербатим старых ручек.
func TestInstall_Unavailable(t *testing.T) {
	t.Run("нет пина", func(t *testing.T) {
		s := newTestService(t, Deps{Arch: "неведомая-арка", Downloader: &fakeDownloader{}})
		err := s.Install(context.Background(), "wdtt")
		if err == nil || err.Error() != "установка недоступна: для этой архитектуры нет закреплённой сборки wdtt" {
			t.Fatalf("текст отказа: %v", err)
		}
	})
	t.Run("нет загрузчика", func(t *testing.T) {
		s := newTestService(t, Deps{Arch: "aarch64-3.10"})
		err := s.Install(context.Background(), "freeturn")
		if err == nil || err.Error() != "установка недоступна: для этой архитектуры нет закреплённой сборки freeturn" {
			t.Fatalf("текст отказа: %v", err)
		}
		if st := mustStatus(t, s, "freeturn"); st.InstallAvailable || st.InstallVersion != "" {
			t.Fatalf("без загрузчика установка не может быть доступна: %+v", st)
		}
	})
	t.Run("неизвестная подсистема", func(t *testing.T) {
		s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
		if err := s.Install(context.Background(), "singbox"); err == nil {
			t.Fatal("неизвестная подсистема обязана быть отказом")
		}
	})
}

// Подменённый бинарь не активируется, а несовпадение суммы попадает в журнал.
func TestInstall_ChecksumMismatch(t *testing.T) {
	body := []byte("client-bin")
	dl := &fakeDownloader{payload: map[string][]byte{"https://x/client": body}}
	var warns []string
	s := newTestService(t, Deps{Downloader: dl, Warn: func(m string) { warns = append(warns, m) }})
	sub := s.subs[SubsystemWdtt]
	setSpecs(s, SubsystemWdtt, ArchSpecs{
		Client: BinarySpec{Version: "1.4.4-awgm", URL: "https://x/client", SHA256: strings.Repeat("0", 64), Size: int64(len(body))},
	})

	err := s.Install(context.Background(), "wdtt")
	if err == nil || !strings.Contains(err.Error(), "контрольная сумма") {
		t.Fatalf("want sha mismatch error, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "клиент: ") {
		t.Fatalf("отказ обязан называть бинарь: %v", err)
	}
	if binaryPresent(sub.clientBin) {
		t.Error("подменённый бинарь не должен активироваться")
	}
	if _, statErr := os.Stat(sub.clientBin + ".tmp"); statErr == nil {
		t.Error("tmp обязан быть убран")
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "sha256 mismatch") || !strings.Contains(warns[0], "https://x/client") {
		t.Fatalf("журнал несовпадения: %q", warns)
	}
}

// installing виден в статусе ИМЕННО во время установки — по нему фронт
// блокирует кнопку; второй параллельный вызов получает отказ.
func TestInstall_InFlightIsVisibleAndSerialized(t *testing.T) {
	body := []byte("client-bin")
	dl := &fakeDownloader{payload: map[string][]byte{"https://x/client": body}}
	s := newTestService(t, Deps{Downloader: dl})
	setSpecs(s, SubsystemWdtt, ArchSpecs{
		Client: BinarySpec{Version: "1.4.4-awgm", URL: "https://x/client", SHA256: sha256Hex(body), Size: int64(len(body))},
	})

	var during InstallStatus
	var secondErr error
	// Хук одноразовый: если гейт «уже выполняется» снять, второй вызов дойдёт
	// до загрузчика, и без этого флага тест ушёл бы в бесконечную рекурсию
	// вместо провала утверждения.
	probed := false
	dl.before = func() {
		if probed {
			return
		}
		probed = true
		during = mustStatus(t, s, "wdtt")
		secondErr = s.Install(context.Background(), "wdtt")
	}
	if err := s.Install(context.Background(), "wdtt"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	want := InstallStatus{
		ServerSupported:  false,
		InstallAvailable: true,
		InstallVersion:   "1.4.4-awgm",
		InstalledVersion: "",
		UpdateAvailable:  true,
		Installing:       true,
		RouterClock:      "2026-08-24 15:04:05 MSK",
	}
	if !reflect.DeepEqual(during, want) {
		t.Fatalf("статус во время установки:\n got %+v\nwant %+v", during, want)
	}
	if secondErr == nil || secondErr.Error() != "установка уже выполняется" {
		t.Fatalf("параллельная установка: %v", secondErr)
	}
	if st := mustStatus(t, s, "wdtt"); st.Installing {
		t.Fatal("после установки флаг обязан сняться")
	}
}

// Установка freeturn не трогает бинари wdtt и наоборот: подсистемы
// независимы, общий у них только механизм.
func TestInstall_SubsystemsAreIndependent(t *testing.T) {
	body := []byte("ft-client")
	dl := &fakeDownloader{payload: map[string][]byte{
		"https://x/ft-client": body,
		"https://x/ft-server": body,
	}}
	s := newTestService(t, Deps{Downloader: dl})
	setSpecs(s, SubsystemFreeTurn, ArchSpecs{
		Client: BinarySpec{Version: "2.1.1-1", URL: "https://x/ft-client", SHA256: sha256Hex(body), Size: int64(len(body))},
		Server: BinarySpec{Version: "2.1.1-1", URL: "https://x/ft-server", SHA256: sha256Hex(body), Size: int64(len(body))},
	})

	if err := s.Install(context.Background(), "freeturn"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, present := s.Binary(instancestore.KindWdttClient); present {
		t.Error("установка freeturn принесла бинарь wdtt")
	}
	if _, err := os.Stat(s.subs[SubsystemWdtt].versionPath); err == nil {
		t.Error("установка freeturn написала version-файл wdtt")
	}
}
