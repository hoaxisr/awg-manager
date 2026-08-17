package wdtt

import (
	"slices"
	"strings"
	"testing"
)

// Один инстанс: wdtt0 общий, второй сервер создать нельзя.
func TestCreateServer_SingleInstance(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	if _, err := s.GetConfig(); err != nil { // materialize default server
		t.Fatal(err)
	}
	if _, err := s.CreateServer(CreateServerInput{Name: "второй"}); err == nil {
		t.Fatal("ожидалась ошибка: второй инстанс сервера")
	}
}

// UpdateServerInstance возвращает сохранённый конфиг (после нормализации).
func TestUpdateServerInstance_ReturnsSaved(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	saved, err := s.UpdateServerInstance(DefaultInstanceID, ServerConfig{Password: "  p  "})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Listen == "" || saved.WgPort <= 0 || saved.Password != strings.TrimSpace("  p  ") {
		t.Fatalf("конфиг не нормализован: %+v", saved)
	}
}

func TestBuildServerArgsNoNAT(t *testing.T) {
	got := buildServerArgs(ServerConfig{
		Listen:   "0.0.0.0:56002",
		WgPort:   56001,
		Password: "secret",
	})
	want := []string{
		"-listen", "0.0.0.0:56002",
		"-wg-port", "56001",
		"-password", "secret",
		"-no-nat",
		"-listen-raw", "0.0.0.0:56003",
		"-dns", "10.66.66.1",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("buildServerArgs() = %v, want %v", got, want)
	}
}

// wdtt-server (v1.4.62) не знает флага -debug: flag.Parse печатает
// «flag provided but not defined» и выходит с кодом 2 — сервер не стартует.
func TestValidateServerMainPassword_RejectsClientMatch(t *testing.T) {
	clientPass := "b3cb9612bee5da6b8260cf9eb4340402"
	err := validateServerMainPassword(clientPass, []ServerClient{{Password: clientPass, Comment: "Иван"}})
	if err == nil {
		t.Fatal("ожидалась ошибка при совпадении пароля сервера и клиента")
	}
	if err := validateServerMainPassword("owner-main-pass", []ServerClient{{Password: clientPass}}); err != nil {
		t.Fatalf("разные пароли должны проходить: %v", err)
	}
}

func TestUpdateServerInstance_RejectsMainPasswordSameAsClient(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg := DefaultServerConfig()
	cfg.Password = "owner-main-pass00000000"
	if _, err := s.UpdateServerInstance(DefaultInstanceID, cfg); err != nil {
		t.Fatal(err)
	}
	st, err := s.AddServerClient(DefaultInstanceID, "", "Иван", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Выбираем по имени: главного пароля в списке больше нет, а порядок и число
	// записей задаются составом конфига.
	clientPass := serverClientPasswordByComment(t, st, "Иван")
	if _, err := s.UpdateServerInstance(DefaultInstanceID, ServerConfig{Password: clientPass}); err == nil {
		t.Fatal("ожидалась ошибка: пароль сервера совпадает с паролем клиента")
	}
}

func TestBuildServerArgsNoDebugFlag(t *testing.T) {
	got := buildServerArgs(ServerConfig{Listen: "0.0.0.0:56002", WgPort: 56001, Debug: true})
	if slices.Contains(got, "-debug") {
		t.Fatalf("buildServerArgs() отдал -debug: %v", got)
	}
}

func TestBuildServerArgsDNS(t *testing.T) {
	hasFlag := func(args []string, flag, want string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == flag && args[i+1] == want {
				return true
			}
		}
		return false
	}
	wg := DefaultServerConfig() // RelayMode: wg
	if !hasFlag(buildServerArgs(wg), "-dns", wg.serverAccessAddress()) {
		t.Fatalf("wg-relay: нет -dns %s в %v", wg.serverAccessAddress(), buildServerArgs(wg))
	}
	raw := DefaultServerConfig()
	raw.RelayMode = ConnModeRaw
	if !hasFlag(buildServerArgs(raw), "-dns", DefaultRawServerAddr) {
		t.Fatalf("raw-relay: нет -dns %s в %v", DefaultRawServerAddr, buildServerArgs(raw))
	}
}
