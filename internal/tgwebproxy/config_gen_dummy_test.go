package tgwebproxy

import (
	"encoding/json"
	"testing"
)

// Все пользователи выключены: профиль-заглушка не получает ни нулевой
// секрет, ни секрет выключенного пользователя.
func TestGenerateProfiles_DisabledUsersGetRandomDummy(t *testing.T) {
	sec := "00112233445566778899aabbccddeeff"
	cfg := Config{
		Enabled: true,
		Secret:  sec,
		Users:   []TelegramUser{{ID: "default", Secret: sec, Enabled: false}},
	}
	b, err := GenerateProfilesJSON(cfg)
	if err != nil {
		t.Fatalf("GenerateProfilesJSON: %v", err)
	}
	var parsed struct {
		Profiles []struct {
			Secret string `json:"secret"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil || len(parsed.Profiles) != 1 {
		t.Fatalf("profiles: %v %+v", err, parsed)
	}
	got := parsed.Profiles[0].Secret
	if got == sec || got == "00000000000000000000000000000000" || len(got) != 32 {
		t.Fatalf("unexpected dummy secret %q", got)
	}
}
