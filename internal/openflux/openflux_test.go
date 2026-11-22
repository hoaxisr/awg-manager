package openflux

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestConfigValidation(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultConfig should be valid: %v", err)
	}

	cfg.Secret = "short"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for short secret, got nil")
	}

	cfg.Secret = "1234567890123456"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid secret, got %v", err)
	}

	cfg.Transports = append(cfg.Transports, "direct")
	cfg.DirectListen = "0.0.0.0:22"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for reserved port 22, got nil")
	}

	cfg.DirectListen = "0.0.0.0:8445"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid direct listen, got %v", err)
	}

	cfg.NatMode = "none"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid natMode none, got %v", err)
	}
	cfg.NatMode = "internet-only"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for internet-only without WAN, got nil")
	}
	cfg.NatStaticWANs = []string{"ISP"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid internet-only with WAN, got %v", err)
	}

	// UpstreamProxy only supported in l4
	cfg.Mode = "l3"
	cfg.UpstreamProxy = "127.0.0.1:10808"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for upstreamProxy in l3 mode")
	}
	cfg.Mode = "l4"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid upstreamProxy in l4 mode: %v", err)
	}

	// Lanes bounds
	cfg.Lanes = 99
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for excessive lanes count")
	}
}

func TestBuildArgs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Secret = "supersecretkey12345"
	cfg.Transports = []string{"mailru", "yandex", "direct"}
	cfg.MailruURL = "https://docs.mail.ru/sample"
	cfg.YandexURL = "https://docs.yandex.ru/sample"

	args := cfg.BuildArgs("/tmp/secret.key")
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "--role=exit") {
		t.Errorf("args missing --role=exit: %s", argsStr)
	}
	if !strings.Contains(argsStr, "--mode=l4") {
		t.Errorf("args missing --mode=l4: %s", argsStr)
	}
	if !strings.Contains(argsStr, "--low-memory") {
		t.Errorf("args missing --low-memory: %s", argsStr)
	}
	if !strings.Contains(argsStr, "--negotiate") {
		t.Errorf("args missing --negotiate: %s", argsStr)
	}
	if !strings.Contains(argsStr, "--encryption-key-file=/tmp/secret.key") {
		t.Errorf("args missing --encryption-key-file: %s", argsStr)
	}
	if !strings.Contains(argsStr, "--direct-listen=0.0.0.0:8445") {
		t.Errorf("args missing --direct-listen: %s", argsStr)
	}
	if !strings.Contains(argsStr, "--mailru-url=https://docs.mail.ru/sample") {
		t.Errorf("args missing --mailru-url: %s", argsStr)
	}
	if !strings.Contains(argsStr, "--yandex-url=https://docs.yandex.ru/sample") {
		t.Errorf("args missing --yandex-url: %s", argsStr)
	}
}

func TestUpgradeFlagsAndTransportsBuildArgs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Secret = "supersecretkey12345"
	cfg.Mode = "l4"
	cfg.UpstreamProxy = "socks5://127.0.0.1:10808"
	cfg.LowMemory = true
	cfg.Lanes = 4
	cfg.NativeScriptsDir = "/opt/etc/awg-manager/openflux/scripts"
	cfg.Transports = []string{"boards", "avito", "telemost", "oneme"}
	cfg.BoardsURL = "https://boards.yandex.ru/b/1"
	cfg.AvitoURL = "https://code.avito.ru/room/abc"
	cfg.AvitoCookies = "session=xyz"
	cfg.TelemostURL = "https://telemost.yandex.ru/j/999"
	cfg.OnemeToken = "tok-123"
	cfg.OnemeUID = "42"

	argsStr := strings.Join(cfg.BuildArgs("/tmp/secret.key"), " ")
	for _, want := range []string{
		"--upstream-proxy=socks5://127.0.0.1:10808",
		"--low-memory",
		"--lanes=4",
		"--native-scripts=/opt/etc/awg-manager/openflux/scripts",
		"--avito-cookies=session=xyz",
		"--transports=boards:88,avito:85,telemost:75,oneme:70",
		"--boards-url=https://boards.yandex.ru/b/1",
		"--avito-url=https://code.avito.ru/room/abc",
		"--telemost-url=https://telemost.yandex.ru/j/999",
		"--oneme-token=tok-123",
		"--oneme-uid=42",
	} {
		if !strings.Contains(argsStr, want) {
			t.Errorf("server BuildArgs missing %q in %s", want, argsStr)
		}
	}

	// Single oneme mode uses --maxToken / --maxUid
	singleOneme := DefaultConfig()
	singleOneme.TransportMode = "single"
	singleOneme.Transports = []string{"oneme"}
	singleOneme.OnemeToken = "tok-single"
	singleOneme.OnemeUID = "777"
	singleArgs := strings.Join(singleOneme.BuildArgs(""), " ")
	if !strings.Contains(singleArgs, "--transport=oneme") ||
		!strings.Contains(singleArgs, "--maxToken=tok-single") ||
		!strings.Contains(singleArgs, "--maxUid=777") {
		t.Errorf("single oneme BuildArgs mismatch: %s", singleArgs)
	}

	// Client share round-trip for boards + avito
	link, err := GenerateLink(cfg, "")
	if err != nil {
		t.Fatalf("GenerateLink failed for boards/avito/telemost: %v", err)
	}
	sc, err := Decode(link)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	// oneme must never leak into share links
	for _, tr := range sc.Transports {
		if tr.Type == "oneme" {
			t.Fatalf("oneme must not be included in share links")
		}
	}
	cli := ClientConfigFromShare(sc, DefaultClientConfig())
	if cli.BoardsURL != cfg.BoardsURL {
		t.Errorf("BoardsURL mismatch: got %q, want %q", cli.BoardsURL, cfg.BoardsURL)
	}
	if cli.AvitoURL != cfg.AvitoURL {
		t.Errorf("AvitoURL mismatch: got %q, want %q", cli.AvitoURL, cfg.AvitoURL)
	}
	if cli.TelemostURL != cfg.TelemostURL {
		t.Errorf("TelemostURL mismatch: got %q, want %q", cli.TelemostURL, cfg.TelemostURL)
	}
	cli.LowMemory = true
	cli.Lanes = 3
	cli.NativeScriptsDir = "/opt/scripts"
	cli.AvitoCookies = "c=1"
	cliArgs := strings.Join(cli.BuildArgs("/tmp/c.key"), " ")
	for _, want := range []string{
		"--low-memory",
		"--lanes=3",
		"--native-scripts=/opt/scripts",
		"--avito-cookies=c=1",
		"--boards-url=https://boards.yandex.ru/b/1",
		"--avito-url=https://code.avito.ru/room/abc",
		"--telemost-url=https://telemost.yandex.ru/j/999",
	} {
		if !strings.Contains(cliArgs, want) {
			t.Errorf("client BuildArgs missing %q in %s", want, cliArgs)
		}
	}
}

