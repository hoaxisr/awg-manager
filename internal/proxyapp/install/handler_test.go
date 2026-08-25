package install

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// envelope — конверт response.APIResponse; ручки этого пакета отдают его же.
type envelope struct {
	Success bool            `json:"success"`
	Error   bool            `json:"error"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
	Code    string          `json:"code"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("ответ не разобрался: %v (%s)", err, rec.Body.String())
	}
	return e
}

func TestServeStatus_Form(t *testing.T) {
	s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
	sub := s.subs[SubsystemWdtt]
	setSpecs(s, SubsystemWdtt, ArchSpecs{
		Client: BinarySpec{Version: "1.4.4-awgm", URL: "https://x/client", SHA256: writeBin(t, sub.clientBin, "c")},
		Server: BinarySpec{Version: "1.4.4-awgm", URL: "https://x/server", SHA256: writeBin(t, sub.serverBin, "s")},
	})
	if err := sub.writeInstalledVersion("1.4.4-awgm+server-1.4.4-awgm"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.ServeStatus(rec, httptest.NewRequest(http.MethodGet, "/api/proxyrt/install/status?subsystem=wdtt", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код = %d", rec.Code)
	}
	env := decode(t, rec)
	if !env.Success {
		t.Fatalf("конверт: %+v", env)
	}
	var got InstallStatus
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatal(err)
	}
	want := InstallStatus{
		ServerSupported:  true,
		InstallAvailable: true,
		InstallVersion:   "1.4.4-awgm+server-1.4.4-awgm",
		InstalledVersion: "1.4.4-awgm+server-1.4.4-awgm",
		UpdateAvailable:  false,
		Installing:       false,
		RouterClock:      "2026-08-24 15:04:05 MSK",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("статус:\n got %+v\nwant %+v", got, want)
	}

	// json-имена полей — контракт фронта, проверяются по сырому телу.
	raw := map[string]any{}
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"serverSupported", "installAvailable", "installVersion",
		"installedVersion", "updateAvailable", "installing", "routerClock"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("в теле нет поля %q: %v", key, raw)
		}
	}
}

