package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTokenKeyRequiresPrivatePersistentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.key")
	if _, err := ReadTokenKey(path); err == nil {
		t.Fatal("missing key accepted")
	}
	for _, size := range []int{0, 31, 33, 32} {
		if err := os.WriteFile(path, bytes.Repeat([]byte{7}, size), 0600); err != nil {
			t.Fatal(err)
		}
		key, err := ReadTokenKey(path)
		if (err == nil) != (size == 32) {
			t.Fatalf("unexpected key validation for %d bytes: %v", size, err)
		}
		if size == 32 && !bytes.Equal(key[:], bytes.Repeat([]byte{7}, 32)) {
			t.Fatal("key changed while reading")
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenKey(path); err == nil {
		t.Fatal("group-readable key accepted")
	}
}
