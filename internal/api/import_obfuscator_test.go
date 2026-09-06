package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// phobosConfFixture — клиентский .conf панели Phobos (копия фикстуры
// internal/obfuscator: тот пакет её не экспортирует).
const phobosConfFixture = `[Interface]
PrivateKey = MBrnZoTdyT/LR4XpB7tElSxyVTQdXFw0tvVJOMSL/GI=
Address = 10.8.0.4/32, fdcc:ad94:bacf:61a4::cafe:4/128
MTU = 1420

[Peer]
PublicKey = g/G4y2XkTY5mPLMYYXXCarvyxUSHUzM1vpIYRHwwFT4=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 127.0.0.1:13255

[instance]
source-if = 127.0.0.1
source-lport = 13255
target = 130.49.185.136:51824
key = XR0NEf8MhGAGcCpc
masking = STUN
obfuscate-bytes = 16
max-dummy = 45
idle-timeout = 300
verbose = INFO
media-ssrc = 1
`

func importObfHarness(t *testing.T) (*ImportHandler, *importStubSvc, *appLogSpy) {
	t.Helper()
	svc := &importStubSvc{imported: &service.TunnelWithStatus{ID: "awg20", Name: "imported"}}
	spy := &appLogSpy{}
	return NewImportHandler(svc, storage.NewAWGTunnelStore(t.TempDir()), spy), svc, spy
}

func postImport(t *testing.T, h *ImportHandler, req ImportConfRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ImportConf(rec, httptest.NewRequest(http.MethodPost, "/api/import/conf", bytes.NewReader(body)))
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return resp.Code
}

// Секция [instance] клиентского .conf — это и есть параметры релея: они
// обязаны уехать в Import отдельным полем, а не быть разобранными как поля
// пира. Незнакомые ключи секции импорт не роняют, но и не молчат.
func TestImportConf_InstanceSectionBecomesPhobosObfuscator(t *testing.T) {
	h, svc, spy := importObfHarness(t)

	postImport(t, h, ImportConfRequest{Content: phobosConfFixture, Name: "ph"})

	o := svc.link.Obfuscator
	if o == nil || o.Flavor != storage.ObfuscatorFlavorPhobos || o.Target != "130.49.185.136:51824" {
		t.Fatalf("link.Obfuscator = %+v", o)
	}
	if o.Key != "XR0NEf8MhGAGcCpc" || o.Masking != "STUN" || o.MaxDummy != 45 || o.IdleTimeout != 300 || o.ObfuscateBytes != 16 {
		t.Fatalf("параметры релея потерялись: %+v", *o)
	}
	// [instance] из контента срезает сам сервис (Import), поэтому здесь
	// проверяется только то, за что отвечает handler.
	found := false
	for _, e := range spy.entries {
		if strings.Contains(e, "media-ssrc") {
			found = true
		}
	}
	if !found {
		t.Errorf("журнал = %v, want Warn о неизвестном ключе media-ssrc", spy.entries)
	}
}

// phobos://-ссылка: имя туннеля берётся из фрагмента, а дописанные
// производителем ссылки `= none` до парсера не доезжают.
func TestImportConf_PhobosLinkDecoded(t *testing.T) {
	h, svc, _ := importObfHarness(t)
	conf := strings.Replace(phobosConfFixture, "MTU = 1420", "MTU = none", 1)
	link := "phobos://" + base64.RawURLEncoding.EncodeToString([]byte(conf)) + "#Mobil%20phone"

	postImport(t, h, ImportConfRequest{Content: link})

	if svc.link.Obfuscator == nil || svc.link.Obfuscator.Target != "130.49.185.136:51824" {
		t.Fatalf("link.Obfuscator = %+v", svc.link.Obfuscator)
	}
	if svc.name != "Mobil phone" {
		t.Errorf("имя = %q, want из фрагмента ссылки", svc.name)
	}
	if strings.Contains(svc.content, "none") {
		t.Errorf("`= none` доехал до парсера: %q", svc.content)
	}
	if strings.HasPrefix(svc.content, "phobos://") {
		t.Error("ссылка не декодирована")
	}
}

// Ручной ввод: разновидность по умолчанию — ClusterM, masking приводится
// к верхнему регистру (в конфиге релея он только такой).
func TestImportConf_ManualClusterM(t *testing.T) {
	h, svc, _ := importObfHarness(t)
	conf := "[Interface]\nPrivateKey = MBrnZoTdyT/LR4XpB7tElSxyVTQdXFw0tvVJOMSL/GI=\nAddress = 10.8.0.4/32\n" +
		"\n[Peer]\nPublicKey = g/G4y2XkTY5mPLMYYXXCarvyxUSHUzM1vpIYRHwwFT4=\nAllowedIPs = 0.0.0.0/0\nEndpoint = 1.2.3.4:51820\n"

	rec := postImport(t, h, ImportConfRequest{Content: conf, Name: "manual", Obfuscator: &ObfuscatorImportRequest{
		Target: "130.49.185.136:51824", Key: "k", Masking: "stun", MaxDummy: 45,
	}})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	o := svc.link.Obfuscator
	if o == nil || o.Flavor != storage.ObfuscatorFlavorClusterM || o.Masking != "STUN" {
		t.Fatalf("link.Obfuscator = %+v", o)
	}
}

