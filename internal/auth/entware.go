package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/auth/shadowcrypt"
)

// defaultShadowPath is the Entware shadow database on Keenetic routers.
const defaultShadowPath = "/opt/etc/shadow"

// defaultPasswdPath is the Entware passwd database. Accounts created without
// a shadow entry (hand-made users, old busybox without the shadow feature)
// carry their crypt hash directly in the second passwd field — libc/dropbear
// fall back to it, and so do we.
const defaultPasswdPath = "/opt/etc/passwd"

var (
	// ErrEntwareUnavailable means /opt/etc/shadow is missing or unreadable —
	// Entware credential verification cannot be performed at all.
	ErrEntwareUnavailable = errors.New("entware shadow database unavailable")
	// ErrEntwareUserNotFound means the login has no shadow entry.
	ErrEntwareUserNotFound = errors.New("entware user not found")
	// ErrEntwareAccountLocked means the entry exists but its hash field is
	// empty or locked ("!", "*") — the account cannot be used for login.
	ErrEntwareAccountLocked = errors.New("entware account locked")
	// ErrUnsupportedHash means the stored hash uses a scheme shadowcrypt
	// cannot verify (yescrypt, bcrypt, DES, ...). Toward the HTTP client
	// this is indistinguishable from invalid credentials.
	ErrUnsupportedHash = errors.New("неподдерживаемый формат хэша")
)

// EntwareVerifier verifies logins against the Entware shadow database
// entirely locally — no NDMS /auth call, hence no router-side
// authentication notifications (issue #441).
type EntwareVerifier struct {
	// ShadowPath/PasswdPath are variable so tests can point at fixtures.
	ShadowPath string
	PasswdPath string
}

// NewEntwareVerifier returns a verifier reading /opt/etc/shadow with a
// /opt/etc/passwd fallback (mirroring the libc getspnam→getpwnam order).
func NewEntwareVerifier() *EntwareVerifier {
	return &EntwareVerifier{ShadowPath: defaultShadowPath, PasswdPath: defaultPasswdPath}
}

// dummyShadowHash is a fixed, well-formed $6$ SHA-512-crypt hash with the
// default 5000 rounds (the official Drepper "Hello world!" vector). It exists
// only to run a full KDF of comparable cost when the account is absent or
// locked, so those cases take about the same time as a wrong password against
// a real $6$ entry — closing the user-enumeration timing channel (issue #441).
// No password ever matches it; the result is intentionally discarded.
const dummyShadowHash = "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"

// dummyKDFSink receives the discarded dummy-KDF result. Writing to a
// package-level variable prevents the compiler from proving the call dead and
// optimizing the equalizing work away.
var dummyKDFSink error

// runDummyKDF performs a full SHA-512-crypt computation whose cost matches the
// wrong-password path, then throws the result away. See dummyShadowHash.
func runDummyKDF(password string) {
	dummyKDFSink = shadowcrypt.Verify(password, dummyShadowHash)
}

// Verify checks login/password against the shadow database. Returns nil on
// success. Failures are distinguished internally (ErrEntwareUnavailable,
// ErrEntwareUserNotFound, ErrEntwareAccountLocked, ErrUnsupportedHash,
// ErrInvalidCredentials) so the caller can log the reason — but the HTTP
// layer must always collapse them into the same 401 invalid-credentials
// response to avoid user enumeration.
func (v *EntwareVerifier) Verify(login, password string) error {
	hash, err := v.lookupHash(login)
	if err != nil {
		// The account is absent or locked, so there is no stored hash to
		// verify against — but a wrong password for an EXISTING user runs the
		// full 5000-round KDF. Run an equal-cost dummy KDF here so response
		// latency does not reveal which logins exist. The internal error
		// distinction is preserved for logging.
		if errors.Is(err, ErrEntwareUserNotFound) || errors.Is(err, ErrEntwareAccountLocked) {
			runDummyKDF(password)
		}
		return err
	}
	switch err := shadowcrypt.Verify(password, hash); {
	case err == nil:
		return nil
	case errors.Is(err, shadowcrypt.ErrMismatch):
		return ErrInvalidCredentials
	case errors.Is(err, shadowcrypt.ErrUnsupported):
		// Name the scheme in the log-visible error — «$y$ (yescrypt)» tells
		// the user immediately why the login cannot work and that re-running
		// `passwd` in Entware (writes $5$) fixes it.
		return fmt.Errorf("%w: схема %s (login %q)", ErrUnsupportedHash, hashSchemeLabel(hash), login)
	default:
		// Malformed hash — treat like an unsupported entry.
		return fmt.Errorf("%w: %v", ErrUnsupportedHash, err)
	}
}

// hashSchemeLabel returns a human-readable label of a crypt hash scheme for
// diagnostics ("$y$ (yescrypt)", "$2b$ (bcrypt)", "$prefix$", "неизвестный").
func hashSchemeLabel(hash string) string {
	if strings.HasPrefix(hash, "$") {
		if end := strings.Index(hash[1:], "$"); end >= 0 {
			prefix := hash[:end+2]
			switch prefix {
			case "$y$", "$7$":
				return prefix + " (yescrypt)"
			case "$2a$", "$2b$", "$2y$":
				return prefix + " (bcrypt)"
			}
			return prefix
		}
	}
	return "неизвестный"
}

// lookupHash finds the crypt hash for login: the shadow file first, then —
// when the user has no shadow entry (or the shadow db is missing entirely) —
// the passwd file, whose second field carries the hash for accounts created
// without shadow. Mirrors the libc getspnam→getpwnam order that dropbear
// and login(1) follow, so any account that works over SSH works here too.
func (v *EntwareVerifier) lookupHash(login string) (string, error) {
	hash, shadowErr := lookupHashIn(v.ShadowPath, login)
	if shadowErr == nil {
		return hash, nil
	}
	if v.PasswdPath != "" &&
		(errors.Is(shadowErr, ErrEntwareUserNotFound) || errors.Is(shadowErr, ErrEntwareUnavailable)) {
		// "x"/"*"/"!" в passwd — не хэш (учётка shadow-managed или
		// заблокирована): остаёмся с исходной shadow-ошибкой.
		if h, err := lookupHashIn(v.PasswdPath, login); err == nil && h != "x" {
			return h, nil
		}
	}
	return "", shadowErr
}

// lookupHashIn scans one passwd/shadow-format file for login's hash field.
func lookupHashIn(path, login string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrEntwareUnavailable, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 2 || fields[0] != login {
			continue
		}
		hash := fields[1]
		if hash == "" || strings.HasPrefix(hash, "!") || strings.HasPrefix(hash, "*") {
			return "", ErrEntwareAccountLocked
		}
		return hash, nil
	}
	return "", ErrEntwareUserNotFound
}