func TestServerRoutingAndEgressArgs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Secret = "supersecretkey12345"
	cfg.EgressInterface = "192.168.1.50"

	args := cfg.BuildArgs("/tmp/secret.key")
	argsStr := strings.Join(args, " ")
	if !strings.Contains(argsStr, "--local-ip=192.168.1.50") {
		t.Errorf("expected --local-ip=192.168.1.50, got %s", argsStr)
	}

	cfg2 := DefaultConfig()
	cfg2.Secret = "supersecretkey12345"
	cfg2.NatMode = "internet-only"
	cfg2.NatStaticWANs = []string{"192.168.1.100"}
	args2 := cfg2.BuildArgs("/tmp/secret.key")
	args2Str := strings.Join(args2, " ")
	if !strings.Contains(args2Str, "--local-ip=192.168.1.100") {
		t.Errorf("expected --local-ip=192.168.1.100 from NatStaticWANs, got %s", args2Str)
	}
}

func TestEncodeAndDecodeLink(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Name = "HomeTest"
	cfg.Secret = "supersecretkey12345"
	cfg.MailruURL = "https://docs.mail.ru/sample"

	link, err := GenerateLink(cfg, "192.168.90.1")
	if err != nil {
		t.Fatalf("GenerateLink failed: %v", err)
	}

	if !strings.HasPrefix(link, Prefix) {
		t.Fatalf("link does not start with %s: %s", Prefix, link)
	}

	body := strings.TrimPrefix(link, Prefix)
	compressed, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}

	r := flate.NewReader(bytes.NewReader(compressed))
	decompressed, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("flate read failed: %v", err)
	}

	var parsed ShareConfig
	if err := json.Unmarshal(decompressed, &parsed); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}

	if parsed.Name != "HomeTest" {
		t.Errorf("expected name HomeTest, got %s", parsed.Name)
	}
	if parsed.Secret != "supersecretkey12345" {
		t.Errorf("expected secret match, got %s", parsed.Secret)
	}
	if !parsed.Negotiate {
		t.Errorf("expected Negotiate=true")
	}
	if len(parsed.Transports) == 0 {
		t.Errorf("expected at least one transport")
	}
	if parsed.Codec != "" {
		t.Errorf("expected empty codec for batched default, got %s", parsed.Codec)
	}

	cfgLegacy := cfg
	cfgLegacy.Codec = "legacy"
	linkLegacy, err := GenerateLink(cfgLegacy, "192.168.90.1")
	if err != nil {
		t.Fatalf("GenerateLink with legacy codec failed: %v", err)
	}
	bodyLegacy := strings.TrimPrefix(linkLegacy, Prefix)
	compressedLegacy, _ := base64.RawURLEncoding.DecodeString(bodyLegacy)
	rLegacy := flate.NewReader(bytes.NewReader(compressedLegacy))
	decompLegacy, _ := io.ReadAll(rLegacy)
	var parsedLegacy ShareConfig
	_ = json.Unmarshal(decompLegacy, &parsedLegacy)
	if parsedLegacy.Codec != "legacy" {
		t.Errorf("expected codec legacy, got %s", parsedLegacy.Codec)
	}

	cfgSingle := DefaultConfig()
	cfgSingle.TransportMode = "single"
	cfgSingle.Transports = []string{"mailru"}
	cfgSingle.MailruURL = "https://docs.mail.ru/single"
	linkSingle, err := GenerateLink(cfgSingle, "192.168.90.1")
	if err != nil {
		t.Fatalf("GenerateLink single mode failed: %v", err)
	}
	bodySingle := strings.TrimPrefix(linkSingle, Prefix)
	compressedSingle, _ := base64.RawURLEncoding.DecodeString(bodySingle)
	rSingle := flate.NewReader(bytes.NewReader(compressedSingle))
	decompSingle, _ := io.ReadAll(rSingle)
	var parsedSingle ShareConfig
	_ = json.Unmarshal(decompSingle, &parsedSingle)
	if parsedSingle.Negotiate {
		t.Errorf("expected Negotiate=false for single transport")
	}
	if len(parsedSingle.Transports) != 1 || parsedSingle.Transports[0].Type != "mailru" {
		t.Errorf("expected 1 mailru transport in single mode, got %+v", parsedSingle.Transports)
	}

	cfgEmpty := DefaultConfig()
	cfgEmpty.Transports = nil
	if _, err := GenerateLink(cfgEmpty, ""); err == nil {
		t.Errorf("expected error for empty transports, got nil")
	}
}

