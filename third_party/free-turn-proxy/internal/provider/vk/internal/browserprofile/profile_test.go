package browserprofile

import (
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/tls-client/profiles"
)

func TestKindFromString(t *testing.T) {
	cases := []struct {
		in   string
		want Kind
	}{
		{"chrome", Chrome},
		{"firefox", Firefox},
		{"safari", Safari},
		{"", Chrome},
		{"unknown", Chrome},
	}
	for _, tc := range cases {
		if got := KindFromString(tc.in); got != tc.want {
			t.Fatalf("KindFromString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPlatformFromString(t *testing.T) {
	cases := []struct {
		in   string
		want Platform
	}{
		{"mobile", Mobile},
		{"desktop", Desktop},
		{"", Desktop},
		{"unknown", Desktop},
	}
	for _, tc := range cases {
		if got := PlatformFromString(tc.in); got != tc.want {
			t.Fatalf("PlatformFromString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestForChromeDesktop146(t *testing.T) {
	p := For(Chrome, Desktop)
	if p.UserAgent != "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36" {
		t.Fatalf("unexpected chrome UA: %q", p.UserAgent)
	}
	if p.SecChUa != `"Google Chrome";v="146", "Chromium";v="146", "Not)A;Brand";v="24"` {
		t.Fatalf("unexpected chrome sec-ch-ua: %q", p.SecChUa)
	}
}

func TestApplyFhttpOmitsClientHintsForFirefoxAndSafari(t *testing.T) {
	for _, kind := range []Kind{Firefox, Safari} {
		req, err := fhttp.NewRequest(fhttp.MethodGet, "https://example.com/", nil)
		if err != nil {
			t.Fatal(err)
		}
		ApplyFhttp(req, For(kind, Desktop))
		if req.Header.Get("sec-ch-ua") != "" {
			t.Fatalf("%s sec-ch-ua = %q, want empty", kind, req.Header.Get("sec-ch-ua"))
		}
	}
}

func TestFamilyAndPlatformSet(t *testing.T) {
	for _, kind := range []Kind{Chrome, Firefox, Safari} {
		for _, plat := range []Platform{Desktop, Mobile} {
			p := For(kind, plat)
			if Family(p) != kind {
				t.Fatalf("Family(For(%s,%s)) = %q, want %q", kind, plat, Family(p), kind)
			}
			if IsMobile(p) != (plat == Mobile) {
				t.Fatalf("IsMobile(For(%s,%s)) = %v", kind, plat, IsMobile(p))
			}
		}
	}
}

// Персона обязана быть самосогласованной: мобильная платформа -> мобильный UA,
// мобильный sec-ch-ua (для chromium) и непустой device для captcha.
func TestPersonaSelfConsistent(t *testing.T) {
	for _, kind := range []Kind{Chrome, Firefox, Safari} {
		p := For(kind, Mobile)
		ua := strings.ToLower(p.UserAgent)
		if !strings.Contains(ua, "mobile") && !strings.Contains(ua, "iphone") && !strings.Contains(ua, "android") {
			t.Fatalf("%s mobile UA not mobile: %q", kind, p.UserAgent)
		}
		if p.DeviceJSON == "" {
			t.Fatalf("%s mobile persona has empty DeviceJSON", kind)
		}
		if kind == Chrome && p.SecChUaMobile != "?1" {
			t.Fatalf("chrome mobile sec-ch-ua-mobile = %q, want ?1", p.SecChUaMobile)
		}
	}
	if For(Chrome, Desktop).SecChUaMobile != "?0" {
		t.Fatalf("chrome desktop sec-ch-ua-mobile != ?0")
	}
}

// TLS-отпечаток - часть персоны: Safari desktop/iOS различаются, Chrome/Firefox
// одинаковы на всех платформах (один TLS-стек).
func TestClientProfilePlatformAware(t *testing.T) {
	cases := []struct {
		family Kind
		plat   Platform
		want   profiles.ClientProfile
	}{
		{Safari, Desktop, profiles.Safari_16_0},
		{Safari, Mobile, profiles.Safari_IOS_17_0},
		{Chrome, Desktop, profiles.Chrome_146},
		{Chrome, Mobile, profiles.Chrome_146},
		{Firefox, Desktop, profiles.Firefox_148},
		{Firefox, Mobile, profiles.Firefox_148},
	}
	for _, tc := range cases {
		got := For(tc.family, tc.plat).ClientProfile()
		if got.GetClientHelloStr() != tc.want.GetClientHelloStr() {
			t.Fatalf("For(%s,%s).ClientProfile() = %s, want %s", tc.family, tc.plat, got.GetClientHelloStr(), tc.want.GetClientHelloStr())
		}
	}
}
