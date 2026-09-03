package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newKeyStore(t *testing.T) *McpKeyStore {
	t.Helper()
	s := NewMcpKeyStore(t.TempDir())
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMcpKeyStore_CreateVerifyRevoke(t *testing.T) {
	s := newKeyStore(t)
	key, plain, err := s.Create("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, McpKeyPrefix) || len(plain) < len(McpKeyPrefix)+40 {
		t.Fatalf("plaintext %q: bad shape", plain)
	}
	if key.Name != "laptop" || key.ID == "" || key.Hash == "" {
		t.Fatalf("key = %+v", key)
	}
	if got, ok := s.Verify(plain); !ok || got.ID != key.ID {
		t.Fatalf("Verify(valid) = %+v, %v", got, ok)
	}
	if _, ok := s.Verify(plain + "x"); ok {
		t.Fatal("Verify(tampered) = ok")
	}
	if _, ok := s.Verify(""); ok {
		t.Fatal("Verify(empty) = ok")
	}
	if err := s.Revoke(key.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify(plain); ok {
		t.Fatal("Verify after revoke = ok")
	}
	if err := s.Revoke(key.ID); err != ErrMcpKeyNotFound {
		t.Fatalf("Revoke twice = %v, want ErrMcpKeyNotFound", err)
	}
}

func TestMcpKeyStore_ListHidesHashAndPersists(t *testing.T) {
	s := newKeyStore(t)
	if _, _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create("b"); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 2 || list[0].Name != "a" || list[1].Name != "b" {
		t.Fatalf("List = %+v", list)
	}
	for _, k := range list {
		if k.Hash != "" {
			t.Fatal("List leaks hash")
		}
	}
	path := filepath.Join(s.dataDir, "mcp_keys.json")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 0600", st.Mode().Perm())
	}
	reloaded := NewMcpKeyStore(s.dataDir)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.List()) != 2 {
		t.Fatal("keys not persisted")
	}
}

func TestMcpKeyStore_CreateRejectsBadName(t *testing.T) {
	s := newKeyStore(t)
	if _, _, err := s.Create("   "); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, _, err := s.Create(strings.Repeat("x", 65)); err == nil {
		t.Fatal("65 ASCII char name accepted")
	}
	if _, _, err := s.Create(strings.Repeat("x", 64)); err != nil {
		t.Fatalf("64 ASCII char name rejected: %v", err)
	}
	// Name length must be measured in runes, not bytes: each "я" is two
	// UTF-8 bytes, so a byte-based check would wrongly reject a 64-rune
	// Cyrillic name (128 bytes) and wrongly accept some multi-byte names
	// short of the character limit.
	if _, _, err := s.Create(strings.Repeat("я", 64)); err != nil {
		t.Fatalf("64 Cyrillic char name rejected: %v", err)
	}
	if _, _, err := s.Create(strings.Repeat("я", 65)); err == nil {
		t.Fatal("65 Cyrillic char name accepted")
	}
}

func TestMcpKeyStore_PlaintextNeverPersisted(t *testing.T) {
	s := newKeyStore(t)
	key, plain, err := s.Create("laptop")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.dataDir, "mcp_keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), plain) {
		t.Fatal("plaintext key persisted to disk")
	}
	if key.Hash == plain {
		t.Fatal("stored hash equals plaintext")
	}
}

func TestMcpKeyStore_CreateRollsBackOnSaveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	dataDir := t.TempDir()
	s := NewMcpKeyStore(dataDir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dataDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dataDir, 0o700) })

	_, plain, err := s.Create("laptop")
	if err == nil {
		t.Fatal("Create succeeded despite unwritable data dir")
	}
	if list := s.List(); len(list) != 0 {
		t.Fatalf("phantom key left in memory: %+v", list)
	}
	if _, ok := s.Verify(plain); ok {
		t.Fatal("phantom key verifies after failed Create")
	}
}

func TestMcpKeyStore_RevokeRollsBackOnSaveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	dataDir := t.TempDir()
	s := NewMcpKeyStore(dataDir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	key, plain, err := s.Create("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dataDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dataDir, 0o700) })

	if err := s.Revoke(key.ID); err == nil {
		t.Fatal("Revoke succeeded despite unwritable data dir")
	}
	if list := s.List(); len(list) != 1 {
		t.Fatalf("key lost from memory after failed Revoke: %+v", list)
	}
	if _, ok := s.Verify(plain); !ok {
		t.Fatal("key stopped verifying after failed Revoke")
	}
}

func TestMcpKeyStore_TouchThrottled(t *testing.T) {
	s := newKeyStore(t)
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	key, _, _ := s.Create("k")
	s.Touch(key.ID)
	first := s.List()[0].LastUsedAt
	if !first.Equal(now) {
		t.Fatalf("LastUsedAt = %v, want %v", first, now)
	}
	now = now.Add(30 * time.Second)
	s.Touch(key.ID)
	if !s.List()[0].LastUsedAt.Equal(first) {
		t.Fatal("Touch within a minute rewrote LastUsedAt")
	}
	now = now.Add(31 * time.Second)
	s.Touch(key.ID)
	if !s.List()[0].LastUsedAt.Equal(now) {
		t.Fatal("Touch after a minute did not update LastUsedAt")
	}
}

func TestMcpKeyStore_LoadMissingFileIsEmpty(t *testing.T) {
	s := newKeyStore(t)
	if len(s.List()) != 0 {
		t.Fatal("expected no keys")
	}
}
