package wdtt

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// writeBin кладёт исполняемый файл и возвращает его SHA256.
func writeBin(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func newBinariesService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	return NewService(dir, dir, filepath.Join(dir, "wdtt-client"), filepath.Join(dir, "wdtt-server"))
}

func setSpecs(s *Service, clientSHA, serverSHA string) {
	s.SetInstallSpecs(ArchSpecs{
		Client: BinarySpec{Version: "1.4.4-awgm", URL: "http://example/client", SHA256: clientSHA},
		Server: BinarySpec{Version: "1.4.4-awgm", URL: "http://example/server", SHA256: serverSHA},
	})
}

func TestInstallStatusFields_MatchingBinariesAreUpToDate(t *testing.T) {
	s := newBinariesService(t)
	setSpecs(s, writeBin(t, s.clientBin, "client-1.4.4"), writeBin(t, s.serverBin, "server-1.4.4"))
	if err := s.writeInstalledVersion("1.4.4-awgm+server-1.4.4-awgm"); err != nil {
		t.Fatal(err)
	}

	installed, updateAvailable := s.installStatusFields("1.4.4-awgm+server-1.4.4-awgm")
	if updateAvailable {
		t.Fatal("совпавшие с пином бинари не должны звать обновляться")
	}
	if installed != "1.4.4-awgm+server-1.4.4-awgm" {
		t.Fatalf("installed = %q", installed)
	}
}

// Регрессия: сервер из протухшей сборки не знает -listen-raw и падает на
// flag.Parse, а wdtt-version.json объявлял его пином — UI показывал
// «актуально», и кнопки обновления человек не видел.
func TestInstallStatusFields_StaleBinaryOffersUpdate(t *testing.T) {
	s := newBinariesService(t)
	clientSHA := writeBin(t, s.clientBin, "client-1.4.4")
	writeBin(t, s.serverBin, "server-old-without-listen-raw")
	setSpecs(s, clientSHA, "b639505b9952485bc16e9e3d43d6503975a878b0b18aba3fa5269953b61fd000")
	if err := s.writeInstalledVersion("1.4.4-awgm+server-1.4.4-awgm"); err != nil {
		t.Fatal(err)
	}

	installed, updateAvailable := s.installStatusFields("1.4.4-awgm+server-1.4.4-awgm")
	if !updateAvailable {
		t.Fatal("соврамший wdtt-version.json оставил роутер без обновления")
	}
	if installed != "" {
		t.Fatalf("версия чужого бинаря выдана за установленную: %q", installed)
	}
}

// mips/mipsel: сервера в пине нет — статус считается по одному клиенту.
func TestInstallStatusFields_ClientOnlyArch(t *testing.T) {
	s := newBinariesService(t)
	clientSHA := writeBin(t, s.clientBin, "client-1.4.4")
	s.SetInstallSpecs(ArchSpecs{
		Client: BinarySpec{Version: "1.4.4-awgm", URL: "http://example/client", SHA256: clientSHA},
	})
	if err := s.writeInstalledVersion("1.4.4-awgm"); err != nil {
		t.Fatal(err)
	}

	if _, updateAvailable := s.installStatusFields("1.4.4-awgm"); updateAvailable {
		t.Fatal("совпавший клиент на арке без сервера не должен звать обновляться")
	}
}
