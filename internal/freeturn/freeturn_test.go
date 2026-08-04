package freeturn

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

// ---------------------------------------------------------------------------
// link.go
// ---------------------------------------------------------------------------

func TestLink_Roundtrip(t *testing.T) {
	p := LinkPayload{V: 1, Provider: "vk", Peer: "1.2.3.4:56000", Obf: "rtpopus2", Key: "aabb", MTU: 1280, WG: "[Interface]\nPrivateKey = x\n"}
	link, err := EncodeLink(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, LinkScheme) {
		t.Fatalf("no scheme prefix: %q", link)
	}
	if strings.HasSuffix(link, "=") {
		t.Fatalf("padding must be stripped (JS-generator parity): %q", link)
	}
	got, err := DecodeLink(link)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("roundtrip mismatch:\n got %+v\nwant %+v", got, p)
	}
}

func TestStripWGConfMTU(t *testing.T) {
	conf := "[Interface]\nPrivateKey = x\nMTU = 1376\n[Peer]\nPublicKey = y\n"
	got := StripWGConfMTU(conf)
	if strings.Contains(got, "MTU") {
		t.Fatalf("MTU line must be stripped: %q", got)
	}
	if !strings.Contains(got, "PrivateKey") {
		t.Fatalf("other lines preserved: %q", got)
	}
}

