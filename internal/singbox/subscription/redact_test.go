package subscription

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	subURL      = "https://sub.example.com/sub/Tok3nAbc"
	redirectURL = "https://cdn.example.net/r/Hop5ecret?key=K3y"
	shareLink   = "vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@h1.example:443?security=tls#A"
)

var secrets = []string{"Tok3nAbc", "Hop5ecret", "K3y", "3a3b1c2e"}

func assertNoSecret(t *testing.T, where, got string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(got, s) {
			t.Fatalf("%s: %q survived in %q", where, s, got)
		}
	}
}

// TestMaskURL_CatchesWhatExactMatchMisses — MaskURL заменял только точную
// строку настроенного адреса. Ошибка на хопе редиректа цитирует уже новый
// адрес, а ошибка парсера — share-link с uuid сервера. Оба доходят до
// lastError, который показывают REST, веб-интерфейс и журнал.
func TestMaskURL_CatchesWhatExactMatchMisses(t *testing.T) {
	msg := `Get "` + redirectURL + `": x509: unknown authority; line 1 (vless): parse "` + shareLink + `": invalid port; from ` + subURL
	got := MaskURL(msg, subURL)
	assertNoSecret(t, "MaskURL", got)
	if !strings.Contains(got, "<subscription-url>") {
		t.Fatalf("the configured address must still read as <subscription-url>: %q", got)
	}
	if !strings.Contains(got, "cdn.example.net") {
		t.Fatalf("the redirect host tells the reader which hop failed: %q", got)
	}
}

// TestStore_ScrubsLastErrorSavedBeforeTheFix — ошибка, записанная до
// исправления, лежит на диске с токеном и отдаётся REST до следующей
// удачной загрузки, а у выключенной подписки или без автообновления —
// бессрочно. Загрузка хранилища очищает её и сохраняет файл.
func TestStore_ScrubsLastErrorSavedBeforeTheFix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	legacy := `[{"id":"a1","label":"x","url":"` + subURL + `","enabled":false,"lastError":"Get \"` + redirectURL + `\": EOF; parse \"` + shareLink + `\""}]`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := store.Get("a1")
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "loaded lastError", sub.LastError)
	if sub.LastError == "" {
		t.Fatal("the failure must stay recorded, only scrubbed")
	}
	// The file keeps the subscription's own url, token and all: that is
	// its configuration. Only the stored error is checked.
	onDisk, _ := os.ReadFile(path)
	var saved []struct {
		LastError string `json:"lastError"`
	}
	if err := json.Unmarshal(onDisk, &saved); err != nil || len(saved) != 1 {
		t.Fatalf("file on disk: %v %s", err, onDisk)
	}
	assertNoSecret(t, "lastError on disk", saved[0].LastError)
}