func TestNormalizeArch(t *testing.T) {
	if got := NormalizeArch("aarch64"); got != "aarch64-3.10" {
		t.Errorf("NormalizeArch(aarch64) = %s, want aarch64-3.10", got)
	}
	if got := NormalizeArch("arm64"); got != "aarch64-3.10" {
		t.Errorf("NormalizeArch(arm64) = %s, want aarch64-3.10", got)
	}
	if got := NormalizeArch("amd64"); got != "x86_64" {
		t.Errorf("NormalizeArch(amd64) = %s, want x86_64", got)
	}
	if !IsArchSupported("aarch64") || !IsArchSupported("x86_64") || !IsArchSupported("mipsel-3.4") || !IsArchSupported("mips-3.4") || !IsArchSupported("arm") {
		t.Errorf("expected all 5 pinned architectures supported")
	}
}

func TestEnvBinaryOverrideAndInstallSHA(t *testing.T) {
	tmpDir := t.TempDir()
	customBin := filepath.Join(tmpDir, "custom-openflux")
	if err := os.WriteFile(customBin, []byte("#!/bin/sh\necho "+PinnedVersion+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWGM_OPENFLUX_BIN", customBin)
	svc := New(tmpDir, "x86_64", nil)
	if !svc.IsInstalled() {
		t.Fatalf("expected IsInstalled=true when AWGM_OPENFLUX_BIN exists")
	}
	if svc.binPath != customBin {
		t.Fatalf("expected binPath=%q, got %q", customBin, svc.binPath)
	}

	// Test SHA-256 verification on Install
	payload := []byte("fake-openflux-binary")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	instDir := t.TempDir()
	t.Setenv("AWGM_OPENFLUX_BIN", "")
	svc2 := New(instDir, "x86_64", nil)
	svc2.spec = BinarySpec{
		URL:     srv.URL + "/openflux-linux-x86_64",
		SHA256:  "0000000000000000000000000000000000000000000000000000000000000000",
		Version: PinnedVersion,
	}
	svc2.freeDisk = nil
	if err := svc2.Install(context.Background()); err == nil {
		t.Fatalf("expected SHA-256 mismatch error")
	}

	svc2.spec.SHA256 = hex.EncodeToString(sum[:])
	if err := svc2.Install(context.Background()); err != nil {
		t.Fatalf("Install failed with matching SHA-256: %v", err)
	}
	if !svc2.IsInstalled() {
		t.Fatalf("expected installed after Install")
	}
}

func TestDecodeLinkAndClientConfigFromShare(t *testing.T) {
	srvCfg := DefaultConfig()
	srvCfg.Name = "KeeneticDacha"
	srvCfg.Secret = "supersecretkey12345"
	srvCfg.Transports = []string{"mailru", "yandex", "direct"}
	srvCfg.MailruURL = "https://docs.mail.ru/sample"
	srvCfg.YandexURL = "https://docs.yandex.ru/sample"

	link, err := GenerateLink(srvCfg, "203.0.113.10")
	if err != nil {
		t.Fatalf("GenerateLink failed: %v", err)
	}

	sc, err := Decode("  \n" + link + "  \t")
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if sc.Name != "KeeneticDacha" {
		t.Errorf("expected Name KeeneticDacha, got %q", sc.Name)
	}
	if !sc.Negotiate {
		t.Errorf("expected Negotiate=true")
	}
	if sc.Secret != "supersecretkey12345" {
		t.Errorf("expected Secret match, got %q", sc.Secret)
	}

	base := DefaultClientConfig()
	base.InboundMode = "socks5"
	base.Socks5Listen = "127.0.0.1:1088"
	cliCfg := ClientConfigFromShare(sc, base)

	if cliCfg.Name != "KeeneticDacha" {
		t.Errorf("expected cliCfg.Name KeeneticDacha, got %q", cliCfg.Name)
	}
	if cliCfg.Socks5Listen != "127.0.0.1:1088" {
		t.Errorf("expected preserved Socks5Listen 127.0.0.1:1088, got %q", cliCfg.Socks5Listen)
	}
	if cliCfg.DirectDial != "203.0.113.10:8445" {
		t.Errorf("expected DirectDial 203.0.113.10:8445, got %q", cliCfg.DirectDial)
	}
	if cliCfg.MailruURL != "https://docs.mail.ru/sample" {
		t.Errorf("expected MailruURL, got %q", cliCfg.MailruURL)
	}
	if cliCfg.YandexURL != "https://docs.yandex.ru/sample" {
		t.Errorf("expected YandexURL, got %q", cliCfg.YandexURL)
	}
	if err := cliCfg.ValidateForStart(); err != nil {
		t.Errorf("expected converted ClientConfig to be valid for start: %v", err)
	}

	args := cliCfg.BuildArgs("/tmp/client.key")
	argsStr := strings.Join(args, " ")
	for _, expected := range []string{
		"--role=client",
		"--inbound=socks5",
		"--socks5=127.0.0.1:1088",
		"--negotiate",
		"--encryption-key-file=/tmp/client.key",
		"--transports=mailru:100,yandex:90,direct:50",
		"--direct-dial=203.0.113.10:8445",
		"--mailru-url=https://docs.mail.ru/sample",
		"--yandex-url=https://docs.yandex.ru/sample",
	} {
		if !strings.Contains(argsStr, expected) {
			t.Errorf("client BuildArgs missing %q in %s", expected, argsStr)
		}
	}

	tunBase := DefaultClientConfig()
	tunBase.InboundMode = "tun"
	tunBase.TunIface = "opkgtun2"
	tunBase.TunAddr = "10.10.10.2/24"
	tunBase.TunMTU = 1280
	tunCfg := ClientConfigFromShare(sc, tunBase)
	tunArgs := tunCfg.BuildArgs("/tmp/client.key")
	tunArgsStr := strings.Join(tunArgs, " ")
	for _, expected := range []string{
		"--inbound=tun",
		"--tun-name=opkgtun2",
		"--tun-addr=10.10.10.2/24",
		"--tun-mtu=1280",
		"--transports=mailru:100,yandex:90,direct:50",
	} {
		if !strings.Contains(tunArgsStr, expected) {
			t.Errorf("expected TUN client args to have %q, got: %s", expected, tunArgsStr)
		}
	}
}

func TestClientConfigValidationEdgeCases(t *testing.T) {
	cfg := DefaultClientConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("stopped DefaultClientConfig should pass Validate(): %v", err)
	}
	if err := cfg.ValidateForStart(); err == nil {
		t.Fatalf("expected error from ValidateForStart() on empty default config")
	}

	for _, reservedPort := range []string{"127.0.0.1:1099", "127.0.0.1:8443", "127.0.0.1:9090"} {
		cfg.Socks5Listen = reservedPort
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected error for reserved port %s", reservedPort)
		}
	}
	cfg.Socks5Listen = "127.0.0.1:1085"

	cfg.HttpProxyListen = "127.0.0.1:1085"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error when Socks5Listen and HttpProxyListen use same port")
	}
	cfg.HttpProxyListen = "127.0.0.1:1086"

	telemostCfg := DefaultClientConfig()
	telemostCfg.TransportMode = "session"
	telemostCfg.Transports = []string{"telemost"}
	telemostCfg.TelemostURL = "https://telemost.yandex.ru/j/1234567890"
	telemostCfg.Secret = "1234567890123456"
	if err := telemostCfg.ValidateForStart(); err != nil {
		t.Fatalf("expected telemostCfg to pass ValidateForStart: %v", err)
	}
	telemostArgs := strings.Join(telemostCfg.BuildArgs("/tmp/client.key"), " ")
	if !strings.Contains(telemostArgs, "--transport=telemost") || !strings.Contains(telemostArgs, "--url=https://telemost.yandex.ru/j/1234567890") {
		t.Fatalf("expected telemost single-transport fallback args, got: %s", telemostArgs)
	}

	for _, bad := range []string{
		"",
		"http://example.com",
		"OPENFLUX://v1/abc",
		"openflux://v2/abc",
		"openflux://v1/!!!not_base64!!!",
	} {
		if _, err := Decode(bad); err == nil {
			t.Errorf("expected Decode(%q) to fail, got nil", bad)
		}
	}
}

