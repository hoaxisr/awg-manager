package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRAMLogTruncatesWithoutRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awgmautolist.log")
	log, err := OpenRAMLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if _, err := log.Write(bytes.Repeat([]byte{'a'}, int(MaxLogBytes)-10)); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > MaxLogBytes || !bytes.HasPrefix(data, truncationMarker) ||
		!bytes.HasSuffix(data, []byte("0123456789abcdef")) {
		t.Fatalf("size=%d prefix=%q", len(data), data[:min(len(data), 80)])
	}
	if _, err := os.Stat(path + ".old"); !os.IsNotExist(err) {
		t.Fatalf("rotation file unexpectedly exists: %v", err)
	}
}

func TestRAMLogSingleOversizedRecordStaysBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awgmautolist.log")
	log, err := OpenRAMLog(path)
	if err != nil {
		t.Fatal(err)
	}
	input := bytes.Repeat([]byte("x"), int(MaxLogBytes)*2)
	n, err := log.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() != MaxLogBytes {
		t.Fatalf("size=%d", stat.Size())
	}
}

func TestRAMLogConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awgmautolist.log")
	log, err := OpenRAMLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, err := log.Write([]byte(strings.Repeat("z", 1024))); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() > MaxLogBytes {
		t.Fatalf("size=%d", stat.Size())
	}
}
