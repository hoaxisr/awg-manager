package tgwebproxy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The API limits reveals per (user, secret revision). The revision that was
// rate-limited must be the revision of the secret actually disclosed, even if
// the user's secret changes concurrently between resolution and building.
func TestReveal_LimitedRevisionMatchesDisclosedSecret(t *testing.T) {
	svc, _, _ := setupTestService(t)
	svc.config = revealScopeConfig()

	var limitedID, limitedRev string
	data, err := svc.RevealUserSecretLimited("u-a", func(id, rev string) bool {
		limitedID, limitedRev = id, rev
		// Concurrent regenerate lands after the limiter decision.
		svc.mu.Lock()
		svc.config.Users[0].Secret = "0123456789abcdef0123456789abcdef"
		svc.mu.Unlock()
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if limitedID != "u-a" || data.UserID != "u-a" {
		t.Fatalf("limited %q, disclosed %q", limitedID, data.UserID)
	}
	if got := revealRevision(data.Secret); got != limitedRev {
		t.Fatalf("disclosed secret revision %q differs from rate-limited revision %q", got, limitedRev)
	}
}

func TestReveal_LimiterRejectionDisclosesNothing(t *testing.T) {
	svc, _, _ := setupTestService(t)
	svc.config = revealScopeConfig()

	data, err := svc.RevealUserSecretLimited("u-a", func(string, string) bool { return false })
	if !errors.Is(err, ErrRevealRateLimited) {
		t.Fatalf("err = %v, want ErrRevealRateLimited", err)
	}
	raw, _ := json.Marshal(data)
	for _, sec := range []string{revealSecretA, revealSecretB, revealLegacy, "tg://"} {
		if strings.Contains(string(raw), sec) {
			t.Fatalf("rejected reveal carries secret material: %s", raw)
		}
	}
}