func TestClientServiceConfigPersistenceAndStatus(t *testing.T) {
	ctx := t.Context()
	tmpDir := t.TempDir()
	svc := New(tmpDir, "aarch64", func(_ context.Context) string {
		return "198.51.100.7"
	})

	st := svc.GetStatus(ctx)
	if st.Client == nil {
		t.Fatalf("expected st.Client to be non-nil")
	}
	if st.Client.Socks5Port != DefaultClientSocks5Port {
		t.Fatalf("expected default Socks5Port %d, got %d", DefaultClientSocks5Port, st.Client.Socks5Port)
	}

	srvCfg := DefaultConfig()
	srvCfg.Name = "RouterExit"
	srvCfg.Secret = "0123456789abcdef0123456789abcdef"
	srvCfg.Transports = []string{"mailru", "yandex", "direct"}
	srvCfg.MailruURL = "https://docs.mail.ru/d/testdoc"
	if err := svc.SaveConfig(ctx, srvCfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	st = svc.GetStatus(ctx)
	if st.Link == "" {
		t.Fatalf("expected non-empty server link")
	}

	share, parsedClientCfg, err := svc.ParseClientLink(st.Link)
	if err != nil {
		t.Fatalf("ParseClientLink failed: %v", err)
	}
	if share.Name != "RouterExit" {
		t.Fatalf("expected share.Name RouterExit, got %q", share.Name)
	}
	if parsedClientCfg.DirectDial != "198.51.100.7:8445" {
		t.Fatalf("expected DirectDial 198.51.100.7:8445, got %q", parsedClientCfg.DirectDial)
	}

	parsedClientCfg.Socks5Listen = "127.0.0.1:1089"
	parsedClientCfg.HttpProxyListen = "127.0.0.1:1090"
	if err := svc.SaveClientConfig(ctx, parsedClientCfg); err != nil {
		t.Fatalf("SaveClientConfig failed: %v", err)
	}

	svc2 := New(tmpDir, "aarch64", nil)
	cliSt := svc2.GetClientStatus()
	if cliSt.Socks5Port != 1089 {
		t.Fatalf("expected reloaded Socks5Port 1089, got %d", cliSt.Socks5Port)
	}
	if cliSt.Config == nil || cliSt.Config.Name != "RouterExit" {
		t.Fatalf("expected reloaded client config Name RouterExit, got %+v", cliSt.Config)
	}
	if cliSt.HttpURI != "http://127.0.0.1:1090" {
		t.Fatalf("expected HttpURI http://127.0.0.1:1090, got %q", cliSt.HttpURI)
	}

	if err := svc2.DeleteClient(ctx); err != nil {
		t.Fatalf("DeleteClient failed: %v", err)
	}
	delSt := svc2.GetClientStatus()
	if delSt.Config != nil {
		t.Fatalf("expected nil Config after DeleteClient, got %+v", delSt.Config)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "openflux", "client_config.json")); !os.IsNotExist(err) {
		t.Fatalf("expected client_config.json to be deleted, got err: %v", err)
	}
}

type mockRouterCmds struct {
	allocatedNdms string
	allocatedTun  string
	createdNdms   string
	createdDesc   string
	createdMtu    int
	createdAddr   string
	downNdms      string
	delNdms       string
	permittedPol  string
	deniedPol     string
	createErr     error
	permitErr     error
}

func (m *mockRouterCmds) AllocateTunInterface(ctx context.Context, preferred string) (string, string, error) {
	if m.allocatedNdms != "" {
		return m.allocatedNdms, m.allocatedTun, nil
	}
	ndms, tun := AutoDiscoverFreeTun(preferred)
	return ndms, tun, nil
}

