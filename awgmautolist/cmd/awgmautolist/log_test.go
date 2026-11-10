package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingLogKeepsSingleOldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awgmautolist.log")
	log, err := openRotatingLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if _, err := log.Write(bytes.Repeat([]byte("a"), maxLogBytes/2)); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Write(bytes.Repeat([]byte("b"), maxLogBytes)); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(path + ".old")
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != maxLogBytes || current[0] != 'b' {
		t.Fatalf("current log size/content = %d", len(current))
	}
	if len(old) != maxLogBytes/2 || old[0] != 'a' {
		t.Fatalf("old log size/content = %d", len(old))
	}
}
