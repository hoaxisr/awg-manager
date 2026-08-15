package awgmproto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenLogTruncatesPreviousRun(t *testing.T) {
	// Границу запуска задаёт процесс усечением при открытии: на этом стоят
	// предикаты здоровья, считающие содержимое файла журналом текущего запуска.
	path := filepath.Join(t.TempDir(), "a.log")
	if err := os.WriteFile(path, []byte("хвост прошлого запуска\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("файл не усечён при открытии: %q", data)
	}
}

func TestLogTruncatesOnCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")
	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	line := []byte(strings.Repeat("x", 4096) + "\n")
	for written := 0; written < LogCapBytes+8*len(line); written += len(line) {
		if _, err := lg.Write(line); err != nil {
			t.Fatal(err)
		}
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > LogCapBytes {
		t.Fatalf("журнал %d байт при потолке %d — усечение по потолку не работает", st.Size(), LogCapBytes)
	}
	if st.Size() == 0 {
		t.Fatal("после усечения запись должна продолжаться")
	}
}
