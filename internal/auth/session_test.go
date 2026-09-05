package auth

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestSessionStore_CreateAndGet(t *testing.T) {
	store := NewSessionStore(nil)
	t.Cleanup(store.Stop)

	token, err := store.Create("admin")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if token == "" {
		t.Fatal("token is empty")
	}
	if len(token) != 64 {
		t.Fatalf("token len = %d, want 64", len(token))
	}
	if _, err := hex.DecodeString(token); err != nil {
		t.Fatalf("token is not valid hex: %v", err)
	}

	s := store.Get(token)
	if s == nil {
		t.Fatal("Get() returned nil")
	}
	if s.Login != "admin" {
		t.Fatalf("session.Login = %q, want %q", s.Login, "admin")
	}
	if s.Token != token {
		t.Fatalf("session.Token = %q, want %q", s.Token, token)
	}
}

func TestSessionStore_GetUpdatesLastSeen(t *testing.T) {
	store := NewSessionStore(nil)
	t.Cleanup(store.Stop)

	token, err := store.Create("admin")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	store.mu.RLock()
	before := store.sessions[token].LastSeen
	store.mu.RUnlock()

	time.Sleep(time.Millisecond)
	got := store.Get(token)
	if got == nil {
		t.Fatal("Get() returned nil")
	}
	if !got.LastSeen.After(before) {
		t.Fatalf("LastSeen was not updated: before=%v after=%v", before, got.LastSeen)
	}
}

func TestSessionStore_GetMissingReturnsNil(t *testing.T) {
	store := NewSessionStore(nil)
	t.Cleanup(store.Stop)

	if got := store.Get("missing"); got != nil {
		t.Fatalf("Get(missing) = %#v, want nil", got)
	}
}

func TestSessionStore_DeleteRemovesSession(t *testing.T) {
	store := NewSessionStore(nil)
	t.Cleanup(store.Stop)

	token, err := store.Create("admin")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	store.Delete(token)
	if got := store.Get(token); got != nil {
		t.Fatalf("Get() after Delete = %#v, want nil", got)
	}
}

func TestSessionStore_ExpiredDeletedOnGet(t *testing.T) {
	store := NewSessionStore(nil)
	t.Cleanup(store.Stop)

	store.mu.Lock()
	store.sessions["expired"] = &Session{
		Token:    "expired",
		Login:    "admin",
		LastSeen: time.Now().Add(-defaultSessionTTL - time.Second),
	}
	store.mu.Unlock()

	if got := store.Get("expired"); got != nil {
		t.Fatalf("Get(expired) = %#v, want nil", got)
	}

	store.mu.RLock()
	_, ok := store.sessions["expired"]
	store.mu.RUnlock()
	if ok {
		t.Fatal("expired session was not removed from map")
	}
}

func TestSessionStore_CleanupRemovesExpiredKeepsActive(t *testing.T) {
	store := NewSessionStore(nil)
	t.Cleanup(store.Stop)

	store.mu.Lock()
	store.sessions["expired"] = &Session{
		Token:    "expired",
		Login:    "admin",
		LastSeen: time.Now().Add(-defaultSessionTTL - time.Second),
	}
	store.sessions["active"] = &Session{
		Token:    "active",
		Login:    "admin",
		LastSeen: time.Now(),
	}
	store.mu.Unlock()

	store.cleanup()

	store.mu.RLock()
	_, hasExpired := store.sessions["expired"]
	_, hasActive := store.sessions["active"]
	store.mu.RUnlock()
	if hasExpired {
		t.Fatal("expired session still present after cleanup")
	}
	if !hasActive {
		t.Fatal("active session removed by cleanup")
	}
}

func TestSessionStore_TTLDefaults(t *testing.T) {
	t.Run("nil getter", func(t *testing.T) {
		store := NewSessionStore(nil)
		t.Cleanup(store.Stop)
		if got := store.TTL(); got != defaultSessionTTL {
			t.Fatalf("TTL() = %v, want %v", got, defaultSessionTTL)
		}
	})
	t.Run("non-positive getter falls back", func(t *testing.T) {
		store := NewSessionStore(func() time.Duration { return 0 })
		t.Cleanup(store.Stop)
		if got := store.TTL(); got != defaultSessionTTL {
			t.Fatalf("TTL() = %v, want %v", got, defaultSessionTTL)
		}
	})
	t.Run("configured getter wins", func(t *testing.T) {
		store := NewSessionStore(func() time.Duration { return 2 * time.Hour })
		t.Cleanup(store.Stop)
		if got := store.TTL(); got != 2*time.Hour {
			t.Fatalf("TTL() = %v, want 2h", got)
		}
	})
}

// TestSessionStore_LiveTTLShorteningExpiresExistingSessions pins the live
// semantics: the TTL getter is consulted on every expiry check, so
// shortening the configured TTL immediately invalidates sessions whose
// idle time already exceeds the new value.
func TestSessionStore_LiveTTLShorteningExpiresExistingSessions(t *testing.T) {
	ttl := 24 * time.Hour
	store := NewSessionStore(func() time.Duration { return ttl })
	t.Cleanup(store.Stop)

	token, err := store.Create("admin")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// Simulate 2h of inactivity.
	store.mu.Lock()
	store.sessions[token].LastSeen = time.Now().Add(-2 * time.Hour)
	store.mu.Unlock()

	if store.Get(token) == nil {
		t.Fatal("session expired under 24h TTL after 2h idle")
	}
	// Re-set idle time (Get refreshed LastSeen), then shorten the TTL.
	store.mu.Lock()
	store.sessions[token].LastSeen = time.Now().Add(-2 * time.Hour)
	store.mu.Unlock()
	ttl = time.Hour

	if got := store.Get(token); got != nil {
		t.Fatalf("Get() = %#v, want nil after TTL shortened below idle time", got)
	}
}

func TestSessionStore_CleanupUsesLiveTTL(t *testing.T) {
	ttl := 24 * time.Hour
	store := NewSessionStore(func() time.Duration { return ttl })
	t.Cleanup(store.Stop)

	store.mu.Lock()
	store.sessions["idle2h"] = &Session{
		Token:    "idle2h",
		Login:    "admin",
		LastSeen: time.Now().Add(-2 * time.Hour),
	}
	store.mu.Unlock()

	store.cleanup()
	store.mu.RLock()
	_, ok := store.sessions["idle2h"]
	store.mu.RUnlock()
	if !ok {
		t.Fatal("session removed by cleanup under 24h TTL")
	}

	ttl = time.Hour
	store.cleanup()
	store.mu.RLock()
	_, ok = store.sessions["idle2h"]
	store.mu.RUnlock()
	if ok {
		t.Fatal("session survived cleanup after TTL shortened below idle time")
	}
}

// Токен — криптослучайный, полной длины и уникальный: два входа дают два
// разных токена. Мутант «константный токен» проходил все тесты стора — они
// создавали по одной сессии и сравнивали её с самой собой.
func TestSessionStore_TokensAreRandomAndDistinct(t *testing.T) {
	store := NewSessionStore(nil)
	t.Cleanup(store.Stop)

	a, err := store.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("два входа дали один токен")
	}
	if len(a) != tokenLength*2 {
		t.Fatalf("длина токена %d, ждали %d hex-символов", len(a), tokenLength*2)
	}
	if strings.Trim(a, "0") == "" {
		t.Fatal("токен из нулей — генератор не случаен")
	}
	if store.Get(a) == nil || store.Get(b) == nil {
		t.Fatal("обе сессии обязаны жить независимо")
	}
}
