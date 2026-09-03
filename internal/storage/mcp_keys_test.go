package storage

import (
	"errors"
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

func TestMcpKeyStore_CreateBadNameWrapsSentinel(t *testing.T) {
	s := newKeyStore(t)
	_, _, err := s.Create("   ")
	if err == nil || !errors.Is(err, ErrMcpKeyInvalidName) {
		t.Fatalf("empty name error does not wrap ErrMcpKeyInvalidName: %v", err)
	}
	_, _, err = s.Create(strings.Repeat("x", 65))
	if err == nil || !errors.Is(err, ErrMcpKeyInvalidName) {
		t.Fatalf("too-long name error does not wrap ErrMcpKeyInvalidName: %v", err)
	}
}

func TestMcpKeyStore_CreateSaveFailureDoesNotWrapInvalidName(t *testing.T) {
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

	_, _, err := s.Create("laptop")
	if err == nil {
		t.Fatal("Create succeeded despite unwritable data dir")
	}
	if errors.Is(err, ErrMcpKeyInvalidName) {
		t.Fatalf("save failure wrongly wraps ErrMcpKeyInvalidName: %v", err)
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

// TestMcpKeyStore_UnreadableFileMakesStoreReadOnly — провал чтения (не
// «файла нет») означает, что список в памяти НЕ равен файлу. Любая запись
// после этого переименовала бы усечённый список поверх настоящего и молча
// отозвала все выданные ключи, поэтому хранилище уходит в read-only.
func TestMcpKeyStore_UnreadableFileMakesStoreReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permission bits")
	}
	dataDir := t.TempDir()
	seed := NewMcpKeyStore(dataDir)
	if err := seed.Load(); err != nil {
		t.Fatal(err)
	}
	key, plain, err := seed.Create("laptop")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "mcp_keys.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	s := NewMcpKeyStore(dataDir)
	loadErr := s.Load()
	if loadErr == nil {
		t.Fatal("Load of an unreadable file returned nil")
	}

	_, _, err = s.Create("phone")
	if !errors.Is(err, ErrMcpKeyStoreReadOnly) {
		t.Fatalf("Create = %v, want ErrMcpKeyStoreReadOnly", err)
	}
	if !strings.Contains(err.Error(), "refusing to write after load failure") {
		t.Fatalf("Create error does not name the cause: %v", err)
	}
	if err := s.Revoke(key.ID); !errors.Is(err, ErrMcpKeyStoreReadOnly) {
		t.Fatalf("Revoke = %v, want ErrMcpKeyStoreReadOnly", err)
	}
	s.Touch(key.ID) // must not write either

	// Fail-closed for auth is correct and stays.
	if _, ok := s.Verify(plain); ok {
		t.Fatal("Verify must fail closed while the store could not be read")
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("the key file was rewritten while read-only:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestMcpKeyStore_SaveKeepsBackup — перед каждой перезаписью рядом остаётся
// .bak с прежним содержимым: дешёвая страховка на флеше роутера.
func TestMcpKeyStore_SaveKeepsBackup(t *testing.T) {
	dataDir := t.TempDir()
	s := NewMcpKeyStore(dataDir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create("first"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "mcp_keys.json")
	afterFirst, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("no .bak expected before the second write: %v", err)
	}
	if _, _, err := s.Create("second"); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no .bak after an overwrite: %v", err)
	}
	if string(bak) != string(afterFirst) {
		t.Fatalf(".bak does not hold the previous contents:\nwant=%s\ngot=%s", afterFirst, bak)
	}
	if !strings.Contains(string(bak), "first") || strings.Contains(string(bak), "second") {
		t.Fatalf(".bak = %s", bak)
	}
}

// TestMcpKeyStore_TouchDoesNotHoldTheLockAcrossTheWrite — Touch снимает
// снимок под локом и пишет уже без него, иначе fsync на флеше блокировал бы
// Verify для всех параллельных запросов.
func TestMcpKeyStore_TouchDoesNotHoldTheLockAcrossTheWrite(t *testing.T) {
	s := newKeyStore(t)
	key, plain, err := s.Create("k")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			if _, ok := s.Verify(plain); !ok {
				t.Error("Verify failed during Touch")
				return
			}
		}
	}()
	for i := 0; i < 50; i++ {
		s.now = func() time.Time { return time.Now().Add(time.Duration(i) * time.Hour) }
		s.Touch(key.ID)
	}
	<-done
	if s.List()[0].LastUsedAt.IsZero() {
		t.Fatal("Touch did not record LastUsedAt")
	}
	reloaded := NewMcpKeyStore(s.dataDir)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if reloaded.List()[0].LastUsedAt.IsZero() {
		t.Fatal("Touch did not persist LastUsedAt")
	}
}

// touchRacesWith запускает Touch, замирает в хуке (mu уже отпущен, файл ещё
// не записан), пока идёт мутация mutate, и возвращает список ключей,
// перечитанный С ДИСКА после того, как обе операции завершились.
//
// Хук — единственный способ проверить порядок детерминированно: гонка
// «снимок Touch устарел, но всё равно лёг поверх» иначе ловится только
// таймингом.
func touchRacesWith(t *testing.T, s *McpKeyStore, touchID string, mutate func()) []McpKey {
	t.Helper()
	mutateDone := make(chan struct{})
	s.afterTouchUnlock = func() {
		go func() {
			defer close(mutateDone)
			mutate()
		}()
		// Дать мутатору время дойти до записи. С правильным порядком блокировок
		// он упрётся в fileMu и не пройдёт дальше, пока Touch не запишет;
		// со сломанным — успеет записать свой список, и снимок Touch затрёт его.
		select {
		case <-mutateDone:
		case <-time.After(time.Second):
		}
	}
	s.Touch(touchID)
	<-mutateDone
	s.afterTouchUnlock = nil

	reloaded := NewMcpKeyStore(s.dataDir)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	return reloaded.List()
}

// TestMcpKeyStore_TouchDoesNotResurrectRevokedKey — Touch пишет уже без mu,
// поэтому его снимок обязан быть упорядочен относительно мутаций через
// fileMu. Иначе Revoke успешно отзывает ключ и возвращает успех, а устаревший
// снимок Touch кладёт отозванный ключ обратно в файл — после перезапуска он
// снова рабочий.
func TestMcpKeyStore_TouchDoesNotResurrectRevokedKey(t *testing.T) {
	s := newKeyStore(t)
	keep, _, err := s.Create("keep")
	if err != nil {
		t.Fatal(err)
	}
	doomed, doomedPlain, err := s.Create("doomed")
	if err != nil {
		t.Fatal(err)
	}

	var revokeErr error
	onDisk := touchRacesWith(t, s, keep.ID, func() { revokeErr = s.Revoke(doomed.ID) })
	if revokeErr != nil {
		t.Fatalf("Revoke = %v", revokeErr)
	}

	for _, k := range onDisk {
		if k.ID == doomed.ID {
			t.Fatalf("Revoke returned success but the key is back in the file: %+v", onDisk)
		}
	}
	if len(onDisk) != 1 || onDisk[0].ID != keep.ID {
		t.Fatalf("on-disk keys = %+v, want only %q", onDisk, keep.ID)
	}
	if onDisk[0].LastUsedAt.IsZero() {
		t.Fatal("the touched key's LastUsedAt did not reach the file")
	}

	// И живое состояние тоже: отозванный ключ не должен пройти проверку
	// после перезагрузки хранилища с диска.
	reloaded := NewMcpKeyStore(s.dataDir)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Verify(doomedPlain); ok {
		t.Fatal("a revoked key verifies again after a restart")
	}
}

// TestMcpKeyStore_TouchDoesNotEraseNewKey — зеркальный случай: Create выдал
// пользователю plaintext и вернул успех, а устаревший снимок Touch стёр
// новый ключ с диска.
func TestMcpKeyStore_TouchDoesNotEraseNewKey(t *testing.T) {
	s := newKeyStore(t)
	old, _, err := s.Create("old")
	if err != nil {
		t.Fatal(err)
	}

	var fresh McpKey
	var freshPlain string
	var createErr error
	onDisk := touchRacesWith(t, s, old.ID, func() { fresh, freshPlain, createErr = s.Create("fresh") })
	if createErr != nil {
		t.Fatalf("Create = %v", createErr)
	}

	found := false
	for _, k := range onDisk {
		if k.ID == fresh.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("Create handed out a key that is missing from the file: %+v", onDisk)
	}
	if len(onDisk) != 2 {
		t.Fatalf("on-disk keys = %+v, want both", onDisk)
	}

	reloaded := NewMcpKeyStore(s.dataDir)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Verify(freshPlain); !ok {
		t.Fatal("a freshly issued key does not verify after a restart")
	}
}
