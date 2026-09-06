package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	sysfiles "github.com/hoaxisr/awg-manager/internal/sys/files"
	"github.com/hoaxisr/awg-manager/internal/sys/opkg"
	sysports "github.com/hoaxisr/awg-manager/internal/sys/ports"
	"github.com/hoaxisr/awg-manager/internal/sys/procmon"
	"github.com/hoaxisr/awg-manager/internal/sys/services"
)

// newSystemToolsForTest поднимает хендлер на временных корнях песочницы:
// боевые /opt и /tmp в тестах трогать нельзя, а Sandbox — единственная
// защита между HTTP-ручкой и файловой системой роутера.
func newSystemToolsForTest(t *testing.T, level string) (*SystemToolsHandler, string) {
	t.Helper()
	dataDir := t.TempDir()
	store := storage.NewSettingsStore(dataDir)
	st, err := store.Load()
	if err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	st.UsageLevel = level
	if err := store.Update(func(cur *storage.Settings) error { *cur = *st; return nil }); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	root := t.TempDir()
	roRoot := filepath.Join(root, "bin")
	if err := os.MkdirAll(roRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &SystemToolsHandler{
		settings: store,
		files: sysfiles.NewSandbox([]sysfiles.Root{
			{Path: roRoot, Label: "bin", ReadOnly: true},
			{Path: root, Label: "root"},
		}),
		services: &services.Scanner{InitDir: t.TempDir()},
		opkg:     opkg.NewClient(),
		ports:    sysports.NewScanner(),
		procmon:  procmon.NewSampler(),
	}
	return h, root
}

// withToolsProbe подключает к хендлеру шину-пробу и спай журнала приложения.
func withToolsProbe(t *testing.T, h *SystemToolsHandler) (*busProbe, *appLogSpy) {
	t.Helper()
	p := newBusProbe(t)
	h.SetEventBus(p.bus())
	spy := &appLogSpy{}
	h.log = logging.NewScopedLogger(spy, "system", "tools") // литералы прод-конструктора NewSystemToolsHandler
	return p, spy
}

func postJSON(t *testing.T, fn http.HandlerFunc, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

// Гейт usage level стоит один раз на регистрации маршрута — проверяем обе
// стороны: не-expert получает 403 и НЕ доходит до хендлера.
func TestExpertOnly_GatesNonExpert(t *testing.T) {
	h, _ := newSystemToolsForTest(t, storage.UsageLevelAdvanced)
	called := false
	guarded := h.ExpertOnly(func(http.ResponseWriter, *http.Request) { called = true })

	rec := httptest.NewRecorder()
	guarded(rec, httptest.NewRequest(http.MethodGet, "/system/files/roots", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if called {
		t.Fatal("хендлер выполнился, несмотря на гейт")
	}
}

func TestExpertOnly_PassesExpert(t *testing.T) {
	h, _ := newSystemToolsForTest(t, storage.UsageLevelExpert)
	called := false
	guarded := h.ExpertOnly(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	guarded(rec, httptest.NewRequest(http.MethodGet, "/system/files/roots", nil))

	if !called || rec.Code != http.StatusOK {
		t.Fatalf("expert не пропущен: called=%v status=%d", called, rec.Code)
	}
}

// Запись и чтение внутри корня, плюс отказ на путь снаружи: это контракт,
// ради которого песочница существует.
func TestFilesWriteRead_InsideSandbox(t *testing.T) {
	h, root := newSystemToolsForTest(t, storage.UsageLevelExpert)
	target := filepath.Join(root, "note.txt")

	rec := postJSON(t, h.FilesWrite, "/system/files/write", map[string]any{"path": target, "content": "hello"})
	if rec.Code != http.StatusOK {
		t.Fatalf("write status = %d, body=%s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/system/files/read?path="+target, nil)
	readRec := httptest.NewRecorder()
	h.FilesRead(readRec, req)
	if readRec.Code != http.StatusOK {
		t.Fatalf("read status = %d, body=%s", readRec.Code, readRec.Body.String())
	}
	if !strings.Contains(readRec.Body.String(), "hello") {
		t.Fatalf("содержимое не вернулось: %s", readRec.Body.String())
	}
}

func TestFilesRead_OutsideSandboxDenied(t *testing.T) {
	h, _ := newSystemToolsForTest(t, storage.UsageLevelExpert)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("root:x"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/system/files/read?path="+outside, nil)
	rec := httptest.NewRecorder()
	h.FilesRead(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestFilesWrite_ReadOnlyRootDenied(t *testing.T) {
	h, root := newSystemToolsForTest(t, storage.UsageLevelExpert)
	target := filepath.Join(root, "bin", "payload")

	rec := postJSON(t, h.FilesWrite, "/system/files/write", map[string]any{"path": target, "content": "x"})
	if rec.Code != http.StatusForbidden || decodeJSONBody(t, rec)["code"] != "FORBIDDEN" {
		t.Fatalf("запись в read-only корень: код=%d тело=%s, ждали 403 FORBIDDEN", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("файл создан в read-only корне")
	}
}

// chmod: лишняя старшая цифра режима не должна давать ничего сверх rwx.
// Оговорка про Go: os.Chmod принимает fs.FileMode, где 04000 — не setuid
// (у setuid в FileMode свой бит 1<<23), поэтому «4755» и без маски в
// syscall не превратилось бы в setuid. Маска в files.Chmod остаётся
// страховкой на случай перехода на syscall.Chmod, а тест закрепляет
// наблюдаемый контракт: 4755 -> 0755, никаких особых битов.
func TestFilesChmod_ExtraModeDigitIgnored(t *testing.T) {
	h, root := newSystemToolsForTest(t, storage.UsageLevelExpert)
	target := filepath.Join(root, "tool")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := postJSON(t, h.FilesChmod, "/system/files/chmod", map[string]any{"path": target, "mode": "4755"})
	if rec.Code != http.StatusOK {
		t.Fatalf("chmod status = %d, body=%s", rec.Code, rec.Body.String())
	}

	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("выставлены особые биты: mode=%v", fi.Mode())
	}
	if perm := fi.Mode().Perm(); perm != 0o755 {
		t.Fatalf("perm = %o, want 755", perm)
	}
}

// Режим «0» когда-то ломался обрезкой ведущего нуля (TrimPrefix давал пустую
// строку и запрос падал) — снятие всех прав должно применяться.
func TestFilesChmod_ZeroMode(t *testing.T) {
	h, root := newSystemToolsForTest(t, storage.UsageLevelExpert)
	target := filepath.Join(root, "locked")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o644) })

	rec := postJSON(t, h.FilesChmod, "/system/files/chmod", map[string]any{"path": target, "mode": "0"})
	if rec.Code != http.StatusOK {
		t.Fatalf("chmod status = %d, body=%s", rec.Code, rec.Body.String())
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0 {
		t.Fatalf("perm = %o, want 000", perm)
	}
}

// Каждая мутирующая файловая ручка ходит через Sandbox, а не в ОС напрямую.
// Мутант «os.Remove(req.Path)» проходил: Remove/Rename/Copy/Mkdir/Upload/
// Download не исполнялись ни одним тестом, песочница проверялась только на
// Read/Write.
func TestFiles_OutsideSandboxDenied(t *testing.T) {
	h, _ := newSystemToolsForTest(t, "expert")
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		rec  *httptest.ResponseRecorder
	}{
		{"remove", postJSON(t, h.FilesRemove, "/system/files/remove", map[string]any{"path": victim})},
		{"mkdir", postJSON(t, h.FilesMkdir, "/system/files/mkdir", map[string]any{"path": filepath.Join(outside, "new")})},
		{"rename", postJSON(t, h.FilesRename, "/system/files/rename", map[string]any{"from": victim, "to": victim + ".moved"})},
		{"copy", postJSON(t, h.FilesCopy, "/system/files/copy", map[string]any{"from": victim, "to": victim + ".copy"})},
	}
	for _, c := range cases {
		if c.rec.Code != http.StatusForbidden {
			t.Errorf("%s вне песочницы: код %d, тело %s", c.name, c.rec.Code, c.rec.Body.String())
		}
	}
	dl := httptest.NewRecorder()
	h.FilesDownload(dl, httptest.NewRequest(http.MethodGet, "/system/files/download?path="+victim, nil))
	if dl.Code != http.StatusForbidden {
		t.Errorf("download вне песочницы: код %d", dl.Code)
	}

	if _, err := os.Stat(victim); err != nil {
		t.Fatal("файл вне песочницы тронут:", err)
	}
	for _, p := range []string{victim + ".moved", victim + ".copy", filepath.Join(outside, "new")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("вне песочницы появилось %s", p)
		}
	}
}

func TestFiles_ReadOnlyRootAndRootItselfProtected(t *testing.T) {
	h, root := newSystemToolsForTest(t, "expert")
	ro := filepath.Join(root, "bin", "tool")
	if err := os.WriteFile(ro, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// read-only корень — 403 FORBIDDEN: ResolveWrite оборачивает ErrPathDenied
	if rec := postJSON(t, h.FilesRemove, "/system/files/remove", map[string]any{"path": ro}); rec.Code != http.StatusForbidden {
		t.Errorf("remove в read-only корне: код %d, ждали 403", rec.Code)
	}
	if _, err := os.Stat(ro); err != nil {
		t.Fatal("файл read-only корня удалён")
	}
	rec := postJSON(t, h.FilesRemove, "/system/files/remove", map[string]any{"path": root})
	if rec.Code == http.StatusOK {
		t.Fatal("удаление корня песочницы прошло")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("корень песочницы удалён")
	}
}

func multipartUpload(t *testing.T, dir, filename, content string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("path", dir); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/system/files/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// Имя файла в upload не может увести за каталог: filepath.Base в ручке и
// проверка "/" и ".." в Sandbox.SaveUpload — обе стороны пинуются одним
// тестом: файл создаётся ТОЛЬКО внутри указанного каталога.
func TestFilesUpload_StaysInsideTargetDir(t *testing.T) {
	h, root := newSystemToolsForTest(t, "expert")
	target := filepath.Join(root, "up")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.FilesUpload(rec, multipartUpload(t, target, "../escaped.txt", "x"))
	if _, err := os.Stat(filepath.Join(root, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatal("upload вышел из каталога назначения")
	}
	if rec.Code == http.StatusOK {
		if _, err := os.Stat(filepath.Join(target, "escaped.txt")); err != nil {
			t.Fatalf("200, но файла в каталоге назначения нет: %v", err)
		}
	}

	outside := t.TempDir()
	rec = httptest.NewRecorder()
	h.FilesUpload(rec, multipartUpload(t, outside, "ok.txt", "x"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("upload в каталог вне песочницы: код %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(outside, "ok.txt")); !os.IsNotExist(err) {
		t.Fatal("файл лёг вне песочницы")
	}
}

func TestFiles_RenameAndCopyWithinRoot(t *testing.T) {
	h, root := newSystemToolsForTest(t, "expert")
	src := filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := postJSON(t, h.FilesCopy, "/system/files/copy", map[string]any{"from": src, "to": filepath.Join(root, "b.txt")}); rec.Code != http.StatusOK {
		t.Fatalf("copy: код %d, тело %s", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(got) != "payload" {
		t.Fatalf("копия: %q", got)
	}
	if rec := postJSON(t, h.FilesRename, "/system/files/rename", map[string]any{"from": src, "to": filepath.Join(root, "c.txt")}); rec.Code != http.StatusOK {
		t.Fatalf("rename: код %d, тело %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("источник после rename жив")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "c.txt")); string(got) != "payload" {
		t.Fatalf("после rename: %q", got)
	}
}

// Исполнение чего-либо вне разрешённых корней — это ровно тот RCE, который
// закрывали в первом круге ревью.
func TestFilesScriptAction_OutsideSandboxDenied(t *testing.T) {
	h, _ := newSystemToolsForTest(t, storage.UsageLevelExpert)

	rec := postJSON(t, h.FilesScriptAction, "/system/files/script-action",
		map[string]any{"path": "/bin/sh", "action": "run"})

	if rec.Code == http.StatusOK {
		t.Fatalf("запуск вне песочницы разрешён: %s", rec.Body.String())
	}
}

// Аргументы командной строки в ручку не принимаются: иначе "/bin/sh -c …"
// возвращается через параметр.
func TestScriptActionRequest_HasNoArgs(t *testing.T) {
	raw := []byte(`{"path":"/tmp/x.sh","action":"run","args":["-c","id"]}`)
	var req scriptActionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	// Компилируемая часть контракта: поля Args в структуре нет, а лишний
	// ключ JSON молча игнорируется — в exec он попасть не может.
	if req.Path != "/tmp/x.sh" || req.Action != "run" {
		t.Fatalf("разбор тела сломан: %+v", req)
	}
}

func TestOpkgInstall_RejectsFlagLikeName(t *testing.T) {
	h, _ := newSystemToolsForTest(t, storage.UsageLevelExpert)

	rec := postJSON(t, h.OpkgInstall, "/system/opkg/install",
		map[string]any{"packages": []string{"--force-reinstall"}})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid package name") {
		t.Fatalf("отказ не от валидации имени: %s", rec.Body.String())
	}
}

func TestPortsKill_RefusesInit(t *testing.T) {
	h, _ := newSystemToolsForTest(t, storage.UsageLevelExpert)

	rec := postJSON(t, h.PortsKill, "/system/ports/kill", map[string]any{"pid": 1, "signal": "SIGKILL"})

	if rec.Code == http.StatusOK {
		t.Fatalf("kill PID 1 разрешён: %s", rec.Body.String())
	}
}

func TestFilesList_WrongMethod(t *testing.T) {
	h, _ := newSystemToolsForTest(t, storage.UsageLevelExpert)

	req := httptest.NewRequest(http.MethodPost, "/system/files/list", nil)
	rec := httptest.NewRecorder()
	h.FilesList(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// Отказ по managed-службе — это 403, а не 400: фронт и swagger различают их,
// и файл при отказе обязан остаться на месте.
func TestServicesToggleEnable_ManagedIsForbidden(t *testing.T) {
	h, _ := newSystemToolsForTest(t, "expert")
	dir := t.TempDir()
	h.services = &services.Scanner{InitDir: dir}
	script := filepath.Join(dir, "S99dropbear")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := postJSON(t, h.ServicesToggleEnable, "/api/system/services/toggle-enable",
		map[string]any{"script": script, "enabled": false})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("script must stay enabled: %v", err)
	}
}

// Пропущенное поле enabled не должно читаться как "выключить".
func TestServicesToggleEnable_RequiresEnabledFlag(t *testing.T) {
	h, _ := newSystemToolsForTest(t, "expert")
	dir := t.TempDir()
	h.services = &services.Scanner{InitDir: dir}
	script := filepath.Join(dir, "S90myservice")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := postJSON(t, h.ServicesToggleEnable, "/api/system/services/toggle-enable",
		map[string]any{"script": script})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("script must stay enabled: %v", err)
	}
}