func TestDecodeLink_WithoutScheme(t *testing.T) {
	link, _ := EncodeLink(LinkPayload{V: 1, Peer: "h:1"})
	got, err := DecodeLink(strings.TrimPrefix(link, LinkScheme))
	if err != nil || got.Peer != "h:1" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestDecodeLink_Rejects(t *testing.T) {
	for _, bad := range []string{"", "freeturn://", "freeturn://%%%", "freeturn://aGVsbG8"} {
		if _, err := DecodeLink(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// service.go — CLI-аргументы
// ---------------------------------------------------------------------------

func TestBuildClientArgs_FullAndZero(t *testing.T) {
	full := ClientConfig{
		Listen: "127.0.0.1:9000", Peer: "h:56000", Provider: "vk",
		Links: "https://vk.ru/call/join/a", Streams: 4, Transport: "tcp",
		Mode: "udp", Bond: true, TurnHost: "turn.host", TurnPort: 3478,
		ObfProfile: "rtpopus2", ObfKey: "deadbeef", StreamsPerCred: 2,
		Platform:       "mobile", DNSMode: "doh",
		DNSServers: "1.1.1.1", ClientID: "cid", Sub: "s", Debug: true,
	}
	want := []string{
		"-listen", "127.0.0.1:9000", "-peer", "h:56000", "-provider", "vk",
		"-links", "https://vk.ru/call/join/a", "-n", "4", "-transport", "tcp",
		"-mode", "udp", "-bond", "-turn", "turn.host", "-port", "3478",
		"-obf-profile", "rtpopus2", "-obf-key", "deadbeef",
		"-streams-per-cred", "2", "-platform", "mobile",
		"-dns-mode", "doh", "-dns-servers", "1.1.1.1", "-client-id", "cid",
		"-sub", "s", "-debug",
	}
	if got := buildClientArgs(full); !reflect.DeepEqual(got, want) {
		t.Fatalf("full args:\n got %v\nwant %v", got, want)
	}
	// Нулевые значения не эмитятся — остаются дефолты бинаря.
	if got := buildClientArgs(ClientConfig{}); len(got) != 0 {
		t.Fatalf("zero config must emit no args, got %v", got)
	}
}

func TestBuildServerArgs(t *testing.T) {
	got := buildServerArgs(ServerConfig{Listen: "0.0.0.0:56000", Connect: "127.0.0.1:51820", ObfProfile: "rtpopus", Debug: true})
	want := []string{"-listen", "0.0.0.0:56000", "-connect", "127.0.0.1:51820", "-obf-profile", "rtpopus", "-debug"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestValidateObfKey(t *testing.T) {
	valid := strings.Repeat("ab", 32) // 64 hex-символа
	cases := []struct {
		name    string
		profile string
		key     string
		wantErr bool
	}{
		{"no-profile", "", "", false},
		{"none-profile", "none", "", false},
		{"none-ignores-key", "none", "xx", false},
		{"empty-key", "rtpopus2", "", true},
		{"short-key", "rtpopus2", "deadbeef", true},
		{"non-hex-key", "rtpopus2", strings.Repeat("zz", 32), true},
		{"valid", "rtpopus2", valid, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateObfKey(c.profile, c.key)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateObfKey(%q, %q) = %v, wantErr=%v", c.profile, c.key, err, c.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// store.go
// ---------------------------------------------------------------------------

func TestStore_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	cfg, err := s.Load() // отсутствующий файл → дефолты
	if err != nil {
		t.Fatal(err)
	}
	cfg.Clients[0].Config.Peer = "h:56000"
	cfg.Servers[0].Config.Connect = "127.0.0.1:51820"
	if err := s.Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Clients[0].Config.Peer != "h:56000" || got.Servers[0].Config.Connect != "127.0.0.1:51820" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestCompareFreeturnVersion_Revision(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.8.0-2", "1.8.0-3", -1}, // баг, который чинит фикс: semver.Compare даёт 0
		{"1.8.0-3", "1.8.0-2", 1},
		{"1.8.0-3", "1.8.0-3", 0},
		{"1.8.0", "1.8.0-1", -1}, // нет суффикса → ревизия 0
		{"1.8.1-1", "1.8.0-9", 1}, // разные базы решает semver, ревизия не важна
	}
	for _, c := range cases {
		if got := compareFreeturnVersion(c.a, c.b); got != c.want {
			t.Errorf("compareFreeturnVersion(%q,%q)=%d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestResolveInstallSpecs_AlwaysPin(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, filepath.Join(dir, "c"), filepath.Join(dir, "s"))
	s.installSpecs = &ArchSpecs{
		Client: BinarySpec{Version: "1.0.0", URL: "https://pin/client", SHA256: strings.Repeat("a", 64), Size: 1},
		Server: BinarySpec{Version: "1.0.0", URL: "https://pin/server", SHA256: strings.Repeat("b", 64), Size: 1},
	}
	specs, ver := s.resolveInstallSpecs()
	if ver != "1.0.0" || specs.Client.URL != "https://pin/client" {
		t.Fatalf("resolve: ver=%s specs=%+v (ожидался пин 1.0.0)", ver, specs)
	}
}

func TestStore_MigrateV1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "freeturn.json")
	v1 := `{"client":{"peer":"h:56000"},"server":{"connect":"127.0.0.1:51820"}}`
	if err := os.WriteFile(path, []byte(v1), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != ConfigVersion || len(got.Clients) != 1 || len(got.Servers) != 1 {
		t.Fatalf("migrate: %+v", got)
	}
	if got.Clients[0].ID != DefaultInstanceID || got.Clients[0].Config.Peer != "h:56000" {
		t.Fatalf("client migrate: %+v", got.Clients[0])
	}
	if got.Servers[0].Config.Connect != "127.0.0.1:51820" {
		t.Fatalf("server migrate: %+v", got.Servers[0])
	}
}

func TestStore_DeleteAllClientsRestoresDefault(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	cfg, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Clients = nil
	if err := s.Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("after save with empty clients want 1, got %d", len(got.Clients))
	}
	if got.Clients[0].ID != DefaultInstanceID {
		t.Fatalf("want default id %q, got %q", DefaultInstanceID, got.Clients[0].ID)
	}
}

func TestStore_LoadReturnsIsolatedCopy(t *testing.T) {
	// Кэш-ветка: прогреваем кэш, далее мутируем результат Load.
	t.Run("cache", func(t *testing.T) {
		s := NewStore(t.TempDir())
		if _, err := s.Load(); err != nil {
			t.Fatal(err)
		}
		assertLoadIsolated(t, s)
	})

	// Первый Load, disk-ветка file-missing (DefaultConfig): срезы результата
	// не должны алиасить кэш, который выставил saveLocked.
	t.Run("first-load-missing", func(t *testing.T) {
		assertLoadIsolated(t, NewStore(t.TempDir()))
	})

	// Первый Load, disk-ветка file-exists: читаем готовый файл, срезы
	// результата не должны алиасить кэш s.cfg.
	t.Run("first-load-existing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "freeturn.json"),
			[]byte(`{"version":1,"clients":[{"id":"default"}],"servers":[{"id":"default"}]}`), 0644); err != nil {
			t.Fatal(err)
		}
		assertLoadIsolated(t, NewStore(dir))
	})
}

// assertLoadIsolated: результат первого зафиксированного Load мутируется, и
// эта мутация не должна быть видна в последующем Load (т.е. не протекла в кэш).
func assertLoadIsolated(t *testing.T, s *Store) {
	t.Helper()
	cfg, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clients) == 0 || len(cfg.Servers) == 0 {
		t.Fatal("want at least one client and server")
	}
	cfg.Clients[0].Config.Peer = "leaked-client"
	cfg.Servers[0].Config.Connect = "leaked-server"

	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Clients[0].Config.Peer == "leaked-client" {
		t.Fatal("мутация client протекла в кэш")
	}
	if got.Servers[0].Config.Connect == "leaked-server" {
		t.Fatal("мутация server протекла в кэш")
	}
}

func TestStore_DeleteAllServersRestoresDefault(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	cfg, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Servers = nil
	if err := s.Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 1 {
		t.Fatalf("after save with empty servers want 1, got %d", len(got.Servers))
	}
	if got.Servers[0].ID != DefaultInstanceID {
		t.Fatalf("want default id %q, got %q", DefaultInstanceID, got.Servers[0].ID)
	}
}

func TestService_CreateMultipleClients(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, filepath.Join(dir, "c"), filepath.Join(dir, "s"))

	first, err := s.CreateClient(CreateClientInput{Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateClient(CreateClientInput{Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Config.Listen == second.Config.Listen {
		t.Fatalf("listen ports must differ: %s", first.Config.Listen)
	}
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clients) != 3 { // default + 2
		t.Fatalf("want 3 clients, got %d", len(cfg.Clients))
	}
	if err := s.DeleteClient(second.ID); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.GetConfig()
	if len(cfg.Clients) != 2 {
		t.Fatalf("after delete want 2 clients, got %d", len(cfg.Clients))
	}
}

// ---------------------------------------------------------------------------
// process.go — через seam startCmd; p.binary указывает на /bin/sh, чтобы
// пройти проверку binaryPresent, а реальная команда подменяется seam'ом.
// ---------------------------------------------------------------------------

func newTestProcess(t *testing.T, script string) *process {
	t.Helper()
	p := newProcess("client", "/bin/sh", t.TempDir())
	p.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}
	return p
}

func TestProcess_StartupFailureCapturesStderr(t *testing.T) {
	p := newTestProcess(t, "echo boom >&2; exit 1")
	err := p.Start(nil)
	if err == nil {
		t.Fatal("want startup error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr tail not in error: %v", err)
	}
	if running, _ := p.IsRunning(); running {
		t.Fatal("must not be running after startup failure")
	}
	if st := p.Status(); st.LastError == "" {
		t.Fatal("LastError must survive for the status endpoint")
	}
}

// TestProcess_StartupFailure_StderrCaptured_UnderDrainDelay детерминированно
// воспроизводит гонку os/exec: если Wait() закрывает пайпы раньше, чем drain
// успел прочитать stderr, «boom» теряется. drainStartDelay форсирует окно.
func TestProcess_StartupFailure_StderrCaptured_UnderDrainDelay(t *testing.T) {
	p := newTestProcess(t, "echo boom >&2; exit 1")
	p.drainStartDelay = 50 * time.Millisecond
	err := p.Start(nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr должен быть в ошибке даже с задержкой drain, got: %v", err)
	}
}

func TestProcess_StartStop(t *testing.T) {
	p := newTestProcess(t, "sleep 30")
	if err := p.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if running, _ := p.IsRunning(); !running {
		t.Fatal("must be running after grace period")
	}
	if st := p.Status(); !st.Running || st.PID == 0 || st.StartedAt == nil {
		t.Fatalf("bad status: %+v", st)
	}
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if running, _ := p.IsRunning(); !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("still running after Stop")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Штатная остановка — не ошибка: лог не должен попадать в LastError.
	if st := p.Status(); st.LastError != "" {
		t.Fatalf("clean Stop must not leave LastError, got %q", st.LastError)
	}
}

func TestProcess_StartMissingBinary(t *testing.T) {
	p := newProcess("client", "/nonexistent/freeturn-client", t.TempDir())
	err := p.Start(nil)
	if err == nil || !strings.Contains(err.Error(), "не найден") {
		t.Fatalf("want clear missing-binary error, got %v", err)
	}
}

func TestProcess_StartRestartsOrphanPID(t *testing.T) {
	dir := t.TempDir()
	// Бинарь = /bin/sleep, чтобы /proc cmdline осиротевшего процесса совпал с
	// нашим — иначе pid считается чужим (см. TestProcess_StartKeepsForeignPID).
	p1 := newProcess("client", "/bin/sleep", dir)
	p1.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sleep", "30")
	}
	if err := p1.Start(nil); err != nil {
		t.Fatal(err)
	}
	orphanPID, _ := p1.readPID()

	// Новый процесс awg-manager: тот же pidfile, startedAt не задан.
	p2 := newProcess("client", "/bin/sleep", dir)
	p2.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "echo adopted; sleep 30")
	}
	if err := p2.Start(nil); err != nil {
		t.Fatalf("Start orphan: %v", err)
	}
	st := p2.Status()
	if st.StartedAt == nil {
		t.Fatal("want StartedAt after orphan restart")
	}
	if st.PID == orphanPID {
		t.Fatalf("want new PID, still on orphan %d", orphanPID)
	}
	if !strings.Contains(st.Log, "adopted") {
		t.Fatalf("want log from restarted process, got %q", st.Log)
	}
	if childproc.IsAlive(orphanPID) {
		t.Fatal("осиротевший процесс должен быть остановлен")
	}
	_ = p2.Stop()
}

// Pid-файл лежит на флешке и переживает ребут: записанный PID мог достаться
// постороннему процессу. Такой pid — не «наш прокси запущен», и убивать его
// нельзя.
func TestProcess_StartKeepsForeignPID(t *testing.T) {
	foreign := exec.Command("/bin/sleep", "30")
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Process.Kill(); _ = foreign.Wait() }()

	p := newProcess("client", "/opt/bin/freeturn-client", t.TempDir())
	if err := p.writePID(foreign.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if running, _ := p.IsRunning(); running {
		t.Fatal("чужой pid не должен считаться запущенным прокси")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if !childproc.IsAlive(foreign.Process.Pid) {
		t.Fatal("Stop убил посторонний процесс")
	}
	if _, err := p.readPID(); err == nil {
		t.Fatal("протухший pid-файл должен быть удалён")
	}
}

func TestBinaryPresent(t *testing.T) {
	if binaryPresent("/nonexistent/path") {
		t.Error("missing path must be absent")
	}
	if !binaryPresent("/bin/sh") {
		t.Error("/bin/sh must be present+executable")
	}
	if binaryPresent(t.TempDir()) {
		t.Error("directory must not count as binary")
	}
}

// ---------------------------------------------------------------------------
// install.go
// ---------------------------------------------------------------------------

type fakeDownloader struct {
	payload map[string][]byte // url → содержимое
}

func (f *fakeDownloader) DownloadFile(_ context.Context, url, destPath string, _ int64) error {
	body, ok := f.payload[url]
	if !ok {
		return os.ErrNotExist
	}
	return os.WriteFile(destPath, body, 0o644)
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func newInstallService(t *testing.T, dl childproc.Downloader, specs ArchSpecs) *Service {
	t.Helper()
	dir := t.TempDir()
	s := NewService(dir, dir, filepath.Join(dir, "freeturn-client"), filepath.Join(dir, "freeturn-server"))
	s.SetInstallSpecs(specs)
	s.SetDownloader(dl)
	return s
}

func TestInstallBinaries_HappyPath(t *testing.T) {
	clientBody, serverBody := []byte("client-bin"), []byte("server-bin")
	specs := ArchSpecs{
		Client: BinarySpec{Version: "1.8.0", URL: "https://x/client", SHA256: sha256Hex(clientBody), Size: int64(len(clientBody))},
		Server: BinarySpec{Version: "1.8.0", URL: "https://x/server", SHA256: sha256Hex(serverBody), Size: int64(len(serverBody))},
	}
	dl := &fakeDownloader{payload: map[string][]byte{"https://x/client": clientBody, "https://x/server": serverBody}}
	s := newInstallService(t, dl, specs)

	if err := s.InstallBinaries(context.Background()); err != nil {
		t.Fatalf("InstallBinaries: %v", err)
	}
	for _, p := range []string{s.clientBin, s.serverBin} {
		if !binaryPresent(p) {
			t.Errorf("%s must be installed and executable", p)
		}
	}
	st := s.Status()
	if !st.InstallAvailable || st.InstallVersion != "1.8.0" || st.Installing {
		t.Errorf("status: %+v", st)
	}
	if !st.Client.BinaryPresent || !st.Server.BinaryPresent {
		t.Errorf("binaryPresent must flip after install: %+v", st)
	}
	if st.InstalledVersion != "1.8.0" || st.UpdateAvailable {
		t.Errorf("want installed version recorded and up-to-date: %+v", st)
	}
}

func TestEnsureBundledInstall_RepairsNonExecutableBinaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execute bit semantics differ on Windows")
	}
	dir := t.TempDir()
	client := filepath.Join(dir, "freeturn-client")
	server := filepath.Join(dir, "freeturn-server")
	for _, p := range []string{client, server} {
		if err := os.WriteFile(p, []byte("bin"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := NewService(dir, dir, client, server)
	s.SetInstallSpecs(EmbeddedBinaries["aarch64-3.10"])

	s.EnsureBundledInstall()

	if !binaryPresent(client) || !binaryPresent(server) {
		t.Fatal("EnsureBundledInstall must chmod bundled binaries executable")
	}
}

func TestEffectiveInstalledVersion_PrefersSHAOverStaleVersionFile(t *testing.T) {
	clientBody, serverBody := []byte("client-bin"), []byte("server-bin")
	specs := ArchSpecs{
		Client: BinarySpec{Version: PinnedVersion, URL: "https://x/client", SHA256: sha256Hex(clientBody), Size: int64(len(clientBody))},
		Server: BinarySpec{Version: PinnedVersion, URL: "https://x/server", SHA256: sha256Hex(serverBody), Size: int64(len(serverBody))},
	}
	dl := &fakeDownloader{payload: map[string][]byte{"https://x/client": clientBody, "https://x/server": serverBody}}
	s := newInstallService(t, dl, specs)
	if err := s.InstallBinaries(context.Background()); err != nil {
		t.Fatalf("InstallBinaries: %v", err)
	}
	if err := s.writeInstalledVersion("1.8.0-3"); err != nil {
		t.Fatal(err)
	}

	if got := s.effectiveInstalledVersion(); got != PinnedVersion {
		t.Fatalf("want effective %q from SHA, got %q", PinnedVersion, got)
	}
	installed, update := s.installStatusFields(PinnedVersion)
	if installed != PinnedVersion || update {
		t.Fatalf("stale version file must not trigger update: installed=%q update=%v", installed, update)
	}

	s.EnsureBundledInstall()
	if s.readInstalledVersion() != PinnedVersion {
		t.Fatalf("EnsureBundledInstall must rewrite version file, got %q", s.readInstalledVersion())
	}
}

func TestEnsureBundledInstall_WritesVersionWhenBinariesPresent(t *testing.T) {
	clientBody, serverBody := []byte("client-bin"), []byte("server-bin")
	specs := ArchSpecs{
		Client: BinarySpec{Version: PinnedVersion, URL: "https://x/client", SHA256: sha256Hex(clientBody), Size: int64(len(clientBody))},
		Server: BinarySpec{Version: PinnedVersion, URL: "https://x/server", SHA256: sha256Hex(serverBody), Size: int64(len(serverBody))},
	}
	dl := &fakeDownloader{payload: map[string][]byte{"https://x/client": clientBody, "https://x/server": serverBody}}
	s := newInstallService(t, dl, specs)
	if err := s.InstallBinaries(context.Background()); err != nil {
		t.Fatalf("InstallBinaries: %v", err)
	}
	if err := os.Remove(s.versionPath); err != nil {
		t.Fatalf("remove version file: %v", err)
	}

	s.EnsureBundledInstall()

	st := s.Status()
	if st.InstalledVersion != PinnedVersion {
		t.Fatalf("want installed version %q, got %q", PinnedVersion, st.InstalledVersion)
	}
	if st.UpdateAvailable {
		t.Fatalf("bundled install must not show update: %+v", st)
	}
}

func TestInstallStatusFields_BundledWithoutVersionFile(t *testing.T) {
	clientBody, serverBody := []byte("client-bin"), []byte("server-bin")
	specs := ArchSpecs{
		Client: BinarySpec{Version: PinnedVersion, URL: "https://x/client", SHA256: sha256Hex(clientBody), Size: int64(len(clientBody))},
		Server: BinarySpec{Version: PinnedVersion, URL: "https://x/server", SHA256: sha256Hex(serverBody), Size: int64(len(serverBody))},
	}
	dl := &fakeDownloader{payload: map[string][]byte{"https://x/client": clientBody, "https://x/server": serverBody}}
	s := newInstallService(t, dl, specs)
	if err := s.InstallBinaries(context.Background()); err != nil {
		t.Fatalf("InstallBinaries: %v", err)
	}
	if err := os.Remove(s.versionPath); err != nil {
		t.Fatalf("remove version file: %v", err)
	}

	installed, update := s.installStatusFields(PinnedVersion)
	if installed != PinnedVersion || update {
		t.Fatalf("want bundled pin without version file, got installed=%q update=%v", installed, update)
	}
}

func TestInstallBinaries_SHA256Mismatch(t *testing.T) {
	body := []byte("client-bin")
	specs := ArchSpecs{
		Client: BinarySpec{Version: "1.8.0", URL: "https://x/client", SHA256: strings.Repeat("0", 64), Size: int64(len(body))},
		Server: BinarySpec{Version: "1.8.0", URL: "https://x/server", SHA256: strings.Repeat("0", 64), Size: 1},
	}
	dl := &fakeDownloader{payload: map[string][]byte{"https://x/client": body}}
	s := newInstallService(t, dl, specs)

	err := s.InstallBinaries(context.Background())
	if err == nil || !strings.Contains(err.Error(), "контрольная сумма") {
		t.Fatalf("want sha mismatch error, got %v", err)
	}
	if binaryPresent(s.clientBin) {
		t.Error("tampered binary must NOT be activated")
	}
	if _, statErr := os.Stat(s.clientBin + ".tmp"); statErr == nil {
		t.Error("tmp must be cleaned up")
	}
}

func TestInstallBinaries_Unavailable(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, filepath.Join(dir, "c"), filepath.Join(dir, "s"))
	if err := s.InstallBinaries(context.Background()); err == nil {
		t.Fatal("want error when specs/downloader not wired")
	}
	if _, ok := s.InstallInfo(); ok {
		t.Fatal("InstallInfo must report unavailable")
	}
}

func TestEmbeddedBinaries_CoverAllArches(t *testing.T) {
	for _, arch := range []string{"aarch64-3.10", "mipsel-3.4", "mips-3.4"} {
		specs, ok := EmbeddedBinaries[arch]
		if !ok {
			t.Fatalf("%s: no specs", arch)
		}
		for name, sp := range map[string]BinarySpec{"client": specs.Client, "server": specs.Server} {
			if sp.Version != PinnedVersion || len(sp.SHA256) != 64 || sp.Size <= 0 ||
				sp.URL == "" {
				t.Errorf("%s/%s: bad spec %+v", arch, name, sp)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// link.go — официальный формат (#530): base64url + поля uri.md, и обратная
// совместимость со старым неофициальным форматом (StdEncoding без padding).
// ---------------------------------------------------------------------------

func TestLink_Roundtrip_UpstreamFields(t *testing.T) {
	p := LinkPayload{
		V: 1, Provider: "vk", Peer: "1.2.3.4:56000",
		Transport: "tcp", Mode: "udp", Bond: true,
		Obf: "rtpopus2", Key: "aabb",
		N: 4, StreamsPerCred: 2, ClientID: "deadbeefcafe",
		Listen: "127.0.0.1:9000", DNSMode: "doh", DNSServers: "1.1.1.1",
		ManualCaptcha: true, Name: "гость",
	}
	link, err := EncodeLink(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeLink(link)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("roundtrip mismatch:\n got %+v\nwant %+v", got, p)
	}
}

// Ссылка настоящего клиента/приложения: base64url без padding
// (Go base64.RawURLEncoding, docs/uri.md) — в т.ч. с символами '-'/'_',
// которых нет в стандартном алфавите.
func TestDecodeLink_UpstreamBase64URL(t *testing.T) {
	// Подбираем name, при котором url-safe кодирование содержит '-'/'_' —
	// иначе тест не отличит алфавиты. Кириллица (байты ≥0xC0) даёт их быстро.
	var p LinkPayload
	var body string
	for i := 0; i < 64; i++ {
		p = LinkPayload{V: 1, Peer: "h:56000", ClientID: "cid1", Name: "я" + strings.Repeat("~", i)}
		raw, _ := json.Marshal(p)
		body = base64.RawURLEncoding.EncodeToString(raw)
		if strings.ContainsAny(body, "-_") {
			break
		}
		body = ""
	}
	if body == "" {
		t.Fatal("could not construct a url-safe sample")
	}
	got, err := DecodeLink(LinkScheme + body)
	if err != nil {
		t.Fatalf("DecodeLink: %v", err)
	}
	if got.ClientID != "cid1" || got.Name != p.Name {
		t.Fatalf("got %+v want %+v", got, p)
	}
}

// Старый неофициальный формат (entware-installer / awg-manager до #530):
// стандартный алфавит, padding срезан — обязан читаться и дальше.
func TestDecodeLink_LegacyStdEncoding(t *testing.T) {
	p := LinkPayload{V: 1, Provider: "vk", Peer: "h:56000", Obf: "rtpopus", Key: "aa", MTU: 1280, WG: "[Interface]\n"}
	raw, _ := json.Marshal(p)
	legacy := strings.TrimRight(base64.StdEncoding.EncodeToString(raw), "=")
	got, err := DecodeLink(LinkScheme + legacy)
	if err != nil {
		t.Fatalf("DecodeLink(legacy): %v", err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("legacy mismatch:\n got %+v\nwant %+v", got, p)
	}
}

func TestDecodeLink_CompactURLFormat(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{
		"url": "85.137.95.32:56000?obf-profile=rtpopus&obf-key=deadbeef&transport=tcp&n=8",
	})
	link := LinkScheme + base64.RawURLEncoding.EncodeToString(raw)
	got, err := DecodeLink(link)
	if err != nil {
		t.Fatalf("DecodeLink: %v", err)
	}
	if got.Peer != "85.137.95.32:56000" {
		t.Fatalf("peer = %q", got.Peer)
	}
	if got.Obf != "rtpopus" || got.Key != "deadbeef" || got.Transport != "tcp" || got.N != 8 {
		t.Fatalf("got %+v", got)
	}
	if got.Provider != "vk" || got.V != 1 {
		t.Fatalf("defaults: %+v", got)
	}
}

func TestHasBundledWgConfig(t *testing.T) {
	if HasBundledWgConfig("") || HasBundledWgConfig("[Interface]\n") {
		t.Fatal("empty/stub must be false")
	}
	if !HasBundledWgConfig("[Interface]\nPrivateKey = abc\n") {
		t.Fatal("real config must be true")
	}
}
