package traffic

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSysfsCounters_Happy(t *testing.T) {
	root := t.TempDir()
	iface := filepath.Join(root, "nwg0", "statistics")
	if err := os.MkdirAll(iface, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iface, "rx_bytes"), []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iface, "tx_bytes"), []byte("678\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rx, tx, err := readSysfsCounters(root, "nwg0")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rx != 12345 {
		t.Errorf("rx: want 12345, got %d", rx)
	}
	if tx != 678 {
		t.Errorf("tx: want 678, got %d", tx)
	}
}

func TestReadSysfsCounters_MissingIface(t *testing.T) {
	root := t.TempDir()
	_, _, err := readSysfsCounters(root, "nwg0")
	if err == nil {
		t.Fatal("want error on missing iface, got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("want os.IsNotExist err, got %v", err)
	}
}

func TestReadSysfsCounters_MalformedValue(t *testing.T) {
	root := t.TempDir()
	iface := filepath.Join(root, "nwg0", "statistics")
	if err := os.MkdirAll(iface, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iface, "rx_bytes"), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iface, "tx_bytes"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := readSysfsCounters(root, "nwg0")
	if err == nil {
		t.Fatal("want error on malformed value, got nil")
	}
}
