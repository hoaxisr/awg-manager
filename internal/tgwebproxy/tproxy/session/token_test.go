package session

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/config"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/frame"
)

func TestTokenProvenanceSurvivesExpiryAndRestart(t *testing.T) {
	value := config.Defaults()
	value.Profiles = []config.Profile{{Name: "test", Capability: [32]byte{2}, Backend: "127.0.0.1:1"}}
	key := [32]byte{1}
	manager := NewManager(value, key)
	defer manager.Shutdown()
	bootstrap, err := manager.IssueBootstrap(&value.Profiles[0], "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(bootstrap, "127.0.0.1", frame.Encode(frame.Hello, 0, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	created.Session.Close()
	waitFor(t, func() bool { return manager.Capacity().Sessions == 0 })
	manager.mu.Lock()
	manager.removeExpiredBootstrapsLocked(time.Now().Add(time.Hour))
	manager.mu.Unlock()
	if manager.HasBootstrap(bootstrap) {
		t.Fatal("expired bootstrap still authorized")
	}
	if _, err := manager.Get(created.Token); err == nil {
		t.Fatal("closed session still authorized")
	}
	restarted := NewManager(value, key)
	defer restarted.Shutdown()
	other := NewManager(value, [32]byte{3})
	defer other.Shutdown()
	for token, want := range map[string]TokenClass{bootstrap: TokenBootstrap, created.Token: TokenSession} {
		if len(token) != 43 {
			t.Fatal("wire token length changed")
		}
		for _, current := range []*Manager{manager, restarted} {
			if current.ClassifyToken(token) != want {
				t.Fatal("lost token provenance after expiry or restart")
			}
		}
		if other.ClassifyToken(token) != TokenExternal {
			t.Fatal("token authenticated under a different key")
		}
		decoded, _ := base64.RawURLEncoding.DecodeString(token)
		for i := range decoded {
			decoded[i] ^= 1
			if manager.ClassifyToken(base64.RawURLEncoding.EncodeToString(decoded)) != TokenExternal {
				t.Fatal("tampered token authenticated")
			}
			decoded[i] ^= 1
		}
	}
	if _, err := restarted.Create(bootstrap, "127.0.0.1", frame.Encode(frame.Hello, 0, []byte{1})); err == nil {
		t.Fatal("authentic but missing bootstrap authorized after restart")
	}
}

// A failed update restores the previous release, which draws all 16 nonce
// bytes at random and knows no filter tag. It must still recognize tokens
// issued by this release, or their carrier requests reach the website.
func TestPreviousReleaseRecognizesTaggedTokens(t *testing.T) {
	value := config.Defaults()
	value.Profiles = []config.Profile{{Name: "test", Capability: [32]byte{2}, Backend: "127.0.0.1:1"}}
	key := [32]byte{1}
	manager := NewManager(value, key)
	defer manager.Shutdown()
	bootstrap, err := manager.IssueBootstrap(&value.Profiles[0], "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(bootstrap, "127.0.0.1", frame.Encode(frame.Hello, 0, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	previousRelease := func(token string) TokenClass {
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(decoded) != 32 {
			return TokenExternal
		}
		for _, kind := range []TokenClass{TokenBootstrap, TokenSession} {
			mac := hmac.New(sha256.New, key[:])
			_, _ = mac.Write([]byte("tproxy-server-token-v1\x00"))
			_, _ = mac.Write([]byte{byte(kind)})
			_, _ = mac.Write(decoded[:16])
			if hmac.Equal(decoded[16:], mac.Sum(nil)[:16]) {
				return kind
			}
		}
		return TokenExternal
	}
	if previousRelease(bootstrap) != TokenBootstrap || previousRelease(created.Token) != TokenSession {
		t.Fatal("previous release does not recognize current tokens")
	}
}
