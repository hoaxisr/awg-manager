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
	return newKeyStoreIn(t, t.TempDir())
}

// blockWrites makes every persist fail regardless of uid: the store's
// directory is re-pointed under a regular file, so AtomicWritePerm's
// MkdirAll fails with ENOTDIR. Permission bits would not do — the local
// Docker test run is root, which ignores them, and these tests were
// silently skipped there.
func blockWrites(t *testing.T, s *McpKeyStore) {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.dataDir = filepath.Join(blocker, "sub")
	s.mu.Unlock()
}

func newKeyStoreIn(t *testing.T, dataDir string) *McpKeyStore {
	t.Helper()
	s := NewMcpKeyStore(dataDir)
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
	s := newKeyStore(t)
	blockWrites(t, s)

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
	s := newKeyStore(t)
	blockWrites(t, s)

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
	s := newKeyStore(t)
	key, plain, err := s.Create("laptop")
	if err != nil {
		t.Fatal(err)
	}
	blockWrites(t, s)

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
	now = now.Add(30 * time.Minute)
	s.Touch(key.ID)
	if !s.List()[0].LastUsedAt.Equal(first) {
		t.Fatal("Touch within an hour rewrote LastUsedAt")
	}
	now = now.Add(31 * time.Minute)
	s.Touch(key.ID)
	if !s.List()[0].LastUsedAt.Equal(now) {
		t.Fatal("Touch after an hour did not update LastUsedAt")
	}
}

