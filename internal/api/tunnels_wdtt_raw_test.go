package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// TestGet_WdttRawCard пинит контракт карточки зеркальной записи: GET одного
// туннеля обязан идти по raw-ветке и отдавать состояние САМОЙ записи —
// наложения живых полей из старого движка больше нет, запись и есть источник.
func TestGet_WdttRawCard(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	// Ни одно поле фикстуры не совпадает с дефолтом ветки: Enabled=false дал
	// бы "stopped" сам собой, пустой RawKernelIface — подстановку id, а
	// заданный ConnectivityCheck отличает чтение записи от дефолта "http".
	if err := store.Create(&storage.AWGTunnel{
		ID:                "wdttraw-de",
		Name:              "Германия",
		Backend:           backendWdttRaw,
		WdttClientID:      "de",
		Enabled:           true,
		RawKernelIface:    "opkgtun18",
		RawNdmsIface:      "OpkgTun18",
		ConnectivityCheck: &storage.ConnectivityCheckConfig{Method: "ping"},
	}); err != nil {
		t.Fatal(err)
	}

	h := &TunnelsHandler{store: store}
	w := httptest.NewRecorder()
	h.Get(w, httptest.NewRequest(http.MethodGet, "/api/tunnels/get?id=wdttraw-de", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp struct {
		Data struct {
			Backend           string `json:"backend"`
			State             string `json:"state"`
			Enabled           bool   `json:"enabled"`
			InterfaceName     string `json:"interfaceName"`
			NDMSName          string `json:"ndmsName"`
			ConnectivityCheck struct {
				Method string `json:"method"`
			} `json:"connectivityCheck"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Backend != backendWdttRaw {
		t.Fatalf("backend = %q, want %q", resp.Data.Backend, backendWdttRaw)
	}
	if resp.Data.State != "running" || !resp.Data.Enabled {
		t.Fatalf("state = %q, enabled = %v", resp.Data.State, resp.Data.Enabled)
	}
	if resp.Data.InterfaceName != "opkgtun18" || resp.Data.NDMSName != "OpkgTun18" {
		t.Fatalf("ifaces = %q / %q", resp.Data.InterfaceName, resp.Data.NDMSName)
	}
	if resp.Data.ConnectivityCheck.Method != "ping" {
		t.Fatalf("connectivityCheck.method = %q, want ping", resp.Data.ConnectivityCheck.Method)
	}
}

// TestWdttRawConnectivityCheck_Default — запись без проверки связности
// получает тот же дефолт, что кладёт в новую запись зеркало прокси-рантайма.
func TestWdttRawConnectivityCheck_Default(t *testing.T) {
	got := wdttRawConnectivityCheck(&storage.AWGTunnel{ID: "wdttraw-de"})
	if got == nil || got.Method != "http" {
		t.Fatalf("default = %+v, want method http", got)
	}
}

// rawUpdateStore — store с одной зеркальной записью для PATCH-ветки Update.
func rawUpdateStore(t *testing.T) *storage.AWGTunnelStore {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID:             "wdttraw-de",
		Name:           "Германия",
		Backend:        backendWdttRaw,
		WdttClientID:   "de",
		RawKernelIface: "opkgtun18",
		// DefaultRouteSet=true явно: без компаньона миграция стора (awg_store.go:151)
		// сама включила бы DefaultRoute, и «маршрут не изменился» после отказа
		// проверялось бы значением, которое чтение навязывает в любом случае.
		DefaultRoute:      true,
		DefaultRouteSet:   true,
		ConnectivityCheck: &storage.ConnectivityCheckConfig{Method: "http"},
		// Фикстура измерения отличается от всего, что кладёт PATCH ниже:
		// совпадение значений не различило бы сохранение и бездействие.
		PingCheck: &storage.TunnelPingCheck{Enabled: false, Method: "http", Target: "8.8.8.8"},
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func updateRaw(t *testing.T, store *storage.AWGTunnelStore, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := &TunnelsHandler{store: store}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tunnels/update?id=wdttraw-de", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.Update(w, req)
	return w
}

// Требование 7: имя зеркальной записи — производная конфига инстанса, зеркало
// перезапишет его на ближайшем объявлении. PATCH обязан отказать, а не
// подтвердить переименование, которое молча откатится.
func TestUpdate_WdttRawRenameRejected(t *testing.T) {
	store := rawUpdateStore(t)
	w := updateRaw(t, store, `{"name":"Нидерланды","connectivityCheck":{"method":"ping"}}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != "WDTT_RAW_NAME_READONLY" {
		t.Fatalf("code = %q, want WDTT_RAW_NAME_READONLY", resp.Code)
	}

	stored, err := store.Get("wdttraw-de")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "Германия" {
		t.Fatalf("имя в сторе = %q, ожидали неизменное «Германия»", stored.Name)
	}
	// Отказ fail-closed: запись не сохранялась вовсе, а не «имя откатили».
	if stored.ConnectivityCheck == nil || stored.ConnectivityCheck.Method != "http" {
		t.Fatalf("connectivityCheck = %+v, ожидали нетронутый http", stored.ConnectivityCheck)
	}
}

// Тот же PATCH, что шлёт форма редактирования: имя пришло неизменным. Отказ
// по одному лишь факту непустого name запер бы правку связности целиком.
func TestUpdate_WdttRawSameNameSavesConnectivityCheck(t *testing.T) {
	store := rawUpdateStore(t)
	w := updateRaw(t, store, `{"name":"Германия","connectivityCheck":{"method":"ping"}}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	stored, err := store.Get("wdttraw-de")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConnectivityCheck == nil || stored.ConnectivityCheck.Method != "ping" {
		t.Fatalf("connectivityCheck = %+v, ожидали method ping", stored.ConnectivityCheck)
	}
	if stored.Name != "Германия" {
		t.Fatalf("имя в сторе = %q", stored.Name)
	}
}

// Измерение зеркальной записи разрешено (запрещено только автолечение), значит
// PATCH с pingCheck обязан сохраниться и пережить перечитывание. Заодно это
// проверка на ложный отказ: defaultRoute в теле не пришёл, то есть в структуру
// разобрался нулевым (false) при существующем true, — без гарда по
// DefaultRouteSet ветка отказала бы на поле, которого пользователь не касался.
func TestUpdate_WdttRawSavesPingCheck(t *testing.T) {
	store := rawUpdateStore(t)
	w := updateRaw(t, store, `{"pingCheck":{"enabled":true,"method":"icmp","target":"1.1.1.1","interval":30}}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	stored, err := store.Get("wdttraw-de")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PingCheck == nil {
		t.Fatal("pingCheck пропал")
	}
	if !stored.PingCheck.Enabled || stored.PingCheck.Method != "icmp" || stored.PingCheck.Target != "1.1.1.1" || stored.PingCheck.Interval != 30 {
		t.Fatalf("pingCheck = %+v, ожидали enabled icmp 1.1.1.1/30", stored.PingCheck)
	}
	if !stored.DefaultRoute {
		t.Fatal("маршрут по умолчанию затёрт нулевым значением непришедшего поля")
	}
}

// Маршрутом raw-выхода распоряжается прокси-рантайм: явная присылка (компаньон
// DefaultRouteSet) с другим значением — отказ, а не молчаливая потеря.
func TestUpdate_WdttRawDefaultRouteRejected(t *testing.T) {
	store := rawUpdateStore(t)
	// connectivityCheck в том же теле: отказ обязан быть fail-closed, то есть
	// не сохранять ВООБЩЕ ничего, а не «маршрут откатить».
	w := updateRaw(t, store, `{"defaultRoute":false,"defaultRouteSet":true,"connectivityCheck":{"method":"ping"}}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != "WDTT_RAW_ROUTING_READONLY" {
		t.Fatalf("code = %q, want WDTT_RAW_ROUTING_READONLY", resp.Code)
	}
	stored, err := store.Get("wdttraw-de")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.DefaultRoute {
		t.Fatal("маршрут в сторе изменён, ожидали неизменный true")
	}
	if stored.ConnectivityCheck == nil || stored.ConnectivityCheck.Method != "http" {
		t.Fatalf("connectivityCheck = %+v, ожидали нетронутый http", stored.ConnectivityCheck)
	}
}

// WAN-подключением raw-выхода тоже распоряжается прокси-рантайм. Ровно это тело
// шлёт выпадающий список страницы туннеля (updateIspInterface).
func TestUpdate_WdttRawISPInterfaceRejected(t *testing.T) {
	store := rawUpdateStore(t)
	w := updateRaw(t, store, `{"ispInterface":"ISP2","ispInterfaceLabel":"Резервный","connectivityCheck":{"method":"ping"}}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != "WDTT_RAW_WAN_READONLY" {
		t.Fatalf("code = %q, want WDTT_RAW_WAN_READONLY", resp.Code)
	}
	stored, err := store.Get("wdttraw-de")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ISPInterface != "" {
		t.Fatalf("ispInterface в сторе = %q, ожидали пустой", stored.ISPInterface)
	}
	if stored.ConnectivityCheck == nil || stored.ConnectivityCheck.Method != "http" {
		t.Fatalf("connectivityCheck = %+v, ожидали нетронутый http", stored.ConnectivityCheck)
	}
}

// Ложный отказ: "auto" — это способ прислать пустое значение, а у зеркальной
// записи WAN и так пустой. Отказ на нём запер бы соседние настройки.
func TestUpdate_WdttRawISPAutoIsNotAChange(t *testing.T) {
	store := rawUpdateStore(t)
	w := updateRaw(t, store, `{"ispInterface":"auto","connectivityCheck":{"method":"ping"}}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	stored, err := store.Get("wdttraw-de")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConnectivityCheck == nil || stored.ConnectivityCheck.Method != "ping" {
		t.Fatalf("connectivityCheck = %+v, ожидали method ping", stored.ConnectivityCheck)
	}
}

// Частичный PATCH формы связности: ни маршрута, ни WAN, ни имени в теле нет.
// Оба гарда обязаны промолчать, иначе правка связности станет недоступна.
func TestUpdate_WdttRawPartialConnectivityOnly(t *testing.T) {
	store := rawUpdateStore(t)
	w := updateRaw(t, store, `{"connectivityCheck":{"method":"ping"}}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	stored, err := store.Get("wdttraw-de")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConnectivityCheck == nil || stored.ConnectivityCheck.Method != "ping" {
		t.Fatalf("connectivityCheck = %+v, ожидали method ping", stored.ConnectivityCheck)
	}
	if stored.PingCheck == nil || stored.PingCheck.Method != "http" || stored.PingCheck.Enabled {
		t.Fatalf("pingCheck = %+v, ожидали нетронутую фикстуру", stored.PingCheck)
	}
}

// ── удаление зеркальной записи (амендмент F2) ────────────────────

// rawDeleteEnv — хранилище туннелей с одной зеркальной записью и хранилище
// прокси-инстансов с перечисленными id клиентов WDTT. Записи прокси кладутся
// через ПРОД-store: состав, который он выдать не может, тест бы не поймал.
//
// PingCheck в фикстуре не украшение: именно эти пользовательские настройки
// теряются молча, когда запись сносят под живым инстансом, а зеркало
// пересоздаёт её с дефолтами.
func rawDeleteEnv(t *testing.T, clientIDs ...string) (*TunnelsHandler, *storage.AWGTunnelStore) {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID:             "wdttraw-de",
		Name:           "Германия",
		Backend:        backendWdttRaw,
		WdttClientID:   "de",
		RawKernelIface: "opkgtun18",
		PingCheck:      &storage.TunnelPingCheck{Enabled: true, Method: "icmp", Target: "1.1.1.1"},
	}); err != nil {
		t.Fatal(err)
	}
	proxy := instancestore.New(t.TempDir())
	if _, err := proxy.Replace(func(st *instancestore.State) error {
		// Клиент FreeTurn с тем же id, что у владельца: id уникален только
		// ВНУТРИ роли, и сверка без роли приняла бы его за владельца
		// зеркальной записи WDTT.
		st.Records = append(st.Records, instancestore.Record{
			ID: "de", Kind: instancestore.KindFreeTurnClient, Name: "FT",
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9007"}})
		for _, id := range clientIDs {
			st.Records = append(st.Records, instancestore.Record{
				ID: id, Kind: instancestore.KindWdttClient, Name: "WD",
				WdttClient: &roles.WdttClientConfig{Listen: "127.0.0.1:9100"}})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := &TunnelsHandler{store: store}
	h.SetProxyRecords(proxy)
	return h, store
}

func deleteRaw(t *testing.T, h *TunnelsHandler) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.Delete(w, httptest.NewRequest(http.MethodPost, "/api/tunnels/delete?id=wdttraw-de", nil))
	return w
}

// Живой инстанс — отказ, а не молчаливое удаление: запись воскреснет на
// ближайшем объявлении с дефолтами, и настройки карточки пропадут.
func TestDelete_WdttRawRefusedWhileInstanceAlive(t *testing.T) {
	h, store := rawDeleteEnv(t, "de")
	w := deleteRaw(t, h)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != "WDTT_RAW_OWNED" {
		t.Fatalf("code = %q, want WDTT_RAW_OWNED", resp.Code)
	}
	if !strings.Contains(resp.Message, "wdtt-client:de") {
		t.Fatalf("message = %q: отказ обязан назвать ключ инстанса", resp.Message)
	}
	stored, err := store.Get("wdttraw-de")
	if err != nil || stored == nil {
		t.Fatalf("запись обязана остаться на месте: %v", err)
	}
	if stored.PingCheck == nil || !stored.PingCheck.Enabled {
		t.Fatalf("настройки карточки обязаны уцелеть: %+v", stored.PingCheck)
	}
}

// Обратная половина гейта: инстанса нет — запись осиротела, и удаление обязано
// работать. Без этого «починкой» F2 был бы вечный отказ.
func TestDelete_WdttRawOrphanRemoved(t *testing.T) {
	// В хранилище прокси есть ДРУГОЙ клиент: пустой store не отличил бы
	// сверку владельца от «не нашли ничего никогда».
	h, store := rawDeleteEnv(t, "nl")
	w := deleteRaw(t, h)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if store.Exists("wdttraw-de") {
		t.Fatal("осиротевшая запись обязана удаляться")
	}
}

// Владение не проверено — тоже отказ (fail-closed): и когда хранилище не
// читается, и когда его вовсе не подключили. Второй случай прод-проводка
// исключает, но nil-гард, открывающий гейт, сделал бы регресс проводки
// невидимым.
func TestDelete_WdttRawRefusedWhenOwnerUnverifiable(t *testing.T) {
	cases := map[string]ProxyRecordLister{
		"хранилище не читается":   failingProxyRecords{err: errors.New("битый json")},
		"хранилище не подключено": nil,
	}
	for name, records := range cases {
		t.Run(name, func(t *testing.T) {
			h, store := rawDeleteEnv(t, "de")
			h.SetProxyRecords(records)
			w := deleteRaw(t, h)

			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body: %s", w.Code, w.Body.String())
			}
			var resp struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Code != "WDTT_RAW_OWNER_UNKNOWN" {
				t.Fatalf("code = %q, want WDTT_RAW_OWNER_UNKNOWN", resp.Code)
			}
			if !store.Exists("wdttraw-de") {
				t.Fatal("непроверенное владение обязано оставлять запись на месте")
			}
		})
	}
}
