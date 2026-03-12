package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	SessionTTL      = 24 * time.Hour
	SessionCookie   = "awg_session"
	tokenLength     = 32
	cleanupInterval = 5 * time.Minute
)

// Session represents an authenticated user session.
type Session struct {
	Token     string
	Login     string
	CreatedAt time.Time
	LastSeen  time.Time
}

// SessionLogger provides logging for session events.
type SessionLogger interface {
	Warnf(format string, args ...interface{})
}

// SessionStore manages user sessions in memory.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	stopCh   chan struct{}
	log      SessionLogger
}

// NewSessionStore creates a new session store and starts cleanup goroutine.
func NewSessionStore() *SessionStore {
	s := &SessionStore{
		sessions: make(map[string]*Session),
		stopCh:   make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

// SetLogger sets the logger for session events.
func (s *SessionStore) SetLogger(log SessionLogger) {
	s.mu.Lock()
	s.log = log
	s.mu.Unlock()
}

// Create creates a new session for the given login and returns the token.
func (s *SessionStore) Create(login string) (string, error) {
	token, err := generateToken()
	if err != nil {
		return "", err
	}

	now := time.Now()
	session := &Session{
		Token:     token,
		Login:     login,
		CreatedAt: now,
		LastSeen:  now,
	}

	s.mu.Lock()
	s.sessions[token] = session
	s.mu.Unlock()

	return token, nil
}

// Get retrieves a session by token and updates LastSeen.
// Returns nil if session doesn't exist or is expired.
func (s *SessionStore) Get(token string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[token]
	if !ok {
		return nil
	}

	// Check if expired
	if time.Since(session.LastSeen) > SessionTTL {
		if s.log != nil {
			s.log.Warnf("Session expired for user %q (inactive %s)", session.Login, time.Since(session.LastSeen).Truncate(time.Second))
		}
		delete(s.sessions, token)
		return nil
	}

	// Update last seen (session activity extends TTL)
	session.LastSeen = time.Now()
	return session
}

// Delete removes a session by token.
func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// Stop stops the cleanup goroutine.
func (s *SessionStore) Stop() {
	close(s.stopCh)
}

// cleanupLoop periodically removes expired sessions.
func (s *SessionStore) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanup()
		case <-s.stopCh:
			return
		}
	}
}

// cleanup removes all expired sessions.
func (s *SessionStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for token, session := range s.sessions {
		if now.Sub(session.LastSeen) > SessionTTL {
			if s.log != nil {
				s.log.Warnf("Session expired for user %q (inactive %s, cleanup)", session.Login, now.Sub(session.LastSeen).Truncate(time.Second))
			}
			delete(s.sessions, token)
		}
	}
}

// generateToken creates a cryptographically secure random token.
func generateToken() (string, error) {
	bytes := make([]byte, tokenLength)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