func (m *mockRouterCmds) CreateInterface(ctx context.Context, ndmsName, desc string, mtu int, ipWithMask string, global bool, defaultRoute bool) error {
	m.createdNdms = ndmsName
	m.createdDesc = desc
	m.createdMtu = mtu
	m.createdAddr = ipWithMask
	if m.createErr != nil {
		return m.createErr
	}
	return nil
}

func (m *mockRouterCmds) DownInterface(ctx context.Context, ndmsName string) error {
	m.downNdms = ndmsName
	return nil
}

func (m *mockRouterCmds) DeleteInterface(ctx context.Context, ndmsName string) error {
	m.delNdms = ndmsName
	return nil
}

func (m *mockRouterCmds) PermitPolicy(ctx context.Context, policy, iface string) error {
	m.permittedPol = policy
	if m.permitErr != nil {
		return m.permitErr
	}
	return nil
}

func (m *mockRouterCmds) DenyPolicy(ctx context.Context, policy, iface string) error {
	m.deniedPol = policy
	return nil
}

func TestAutoDiscoverFreeTunAndTunAllocation(t *testing.T) {
	ctx := context.Background()

	ndms, tun := AutoDiscoverFreeTun("")
	if !strings.HasPrefix(ndms, "OpkgTun") || !strings.HasPrefix(tun, "opkgtun") {
		t.Fatalf("unexpected AutoDiscoverFreeTun output: %s, %s", ndms, tun)
	}
	if ndms == "OpkgTun0" || ndms == "OpkgTun1" {
		t.Fatalf("AutoDiscoverFreeTun allocated forbidden router mode index: %s", ndms)
	}

	ndms0, tun0 := AutoDiscoverFreeTun("OpkgTun0")
	if ndms0 == "OpkgTun0" || tun0 == "opkgtun0" {
		t.Fatalf("AutoDiscoverFreeTun must reject preferred OpkgTun0, got %s, %s", ndms0, tun0)
	}
	ndms1, tun1 := AutoDiscoverFreeTun("OpkgTun1")
	if ndms1 == "OpkgTun1" || tun1 == "opkgtun1" {
		t.Fatalf("AutoDiscoverFreeTun must reject preferred OpkgTun1, got %s, %s", ndms1, tun1)
	}

	ndms22, tun22 := AutoDiscoverFreeTun("OpkgTun22")
	if ndms22 != "OpkgTun22" || tun22 != "opkgtun22" {
		t.Fatalf("expected OpkgTun22/opkgtun22 for free preferred, got %s, %s", ndms22, tun22)
	}

	tmpDir := t.TempDir()
	svc := New(tmpDir, "aarch64", nil)

	cfg := DefaultClientConfig()
	cfg.InboundMode = "tun"
	cfg.NdmsIface = ""
	cfg.TunIface = ""
	cfg.Policy = "Policy0"

	if err := svc.SaveClientConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveClientConfig failed: %v", err)
	}

	st := svc.GetClientStatus()
	if st.NdmsIface == "" || !strings.HasPrefix(st.NdmsIface, "OpkgTun") || st.NdmsIface == "OpkgTun0" || st.NdmsIface == "OpkgTun1" {
		t.Fatalf("expected valid non-router auto-allocated NdmsIface, got %q", st.NdmsIface)
	}
	if st.TunIface == "" || !strings.HasPrefix(st.TunIface, "opkgtun") || st.TunIface == "opkgtun0" || st.TunIface == "opkgtun1" {
		t.Fatalf("expected valid non-router auto-allocated TunIface, got %q", st.TunIface)
	}
	if st.Policy != "Policy0" {
		t.Fatalf("expected Policy0, got %q", st.Policy)
	}

	mock := &mockRouterCmds{
		allocatedNdms: "OpkgTun7",
		allocatedTun:  "opkgtun7",
	}
	svc.SetRouterCommands(mock)

	cfg2 := DefaultClientConfig()
	cfg2.InboundMode = "tun"
	cfg2.NdmsIface = ""
	cfg2.TunIface = ""
	cfg2.Policy = "Policy1"

	if err := svc.SaveClientConfig(ctx, cfg2); err != nil {
		t.Fatalf("SaveClientConfig with mockRouterCmds failed: %v", err)
	}

	if mock.deniedPol != "Policy0" {
		t.Fatalf("expected old policy Policy0 to be denied on change, got %q", mock.deniedPol)
	}

	st2 := svc.GetClientStatus()
	if st2.NdmsIface != "OpkgTun7" || st2.TunIface != "opkgtun7" || st2.Policy != "Policy1" {
		t.Fatalf("unexpected status: %+v", st2)
	}

	cfg3 := cfg2
	cfg3.InboundMode = "socks5"
	cfg3.Policy = ""
	if err := svc.SaveClientConfig(ctx, cfg3); err != nil {
		t.Fatalf("SaveClientConfig with socks5 failed: %v", err)
	}
	if mock.deniedPol != "Policy1" || mock.downNdms != "OpkgTun7" {
		t.Fatalf("expected Policy1 denied & OpkgTun7 down, got pol=%q down=%q", mock.deniedPol, mock.downNdms)
	}

	mock.downNdms = ""
	mock.delNdms = ""
	mock.allocatedNdms = "OpkgTun7"
	mock.allocatedTun = "opkgtun7"
	cfg4 := cfg2
	cfg4.InboundMode = "tun"
	cfg4.NdmsIface = "OpkgTun7"
	cfg4.TunIface = "opkgtun7"
	if err := svc.SaveClientConfig(ctx, cfg4); err != nil {
		t.Fatalf("SaveClientConfig restore failed: %v", err)
	}

	mock.downNdms = ""
	mock.delNdms = ""
	mock.allocatedNdms = "OpkgTun14"
	mock.allocatedTun = "opkgtun14"
	cfg5 := cfg4
	cfg5.NdmsIface = ""
	cfg5.TunIface = ""
	if err := svc.SaveClientConfig(ctx, cfg5); err != nil {
		t.Fatalf("SaveClientConfig with new iface failed: %v", err)
	}
	if mock.downNdms != "OpkgTun7" || mock.delNdms != "OpkgTun7" {
		t.Fatalf("expected old OpkgTun7 down/deleted, got down=%q del=%q", mock.downNdms, mock.delNdms)
	}
}

