package wdtt

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// EnsureBundledInstall chmod 0755 для wdtt-бинарей из IPK и синхронизирует
// wdtt-version.json, если бинарники уже лежат в /opt/bin (develop/test IPK).
func (s *Service) EnsureBundledInstall() {
	wdttEnsureExecutable(s.clientBin)
	wdttEnsureExecutable(s.serverBin)
	s.syncInstalledVersionFromBinaries()
}

func (s *Service) syncInstalledVersionFromBinaries() {
	if s.installSpecs == nil {
		return
	}
	if !binaryPresent(s.clientBin) {
		return
	}
	if s.installSpecs.serverSupported() && !binaryPresent(s.serverBin) {
		return
	}
	want := installVersionLabel(*s.installSpecs)
	if s.readInstalledVersion() == want {
		return
	}
	// Если в IPK другие SHA256, чем в EmbeddedBinaries — всё равно помечаем установленным.
	if err := s.writeInstalledVersion(want); err != nil && s.appLog != nil {
		s.appLog.Warn("install", "version-file", err.Error())
	}
}

func wdttEnsureExecutable(path string) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return
	}
	if st.Mode().Perm()&0111 != 0 {
		return
	}
	_ = os.Chmod(path, st.Mode().Perm()|0755)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