// Ручные поля поверх конфига Phobos — это два разных релея в одной записи;
// молча выбрать один нельзя.
func TestImportConf_ManualWithInstanceRejected(t *testing.T) {
	h, svc, _ := importObfHarness(t)

	rec := postImport(t, h, ImportConfRequest{Content: phobosConfFixture, Name: "ph", Obfuscator: &ObfuscatorImportRequest{
		Target: "1.2.3.4:5", Key: "k", Masking: "AUTO",
	}})

	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "OBFUSCATOR_CONFLICT" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.link.Obfuscator != nil {
		t.Error("импорт не должен был дойти до сервиса")
	}
}

// Ручные поля проверяются до сервиса: битый target не имеет права дойти до
// записи туннеля.
func TestImportConf_ManualInvalidRejected(t *testing.T) {
	h, _, _ := importObfHarness(t)

	rec := postImport(t, h, ImportConfRequest{Content: "[Interface]", Name: "x", Obfuscator: &ObfuscatorImportRequest{
		Target: "127.0.0.1:51824", Key: "k", Masking: "AUTO",
	}})

	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "OBFUSCATOR_INVALID" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// Ссылка установки: контента в теле нет вовсе, .conf роутер качает сам.
func TestImportConf_InstallURLFetched(t *testing.T) {
	h, svc, _ := importObfHarness(t)
	pkg := packageTarGz(t, map[string]string{
		"phobos-router/wg-obfuscator.conf": "[main]\n",
		"phobos-router/router.conf":        phobosConfFixture,
	})
	// NewTLSServer: панель Phobos под самоподписанным сертификатом — загрузка
	// его не проверяет (Q14).
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/package.tar.gz") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(pkg)
	}))
	defer srv.Close()

	postImport(t, h, ImportConfRequest{Name: "ph", InstallURL: srv.URL + "/api/install/tok"})

	if svc.link.Obfuscator == nil || svc.link.Obfuscator.Target != "130.49.185.136:51824" {
		t.Fatalf("link.Obfuscator = %+v", svc.link.Obfuscator)
	}
	if !strings.Contains(svc.content, "PrivateKey") {
		t.Errorf(".conf из пакета не доехал до сервиса: %q", svc.content)
	}
}

func TestImportConf_InstallURLUnreachable(t *testing.T) {
	h, _, _ := importObfHarness(t)

	rec := postImport(t, h, ImportConfRequest{Name: "ph", InstallURL: "https://p.example/clients"})

	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "INSTALL_LINK_FAILED" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestImportConf_PhobosLinkInvalid(t *testing.T) {
	h, _, _ := importObfHarness(t)

	rec := postImport(t, h, ImportConfRequest{Content: "phobos://v2.abc#x"})

	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "PHOBOS_LINK_INVALID" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// Пустое тело без install-ссылки — по-прежнему MISSING_CONTENT.
func TestImportConf_MissingContentWithoutInstallURL(t *testing.T) {
	h, _, _ := importObfHarness(t)

	rec := postImport(t, h, ImportConfRequest{Name: "ph"})

	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "MISSING_CONTENT" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// Замена конфига связанного клиента (wdtt/freeturn) идёт через ReplaceConfig,
// который параметров релея не принимает: обфускатор в таком запросе потерялся
// бы молча. Отказ вместо тихой потери.
func TestImportConf_ObfuscatorWithLinkedClientRejected(t *testing.T) {
	svc := &importStubSvc{imported: &service.TunnelWithStatus{ID: "awg20", Name: "imported"}}
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{ID: "awg21", Name: "linked", WdttClientID: "default"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := NewImportHandler(svc, store, &appLogSpy{})

	rec := postImport(t, h, ImportConfRequest{
		Content: "[Interface]", Name: "x", WdttClientID: "default",
		Obfuscator: &ObfuscatorImportRequest{Target: "130.49.185.136:51824", Key: "k", Masking: "AUTO"},
	})

	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "OBFUSCATOR_CONFLICT" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.replaceCalls != 0 {
		t.Errorf("ReplaceConfig вызван %d раз — обфускатор потерялся бы молча", svc.replaceCalls)
	}
}

func packageTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}