// TestMcpKeyStore_NewerFileVersionIsReadOnly — файл от более новой сборки
// не порча (карантин не нужен) и не пустота: ключи могут декодироваться
// неполно, а обратная запись стёрла бы поля нового формата. Хранилище
// уходит в read-only, файл остаётся как был.
func TestMcpKeyStore_NewerFileVersionIsReadOnly(t *testing.T) {
	dataDir := t.TempDir()
	raw := []byte(`{"version":2,"keys":[{"id":"abc","name":"laptop","hash":"00","createdAt":"2026-09-01T00:00:00Z"}]}`)
	if err := os.WriteFile(filepath.Join(dataDir, mcpKeysFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewMcpKeyStore(dataDir)
	err := s.Load()
	if !errors.Is(err, ErrMcpKeysFileVersion) {
		t.Fatalf("Load = %v, want ErrMcpKeysFileVersion", err)
	}
	if _, _, err := s.Create("x"); !errors.Is(err, ErrMcpKeyStoreReadOnly) {
		t.Fatalf("Create = %v, want ErrMcpKeyStoreReadOnly", err)
	}
	if err := s.Revoke("abc"); !errors.Is(err, ErrMcpKeyStoreReadOnly) {
		t.Fatalf("Revoke = %v, want ErrMcpKeyStoreReadOnly", err)
	}
	s.Touch("abc")
	got, err := os.ReadFile(filepath.Join(dataDir, mcpKeysFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("file rewritten by a build that cannot read it:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dataDir, mcpKeysFile+".corrupt")); err == nil {
		t.Fatal("a newer-format file must not be quarantined as corrupt")
	}
}

// TestMcpKeyStore_CorruptFileRestoresFromBackup — оборванная запись после
// отключения питания: основной файл в карантин, ключи берутся из .bak,
// который persistFileLocked держит рядом, и тут же записываются обратно —
// иначе следующая загрузка снова стартовала бы с пустого списка.
func TestMcpKeyStore_CorruptFileRestoresFromBackup(t *testing.T) {
	dataDir := t.TempDir()
	seed := NewMcpKeyStore(dataDir)
	if err := seed.Load(); err != nil {
		t.Fatal(err)
	}
	first, firstPlain, err := seed.Create("laptop")
	if err != nil {
		t.Fatal(err)
	}
	phone, phonePlain, err := seed.Create("phone")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, mcpKeysFile)
	if err := os.WriteFile(path, []byte(`{"version":1,"keys":[{"id":"x"`), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewMcpKeyStore(dataDir)
	if err := s.Load(); err != nil {
		t.Fatalf("Load after corruption = %v, want restore", err)
	}
	if _, ok := s.Verify(firstPlain); !ok {
		t.Fatal("key from the backup is not honoured after restore")
	}
	if _, ok := s.Verify(phonePlain); !ok {
		t.Fatal("the backup is the current membership: the second key must survive too")
	}
	if got := s.List(); len(got) != 2 || got[0].ID != first.ID || got[1].ID != phone.ID {
		t.Fatalf("restored list = %+v, want both keys in creation order", got)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatal("corrupt file was not quarantined")
	}
	// The restored list is on disk again: a second boot sees it.
	again := NewMcpKeyStore(dataDir)
	if err := again.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Verify(firstPlain); !ok {
		t.Fatal("restore was not persisted; next boot would start empty")
	}
	// And the store is writable — the on-disk state is known again.
	if _, _, err := again.Create("tablet"); err != nil {
		t.Fatalf("Create after restore = %v", err)
	}
}

// Без .bak (первая запись ещё не сделала копию) порча честно даёт пустой
// список — как раньше, но хранилище остаётся записываемым.
func TestMcpKeyStore_CorruptFileWithoutBackupIsEmptyAndWritable(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, mcpKeysFile), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewMcpKeyStore(dataDir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Fatal("expected no keys")
	}
	if _, _, err := s.Create("x"); err != nil {
		t.Fatalf("Create = %v", err)
	}
}

// TestMcpKeyStore_UnloadedStoreRefusesWrites — конструктор без Load: список
// в памяти ничего не говорит о файле, запись переименовала бы однострочный
// список поверх настоящего. Verify при этом работает (и честно не находит).
func TestMcpKeyStore_UnloadedStoreRefusesWrites(t *testing.T) {
	dataDir := t.TempDir()
	seed := newKeyStoreIn(t, dataDir)
	key, plain, err := seed.Create("laptop")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dataDir, mcpKeysFile))
	if err != nil {
		t.Fatal(err)
	}

	s := NewMcpKeyStore(dataDir) // no Load
	if _, _, err := s.Create("phone"); !errors.Is(err, ErrMcpKeyStoreReadOnly) {
		t.Fatalf("Create on an unloaded store = %v, want ErrMcpKeyStoreReadOnly", err)
	}
	if err := s.Revoke(key.ID); !errors.Is(err, ErrMcpKeyStoreReadOnly) {
		t.Fatalf("Revoke on an unloaded store = %v, want ErrMcpKeyStoreReadOnly", err)
	}
	s.Touch(key.ID)
	if _, ok := s.Verify(plain); ok {
		t.Fatal("an unloaded store must not verify anything")
	}
	after, err := os.ReadFile(filepath.Join(dataDir, mcpKeysFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("the key file was rewritten by an unloaded store:\n%s", after)
	}
}

// TestMcpKeyStore_NameValidationRunsBeforeReadOnlyCheck — плохое имя
// остаётся 400 даже на read-only хранилище; управляющие символы в имени
// (оно уходит в каждую строку журнала) отклоняются.
func TestMcpKeyStore_NameValidationRunsBeforeReadOnlyCheck(t *testing.T) {
	s := NewMcpKeyStore(t.TempDir()) // unloaded → read-only
	if _, _, err := s.Create("   "); !errors.Is(err, ErrMcpKeyInvalidName) {
		t.Fatalf("blank name on a read-only store = %v, want ErrMcpKeyInvalidName", err)
	}
	loaded := newKeyStore(t)
	for _, bad := range []string{"lap\ntop", "lap\ttop", "lap\x00top", "\x1b[31mred"} {
		if _, _, err := loaded.Create(bad); !errors.Is(err, ErrMcpKeyInvalidName) {
			t.Errorf("Create(%q) = %v, want ErrMcpKeyInvalidName", bad, err)
		}
	}
	for _, ok := range []string{"Ноутбук Андрея — дом", "MacBook\u00a0Pro", "wide\u3000space"} {
		if _, _, err := loaded.Create(ok); err != nil {
			t.Errorf("Create(%q) rejected: %v", ok, err)
		}
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
	dataDir := t.TempDir()
	seed := newKeyStoreIn(t, dataDir)
	key, plain, err := seed.Create("laptop")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "mcp_keys.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// An unreadable file that is NOT "missing": a symlink loop fails with
	// ELOOP for every uid (chmod 0 would be ignored by root, i.e. by the
	// local Docker test run). The real file is kept aside to prove that
	// nothing overwrote the path meanwhile.
	aside := path + ".aside"
	if err := os.Rename(path, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("mcp_keys.json", path); err != nil {
		t.Fatal(err)
	}

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

	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the unreadable path was replaced while read-only: %v %v", fi, err)
	}
	after, err := os.ReadFile(aside)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("the real key file changed while read-only:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestMcpKeyStore_BackupIsTheCurrentMembership — .bak после Create/Revoke
// равен основному файлу (отдельный inode, не hardlink): восстановление
// не может вернуть отозванный ключ. Touch копию не трогает.
func TestMcpKeyStore_BackupIsTheCurrentMembership(t *testing.T) {
	dataDir := t.TempDir()
	s := newKeyStoreIn(t, dataDir)
	path := filepath.Join(dataDir, mcpKeysFile)
	bak := path + ".bak"

	if _, _, err := s.Create("first"); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Create("second")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(second.ID); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("no .bak after a save: %v", err)
	}
	if string(got) != string(main) || strings.Contains(string(got), "second") {
		t.Fatalf(".bak must equal the file after the revoke:\nmain=%s\nbak=%s", main, got)
	}
	mi, _ := os.Stat(path)
	bi, _ := os.Stat(bak)
	if os.SameFile(mi, bi) {
		t.Fatal(".bak is a hardlink: a corrupted inode would take both copies")
	}

	// Touch rewrites the main file only.
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	s.Touch(s.List()[0].ID)
	afterTouch, _ := os.ReadFile(bak)
	if string(afterTouch) != string(got) {
		t.Fatal("Touch must not rewrite the backup")
	}
}

// TestMcpKeyStore_RestoreDoesNotResurrectRevokedKey — сценарий утечки:
// ключ отозван, затем основной файл порван; после восстановления из .bak
// отозванный ключ обязан остаться отозванным.
func TestMcpKeyStore_RestoreDoesNotResurrectRevokedKey(t *testing.T) {
	dataDir := t.TempDir()
	seed := newKeyStoreIn(t, dataDir)
	_, keepPlain, err := seed.Create("keep")
	if err != nil {
		t.Fatal(err)
	}
	leaked, leakedPlain, err := seed.Create("leaked")
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Revoke(leaked.ID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, mcpKeysFile)
	if err := os.WriteFile(path, []byte(`{"version":1,"keys":[{"id":"x"`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newKeyStoreIn(t, dataDir)
	if _, ok := s.Verify(leakedPlain); ok {
		t.Fatal("restore brought a revoked key back")
	}
	if _, ok := s.Verify(keepPlain); !ok {
		t.Fatal("restore lost the key that was never revoked")
	}
}

// TestMcpKeyStore_MissingMainWithBackupRestores — перезапись после
// карантина не долетела (ENOSPC, питание): основного файла нет, .bak есть.
// Пустой и записываемый старт затёр бы единственную хорошую копию.
func TestMcpKeyStore_MissingMainWithBackupRestores(t *testing.T) {
	dataDir := t.TempDir()
	seed := newKeyStoreIn(t, dataDir)
	_, plain, err := seed.Create("laptop")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, mcpKeysFile)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s := newKeyStoreIn(t, dataDir)
	if _, ok := s.Verify(plain); !ok {
		t.Fatal("key from the backup is not honoured when the main file is missing")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("main file was not written back: %v", err)
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
	// The hook runs at the exact point between "mu released" and "file
	// written". If Touch still held mu here, Verify (RLock) would block
	// until the hook — and the write — finished, and the timeout below
	// would fire. A background Verify racing a full Touch cannot tell the
	// two apart: it would merely wait and then succeed.
	verified := make(chan bool, 1)
	s.afterTouchUnlock = func() {
		go func() { _, ok := s.Verify(plain); verified <- ok }()
		select {
		case ok := <-verified:
			if !ok {
				t.Error("Verify failed during Touch")
			}
			verified <- ok // hand the result back to the main goroutine
		case <-time.After(2 * time.Second):
			t.Error("Verify blocked while Touch was writing: mu is held across the write")
		}
	}
	s.Touch(key.ID)
	s.afterTouchUnlock = nil
	select {
	case <-verified:
	default:
		t.Fatal("the hook never ran")
	}
	if s.List()[0].LastUsedAt.IsZero() {
		t.Fatal("Touch did not record LastUsedAt")
	}
	reloaded := newKeyStoreIn(t, s.dataDir)
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
		// 200 ms is plenty for an fsync'd write on CI; with the right lock
		// order the mutator never completes here and the full wait is paid.
		select {
		case <-mutateDone:
		case <-time.After(200 * time.Millisecond):
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
