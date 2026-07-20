package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/auth/shadowcrypt"
)

// writeShadow writes a shadow fixture and returns a verifier pointed at it.
func writeShadow(t *testing.T, content string) *EntwareVerifier {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write shadow fixture: %v", err)
	}
	return &EntwareVerifier{ShadowPath: path}
}

// fixture hash: SHA-256-crypt of "Hello world!" — official vector from
// Drepper's SHA-crypt specification (also glibc-verified, see shadowcrypt
// tests for provenance).
const shadowFixture = `# comment line
root:$5$saltstring$5B8vYYiY.CVt1RlTTf8KbXBH3hsxY/GNooZaBBGWEc5:19000:0:99999:7:::
locked:!:19000:0:99999:7:::
locked2:!$5$whatever$hash:19000:0:99999:7:::
star:*:19000::::::
empty::19000::::::
bcryptuser:$2b$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy:19000::::::

malformedline
`

func TestEntwareVerify_Success(t *testing.T) {
	v := writeShadow(t, shadowFixture)
	if err := v.Verify("root", "Hello world!"); err != nil {
		t.Fatalf("Verify(root, correct) = %v, want nil", err)
	}
}

func TestEntwareVerify_WrongPassword(t *testing.T) {
	v := writeShadow(t, shadowFixture)
	if err := v.Verify("root", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Verify(root, wrong) = %v, want ErrInvalidCredentials", err)
	}
}

func TestEntwareVerify_UserNotFound(t *testing.T) {
	v := writeShadow(t, shadowFixture)
	if err := v.Verify("nobody", "x"); !errors.Is(err, ErrEntwareUserNotFound) {
		t.Fatalf("Verify(nobody) = %v, want ErrEntwareUserNotFound", err)
	}
}

func TestEntwareVerify_LockedAccounts(t *testing.T) {
	v := writeShadow(t, shadowFixture)
	for _, login := range []string{"locked", "locked2", "star", "empty"} {
		if err := v.Verify(login, "x"); !errors.Is(err, ErrEntwareAccountLocked) {
			t.Errorf("Verify(%s) = %v, want ErrEntwareAccountLocked", login, err)
		}
	}
}

func TestEntwareVerify_UnsupportedHash(t *testing.T) {
	v := writeShadow(t, shadowFixture)
	if err := v.Verify("bcryptuser", "x"); !errors.Is(err, ErrUnsupportedHash) {
		t.Fatalf("Verify(bcryptuser) = %v, want ErrUnsupportedHash", err)
	}
}

func TestEntwareVerify_MissingShadowFile(t *testing.T) {
	v := &EntwareVerifier{ShadowPath: filepath.Join(t.TempDir(), "does-not-exist")}
	if err := v.Verify("root", "x"); !errors.Is(err, ErrEntwareUnavailable) {
		t.Fatalf("Verify(missing file) = %v, want ErrEntwareUnavailable", err)
	}
}

// TestEntwareVerify_DummyKDFRunsForAbsentUser pins the anti-enumeration fix:
// a not-found or locked account still burns a full KDF (against the fixed
// dummy hash) so its verification time matches the wrong-password path. The
// discarded result lands in dummyKDFSink, which both proves the KDF ran and
// prevents the compiler from optimizing the equalizing work away.
func TestEntwareVerify_DummyKDFRunsForAbsentUser(t *testing.T) {
	v := writeShadow(t, shadowFixture)
	for _, tc := range []struct {
		login   string
		wantErr error
	}{
		{"nobody", ErrEntwareUserNotFound},
		{"locked", ErrEntwareAccountLocked},
	} {
		dummyKDFSink = nil
		if err := v.Verify(tc.login, "some-password"); !errors.Is(err, tc.wantErr) {
			t.Fatalf("Verify(%s) = %v, want %v", tc.login, err, tc.wantErr)
		}
		// The dummy KDF must have executed (wrong password vs the fixed $6$
		// hash → ErrMismatch), proving it was not skipped/optimized away.
		if !errors.Is(dummyKDFSink, shadowcrypt.ErrMismatch) {
			t.Fatalf("Verify(%s): dummyKDFSink = %v, want a completed KDF (ErrMismatch)", tc.login, dummyKDFSink)
		}
	}
}

// TestEntwareVerify_AbsentAndWrongPasswordTakeComparableTime is a coarse,
// non-flaky guard that the absent-user path is no longer a fast early return:
// it must take a KDF-sized amount of time, not microseconds. A wide margin
// keeps it stable under -race / loaded CI.
func TestEntwareVerify_AbsentAndWrongPasswordTakeComparableTime(t *testing.T) {
	v := writeShadow(t, shadowFixture)

	measure := func(login, pw string) time.Duration {
		start := time.Now()
		_ = v.Verify(login, pw)
		return time.Since(start)
	}
	// Warm up (file cache, allocator) so the first call's cost is not blamed
	// on the KDF.
	_ = v.Verify("root", "warmup")

	wrong := measure("root", "definitely-wrong") // full real KDF
	absent := measure("nobody", "definitely-wrong")

	// The absent path must spend at least a small fraction of the real-KDF
	// time; a bare not-found early return would be orders of magnitude faster.
	if absent < wrong/8 {
		t.Fatalf("absent-user path too fast: %v vs wrong-password %v (timing channel not closed)", absent, wrong)
	}
}