func TestExtractLastErrorFromLog(t *testing.T) {
	helpLog := `written by p1neappleXpress
flag provided but not defined: -tun-name
OpenFlux — Network stack research tool. TCP tunnel with pluggable transports.

USAGE
  openflux --role=<role> --transport=<type> [OPTIONS]`

	if got := extractLastErrorFromLog(helpLog); got != "flag provided but not defined: -tun-name" {
		t.Errorf("extractLastErrorFromLog(helpLog) = %q", got)
	}

	bindLog := `2026/10/14 07:39:22 [FATAL] listen tcp 127.0.0.1:1085: bind: address already in use`
	if got := extractLastErrorFromLog(bindLog); !strings.Contains(got, "bind: address already in use") {
		t.Errorf("extractLastErrorFromLog(bindLog) = %q", got)
	}
}

func TestParseStaleNatDeleteArgs(t *testing.T) {
	sampleOutput := `-P PREROUTING ACCEPT
-P POSTROUTING ACCEPT
-A POSTROUTING -o nwg0 -m comment --comment "AWGM_OPENFLUX" -j MASQUERADE
-A POSTROUTING -o ppp0 -m comment --comment "AWGM_OPENFLUX" -j MASQUERADE
-A POSTROUTING -o usb0 -m comment --comment "AWGM_OPENFLUX" -j MASQUERADE
-A POSTROUTING -o eth2.2 -m comment --comment "OTHER_RULE" -j MASQUERADE
`
	delArgs := ParseStaleNatDeleteArgs(sampleOutput, []string{"nwg0", "usb0"})
	if len(delArgs) != 1 {
		t.Fatalf("expected 1 delete command, got %d: %v", len(delArgs), delArgs)
	}
	expected := []string{"-t", "nat", "-D", "POSTROUTING", "-o", "ppp0", "-m", "comment", "--comment", "AWGM_OPENFLUX", "-j", "MASQUERADE"}
	if strings.Join(delArgs[0], " ") != strings.Join(expected, " ") {
		t.Errorf("got %v, want %v", delArgs[0], expected)
	}
}

func TestClientTunNDMSRollback(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	binPath := filepath.Join(tmpDir, "openflux")
	if runtime.GOOS == "windows" {
		binPath = filepath.Join(tmpDir, "openflux.exe")
	}
	_ = os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0755)

	svc := &Service{
		openfluxDir:   tmpDir,
		clientCfgPath: filepath.Join(tmpDir, "client_config.json"),
		clientKeyPath: filepath.Join(tmpDir, "client_secret.key"),
		clientPidPath: filepath.Join(tmpDir, "openflux_client.pid"),
		clientLogPath: filepath.Join(tmpDir, "openflux_client.log"),
		binPath:       binPath,
		clientConfig: ClientConfig{
			Enabled:       true,
			InboundMode:   "tun",
			NdmsIface:     "OpkgTun7",
			TunAddr:       "10.10.10.2/24",
			Policy:        "Policy0",
			TransportMode: "session",
			Transports:    []string{"direct"},
			DirectDial:    "1.2.3.4:8445",
			Secret:        "1234567890123456",
		},
	}

	mock := &mockRouterCmds{permitErr: os.ErrPermission}
	svc.routerCmds = mock

	if err := svc.startClientProcess(ctx, true); err == nil {
		t.Fatalf("expected startClientProcess to fail due to permitErr")
	}
	if mock.downNdms != "OpkgTun7" {
		t.Fatalf("expected rollback DownInterface for OpkgTun7, got %q", mock.downNdms)
	}
}

func TestEarlyCrashCancelsAutoReconnectAndWipesKey(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	binPath := filepath.Join(tmpDir, "openflux_failing")
	if runtime.GOOS == "windows" {
		binPath = filepath.Join(tmpDir, "openflux_failing.bat")
		_ = os.WriteFile(binPath, []byte("@echo off\r\necho fatal crash\r\nexit /b 1\r\n"), 0755)
	} else {
		_ = os.WriteFile(binPath, []byte("#!/bin/sh\necho fatal crash\nexit 1\n"), 0755)
	}

	keyPath := filepath.Join(tmpDir, "client_secret.key")
	svc := &Service{
		openfluxDir:   tmpDir,
		clientCfgPath: filepath.Join(tmpDir, "client_config.json"),
		clientKeyPath: keyPath,
		clientPidPath: filepath.Join(tmpDir, "openflux_client.pid"),
		clientLogPath: filepath.Join(tmpDir, "openflux_client.log"),
		binPath:       binPath,
		clientConfig: ClientConfig{
			Enabled:       true,
			AutoReconnect: true,
			InboundMode:   "socks5",
			TransportMode: "session",
			Secret:        "1234567890123456",
		},
	}

	if err := svc.startClientProcess(ctx, true); err == nil {
		t.Fatalf("expected startClientProcess to fail for failing binary")
	}
	svc.mu.Lock()
	stopCh := svc.clientStopCh
	svc.mu.Unlock()
	if stopCh != nil {
		t.Fatalf("expected clientStopCh to be nil after early crash")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("expected key file deleted after crash, got %v", err)
	}
}

type mockTunnelStore struct {
	tunnels map[string]*storage.AWGTunnel
}

func newMockTunnelStore() *mockTunnelStore {
	return &mockTunnelStore{tunnels: make(map[string]*storage.AWGTunnel)}
}

