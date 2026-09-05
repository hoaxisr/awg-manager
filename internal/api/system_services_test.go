package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newServicesForTest — SystemToolsHandler с init.d в TempDir: боевой
// /opt/etc/init.d в тестах не трогаем, а RunAction/DeleteScript ИСПОЛНЯЮТ
// найденный скрипт, поэтому корень обязан быть подставным.
func newServicesForTest(t *testing.T) *SystemToolsHandler {
	t.Helper()
	h, _ := newSystemToolsForTest(t, storage.UsageLevelExpert)
	h.services.InitDir = t.TempDir()
	return h
}

// actionData вытаскивает data из конверта успеха ServicesAction.
func actionData(t *testing.T, body []byte) SystemServiceActionData {
	t.Helper()
	var env struct {
		Success bool                    `json:"success"`
		Data    SystemServiceActionData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("разбор ответа: %v body=%s", err, body)
	}
	if !env.Success {
		t.Fatalf("success=false: %s", body)
	}
	return env.Data
}

// Контракт фронта: неудачное действие над службой — это 200 с ok:false, а не
// 4xx. Вкладка «Службы» показывает текст ошибки рядом со службой; на 4xx она
// вместо этого рисует общий отказ страницы.
func TestServicesAction_FailureIsOKFalseNot4xx(t *testing.T) {
	cases := []struct {
		name    string
		body    map[string]any
		wantErr string
	}{
		{"нет скрипта", map[string]any{"script": "S99nope", "action": "restart"}, "not found"},
		{"действие вне белого списка", map[string]any{"script": "S99nope", "action": "dance"}, "unsupported action"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newServicesForTest(t)

			rec := postJSON(t, h.ServicesAction, "/system/services/action", tc.body)

			if rec.Code != http.StatusOK {
				t.Fatalf("код=%d, ожидался 200 (тело=%s)", rec.Code, rec.Body.String())
			}
			data := actionData(t, rec.Body.Bytes())
			if data.OK {
				t.Fatalf("ok=true при отказе: %s", rec.Body.String())
			}
			if !strings.Contains(data.Error, tc.wantErr) {
				t.Fatalf("error=%q, ожидалось вхождение %q", data.Error, tc.wantErr)
			}
		})
	}
}

// Сохранение обязано реально положить файл в init.d: ответ «ok» без записи —
// ровно тот отказ, который пользователь заметит только после перезагрузки.
func TestServicesSaveScript_WritesFile(t *testing.T) {
	h := newServicesForTest(t)

	rec := postJSON(t, h.ServicesSaveScript, "/system/services/save",
		map[string]any{"scriptName": "S50test", "content": "#!/bin/sh\nexit 0\n"})

	if rec.Code != http.StatusOK {
		t.Fatalf("код=%d, ожидался 200 (тело=%s)", rec.Code, rec.Body.String())
	}
	full := filepath.Join(h.services.InitDir, "S50test")
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("скрипт не сохранён: %v", err)
	}
	if string(data) != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("содержимое = %q", data)
	}
}

