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
	}
	if !slices.Equal(got, want) {
		t.Fatalf("buildServerArgs() = %v, want %v", got, want)
	}
}

// wdtt-server (v1.4.62) не знает флага -debug: flag.Parse печатает
// «flag provided but not defined» и выходит с кодом 2 — сервер не стартует.
func TestBuildServerArgsNoDebugFlag(t *testing.T) {
	got := buildServerArgs(ServerConfig{Listen: "0.0.0.0:56002", WgPort: 56001, Debug: true})
	if slices.Contains(got, "-debug") {
		t.Fatalf("buildServerArgs() отдал -debug: %v", got)
	}
}