func (m *mockTunnelStore) Get(id string) (*storage.AWGTunnel, error) {
	if t, ok := m.tunnels[id]; ok {
		cp := *t
		return &cp, nil
	}
	return nil, os.ErrNotExist
}

func (m *mockTunnelStore) Exists(id string) bool { _, ok := m.tunnels[id]; return ok }
func (m *mockTunnelStore) Create(t *storage.AWGTunnel) error {
	cp := *t
	m.tunnels[t.ID] = &cp
	return nil
}
func (m *mockTunnelStore) Update(id string, mut func(*storage.AWGTunnel) error) error {
	t, ok := m.tunnels[id]
	if !ok {
		return os.ErrNotExist
	}
	return mut(t)
}
func (m *mockTunnelStore) Delete(id string) error { delete(m.tunnels, id); return nil }

func TestTunnelStoreMirroring(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	svc := New(tmpDir, "aarch64", nil)
	store := newMockTunnelStore()
	svc.SetTunnelStore(store)

	if store.Exists("openflux-client") {
		t.Fatalf("expected store empty initially")
	}

	cfg := DefaultClientConfig()
	cfg.Name = "MyOpenFlux"
	cfg.InboundMode = "tun"
	cfg.NdmsIface = "OpkgTun5"
	cfg.TunIface = "opkgtun5"
	cfg.TunAddr = "10.10.10.2/24"
	cfg.TunMTU = 1300
	cfg.Transports = []string{"mailru", "yandex"}
	cfg.MailruURL = "https://docs.mail.ru/sample"
	cfg.Secret = "1234567890123456"
	cfg.Enabled = true

	if err := svc.SaveClientConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveClientConfig failed: %v", err)
	}
	if !store.Exists("openflux-client") {
		t.Fatalf("expected openflux-client mirror in store")
	}
	tun, err := store.Get("openflux-client")
	if err != nil {
		t.Fatal(err)
	}
	if tun.Backend != "wdtt-raw" || tun.OpenFluxClientID != "default" || !tun.Enabled {
		t.Fatalf("unexpected mirrored tunnel: %+v", tun)
	}

	if err := svc.StopClient(ctx); err != nil {
		t.Fatalf("StopClient failed: %v", err)
	}
	tun, _ = store.Get("openflux-client")
	if tun.Enabled {
		t.Fatalf("expected Enabled=false after StopClient")
	}

	cfgSocks := cfg
	cfgSocks.InboundMode = "socks5"
	cfgSocks.Enabled = false
	if err := svc.SaveClientConfig(ctx, cfgSocks); err != nil {
		t.Fatalf("SaveClientConfig with socks5 failed: %v", err)
	}
	if store.Exists("openflux-client") {
		t.Fatalf("expected mirror removed when switching to socks5")
	}

	cfg.Enabled = false
	if err := svc.SaveClientConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if !store.Exists("openflux-client") {
		t.Fatalf("expected mirror recreated")
	}
	if err := svc.DeleteClient(ctx); err != nil {
		t.Fatal(err)
	}
	if store.Exists("openflux-client") {
		t.Fatalf("expected mirror deleted on DeleteClient")
	}
}

func TestCloudFirstTransportPriorities(t *testing.T) {
	if DefaultTransportPriority("webdav") != 110 {
		t.Errorf("expected webdav=110, got %d", DefaultTransportPriority("webdav"))
	}
	if DefaultTransportPriority("mailru") != 100 {
		t.Errorf("expected mailru=100, got %d", DefaultTransportPriority("mailru"))
	}
	if DefaultTransportPriority("vyandex") != 95 {
		t.Errorf("expected vyandex=95, got %d", DefaultTransportPriority("vyandex"))
	}
	if DefaultTransportPriority("yandex") != 90 {
		t.Errorf("expected yandex=90, got %d", DefaultTransportPriority("yandex"))
	}
	if DefaultTransportPriority("boards") != 88 {
		t.Errorf("expected boards=88, got %d", DefaultTransportPriority("boards"))
	}
	if DefaultTransportPriority("avito") != 85 {
		t.Errorf("expected avito=85, got %d", DefaultTransportPriority("avito"))
	}
	if DefaultTransportPriority("cupsonline") != 80 {
		t.Errorf("expected cupsonline=80, got %d", DefaultTransportPriority("cupsonline"))
	}
	if DefaultTransportPriority("telemost") != 75 {
		t.Errorf("expected telemost=75, got %d", DefaultTransportPriority("telemost"))
	}
	if DefaultTransportPriority("oneme") != 70 {
		t.Errorf("expected oneme=70, got %d", DefaultTransportPriority("oneme"))
	}
	if DefaultTransportPriority("direct") != 50 {
		t.Errorf("expected direct=50, got %d", DefaultTransportPriority("direct"))
	}
}

func TestWebDAVTransportIntegration(t *testing.T) {
	secPass := "superSecretAppPassword999"
	secUser := "testowner@mail.ru"
	embeddedURL := fmt.Sprintf("https://%s:%s@webdav.cloud.mail.ru/openflux/", secUser, secPass)

	sanitizedCfg := DefaultConfig()
	sanitizedCfg.Secret = "1234567890123456"
	sanitizedCfg.Transports = []string{"webdav"}
	sanitizedCfg.WebdavURL = embeddedURL

	sanitizedArgs := strings.Join(sanitizedCfg.BuildArgs(""), " ")
	if strings.Contains(sanitizedArgs, "--session-context="+embeddedURL) {
		t.Fatalf("BuildArgs leaked credentials in session-context: %s", sanitizedArgs)
	}
	if !strings.Contains(sanitizedArgs, "--session-context=https://webdav.cloud.mail.ru/openflux/") {
		t.Fatalf("expected clean session-context: %s", sanitizedArgs)
	}
	if !strings.Contains(sanitizedArgs, "--webdav-user="+secUser) || !strings.Contains(sanitizedArgs, "--webdav-pass="+secPass) {
		t.Fatalf("expected extracted webdav user/pass: %s", sanitizedArgs)
	}

	link, err := GenerateLink(sanitizedCfg, "")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := Decode(link)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sc.Context, secPass) || strings.Contains(sc.Transports[0].URL, secPass) {
		t.Fatalf("credentials leaked into share link: %+v", sc)
	}
}