// ── /opt/etc/passwd fallback (учётки без shadow-записи) ──────────

// writeShadowPasswd builds a verifier over both fixture files.
func writeShadowPasswd(t *testing.T, shadow, passwd string) *EntwareVerifier {
	t.Helper()
	dir := t.TempDir()
	sp := filepath.Join(dir, "shadow")
	pp := filepath.Join(dir, "passwd")
	if err := os.WriteFile(sp, []byte(shadow), 0600); err != nil {
		t.Fatalf("write shadow: %v", err)
	}
	if err := os.WriteFile(pp, []byte(passwd), 0644); err != nil {
		t.Fatalf("write passwd: %v", err)
	}
	return &EntwareVerifier{ShadowPath: sp, PasswdPath: pp}
}

// Хэш "Hello world!" ($5$, официальный вектор) лежит прямо в passwd —
// так делают busybox без shadow-фичи и рукотворные учётки.
func TestEntwareVerify_PasswdFallback(t *testing.T) {
	v := writeShadowPasswd(t,
		"root:$5$saltstring$5B8vYYiY.CVt1RlTTf8KbXBH3hsxY/GNooZaBBGWEc5:19000:0:99999:7:::\n",
		"inline:$5$saltstring$5B8vYYiY.CVt1RlTTf8KbXBH3hsxY/GNooZaBBGWEc5:0:0:inline:/opt/root:/opt/bin/sh\n")
	if err := v.Verify("inline", "Hello world!"); err != nil {
		t.Fatalf("Verify(inline via passwd) = %v, want nil", err)
	}
	if err := v.Verify("inline", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Verify(inline, wrong) = %v, want ErrInvalidCredentials", err)
	}
}

// Отсутствующий shadow целиком → фолбэк на passwd (зеркало libc, когда
// shadow-suite не установлен вовсе).
func TestEntwareVerify_PasswdFallbackWithoutShadowFile(t *testing.T) {
	dir := t.TempDir()
	pp := filepath.Join(dir, "passwd")
	if err := os.WriteFile(pp, []byte("root:$5$saltstring$5B8vYYiY.CVt1RlTTf8KbXBH3hsxY/GNooZaBBGWEc5:0:0::/:/bin/sh\n"), 0644); err != nil {
		t.Fatalf("write passwd: %v", err)
	}
	v := &EntwareVerifier{ShadowPath: filepath.Join(dir, "no-shadow"), PasswdPath: pp}
	if err := v.Verify("root", "Hello world!"); err != nil {
		t.Fatalf("Verify(root via passwd, no shadow) = %v, want nil", err)
	}
}

// "x" в passwd означает «хэш в shadow» и НЕ является хэшем: пользователь,
// которого нет в shadow, остаётся not-found (никакого входа по паролю "x").
func TestEntwareVerify_PasswdXIsNotAHash(t *testing.T) {
	v := writeShadowPasswd(t,
		"root:$5$saltstring$5B8vYYiY.CVt1RlTTf8KbXBH3hsxY/GNooZaBBGWEc5:19000:0:99999:7:::\n",
		"ghost:x:0:0::/:/bin/sh\n")
	if err := v.Verify("ghost", "x"); !errors.Is(err, ErrEntwareUserNotFound) {
		t.Fatalf("Verify(ghost) = %v, want ErrEntwareUserNotFound", err)
	}
}

// Запись в shadow имеет приоритет: даже при валидном хэше в passwd
// заблокированный shadow-аккаунт остаётся заблокированным.
func TestEntwareVerify_ShadowLockWinsOverPasswd(t *testing.T) {
	v := writeShadowPasswd(t,
		"user:!:19000:0:99999:7:::\n",
		"user:$5$saltstring$5B8vYYiY.CVt1RlTTf8KbXBH3hsxY/GNooZaBBGWEc5:0:0::/:/bin/sh\n")
	if err := v.Verify("user", "Hello world!"); !errors.Is(err, ErrEntwareAccountLocked) {
		t.Fatalf("Verify(locked-in-shadow) = %v, want ErrEntwareAccountLocked", err)
	}
}

// DES-хэш (13 символов, без $-префикса) в shadow — исторический дефолт
// busybox passwd; вход должен работать.
func TestEntwareVerify_DESHash(t *testing.T) {
	v := writeShadow(t, "root:Nrzc8yOSz4klA:19000:0:99999:7:::\n")
	if err := v.Verify("root", "keenetic"); err != nil {
		t.Fatalf("Verify(root, DES hash) = %v, want nil", err)
	}
	if err := v.Verify("root", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Verify(root, wrong vs DES) = %v, want ErrInvalidCredentials", err)
	}
}

// Метка схемы в ошибке — диагностика для Журнала.
func TestHashSchemeLabel(t *testing.T) {
	for in, want := range map[string]string{
		"$y$j9T$salt$hash": "$y$ (yescrypt)",
		"$2b$10$whatever":  "$2b$ (bcrypt)",
		"$sha1$40000$x$y":  "$sha1$",
		"garbage":          "неизвестный",
	} {
		if got := hashSchemeLabel(in); got != want {
			t.Errorf("hashSchemeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
