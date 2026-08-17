package wdtt

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// newServerClientsService поднимает сервис с одним сервером и созданным
// config-dir: фикстуры passwords.json пишутся туда напрямую.
func newServerClientsService(t *testing.T, mainPassword string) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg := DefaultServerConfig()
	cfg.Password = mainPassword
	if _, err := s.UpdateServerInstance(DefaultInstanceID, cfg); err != nil {
		t.Fatal(err)
	}
	cfgDir, err := s.serverConfigDir(DefaultInstanceID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	return s, cfgDir
}

func serverClientPasswordByComment(t *testing.T, st ServerClientsStatus, comment string) string {
	t.Helper()
	for _, u := range st.Users {
		if u.Comment == comment {
			return u.Password
		}
	}
	t.Fatalf("абонент с именем %q не найден: %+v", comment, st.Users)
	return ""
}

func hasServerClientEntry(st ServerClientsStatus, password string) bool {
	for _, u := range st.Users {
		if u.Password == password {
			return true
		}
	}
	return false
}

func configServerClients(t *testing.T, s *Service) []ServerClient {
	t.Helper()
	inst, err := s.serverInstance(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	return inst.Config.Clients
}

// setServerClients кладёт список абонентов в wdtt.json как есть — так
// воспроизводится состояние существующей установки (пусто) и «все просрочены».
func setServerClients(t *testing.T, s *Service, clients []ServerClient) {
	t.Helper()
	if _, err := s.updateServerClients(DefaultInstanceID, func([]ServerClient) ([]ServerClient, bool) {
		return clients, true
	}); err != nil {
		t.Fatal(err)
	}
}

func serverConfigOf(t *testing.T, s *Service, id string) ServerConfig {
	t.Helper()
	inst, err := s.serverInstance(id)
	if err != nil {
		t.Fatal(err)
	}
	return inst.Config
}

func configHasServerClient(t *testing.T, s *Service, password string) bool {
	t.Helper()
	for _, c := range configServerClients(t, s) {
		if c.Password == password {
			return true
		}
	}
	return false
}

func fileHasServerClient(t *testing.T, cfgDir, password string) bool {
	t.Helper()
	doc, err := loadPasswordsJSON(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	_, ok := doc.Passwords[password]
	return ok
}

func TestListServerClients_MainPasswordIsNotAClient(t *testing.T) {
	main := "mainpass0000000000000000"
	s, _ := newServerClientsService(t, main)
	if _, err := s.AddServerClient(DefaultInstanceID, "", "Иван", "", ""); err != nil {
		t.Fatal(err)
	}
	st, err := s.ListServerClients(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if hasServerClientEntry(st, main) {
		t.Fatalf("главный пароль в списке абонентов: %+v", st.Users)
	}
	if serverClientPasswordByComment(t, st, "Иван") == "" {
		t.Fatalf("абонент пропал: %+v", st.Users)
	}
}

func TestListServerClients_ShowsDeactivatedFromFile(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	if _, err := s.AddServerClient(DefaultInstanceID, "client1", "Иван", "", ""); err != nil {
		t.Fatal(err)
	}
	writePasswordsFixture(t, cfgDir, passwordsJSON{
		MainPassword: "mainpass0000000000000000",
		Passwords:    map[string]passwordsJSONUser{"client1": {IsDeactivated: true}},
	})
	st, err := s.ListServerClients(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Available {
		t.Fatalf("passwords.json прочитан, ожидался available=true: %+v", st)
	}
	for _, u := range st.Users {
		if u.Password == "client1" {
			if !u.IsDeactivated {
				t.Fatalf("признак деактивации не поднят: %+v", u)
			}
			return
		}
	}
	t.Fatalf("абонент потерян: %+v", st.Users)
}

func TestListServerClients_ShowsExpired(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	if _, err := s.AddServerClient(DefaultInstanceID, "client1", "Иван", "", ""); err != nil {
		t.Fatal(err)
	}
	writePasswordsFixture(t, cfgDir, passwordsJSON{
		MainPassword: "mainpass0000000000000000",
		Passwords: map[string]passwordsJSONUser{
			"client1": {ExpiresAt: time.Now().Add(-time.Hour).Unix()},
		},
	})
	st, err := s.ListServerClients(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range st.Users {
		if u.Password == "client1" {
			if !u.IsExpired {
				t.Fatalf("просроченный абонент показан рабочим: %+v", u)
			}
			return
		}
	}
	t.Fatalf("просроченный абонент пропал из списка: %+v", st.Users)
}

// Имя и хеш абонента, которых нет в wdtt.json, берутся из файла: у абонентов
// бота личность живёт только там, и список не должен показывать их безымянными.
func TestMergeServerClients_FallsBackToFileLabelAndVkHash(t *testing.T) {
	st := mergeServerClients(
		[]ServerClient{{Password: "client1"}},
		map[string]passwordsJSONUser{"client1": {Label: "Из бота", VkHash: "vk9"}},
		true,
		time.Unix(1700000000, 0),
	)
	if len(st.Users) != 1 {
		t.Fatalf("список = %+v", st.Users)
	}
	if st.Users[0].Comment != "Из бота" {
		t.Fatalf("имя не подхвачено из label: %+v", st.Users[0])
	}
	if st.Users[0].VkHash != "vk9" {
		t.Fatalf("vk_hash не подхвачен из файла: %+v", st.Users[0])
	}
	// Своё имя сильнее файла: его правит пользователь.
	own := mergeServerClients(
		[]ServerClient{{Password: "client1", Comment: "Иван", VkHash: "vk1"}},
		map[string]passwordsJSONUser{"client1": {Label: "Из бота", VkHash: "vk9"}},
		true,
		time.Unix(1700000000, 0),
	)
	if own.Users[0].Comment != "Иван" || own.Users[0].VkHash != "vk1" {
		t.Fatalf("файл затёр личность из wdtt.json: %+v", own.Users[0])
	}
}

func TestListServerClients_AdoptsUnknownFileEntries(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	expires := time.Now().Add(24 * time.Hour).Unix()
	writePasswordsFixture(t, cfgDir, passwordsJSON{
		MainPassword: "mainpass0000000000000000",
		Passwords: map[string]passwordsJSONUser{
			"legacy1": {Label: "Из бота", VkHash: "vk9", ExpiresAt: expires},
		},
	})
	st, err := s.ListServerClients(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasServerClientEntry(st, "legacy1") {
		t.Fatalf("запись файла не усыновлена в список: %+v", st.Users)
	}
	if got := serverClientPasswordByComment(t, st, "Из бота"); got != "legacy1" {
		t.Fatalf("имя взято не из label: %+v", st.Users)
	}
	for _, c := range configServerClients(t, s) {
		if c.Password != "legacy1" {
			continue
		}
		if c.Comment != "Из бота" || c.VkHash != "vk9" {
			t.Fatalf("личность абонента не перенесена в wdtt.json: %+v", c)
		}
		if c.ExpiresAt != expires {
			t.Fatalf("срок не перенесён в wdtt.json: %d, ожидался %d", c.ExpiresAt, expires)
		}
		return
	}
	t.Fatalf("абонент не попал в wdtt.json: %+v", configServerClients(t, s))
}

func TestListServerClients_SurvivesMissingFile(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	// Абонент кладётся в конфиг напрямую: passwords.json не должно существовать.
	if err := s.putServerClient(DefaultInstanceID, ServerClient{Password: "client1", Comment: "Иван"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(passwordsJSONPath(cfgDir)); !os.IsNotExist(err) {
		t.Fatalf("файла быть не должно: %v", err)
	}
	st, err := s.ListServerClients(DefaultInstanceID)
	if err != nil {
		t.Fatalf("список обязан отдаваться без passwords.json: %v", err)
	}
	if st.Available {
		t.Fatalf("файла нет, ожидался available=false: %+v", st)
	}
	if !hasServerClientEntry(st, "client1") {
		t.Fatalf("список из wdtt.json пуст: %+v", st.Users)
	}
}

// Отсутствие passwords.json — не ошибка: до первого старта сервера файла нет.
// Сторожится здесь, а не только через ListServerClients: та ошибку чтения
// проглатывает в журнал и без этого теста мутант «нет файла = ошибка» выживал.
func TestLoadServerClientEntries_MissingFileIsNotAnError(t *testing.T) {
	entries, available, err := loadServerClientEntries(t.TempDir())
	if err != nil {
		t.Fatalf("отсутствие файла отдано ошибкой: %v", err)
	}
	if available {
		t.Fatal("файла нет, ожидался available=false")
	}
	if len(entries) != 0 {
		t.Fatalf("записи взялись из ниоткуда: %#v", entries)
	}
}

// Битый passwords.json (а не отсутствующий) не должен опустошать список: он
// собирается из wdtt.json, ошибка уходит в журнал.
func TestListServerClients_SurvivesUnreadableFile(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	if err := s.putServerClient(DefaultInstanceID, ServerClient{Password: "client1", Comment: "Иван"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordsJSONPath(cfgDir), []byte("это не json"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := s.ListServerClients(DefaultInstanceID)
	if err != nil {
		t.Fatalf("список обязан отдаваться и при битом файле: %v", err)
	}
	if st.Available {
		t.Fatalf("файл не разобран, ожидался available=false: %+v", st)
	}
	if !hasServerClientEntry(st, "client1") {
		t.Fatalf("список из wdtt.json пуст: %+v", st.Users)
	}
}

func TestAddServerClient_RejectsPasswordEqualToEffectiveMain(t *testing.T) {
	// Пароль сервера ещё НЕ сохранён: эффективный главный приезжает аргументом.
	s, cfgDir := newServerClientsService(t, "")
	const main = "future-main-pass00000000"
	_, err := s.AddServerClient(DefaultInstanceID, main, "Иван", "", main)
	if err == nil {
		t.Fatal("ожидался отказ: пароль абонента равен эффективному главному")
	}
	// Текст обязан называть причину и требовать ДРУГОЙ пароль: легаси-формулировка
	// «используйте основной пароль сервера» предлагала ровно то, что отвергнуто.
	if !strings.Contains(err.Error(), "совпадает с главным паролем") ||
		!strings.Contains(err.Error(), "другой пароль") {
		t.Fatalf("текст отказа = %q, ожидалось «совпадает с главным паролем … другой пароль»", err.Error())
	}
	if len(configServerClients(t, s)) != 0 {
		t.Fatalf("абонент остался в wdtt.json: %+v", configServerClients(t, s))
	}
	inst, err := s.serverInstance(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Config.Password != "" {
		t.Fatalf("пароль сервера сохранён вопреки отказу: %q", inst.Config.Password)
	}
	if _, err := os.Stat(passwordsJSONPath(cfgDir)); !os.IsNotExist(err) {
		t.Fatalf("passwords.json записан вопреки отказу: %v", err)
	}
}

func TestAddServerClient_WritesConfigAndFile(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	st, err := s.AddServerClient(DefaultInstanceID, "client1", "Иван", "vk1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasServerClientEntry(st, "client1") {
		t.Fatalf("ответ ручки без абонента: %+v", st.Users)
	}
	if !configHasServerClient(t, s, "client1") {
		t.Fatalf("абонент не сохранён в wdtt.json: %+v", configServerClients(t, s))
	}
	doc, err := loadPasswordsJSON(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := doc.Passwords["client1"]
	if !ok {
		t.Fatalf("абонент не записан в passwords.json: %#v", doc.Passwords)
	}
	if entry.Label != "Иван" || entry.VkHash != "vk1" {
		t.Fatalf("личность абонента не доехала до файла: %#v", entry)
	}
}

// Первый абонент на сервере без сохранённого пароля: побочный эффект дописывает
// пароль сервера, а в passwords.json уезжает ЭФФЕКТИВНЫЙ главный — иначе файл
// получил бы пустой main_password до следующего старта.
func TestAddServerClient_SavesEffectiveMainPassword(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "")
	const main = "brandnew-main-pass000000"
	if _, err := s.AddServerClient(DefaultInstanceID, "client1", "Иван", "", main); err != nil {
		t.Fatal(err)
	}
	inst, err := s.serverInstance(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Config.Password != main {
		t.Fatalf("пароль сервера не сохранён: %q", inst.Config.Password)
	}
	doc, err := loadPasswordsJSON(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	if doc.MainPassword != main {
		t.Fatalf("main_password в файле = %q, ожидался эффективный главный", doc.MainPassword)
	}
	if _, ok := doc.Passwords["client1"]; !ok {
		t.Fatalf("абонент не записан: %#v", doc.Passwords)
	}
}

// Память о сроке сильнее молчания файла: запись без expires_at не снимает срок,
// запомненный в wdtt.json. Иначе отозванный по сроку доступ становился бы
// бессрочным при первом же усыновлении.
func TestAdoptServerClientsFromFile_KeepsRememberedExpiry(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	expires := time.Now().Add(time.Hour).Unix()
	if err := s.putServerClient(DefaultInstanceID, ServerClient{Password: "client1", ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	writePasswordsFixture(t, cfgDir, passwordsJSON{
		MainPassword: "mainpass0000000000000000",
		Passwords:    map[string]passwordsJSONUser{"client1": {Label: "Иван"}},
	})
	clients, err := s.adoptServerClientsFromFile(DefaultInstanceID, cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range clients {
		if c.Password == "client1" {
			if c.ExpiresAt != expires {
				t.Fatalf("срок снят пустым значением файла: %d, ожидался %d", c.ExpiresAt, expires)
			}
			return
		}
	}
	t.Fatalf("абонент пропал при усыновлении: %+v", clients)
}

// Пустой пароль абонента даёт сгенерированный: 32 hex-символа. Форма
// проверяется, потому что этот пароль уезжает в ссылку и в passwords.json.
func TestAddServerClient_GeneratesHexPassword(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	st, err := s.AddServerClient(DefaultInstanceID, "", "Иван", "", "")
	if err != nil {
		t.Fatal(err)
	}
	pass := serverClientPasswordByComment(t, st, "Иван")
	if len(pass) != 32 {
		t.Fatalf("длина пароля = %d, ожидалось 32 hex-символа: %q", len(pass), pass)
	}
	if _, err := hex.DecodeString(pass); err != nil {
		t.Fatalf("пароль не hex: %q (%v)", pass, err)
	}
	if !fileHasServerClient(t, cfgDir, pass) {
		t.Fatalf("сгенерированный пароль не доехал до файла: %q", pass)
	}
}

// Имя и хеш тримятся на границе входа: непорезанные значения уехали бы и в
// wdtt.json, и в passwords.json, а сравнивать их потом не с чем.
func TestAddServerClient_TrimsCommentAndVkHash(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	if _, err := s.AddServerClient(DefaultInstanceID, "client1", "  Иван  ", "  vk1  ", ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range configServerClients(t, s) {
		if c.Password != "client1" {
			continue
		}
		if c.Comment != "Иван" || c.VkHash != "vk1" {
			t.Fatalf("вход не подрезан в wdtt.json: %+v", c)
		}
	}
	doc, err := loadPasswordsJSON(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	if entry := doc.Passwords["client1"]; entry.Label != "Иван" || entry.VkHash != "vk1" {
		t.Fatalf("вход не подрезан в passwords.json: %#v", entry)
	}
}

// Обе плановые проверки удаления: пустой пароль и главный пароль сервера.
// Главный пароль абонентом не является, и снимать его этой ручкой нельзя.
func TestRemoveServerClient_RejectsEmptyAndMainPassword(t *testing.T) {
	const main = "mainpass0000000000000000"
	s, _ := newServerClientsService(t, main)
	if _, err := s.RemoveServerClient(DefaultInstanceID, "   "); err == nil {
		t.Fatal("ожидался отказ: пароль абонента не задан")
	}
	_, err := s.RemoveServerClient(DefaultInstanceID, "  "+main+"  ")
	if err == nil {
		t.Fatal("ожидался отказ: удаление основного пароля сервера")
	}
	if !strings.Contains(err.Error(), "основной пароль") {
		t.Fatalf("текст отказа = %q, ожидалось упоминание основного пароля", err.Error())
	}
	inst, err := s.serverInstance(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Config.Password != main {
		t.Fatalf("пароль сервера задет удалением: %q", inst.Config.Password)
	}
}

// Граница ровно-в-эту-секунду у отказа на занятом пароле: расхождение с
// UsableServerClients меняет ТОЛЬКО текст отказа (отказывают обе ветки), но
// текст — единственное, что объясняет пользователю, почему пароль не взять.
func TestServerClientPasswordFree_ExpiryBoundary(t *testing.T) {
	now := time.Unix(1700000000, 0)
	clients := []ServerClient{{Password: "client1", ExpiresAt: now.Unix()}}
	err := serverClientPasswordFree(clients, "client1", now)
	if err == nil {
		t.Fatal("ожидался отказ на занятом пароле")
	}
	if !strings.Contains(err.Error(), "просроченному") {
		t.Fatalf("на границе секунды текст = %q, ожидался отказ про просроченного", err.Error())
	}
	if err := serverClientPasswordFree(clients, "client2", now); err != nil {
		t.Fatalf("свободный пароль отвергнут: %v", err)
	}
}

func TestRemoveServerClient_DropsClientAndItsDevice(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	for _, p := range []string{"client1", "client2"} {
		if _, err := s.AddServerClient(DefaultInstanceID, p, p, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	writePasswordsFixture(t, cfgDir, passwordsJSON{
		MainPassword: "mainpass0000000000000000",
		Passwords: map[string]passwordsJSONUser{
			"client1": {DeviceIDs: []string{"d1"}},
			"client2": {DeviceIDs: []string{"d2"}},
		},
		Devices: map[string]any{
			"d1": map[string]any{"ip": "10.66.0.2"},
			"d2": map[string]any{"ip": "10.66.0.3"},
		},
	})
	if _, err := s.RemoveServerClient(DefaultInstanceID, "client1"); err != nil {
		t.Fatal(err)
	}
	doc, err := loadPasswordsJSON(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Passwords["client1"]; ok {
		t.Fatalf("удалённый абонент остался в файле: %#v", doc.Passwords)
	}
	if _, ok := doc.Devices["d1"]; ok {
		t.Fatalf("устройство удалённого абонента осталось: %#v", doc.Devices)
	}
	if _, ok := doc.Passwords["client2"]; !ok {
		t.Fatalf("второй абонент потерян: %#v", doc.Passwords)
	}
	if _, ok := doc.Devices["d2"]; !ok {
		t.Fatalf("устройство второго абонента снято: %#v", doc.Devices)
	}
}

func TestRemoveServerClient_DoesNotResurrectViaAdoption(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	for _, p := range []string{"client1", "client2"} {
		if _, err := s.AddServerClient(DefaultInstanceID, p, p, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.RemoveServerClient(DefaultInstanceID, "client1")
	if err != nil {
		t.Fatal(err)
	}
	if hasServerClientEntry(st, "client1") {
		t.Fatalf("удалённый абонент вернулся в ответ ручки: %+v", st.Users)
	}
	if configHasServerClient(t, s, "client1") {
		t.Fatalf("удалённый абонент воскрешён в wdtt.json: %+v", configServerClients(t, s))
	}
	if fileHasServerClient(t, cfgDir, "client1") {
		t.Fatal("удалённый абонент остался в passwords.json")
	}
	if !configHasServerClient(t, s, "client2") || !fileHasServerClient(t, cfgDir, "client2") {
		t.Fatal("второй абонент задет удалением")
	}
}

func TestAddServerClient_AdoptsBeforeWritingFile(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	writePasswordsFixture(t, cfgDir, passwordsJSON{
		MainPassword: "mainpass0000000000000000",
		Passwords:    map[string]passwordsJSONUser{"legacy1": {Label: "Из бота"}},
	})
	if _, err := s.AddServerClient(DefaultInstanceID, "client1", "Иван", "", ""); err != nil {
		t.Fatal(err)
	}
	if !fileHasServerClient(t, cfgDir, "legacy1") {
		t.Fatal("абонент бота вычеркнут из passwords.json записью файла до усыновления")
	}
	if !configHasServerClient(t, s, "legacy1") {
		t.Fatalf("абонент бота не усыновлён: %+v", configServerClients(t, s))
	}
	if !fileHasServerClient(t, cfgDir, "client1") {
		t.Fatal("новый абонент не записан в файл")
	}
}

// Форма сервера отдаёт конфиг целиком снапшотом времени загрузки страницы, а
// абоненты правятся отдельной ручкой: сохранение настроек не должно их терять.
func TestUpdateServerInstanceKeepsClients(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass0000000000000000")
	st, err := s.AddServerClient(DefaultInstanceID, "", "Иван", "", "")
	if err != nil {
		t.Fatal(err)
	}
	clientPass := serverClientPasswordByComment(t, st, "Иван")

	stale := DefaultServerConfig() // без Clients — ровно то, что лежало в форме
	stale.Password = "mainpass0000000000000000"
	stale.NatMode = "internet-only"
	if _, err := s.UpdateServerInstance(DefaultInstanceID, stale); err != nil {
		t.Fatal(err)
	}
	if !configHasServerClient(t, s, clientPass) {
		t.Fatalf("абоненты потеряны при сохранении настроек: %+v", configServerClients(t, s))
	}
	// Двое: «Абонент 1» от инварианта непустоты плюс заказанный Иван. Счёт
	// проверяется, потому что сохранение настроек не должно ни терять абонентов,
	// ни плодить новых.
	if got := len(configServerClients(t, s)); got != 2 {
		t.Fatalf("абонентов %d, ожидалось два: %+v", got, configServerClients(t, s))
	}
	inst, err := s.serverInstance(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Config.NatMode != "internet-only" {
		t.Fatalf("остальные поля не применились: %+v", inst.Config)
	}
}

func TestAddServerClient_RejectsOccupiedPassword(t *testing.T) {
	cases := []struct {
		name      string
		expiresAt int64
		add       string
		wantText  string
	}{
		{
			name:      "просроченный",
			expiresAt: time.Now().Add(-time.Hour).Unix(),
			add:       "client1",
			wantText:  "просроченному",
		},
		{
			// Пароль подаётся С ПРОБЕЛАМИ: без нормализации входа сравнение не
			// совпадёт и отказ не сработает.
			name:      "живой",
			expiresAt: time.Now().Add(time.Hour).Unix(),
			add:       "  client1  ",
			wantText:  "живым",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
			// Абонент лежит В КОНФИГЕ: иначе усыновление законно правит
			// wdtt.json до отказа и «ничего не изменилось» станет ложным по
			// причине, к отказу отношения не имеющей.
			if err := s.putServerClient(DefaultInstanceID, ServerClient{
				Password: "client1", Comment: "Занято", ExpiresAt: tc.expiresAt,
			}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(s.store.path)
			if err != nil {
				t.Fatal(err)
			}
			_, addErr := s.AddServerClient(DefaultInstanceID, tc.add, "Второй", "", "")
			if addErr == nil {
				t.Fatal("ожидался отказ: пароль занят")
			}
			if !strings.Contains(addErr.Error(), tc.wantText) {
				t.Fatalf("текст отказа = %q, ожидалось упоминание %q", addErr.Error(), tc.wantText)
			}
			after, err := os.ReadFile(s.store.path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("wdtt.json изменён отказом:\nбыло %s\nстало %s", before, after)
			}
			if _, err := os.Stat(passwordsJSONPath(cfgDir)); !os.IsNotExist(err) {
				t.Fatalf("passwords.json записан вопреки отказу: %v", err)
			}
		})
	}
}

func TestAdoptServerClientsFromFile_SkipsMainPassword(t *testing.T) {
	const main = "mainpass0000000000000000"
	s, cfgDir := newServerClientsService(t, main)
	// Такую запись создаёт admin-API форка; усыновление её обязано пропустить.
	writePasswordsFixture(t, cfgDir, passwordsJSON{
		MainPassword: main,
		Passwords:    map[string]passwordsJSONUser{main: {Label: "самострел"}},
	})
	clients, err := s.adoptServerClientsFromFile(DefaultInstanceID, cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range clients {
		if c.Password == main {
			t.Fatalf("главный пароль усыновлён абонентом: %+v", clients)
		}
	}
	if configHasServerClient(t, s, main) {
		t.Fatalf("главный пароль попал в wdtt.json: %+v", configServerClients(t, s))
	}
	inst, err := s.serverInstance(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateServerInstance(DefaultInstanceID, inst.Config); err != nil {
		t.Fatalf("сохранение конфига сломано усыновлением главного пароля: %v", err)
	}
}

// Гонка «удаление против конкурентного чтения»: файл пишется последним, и
// чтение, попавшее в окно между вычёркиванием и записью, усыновило бы
// удалённого обратно из ещё не переписанного passwords.json.
func TestServerClients_ConcurrentListDoesNotResurrect(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	const rounds = 20
	for i := 0; i < rounds; i++ {
		victim := fmt.Sprintf("victim%02d", i)
		if _, err := s.AddServerClient(DefaultInstanceID, victim, "Жертва", "", ""); err != nil {
			t.Fatal(err)
		}
		var (
			wg      sync.WaitGroup
			start   = make(chan struct{})
			opErr   error
			opErrMu sync.Mutex
		)
		fail := func(err error) {
			opErrMu.Lock()
			if opErr == nil {
				opErr = err
			}
			opErrMu.Unlock()
		}
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.RemoveServerClient(DefaultInstanceID, victim); err != nil {
				fail(err)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.ListServerClients(DefaultInstanceID); err != nil {
				fail(err)
			}
		}()
		close(start)
		wg.Wait()
		if opErr != nil {
			t.Fatalf("round %d: %v", i, opErr)
		}
		if configHasServerClient(t, s, victim) {
			t.Fatalf("round %d: удалённый абонент воскрешён в wdtt.json", i)
		}
		if fileHasServerClient(t, cfgDir, victim) {
			t.Fatalf("round %d: удалённый абонент остался в passwords.json", i)
		}
	}
}

// TestServerClients_HandlersReloadRunningServer — окно между записью файла и
// его применением. Обе ручки абонентов обязаны пнуть ЖИВОЙ процесс сервера:
// без этого passwords.json меняется, а сервер продолжает работать со старым
// набором wrap-ключей до ближайшего перезапуска.
//
// Процесс подменён shell-скриптом через тот же seam startCmd, что в
// process_test.go; s.serverProcs.get отдаёт кэшированный экземпляр, поэтому
// ручка пнёт ровно его.
func TestServerClients_HandlersReloadRunningServer(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass")
	mark := filepath.Join(t.TempDir(), "hup.mark")

	proc := s.serverProcs.get(DefaultInstanceID)
	script := hupTrapScript(mark)
	proc.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}
	if err := proc.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = proc.Stop() })

	st, err := s.AddServerClient(DefaultInstanceID, "", "Абонент А", "", "")
	if err != nil {
		t.Fatalf("AddServerClient: %v", err)
	}
	waitForHupCount(t, mark, 1, 3*time.Second)

	pass := serverClientPasswordByComment(t, st, "Абонент А")
	if _, err := s.RemoveServerClient(DefaultInstanceID, pass); err != nil {
		t.Fatalf("RemoveServerClient: %v", err)
	}
	waitForHupCount(t, mark, 2, 3*time.Second)
}

// TestServerClients_NoReloadWhenFileWriteFails — сигнал привязан к УСПЕШНОЙ
// записи. Пнув сервер до неё, мы заставили бы его перечитать старый файл и
// считать изменение применённым, хотя ручка вернула ошибку.
//
// Отказ записи делается правами каталога: passwords.json ещё не существует, а в
// каталог 0500 его не создать. Уже существующий файл открылся бы на запись и
// при закрытом каталоге — отсюда требование «до первой записи».
func TestServerClients_NoReloadWhenFileWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root игнорирует права каталога — отказ записи не воспроизвести")
	}
	s, cfgDir := newServerClientsService(t, "mainpass")
	mark := filepath.Join(t.TempDir(), "hup.mark")

	proc := s.serverProcs.get(DefaultInstanceID)
	script := hupTrapScript(mark)
	proc.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}
	if err := proc.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = proc.Stop() })

	if _, err := os.Stat(passwordsJSONPath(cfgDir)); !os.IsNotExist(err) {
		t.Fatalf("предпосылка теста нарушена: passwords.json уже существует (%v)", err)
	}
	if err := os.Chmod(cfgDir, 0500); err != nil {
		t.Fatal(err)
	}
	// Иначе уборка t.TempDir() не снесёт закрытый каталог.
	t.Cleanup(func() { _ = os.Chmod(cfgDir, 0700) })

	if _, err := s.AddServerClient(DefaultInstanceID, "", "Абонент А", "", ""); err == nil {
		t.Fatal("запись в закрытый каталог обязана вернуть ошибку")
	}
	time.Sleep(300 * time.Millisecond)
	if b, err := os.ReadFile(mark); err == nil && len(strings.Fields(string(b))) > 0 {
		t.Fatalf("SIGHUP ушёл при неудачной записи passwords.json: %q", b)
	}
}

// TestServerClients_ReloadTargetsOwnInstance — сигнал обязан уйти процессу ТОГО
// сервера, чей файл переписан. Одноинстансная фикстура этого не проверяет:
// жёсткий DefaultInstanceID вместо serverID в ней неотличим от адресации.
//
// Второй инстанс заводится прямо в хранилище: CreateServer второй запрещает
// (общий интерфейс wdtt0), а ручкам абонентов интерфейс безразличен —
// им нужен только конфиг с паролем и свой config-dir (он выводится из id).
func TestServerClients_ReloadTargetsOwnInstance(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass")

	full, err := s.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	second := DefaultServerConfig()
	second.Password = "secondpass"
	full.Servers = append(full.Servers, ServerInstance{ID: "srv2", Name: "Второй", Config: second})
	if err := s.store.Save(full); err != nil {
		t.Fatal(err)
	}
	if _, err := s.serverInstance("srv2"); err != nil {
		t.Fatalf("предпосылка теста нарушена, второй инстанс не завёлся: %v", err)
	}

	dir := t.TempDir()
	markDefault := filepath.Join(dir, "default.mark")
	markSecond := filepath.Join(dir, "second.mark")
	startMarkedServerProcess(t, s, DefaultInstanceID, markDefault)
	startMarkedServerProcess(t, s, "srv2", markSecond)

	if _, err := s.AddServerClient("srv2", "", "Абонент Б", "", ""); err != nil {
		t.Fatalf("AddServerClient(srv2): %v", err)
	}
	waitForHupCount(t, markSecond, 1, 3*time.Second)
	// Оба процесса получили бы сигнал одновременно; пауза — запас на планировщик.
	time.Sleep(300 * time.Millisecond)
	if b, err := os.ReadFile(markDefault); err == nil && len(strings.Fields(string(b))) > 0 {
		t.Fatalf("SIGHUP ушёл чужому инстансу: изменён srv2, сигнал получил %s: %q", DefaultInstanceID, b)
	}
	if running, _ := s.serverProcs.get(DefaultInstanceID).IsRunning(); !running {
		t.Fatal("процесс чужого инстанса умер — свидетель мёртв, страж вырожден")
	}
}

// startMarkedServerProcess поднимает процесс сервера инстанса на shell-скрипте,
// пишущем строку в маркер на каждый SIGHUP.
func startMarkedServerProcess(t *testing.T, s *Service, instanceID, mark string) {
	t.Helper()
	proc := s.serverProcs.get(instanceID)
	script := hupTrapScript(mark)
	proc.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}
	if err := proc.Start(nil); err != nil {
		t.Fatalf("Start(%s): %v", instanceID, err)
	}
	t.Cleanup(func() { _ = proc.Stop() })
}

// --- Инвариант «сервер никогда не остаётся без абонентов, которых он примет» ---
//
// wdtt-server собирает wrap-ключи из НЕПРОСРОЧЕННЫХ записей passwords.json и на
// пустом наборе умирает `log.Fatalf` уже на старте. Поэтому все три опоры
// инварианта считают ровно тем же предикатом, что и запись файла, —
// UsableServerClients; «абонентов не ноль» и «ключей не ноль» разные величины.

// Опора 1: первый же сохранённый пароль сервера заводит абонента. Мастер
// собирает ссылку ДО первого старта, и к моменту генерации абонент обязан
// существовать.
func TestUpdateServerInstance_FirstPasswordCreatesClient(t *testing.T) {
	const main = "mainpass0000000000000000"
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg := DefaultServerConfig()
	cfg.Password = main

	saved, err := s.UpdateServerInstance(DefaultInstanceID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Clients) != 1 {
		t.Fatalf("ожидался ровно один абонент, получено %d: %+v", len(saved.Clients), saved.Clients)
	}
	c := saved.Clients[0]
	if c.Password == "" {
		t.Fatal("пароль автоматического абонента пуст")
	}
	if c.Password == main {
		t.Fatalf("пароль абонента равен главному: %q", c.Password)
	}
	if c.Comment != "Абонент 1" {
		t.Fatalf("имя автоматического абонента = %q, ожидалось «Абонент 1»", c.Comment)
	}
	if len(UsableServerClients(saved.Clients, main, time.Now())) != 1 {
		t.Fatalf("заведённого абонента сервер не примет: %+v", saved.Clients)
	}
	if !configHasServerClient(t, s, c.Password) {
		t.Fatalf("абонент не сохранён в wdtt.json: %+v", configServerClients(t, s))
	}
}

// Опора 1 срабатывает по отсутствию РАБОЧИХ, а не при каждом сохранении:
// иначе каждое сохранение формы плодило бы абонентов.
func TestUpdateServerInstance_SecondSaveDoesNotAddClient(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass0000000000000000")
	first := configServerClients(t, s)
	if len(first) != 1 {
		t.Fatalf("предпосылка нарушена, абонентов %d: %+v", len(first), first)
	}

	cfg := DefaultServerConfig()
	cfg.Password = "othermainpass00000000000" // в том числе со сменой главного пароля
	saved, err := s.UpdateServerInstance(DefaultInstanceID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Clients) != 1 {
		t.Fatalf("повторное сохранение добавило абонентов: %+v", saved.Clients)
	}
	if saved.Clients[0].Password != first[0].Password {
		t.Fatalf("абонент подменён: было %q, стало %q", first[0].Password, saved.Clients[0].Password)
	}
}

// Прямой страж предиката: под подсчётом «сколько элементов в списке» тест
// красный — просроченный абонент есть, а ключа у сервера нет.
func TestUpdateServerInstance_ExpiredOnlyClientGetsCompanion(t *testing.T) {
	const main = "mainpass0000000000000000"
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	expiresAt := time.Now().Add(-time.Hour).Unix()
	setServerClients(t, s, []ServerClient{{Password: "expired1", Comment: "Просроченный", ExpiresAt: expiresAt}})

	cfg := DefaultServerConfig()
	cfg.Password = main
	saved, err := s.UpdateServerInstance(DefaultInstanceID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	usable := UsableServerClients(saved.Clients, main, time.Now())
	if len(usable) != 1 {
		t.Fatalf("рядом с просроченным не заведён рабочий абонент: %+v", saved.Clients)
	}
	if usable[0].Password == "expired1" {
		t.Fatalf("просроченный посчитан рабочим: %+v", usable)
	}
	for _, c := range saved.Clients {
		if c.Password == "expired1" {
			if c.ExpiresAt != expiresAt {
				t.Fatalf("срок просроченного абонента изменён: %d, был %d", c.ExpiresAt, expiresAt)
			}
			return
		}
	}
	t.Fatalf("просроченный абонент вычеркнут из списка: %+v", saved.Clients)
}

// Опора 2 — главный страж C1: путь лечения СУЩЕСТВУЮЩИХ установок. Там пароль
// сервера задан давно, абонентов нет ни одного (UI говорил «по умолчанию все
// подключаются основным паролем сервера»), и без этой опоры первый же старт
// после обновления упирается в «[WRAP] нет активных паролей» — сервер не
// поднимается вовсе, а супервизор крутит попытки по кругу.
func TestWriteServerClientsFile_CreatesClientWhenNoneUsable(t *testing.T) {
	const main = "mainpass0000000000000000"
	expiresAt := time.Now().Add(-time.Hour).Unix()
	cases := []struct {
		name    string
		clients []ServerClient
	}{
		{name: "пустой список", clients: nil},
		{name: "единственный просроченный", clients: []ServerClient{{Password: "expired1", Comment: "Просроченный", ExpiresAt: expiresAt}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, cfgDir := newServerClientsService(t, main)
			setServerClients(t, s, tc.clients)

			if err := s.syncServerClientsOnStart(DefaultInstanceID, cfgDir, serverConfigOf(t, s, DefaultInstanceID)); err != nil {
				t.Fatal(err)
			}
			clients := configServerClients(t, s)
			usable := UsableServerClients(clients, main, time.Now())
			if len(usable) != 1 {
				t.Fatalf("после синхронизации рабочих абонентов %d: %+v", len(usable), clients)
			}
			if !fileHasServerClient(t, cfgDir, usable[0].Password) {
				t.Fatalf("рабочий абонент не доехал до passwords.json: %q", usable[0].Password)
			}
			if len(tc.clients) == 0 {
				return
			}
			if !configHasServerClient(t, s, "expired1") {
				t.Fatalf("просроченный абонент вычеркнут из wdtt.json: %+v", clients)
			}
			if fileHasServerClient(t, cfgDir, "expired1") {
				t.Fatal("просроченный абонент уехал в passwords.json — сервер его всё равно отвергнет")
			}
		})
	}
}

// Опора 3 закрывает окно «живой сервер»: удаление последнего рабочего абонента
// переписывает passwords.json пустым и SIGHUP обнуляет набор wrap-ключей у
// работающего процесса, а следующий старт умирает вовсе.
func TestRemoveServerClient_RefusesLastUsableClient(t *testing.T) {
	const main = "mainpass0000000000000000"

	t.Run("единственный рабочий", func(t *testing.T) {
		s, _ := newServerClientsService(t, main)
		clients := configServerClients(t, s)
		if len(clients) != 1 {
			t.Fatalf("предпосылка нарушена, абонентов %d: %+v", len(clients), clients)
		}
		pass := clients[0].Password
		_, err := s.RemoveServerClient(DefaultInstanceID, pass)
		if err == nil {
			t.Fatal("ожидался отказ: удаление последнего рабочего абонента")
		}
		if !strings.Contains(err.Error(), "последнего рабочего") {
			t.Fatalf("текст отказа = %q, ожидалось упоминание последнего рабочего абонента", err.Error())
		}
		if !configHasServerClient(t, s, pass) {
			t.Fatalf("абонент удалён вопреки отказу: %+v", configServerClients(t, s))
		}
	})

	t.Run("двое рабочих", func(t *testing.T) {
		s, cfgDir := newServerClientsService(t, main)
		if _, err := s.AddServerClient(DefaultInstanceID, "client2", "Второй", "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RemoveServerClient(DefaultInstanceID, "client2"); err != nil {
			t.Fatalf("удаление при двух рабочих обязано проходить: %v", err)
		}
		if configHasServerClient(t, s, "client2") {
			t.Fatalf("абонент не удалён: %+v", configServerClients(t, s))
		}
		if fileHasServerClient(t, cfgDir, "client2") {
			t.Fatal("удалённый абонент остался в passwords.json")
		}
	})

	// Рабочих не было и до операции: запрещать выход из уже сломанного
	// состояния бессмысленно, нового заведёт опора 2 той же записью файла.
	t.Run("единственный просроченный", func(t *testing.T) {
		s, cfgDir := newServerClientsService(t, main)
		setServerClients(t, s, []ServerClient{{Password: "expired1", ExpiresAt: time.Now().Add(-time.Hour).Unix()}})
		if _, err := s.RemoveServerClient(DefaultInstanceID, "expired1"); err != nil {
			t.Fatalf("удаление просроченного отвергнуто: %v", err)
		}
		if configHasServerClient(t, s, "expired1") {
			t.Fatalf("просроченный абонент не удалён: %+v", configServerClients(t, s))
		}
		usable := UsableServerClients(configServerClients(t, s), main, time.Now())
		if len(usable) != 1 {
			t.Fatalf("после удаления сервер остался без рабочих абонентов: %+v", configServerClients(t, s))
		}
		if !fileHasServerClient(t, cfgDir, usable[0].Password) {
			t.Fatalf("заведённый взамен абонент не доехал до passwords.json: %q", usable[0].Password)
		}
	})
}

// Страж порядка внутри AddServerClient: побочный эффект «дописать пароль
// сервера» идёт ПОСЛЕ абонента. Наоборот — сначала сработал бы инвариант
// непустоты и завёл «Абонента 1» рядом с заказанным.
func TestAddServerClient_OnEmptyServerCreatesExactlyOne(t *testing.T) {
	s, _ := newServerClientsService(t, "")
	const main = "brandnew-main-pass000000"

	st, err := s.AddServerClient(DefaultInstanceID, "", "Иван", "", main)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Users) != 1 {
		t.Fatalf("ответ ручки содержит %d абонентов: %+v", len(st.Users), st.Users)
	}
	clients := configServerClients(t, s)
	if len(clients) != 1 {
		t.Fatalf("рядом с заказанным абонентом заведён лишний: %+v", clients)
	}
	if clients[0].Comment != "Иван" {
		t.Fatalf("имя абонента = %q, ожидалось «Иван»", clients[0].Comment)
	}
}

// Ответ ручки удаления собирается ПОСЛЕ записи файла. Опора 2 заводит
// «Абонента 1» прямо в этой записи, и снимок, сделанный раньше, показал бы
// пустой список там, где абонент уже есть и в wdtt.json, и в passwords.json:
// пользователь решил бы, что сервер остался без абонентов.
func TestRemoveServerClient_AnswerIncludesReplacementClient(t *testing.T) {
	const main = "mainpass0000000000000000"
	s, cfgDir := newServerClientsService(t, main)
	setServerClients(t, s, []ServerClient{{Password: "expired1", Comment: "Просроченный", ExpiresAt: time.Now().Add(-time.Hour).Unix()}})

	st, err := s.RemoveServerClient(DefaultInstanceID, "expired1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Users) != 1 {
		t.Fatalf("ответ ручки содержит %d абонентов, а в конфиге %+v", len(st.Users), configServerClients(t, s))
	}
	if st.Users[0].Comment != "Абонент 1" {
		t.Fatalf("в ответе не тот абонент: %+v", st.Users[0])
	}
	if !fileHasServerClient(t, cfgDir, st.Users[0].Password) {
		t.Fatalf("абонент из ответа не найден в passwords.json: %q", st.Users[0].Password)
	}
}

// Опора 2 обязана отдавать свою ошибку наружу: проглотив её (fail-open), мы
// записали бы passwords.json без единого рабочего абонента — ровно то, ради
// чего опора и заведена. Отказ воспроизводится несуществующим сервером:
// putServerClient не находит инстанс.
func TestWriteServerClientsFile_ReportsInvariantFailure(t *testing.T) {
	const main = "mainpass0000000000000000"
	s, cfgDir := newServerClientsService(t, main)
	cfg := serverConfigOf(t, s, DefaultInstanceID)

	if _, err := s.writeServerClientsFile("нет-такого-сервера", cfgDir, cfg, nil); err == nil {
		t.Fatal("ожидалась ошибка: абонента для пустого списка завести не удалось")
	}
	if _, err := os.Stat(passwordsJSONPath(cfgDir)); !os.IsNotExist(err) {
		t.Fatalf("passwords.json записан вопреки отказу инварианта: %v", err)
	}
}

// Опора 3 считает по УСЫНОВЛЁННОМУ списку. Абонент бота живёт только в
// passwords.json, и по составу wdtt.json его не видно: без усыновления удаление
// нашего единственного абонента получило бы ложный отказ, хотя рабочий у
// сервера остаётся.
func TestRemoveServerClient_CountsAdoptedClients(t *testing.T) {
	const main = "mainpass0000000000000000"
	s, cfgDir := newServerClientsService(t, main)
	clients := configServerClients(t, s)
	if len(clients) != 1 {
		t.Fatalf("предпосылка нарушена, абонентов %d: %+v", len(clients), clients)
	}
	ours := clients[0].Password
	writePasswordsFixture(t, cfgDir, passwordsJSON{
		MainPassword: main,
		Passwords: map[string]passwordsJSONUser{
			ours:      {Label: clients[0].Comment},
			"botpass": {Label: "Из бота", ExpiresAt: time.Now().Add(24 * time.Hour).Unix()},
		},
	})

	if _, err := s.RemoveServerClient(DefaultInstanceID, ours); err != nil {
		t.Fatalf("ложный отказ: рабочий абонент бота есть, но он усыновляется только из файла: %v", err)
	}
	if configHasServerClient(t, s, ours) {
		t.Fatalf("абонент не удалён: %+v", configServerClients(t, s))
	}
	if !configHasServerClient(t, s, "botpass") {
		t.Fatalf("абонент бота не усыновлён: %+v", configServerClients(t, s))
	}
}

// Автоматический абонент заводится БЕССРОЧНЫМ и с плановым именем — на обеих
// опорах. Срок, проставленный по недосмотру, отозвал бы доступ сам собой, и
// сервер снова остался бы без единого wrap-ключа; имя — единственное, по чему
// пользователь узнаёт запись, которую не заводил.
func TestServerClients_AutoClientIsNamedAndUnlimited(t *testing.T) {
	const main = "mainpass0000000000000000"

	t.Run("опора 1: сохранение конфига", func(t *testing.T) {
		dir := t.TempDir()
		s := NewService(dir, dir, "/bin/sh", "/bin/sh")
		cfg := DefaultServerConfig()
		cfg.Password = main
		saved, err := s.UpdateServerInstance(DefaultInstanceID, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(saved.Clients) != 1 {
			t.Fatalf("абонентов %d: %+v", len(saved.Clients), saved.Clients)
		}
		if saved.Clients[0].Comment != "Абонент 1" {
			t.Fatalf("имя = %q, ожидалось «Абонент 1»", saved.Clients[0].Comment)
		}
		if saved.Clients[0].ExpiresAt != 0 {
			t.Fatalf("автоматический абонент заведён со сроком %d", saved.Clients[0].ExpiresAt)
		}
	})

	t.Run("опора 2: запись файла", func(t *testing.T) {
		s, cfgDir := newServerClientsService(t, main)
		setServerClients(t, s, nil)
		if err := s.syncServerClientsOnStart(DefaultInstanceID, cfgDir, serverConfigOf(t, s, DefaultInstanceID)); err != nil {
			t.Fatal(err)
		}
		clients := configServerClients(t, s)
		if len(clients) != 1 {
			t.Fatalf("абонентов %d: %+v", len(clients), clients)
		}
		if clients[0].Comment != "Абонент 1" {
			t.Fatalf("имя = %q, ожидалось «Абонент 1»", clients[0].Comment)
		}
		if clients[0].ExpiresAt != 0 {
			t.Fatalf("автоматический абонент заведён со сроком %d", clients[0].ExpiresAt)
		}
		doc, err := loadPasswordsJSON(cfgDir)
		if err != nil {
			t.Fatal(err)
		}
		entry, ok := doc.Passwords[clients[0].Password]
		if !ok {
			t.Fatalf("абонент не записан в passwords.json: %#v", doc.Passwords)
		}
		if entry.Label != "Абонент 1" {
			t.Fatalf("имя в файле = %q, ожидалось «Абонент 1»", entry.Label)
		}
		if entry.ExpiresAt != 0 {
			t.Fatalf("в файле у автоматического абонента срок %d", entry.ExpiresAt)
		}
	})
}

// recordingAppLogger собирает сообщения журнала для проверки, что настоящая
// чистка устройств слышна.
type recordingAppLogger struct {
	mu       sync.Mutex
	messages []string
}

func (l *recordingAppLogger) AppLog(_ logging.Level, _, _, _, _, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, message)
}

func (l *recordingAppLogger) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.messages {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// TestWriteServerClientsFile_LogsRealGatewayPurge сторожит сам факт
// журналирования: снятие устройства абонента с IP шлюза обязано быть слышно, а
// собственный резерв — нет. Мутация «убрать вызов appLog» роняет первую
// половину, мутация «журналировать всегда» — вторую.
func TestWriteServerClientsFile_LogsRealGatewayPurge(t *testing.T) {
	s, cfgDir := newServerClientsService(t, "adminpass")
	log := &recordingAppLogger{}
	s.SetLogger(log)

	writePasswordsFixture(t, cfgDir, passwordsJSON{
		MainPassword: "adminpass",
		Passwords:    map[string]passwordsJSONUser{},
		Devices:      map[string]any{"dev-abonenta": map[string]any{"ip": DefaultWdttServerGatewayAddr}},
	})
	cfg := serverConfigOf(t, s, DefaultInstanceID)
	if _, err := s.writeServerClientsFile(DefaultInstanceID, cfgDir, cfg, []ServerClient{{Password: "abonent1"}}); err != nil {
		t.Fatal(err)
	}
	if !log.contains("устройства с IP шлюза") {
		t.Fatalf("настоящая чистка прошла молча: %v", log.messages)
	}

	log.messages = nil
	if _, err := s.writeServerClientsFile(DefaultInstanceID, cfgDir, cfg, []ServerClient{{Password: "abonent1"}}); err != nil {
		t.Fatal(err)
	}
	if log.contains("устройства с IP шлюза") {
		t.Fatalf("свой резерв попал в журнал: %v", log.messages)
	}
}
