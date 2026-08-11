package wdtt

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"strings"
)

// binariesMatchSpecs — совпадают ли бинари на диске с SHA256 из пина сборки.
//
// Единственный источник бинарей — зеркало по пину install.go; в IPK они не
// кладутся. Раньше факта «файл на месте» хватало, чтобы объявить его пином:
// протухший бинарь считался установленным, обновление не предлагалось, а
// старт падал на флаге, которого тот бинарь не знает (flag.Parse → exit 2).
//
// Результат кешируется по mtime+size: сверка читает ~23 МБ, а зовётся из
// Status(), который фронт опрашивает постоянно. Ключ меняется при любой
// перезаписи бинаря, так что установка инвалидирует кеш сама.
func (s *Service) binariesMatchSpecs() bool {
	if s.installSpecs == nil {
		return false
	}
	specs := *s.installSpecs
	if specs.Client.SHA256 == "" {
		return false
	}
	if specs.serverSupported() && specs.Server.SHA256 == "" {
		return false
	}
	key := specs.Client.SHA256 + "|" + specs.Server.SHA256 + "|" +
		wdttFileFingerprint(s.clientBin) + "|" + wdttFileFingerprint(s.serverBin)

	s.matchMu.Lock()
	defer s.matchMu.Unlock()
	if key == s.matchKey {
		return s.matchVal
	}
	match := sha256Equals(s.clientBin, specs.Client.SHA256)
	if match && specs.serverSupported() {
		match = sha256Equals(s.serverBin, specs.Server.SHA256)
	}
	s.matchKey, s.matchVal = key, match
	return match
}

func sha256Equals(path, want string) bool {
	got, err := sha256File(path)
	return err == nil && strings.EqualFold(got, want)
}

// wdttFileFingerprint — дешёвая замена содержимому файла: mtime+size.
func wdttFileFingerprint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "_" + strconv.FormatInt(fi.Size(), 10)
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