func TestNormalizeMultiURLAndStripedMailruRoundTrip(t *testing.T) {
	raw := "  https://cloud.mail.ru/public/AAA/111 \n\t https://cloud.mail.ru/public/BBB/222 | https://cloud.mail.ru/public/CCC/333 , "
	got := NormalizeMultiURL(raw)
	want := "https://cloud.mail.ru/public/AAA/111,https://cloud.mail.ru/public/BBB/222,https://cloud.mail.ru/public/CCC/333"
	if got != want {
		t.Fatalf("NormalizeMultiURL = %q, want %q", got, want)
	}

	cfg := DefaultConfig()
	cfg.Secret = "1234567890123456"
	cfg.Transports = []string{"mailru"}
	cfg.MailruURL = raw

	args := strings.Join(cfg.BuildArgs(""), " ")
	if strings.Contains(args, "\n") || strings.Contains(args, "\t") {
		t.Fatalf("BuildArgs contained raw newline/tab whitespace: %q", args)
	}
	if !strings.Contains(args, "--mailru-url="+want) {
		t.Fatalf("expected normalized --mailru-url=%s in args: %s", want, args)
	}
	if !strings.Contains(args, "--session-context="+want) {
		t.Fatalf("expected normalized --session-context=%s in args: %s", want, args)
	}

	link, err := GenerateLink(cfg, "")
	if err != nil {
		t.Fatalf("GenerateLink failed: %v", err)
	}
	sc, err := Decode(link)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	clientCfg := ClientConfigFromShare(sc, DefaultClientConfig())
	if clientCfg.MailruURL != want {
		t.Fatalf("ClientConfigFromShare MailruURL = %q, want %q", clientCfg.MailruURL, want)
	}

	clientArgs := strings.Join(clientCfg.BuildArgs(""), " ")
	if !strings.Contains(clientArgs, "--mailru-url="+want) {
		t.Fatalf("expected normalized client --mailru-url=%s in args: %s", want, clientArgs)
	}
}

func TestMailruCookiesBuildArgsAndShareIsolation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Secret = "1234567890123456"
	cfg.Transports = []string{"mailru"}
	cfg.MailruURL = "https://cloud.mail.ru/public/AAA/111,https://cloud.mail.ru/public/BBB/222"
	cfg.MailruCookies = "Mpop=secretMpopToken; sdc=secretSdcToken"

	args := cfg.BuildArgsWithOptions("/tmp/secret.key", BuildArgsOptions{
		MailruCookiesFile: "/opt/etc/awg-manager/openflux/mailru_cookies.txt",
		CookieStoreFile:   "/opt/etc/awg-manager/openflux/cookies-server.json",
	})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--mailru-cookies=/opt/etc/awg-manager/openflux/mailru_cookies.txt") {
		t.Fatalf("expected --mailru-cookies file arg, got: %s", joined)
	}
	if !strings.Contains(joined, "--cookie-store=/opt/etc/awg-manager/openflux/cookies-server.json") {
		t.Fatalf("expected --cookie-store file arg, got: %s", joined)
	}
	if strings.Contains(joined, "secretMpopToken") || strings.Contains(joined, "secretSdcToken") {
		t.Fatalf("raw cookie values leaked into CLI args when MailruCookiesFile set: %s", joined)
	}

	link, err := GenerateLink(cfg, "203.0.113.1")
	if err != nil {
		t.Fatalf("GenerateLink failed: %v", err)
	}
	if strings.Contains(link, "secretMpopToken") {
		t.Fatalf("share link leaked raw Mail.ru cookie")
	}
	sc, err := Decode(link)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	baseCli := DefaultClientConfig()
	baseCli.MailruCookies = "Mpop=clientExistingMpop"
	cli := ClientConfigFromShare(sc, baseCli)
	if cli.MailruCookies != "Mpop=clientExistingMpop" {
		t.Fatalf("expected ClientConfigFromShare to preserve MailruCookies, got %q", cli.MailruCookies)
	}
	cliArgs := strings.Join(cli.BuildArgsWithOptions("/tmp/cli.key", BuildArgsOptions{
		MailruCookiesFile: "/tmp/client_mailru_cookies.txt",
		CookieStoreFile:   "/tmp/cookies-client.json",
	}), " ")
	if !strings.Contains(cliArgs, "--mailru-cookies=/tmp/client_mailru_cookies.txt") ||
		!strings.Contains(cliArgs, "--cookie-store=/tmp/cookies-client.json") {
		t.Fatalf("unexpected client args: %s", cliArgs)
	}

	tmpDir := t.TempDir()
	svc := New(tmpDir, "x86_64", nil)
	if err := os.WriteFile(svc.cookieStorePath, []byte(`{"old":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgSave := DefaultConfig()
	cfgSave.Enabled = false
	cfgSave.Secret = "1234567890123456"
	cfgSave.Transports = []string{"mailru"}
	cfgSave.MailruURL = "https://cloud.mail.ru/public/AAA/111"
	cfgSave.MailruCookies = "Mpop=freshMpop"
	if err := svc.SaveConfig(context.Background(), cfgSave); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	if _, err := os.Stat(svc.cookieStorePath); !os.IsNotExist(err) {
		t.Fatalf("expected SaveConfig to clear stale cookieStorePath when MailruCookies changes")
	}
}