// Подсистема выбирается параметром: статус freeturn обязан отличаться от
// статуса wdtt на том же сервисе.
func TestServeStatus_SubsystemPicksState(t *testing.T) {
	s := newTestService(t, Deps{Arch: "mipsel-3.4", Downloader: &fakeDownloader{}})
	get := func(sub string) InstallStatus {
		t.Helper()
		rec := httptest.NewRecorder()
		s.ServeStatus(rec, httptest.NewRequest(http.MethodGet, "/api/proxyrt/install/status?subsystem="+sub, nil))
		var st InstallStatus
		if err := json.Unmarshal(decode(t, rec).Data, &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	w, f := get("wdtt"), get("freeturn")
	if w.InstallVersion != WdttPinnedClientVersion {
		t.Errorf("wdtt: installVersion = %q", w.InstallVersion)
	}
	if f.InstallVersion != FreeTurnPinnedVersion {
		t.Errorf("freeturn: installVersion = %q", f.InstallVersion)
	}
	// На mipsel wdtt-сервера в пине нет, а freeturn-сервер есть.
	if w.ServerSupported || !f.ServerSupported {
		t.Errorf("serverSupported: wdtt=%v freeturn=%v", w.ServerSupported, f.ServerSupported)
	}
}

func TestServeStatus_Rejections(t *testing.T) {
	s := newTestService(t, Deps{})
	t.Run("чужой метод", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.ServeStatus(rec, httptest.NewRequest(http.MethodPost, "/api/proxyrt/install/status?subsystem=wdtt", nil))
		if rec.Code != http.StatusMethodNotAllowed || decode(t, rec).Code != "METHOD_NOT_ALLOWED" {
			t.Fatalf("код=%d тело=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("подсистема не названа", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.ServeStatus(rec, httptest.NewRequest(http.MethodGet, "/api/proxyrt/install/status", nil))
		env := decode(t, rec)
		if rec.Code != http.StatusBadRequest || env.Code != "BAD_REQUEST" {
			t.Fatalf("код=%d тело=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(env.Message, "ожидается wdtt или freeturn") {
			t.Fatalf("сообщение: %q", env.Message)
		}
	})
	t.Run("чужая подсистема", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.ServeStatus(rec, httptest.NewRequest(http.MethodGet, "/api/proxyrt/install/status?subsystem=singbox", nil))
		if rec.Code != http.StatusBadRequest || decode(t, rec).Code != "BAD_REQUEST" {
			t.Fatalf("код=%d тело=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestServeInstall_Success(t *testing.T) {
	clientBody, serverBody := []byte("c"), []byte("s")
	dl := &fakeDownloader{payload: map[string][]byte{"https://x/c": clientBody, "https://x/s": serverBody}}
	s := newTestService(t, Deps{Downloader: dl})

	cases := []struct {
		name    Subsystem
		message string
	}{
		{SubsystemWdtt, "installed"},
		{SubsystemFreeTurn, "freeturn installed"},
	}
	for _, c := range cases {
		sub := s.subs[c.name]
		setSpecs(s, c.name, ArchSpecs{
			Client: BinarySpec{Version: "1.0.0", URL: "https://x/c", SHA256: sha256Hex(clientBody), Size: int64(len(clientBody))},
			Server: BinarySpec{Version: "1.0.0", URL: "https://x/s", SHA256: sha256Hex(serverBody), Size: int64(len(serverBody))},
		})
		rec := httptest.NewRecorder()
		body := strings.NewReader(`{"subsystem":"` + string(c.name) + `"}`)
		s.ServeInstall(rec, httptest.NewRequest(http.MethodPost, "/api/proxyrt/install", body))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: код = %d (%s)", c.name, rec.Code, rec.Body.String())
		}
		var data map[string]string
		if err := json.Unmarshal(decode(t, rec).Data, &data); err != nil {
			t.Fatal(err)
		}
		// Тексты успеха у подсистем разные и оставлены прежними.
		if !reflect.DeepEqual(data, map[string]string{"message": c.message}) {
			t.Fatalf("%s: тело успеха = %v", c.name, data)
		}
		if !binaryPresent(sub.clientBin) || !binaryPresent(sub.serverBin) {
			t.Fatalf("%s: бинари не активированы", c.name)
		}
	}
}

func TestServeInstall_Rejections(t *testing.T) {
	t.Run("чужой метод", func(t *testing.T) {
		s := newTestService(t, Deps{})
		rec := httptest.NewRecorder()
		s.ServeInstall(rec, httptest.NewRequest(http.MethodGet, "/api/proxyrt/install", nil))
		if rec.Code != http.StatusMethodNotAllowed || decode(t, rec).Code != "METHOD_NOT_ALLOWED" {
			t.Fatalf("код=%d тело=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("битое тело", func(t *testing.T) {
		s := newTestService(t, Deps{})
		rec := httptest.NewRecorder()
		s.ServeInstall(rec, httptest.NewRequest(http.MethodPost, "/api/proxyrt/install", strings.NewReader("{не json")))
		env := decode(t, rec)
		if rec.Code != http.StatusBadRequest || env.Code != "BAD_REQUEST" || env.Message != "invalid request body" {
			t.Fatalf("код=%d тело=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("чужая подсистема", func(t *testing.T) {
		s := newTestService(t, Deps{Downloader: &fakeDownloader{}})
		rec := httptest.NewRecorder()
		s.ServeInstall(rec, httptest.NewRequest(http.MethodPost, "/api/proxyrt/install", strings.NewReader(`{"subsystem":"singbox"}`)))
		if rec.Code != http.StatusBadRequest || decode(t, rec).Code != "BAD_REQUEST" {
			t.Fatalf("код=%d тело=%s", rec.Code, rec.Body.String())
		}
	})
	// Код отказа установки — свой у каждой подсистемы (вербатим старых ручек
	// /api/wdtt/install и /api/freeturn/install).
	t.Run("отказ установки называет подсистему", func(t *testing.T) {
		s := newTestService(t, Deps{Arch: "неведомая-арка", Downloader: &fakeDownloader{}})
		for name, code := range map[Subsystem]string{
			SubsystemWdtt:     "WDTT_INSTALL_FAILED",
			SubsystemFreeTurn: "FREETURN_INSTALL_FAILED",
		} {
			rec := httptest.NewRecorder()
			body := strings.NewReader(`{"subsystem":"` + string(name) + `"}`)
			s.ServeInstall(rec, httptest.NewRequest(http.MethodPost, "/api/proxyrt/install", body))
			env := decode(t, rec)
			if env.Code != code {
				t.Errorf("%s: код отказа = %q, want %q", name, env.Code, code)
			}
			if !strings.Contains(env.Message, "нет закреплённой сборки "+string(name)) {
				t.Errorf("%s: сообщение = %q", name, env.Message)
			}
		}
	})
}
