//go:build !mips && !mipsle

package wdtt

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestPanelUsersCRUD(t *testing.T) {
	dir := t.TempDir()
	mainPass := "abc123def4567890abcdef1234567890"

	// До первого старта сервера panel.db ещё нет: чтение не создаёт файл.
	st, err := loadPanelUsers(dir, mainPass)
	if err != nil {
		t.Fatal(err)
	}
	if st.Available {
		t.Fatal("expected unavailable before panel.db exists")
	}
	if len(st.Users) != 0 {
		t.Fatalf("expected empty users, got %d", len(st.Users))
	}

	client, err := addPanelUser(dir, mainPass, "", "Alice", "vkhash1")
	if err != nil {
		t.Fatal(err)
	}
	if client.Comment != "Alice" || client.Password == "" {
		t.Fatalf("client: %+v", client)
	}

	st, err = loadPanelUsers(dir, mainPass)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Users) != 1 || st.Users[0].Password != client.Password {
		t.Fatalf("reload: %+v", st.Users)
	}

	if err := removePanelUser(dir, mainPass, client.Password); err != nil {
		t.Fatal(err)
	}
	st, err = loadPanelUsers(dir, mainPass)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Users) != 0 {
		t.Fatalf("after delete: expected 0 users, got %d", len(st.Users))
	}

	// Повторное удаление не ошибка: источник правды — wdtt.json.
	if err := removePanelUser(dir, mainPass, client.Password); err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if err := removePanelUser(dir, mainPass, mainPass); err == nil {
		t.Fatal("expected error deleting main password")
	}

	if _, err := os.Stat(filepath.Join(dir, "panel.db")); err != nil {
		t.Fatalf("panel.db missing: %v", err)
	}
}

// wdtt-server при ошибке чтения panel.db переписывает её пустой базой с одним
// главным паролем (issue #679). Клиенты обязаны вернуться на следующем старте.
func TestRestorePanelUsersRebuildsWipedDB(t *testing.T) {
	dir := t.TempDir()
	mainPass := "mainpass0000000000000000"
	clients := []ServerClient{{Password: "clientpass1", Comment: "Иван", VkHash: "vk1"}}

	if _, err := restorePanelUsers(dir, mainPass, clients); err != nil {
		t.Fatal(err)
	}
	wipePanelUsers(t, dir)

	extra, err := restorePanelUsers(dir, mainPass, clients)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 0 {
		t.Fatalf("unexpected adoption: %+v", extra)
	}
	st, err := loadPanelUsers(dir, mainPass)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Users) != 1 || st.Users[0].Password != "clientpass1" || st.Users[0].Comment != "Иван" {
		t.Fatalf("restore: %+v", st.Users)
	}
}

// Строки, заведённые мимо awgm (старые установки, телеграм-бот), не удаляются,
// а возвращаются наверх для усыновления в wdtt.json.
func TestRestorePanelUsersAdoptsUnknownRows(t *testing.T) {
	dir := t.TempDir()
	mainPass := "mainpass0000000000000000"

	if _, err := addPanelUser(dir, mainPass, "legacy1", "Старый", "vk9"); err != nil {
		t.Fatal(err)
	}
	extra, err := restorePanelUsers(dir, mainPass, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 1 || extra[0].Password != "legacy1" || extra[0].Comment != "Старый" || extra[0].VkHash != "vk9" {
		t.Fatalf("adoption: %+v", extra)
	}
	st, err := loadPanelUsers(dir, mainPass)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Users) != 1 {
		t.Fatalf("legacy row dropped: %+v", st.Users)
	}
}

// Список клиентов живёт в wdtt.json: пропажа panel.db больше не опустошает UI.
func TestServicePanelUsersSurviveWipedDB(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg := DefaultServerConfig()
	cfg.Password = "mainpass0000000000000000"
	if _, err := s.UpdateServerInstance(DefaultInstanceID, cfg); err != nil {
		t.Fatal(err)
	}

	st, err := s.AddServerPanelUser(DefaultInstanceID, "", "Иван", "vk1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Users) != 2 || !st.Users[0].IsMain || st.Users[1].Comment != "Иван" {
		t.Fatalf("after add: %+v", st.Users)
	}
	clientPass := st.Users[1].Password

	cfgDir, err := s.serverConfigDir(DefaultInstanceID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cfgDir, "panel.db")); err != nil {
		t.Fatal(err)
	}

	st, err = s.ListServerPanelUsers(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Available {
		t.Fatal("panel.db removed, expected available=false")
	}
	if len(st.Users) != 2 || !st.Users[0].IsMain || st.Users[1].Password != clientPass {
		t.Fatalf("list without panel.db: %+v", st.Users)
	}

	// Старт сервера пересобирает panel.db из конфига.
	s.restoreServerPanelUsers(DefaultInstanceID, cfgDir, s.mustServerConfig(t))
	back, err := loadPanelUsers(cfgDir, cfg.Password)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Users) != 1 || back.Users[0].Password != clientPass {
		t.Fatalf("panel.db not rebuilt: %+v", back.Users)
	}

	if _, err := s.RemoveServerPanelUser(DefaultInstanceID, clientPass); err != nil {
		t.Fatal(err)
	}
	inst, err := s.serverInstance(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inst.Config.Clients) != 0 {
		t.Fatalf("client left in wdtt.json: %+v", inst.Config.Clients)
	}
}

func (s *Service) mustServerConfig(t *testing.T) ServerConfig {
	t.Helper()
	inst, err := s.serverInstance(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	return inst.Config
}

// wipePanelUsers повторяет то, что делает wdtt-server в saveDB() после неудачной
// загрузки: полная перезапись таблицы пользователей.
func wipePanelUsers(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", panelDSNWrite(panelDBPath(dir)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM wdtt_users; DELETE FROM wdtt_user_devices`); err != nil {
		t.Fatal(err)
	}
}
