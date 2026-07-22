package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
)

func TestCaptchaInitSettingContentRefPrefersSettingsKey(t *testing.T) {
	setting := captchaInitSetting{
		Type:        "slider",
		Settings:    "legacy-settings",
		SettingsKey: "new-settings-key",
	}

	got := setting.contentRef()
	if got.Source != "settings_key" || got.Value != "new-settings-key" {
		t.Fatalf("contentRef = %+v, want settings_key/new-settings-key", got)
	}
}

func TestCaptchaInitSettingContentRefLegacySettings(t *testing.T) {
	setting := captchaInitSetting{
		Type:     "slider",
		Settings: "legacy-settings",
	}

	got := setting.contentRef()
	if got.Source != "captcha_settings" || got.Value != "legacy-settings" {
		t.Fatalf("contentRef = %+v, want captcha_settings/legacy-settings", got)
	}
}

func TestParseCaptchaPageSPA(t *testing.T) {
	html := `<html><head><script>
const powInput = "Pihj7tyAHFxdwm4t";
const difficulty = 2;
</script>
<script src="https://id.vk.ru/static/vkid/1.1.1374/not_robot_captcha.js"></script>
</head><body><div id="spa_root"></div></body></html>`

	page, err := parseCaptchaPage(html)
	if err != nil {
		t.Fatal(err)
	}
	if page.PowInput != "Pihj7tyAHFxdwm4t" || page.PowDifficulty != 2 {
		t.Fatalf("pow parse = %q/%d", page.PowInput, page.PowDifficulty)
	}
	if page.ScriptURL != "https://id.vk.ru/static/vkid/1.1.1374/not_robot_captcha.js" {
		t.Fatalf("script url = %q", page.ScriptURL)
	}
}

func TestParseCaptchaPageMissingPoW(t *testing.T) {
	if _, err := parseCaptchaPage(`<html><body><div id="spa_root"></div></body></html>`); err == nil {
		t.Fatal("expected error when powInput/difficulty absent")
	}
}

func TestSolveCaptchaPoWRawHex(t *testing.T) {
	got := solveCaptchaPoW(context.Background(), "input", 1)
	if len(got) != 64 {
		t.Fatalf("pow = %q, want 64-hex", got)
	}
	if !strings.HasPrefix(got, "0") {
		t.Fatalf("pow = %q, want leading zero for difficulty 1", got)
	}
	if again := solveCaptchaPoW(context.Background(), "input", 1); again != got {
		t.Fatalf("pow not deterministic: %q vs %q", got, again)
	}
}

func TestParseCaptchaInitSession(t *testing.T) {
	raw := map[string]any{"response": map[string]any{
		"show_captcha_type": "slider",
		"captcha_id":        "cid",
		"content_settings": []any{
			map[string]any{"type": "slider", "settings_key": "sliderkey"},
			map[string]any{"type": "sound", "settings_key": "soundkey"},
		},
	}}
	showType, content := parseCaptchaInitSession(raw)
	if showType != "slider" {
		t.Fatalf("show_type = %q, want slider", showType)
	}
	if content.Value != "sliderkey" || content.Source != "settings_key" {
		t.Fatalf("content = %+v, want sliderkey/settings_key", content)
	}
}

func TestParseCaptchaInitSessionCheckbox(t *testing.T) {
	raw := map[string]any{"response": map[string]any{
		"show_captcha_type": "checkbox",
		"content_settings": []any{
			map[string]any{"type": "slider", "settings_key": "k"},
		},
	}}
	showType, _ := parseCaptchaInitSession(raw)
	if showType != "checkbox" {
		t.Fatalf("show_type = %q, want checkbox", showType)
	}
}

func TestCaptchaDomainFromRedirectURI(t *testing.T) {
	tests := []struct {
		name        string
		redirectURI string
		want        string
	}{
		{
			name:        "vk com from query",
			redirectURI: "https://id.vk.ru/not_robot_captcha?domain=vk.com&session_token=x",
			want:        "vk.com",
		},
		{
			name:        "vk ru from query",
			redirectURI: "https://id.vk.ru/not_robot_captcha?domain=vk.ru&session_token=x",
			want:        "vk.ru",
		},
		{
			name:        "fallback without domain",
			redirectURI: "https://id.vk.ru/not_robot_captcha?session_token=x",
			want:        captchaDomain,
		},
		{
			name:        "fallback invalid url",
			redirectURI: "%",
			want:        captchaDomain,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := captchaDomainFromRedirectURI(tt.redirectURI); got != tt.want {
				t.Fatalf("domain = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCaptchaConnectionFieldsGatedByFamily(t *testing.T) {
	for _, family := range []browserprofile.Kind{browserprofile.Firefox, browserprofile.Safari} {
		if got := captchaConnectionFields(family, true); got != nil {
			t.Fatalf("%s must omit NetworkInformation telemetry, got %v", family, got)
		}
	}
	got := captchaConnectionFields(browserprofile.Chrome, true)
	if len(got) != 2 || got[0][0] != "connectionRtt" || got[1][0] != "connectionDownlink" {
		t.Fatalf("chrome connection fields = %v, want rtt+downlink", got)
	}
	// Реальный браузер шлёт фиксированную выборку, не привязанную к курсору.
	var rtt []int
	if err := json.Unmarshal([]byte(got[0][1]), &rtt); err != nil || len(rtt) != captchaConnectionSamples {
		t.Fatalf("connectionRtt = %s, want %d samples", got[0][1], captchaConnectionSamples)
	}
}

func TestCaptchaMobileAccelerometerIsGravity(t *testing.T) {
	var pts []struct{ X, Y, Z float64 }
	if err := json.Unmarshal([]byte(captchaMobileAccelerometer()), &pts); err != nil {
		t.Fatal(err)
	}
	if len(pts) != 3 {
		t.Fatalf("samples = %d, want 3", len(pts))
	}
	// Магнитуда каждого сэмпла ~ g (покоящийся телефон).
	for _, p := range pts {
		mag := p.X*p.X + p.Y*p.Y + p.Z*p.Z
		if mag < 64 || mag > 144 { // |a| в [8, 12]
			t.Fatalf("accelerometer magnitude^2 = %.1f, not gravity-like", mag)
		}
	}
}

func TestReverseSwapPairs(t *testing.T) {
	got := reverseSwapPairs([]int{1, 2, 3, 4, 5, 6})
	want := []int{5, 6, 3, 4, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reverseSwapPairs = %v, want %v", got, want)
		}
	}
}

func TestParseSliderPuzzleSessionDead(t *testing.T) {
	raw := map[string]any{"response": map[string]any{"status": "ERROR"}}
	_, err := parseSliderPuzzle(raw)
	if !errors.Is(err, errCaptchaSessionDead) {
		t.Fatalf("parseSliderPuzzle = %v, want errCaptchaSessionDead", err)
	}
}

func TestBuildSliderCursorIsBrowserLike(t *testing.T) {
	cursor := buildSliderCursor(5, 25)
	if got := captchaCursorPointCount(cursor); got < 70 {
		t.Fatalf("cursor point count = %d, want at least 70", got)
	}
}