// Удаление снимает файл; включённый (S-) скрипт перед этим останавливается (RunAction
// "stop"), поэтому телом кладём заведомо безобидный exit 0.
func TestServicesDeleteScript_RemovesFile(t *testing.T) {
	h := newServicesForTest(t)

	if rec := postJSON(t, h.ServicesSaveScript, "/system/services/save",
		map[string]any{"scriptName": "S50test", "content": "#!/bin/sh\nexit 0\n"}); rec.Code != http.StatusOK {
		t.Fatalf("подготовка: код=%d тело=%s", rec.Code, rec.Body.String())
	}

	rec := postJSON(t, h.ServicesDeleteScript, "/system/services/delete",
		map[string]any{"script": "S50test"})

	if rec.Code != http.StatusOK {
		t.Fatalf("код=%d, ожидался 200 (тело=%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(h.services.InitDir, "S50test")); !os.IsNotExist(err) {
		t.Fatalf("файл остался на месте: err=%v", err)
	}
}

// Пустое тело скрипта — отказ валидации, а не запись пустого файла: пустой
// исполняемый Sxx молча ломает автозапуск службы.
func TestServicesSaveScript_EmptyContentRejected(t *testing.T) {
	h := newServicesForTest(t)

	rec := postJSON(t, h.ServicesSaveScript, "/system/services/save",
		map[string]any{"scriptName": "S50test", "content": ""})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("код=%d, ожидался 400 (тело=%s)", rec.Code, rec.Body.String())
	}
	if body := decodeJSONBody(t, rec); body["code"] != "INVALID_PARAMS" {
		t.Fatalf("code=%v, ожидался INVALID_PARAMS (тело=%s)", body["code"], rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(h.services.InitDir, "S50test")); !os.IsNotExist(err) {
		t.Fatalf("файл создан вопреки отказу: err=%v", err)
	}
}

// Выключенный (K-) скрипт при удалении НЕ исполняется: раньше «удалить» означало
// «выполнить stop у произвольного файла из init.d». Маркер-файл в теле скрипта — улика запуска.
func TestServicesDeleteScript_DisabledScriptIsNotExecuted(t *testing.T) {
	h := newServicesForTest(t)
	marker := filepath.Join(t.TempDir(), "ran")
	if rec := postJSON(t, h.ServicesSaveScript, "/system/services/save",
		map[string]any{"scriptName": "K50test", "content": "#!/bin/sh\ntouch " + marker + "\nexit 0\n"}); rec.Code != http.StatusOK {
		t.Fatalf("подготовка: код=%d тело=%s", rec.Code, rec.Body.String())
	}
	if rec := postJSON(t, h.ServicesDeleteScript, "/system/services/delete",
		map[string]any{"script": "K50test"}); rec.Code != http.StatusOK {
		t.Fatalf("код=%d тело=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(h.services.InitDir, "K50test")); !os.IsNotExist(err) {
		t.Fatalf("файл остался: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("K-скрипт был исполнен при удалении")
	}
}

// Отказ RunAction — Warn и событие с ok:"false"; событие «выполнено» НЕ публикуется.
func TestServicesAction_FailureEmitsWarnNotSuccess(t *testing.T) {
	h := newServicesForTest(t)
	p, spy := withToolsProbe(t, h)
	rec := postJSON(t, h.ServicesAction, "/system/services/action",
		map[string]any{"script": filepath.Join(h.services.InitDir, "S99missing"), "action": "start"})
	if rec.Code != http.StatusOK || actionData(t, rec.Body.Bytes()).OK {
		t.Fatalf("контракт 200+ok:false нарушен: %d %s", rec.Code, rec.Body.String())
	}
	evs := p.events("system:tool-action")
	if len(evs) != 1 || evs[0]["ok"] != "false" || evs[0]["action"] != "start" || evs[0]["error"] == "" {
		t.Fatalf("события = %v", evs)
	}
	if len(spy.entries) != 1 || !strings.HasPrefix(spy.entries[0], "warn|start|") {
		t.Fatalf("журнал = %v", spy.entries)
	}
}

// Успех — прежнее событие без ok:"false" и Info в журнале. Вывод RunAction проходит
// TrimSpace/cleanStatusText, поэтому в details литерал без перевода строки.
func TestServicesAction_SuccessEmitsInfoEvent(t *testing.T) {
	h := newServicesForTest(t)
	if rec := postJSON(t, h.ServicesSaveScript, "/system/services/save",
		map[string]any{"scriptName": "S50ok", "content": "#!/bin/sh\necho started\n"}); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	// Проба — ПОСЛЕ save: ServicesSaveScript сам публикует emitEvent("save", …).
	p, spy := withToolsProbe(t, h)
	rec := postJSON(t, h.ServicesAction, "/system/services/action",
		map[string]any{"script": filepath.Join(h.services.InitDir, "S50ok"), "action": "start"})
	if !actionData(t, rec.Body.Bytes()).OK {
		t.Fatal(rec.Body.String())
	}
	evs := p.events("system:tool-action")
	if len(evs) != 1 || evs[0]["ok"] != "" || evs[0]["details"] != "started" {
		t.Fatalf("события = %v", evs)
	}
	if len(spy.entries) != 1 || !strings.HasPrefix(spy.entries[0], "info|start|") {
		t.Fatalf("журнал = %v", spy.entries)
	}
}
