package subscription

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func clearHappKeysForTest(t *testing.T) {
	t.Helper()
	happKeysMu.Lock()
	happParsedKeys = nil
	happKeysMu.Unlock()
}

func TestMergeHeaders_ProbeProfileOverridesUserUA(t *testing.T) {
	user := []Header{
		{Name: "Authorization", Value: "Bearer tok"},
		{Name: "User-Agent", Value: "custom/1.0"},
	}
	probe := []Header{{Name: "user-agent", Value: "Happ/4.6.0"}}

	got := mergeHeaders(user, probe)

	if len(got) != 2 {
		t.Fatalf("merged headers = %v, want 2 entries", got)
	}
	if got[0].Name != "Authorization" || got[0].Value != "Bearer tok" {
		t.Errorf("user auth header lost: %v", got)
	}
	if got[1].Value != "Happ/4.6.0" {
		t.Errorf("probe profile must win for User-Agent: %v", got)
	}
}

// Без ключей RSA детект обязан вернуть профиль-подсказку, а не ошибку:
// на ошибке фронт показывает 502 вместо приглашения ввести ключи.
func TestDetectHeaders_HappCryptWithoutKeys_ReturnsPrompt(t *testing.T) {
	clearHappKeysForTest(t)
	svc := NewService(nil, nil)

	profile, err := svc.DetectHeaders(context.Background(), "happ://crypt/YWJj", nil)
	if err != nil {
		t.Fatalf("DetectHeaders returned error %v, want prompt profile", err)
	}
	if !profile.IsEncrypted || profile.DecryptedURL != "" {
		t.Fatalf("profile = %+v, want IsEncrypted with empty DecryptedURL", profile)
	}
}

func TestDetectHeaders_KeepsUserHeaders(t *testing.T) {
	clearHappKeysForTest(t)
	var seenAuth, seenUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@h1.example:443?security=tls&sni=h#n1\n"))
	}))
	defer srv.Close()

	svc := NewService(nil, nil)
	profile, err := svc.DetectHeaders(context.Background(), srv.URL,
		[]Header{{Name: "Authorization", Value: "Bearer tok"}})
	if err != nil {
		t.Fatalf("DetectHeaders: %v", err)
	}
	if profile.ServerCount != 1 {
		t.Fatalf("ServerCount = %d, want 1", profile.ServerCount)
	}
	if seenAuth != "Bearer tok" {
		t.Errorf("probe dropped the user Authorization header (got %q)", seenAuth)
	}
	if !strings.HasPrefix(seenUA, "Happ/") {
		t.Errorf("probe User-Agent = %q, want the HAPP profile", seenUA)
	}
}

// Refresh не должен молча уходить качать всё ещё зашифрованную ссылку,
// когда ключей нет: NormalizeSubscriptionURL ошибку расшифровки глушит.
func TestRefresh_HappCryptWithoutKeys_Fails(t *testing.T) {
	clearHappKeysForTest(t)
	svc, _ := newTestService(t)
	sub := createURLSubWithMembers(t, svc, 1)

	encrypted := "happ://crypt/YWJj"
	if _, err := svc.store.Update(sub.ID, UpdatePatch{URL: &encrypted}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Refresh(context.Background(), sub.ID)
	if err == nil {
		t.Fatal("Refresh succeeded on an undecryptable happ:// link, want error")
	}
	if !strings.Contains(err.Error(), "расшифровки") {
		t.Errorf("error = %v, want it to name the decryption failure", err)
	}
}
