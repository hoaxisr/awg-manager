package browserprofile

import (
	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/tls-client/profiles"
)

type Kind string

const (
	Chrome  Kind = "chrome"
	Firefox Kind = "firefox"
	Safari  Kind = "safari"
)

type Platform string

const (
	Desktop Platform = "desktop"
	Mobile  Platform = "mobile"
)

// KindFromString мапит строку флага -browser в Kind. Пустое/неизвестное - Chrome.
func KindFromString(s string) Kind {
	switch s {
	case string(Chrome):
		return Chrome
	case string(Safari):
		return Safari
	case string(Firefox):
		return Firefox
	default:
		return Chrome
	}
}

// PlatformFromString мапит строку флага -platform в Platform. Пустое/неизвестное -
// Desktop.
func PlatformFromString(s string) Platform {
	if s == string(Mobile) {
		return Mobile
	}
	return Desktop
}

// Profile - самосогласованная браузерная личность: единственный источник UA,
// client hints, device-fingerprint и accept-language для VK control-plane и captcha.
// Строится один раз из (Family, Platform); не мутируется в рантайме.
type Profile struct {
	Family          Kind
	Platform        Platform
	UserAgent       string
	SecChUa         string // пусто для не-Chromium (Chromium-only client hint)
	SecChUaMobile   string
	SecChUaPlatform string
	AcceptLanguage  string
	// DeviceJSON - navigator/screen fingerprint для captcha componentDone.
	// Согласован с Family/Platform (Chromium-only поля есть только у Chrome).
	DeviceJSON string
}

const (
	deviceChromeDesktop  = `{"screenWidth":1920,"screenHeight":1080,"screenAvailWidth":1920,"screenAvailHeight":1032,"innerWidth":1147,"innerHeight":945,"devicePixelRatio":1,"language":"ru-RU","languages":["ru-RU"],"webdriver":false,"hardwareConcurrency":8,"deviceMemory":16,"connectionEffectiveType":"4g","notificationsPermission":"denied"}`
	deviceChromeMobile   = `{"screenWidth":393,"screenHeight":852,"screenAvailWidth":393,"screenAvailHeight":852,"innerWidth":393,"innerHeight":659,"devicePixelRatio":3,"language":"ru-RU","languages":["ru-RU"],"webdriver":false,"hardwareConcurrency":8,"deviceMemory":8,"connectionEffectiveType":"4g","notificationsPermission":"denied"}`
	deviceFirefoxDesktop = `{"screenWidth":1920,"screenHeight":1080,"screenAvailWidth":1920,"screenAvailHeight":1032,"innerWidth":1147,"innerHeight":945,"devicePixelRatio":1,"language":"ru-RU","languages":["ru-RU","ru"],"webdriver":false,"hardwareConcurrency":8,"notificationsPermission":"denied"}`
	deviceFirefoxMobile  = `{"screenWidth":393,"screenHeight":852,"screenAvailWidth":393,"screenAvailHeight":852,"innerWidth":393,"innerHeight":659,"devicePixelRatio":3,"language":"ru-RU","languages":["ru-RU","ru"],"webdriver":false,"hardwareConcurrency":8,"notificationsPermission":"denied"}`
	deviceSafariDesktop  = `{"screenWidth":1512,"screenHeight":982,"screenAvailWidth":1512,"screenAvailHeight":944,"innerWidth":1147,"innerHeight":870,"devicePixelRatio":2,"language":"ru-RU","languages":["ru-RU"],"webdriver":false,"hardwareConcurrency":8,"notificationsPermission":"denied"}`
	deviceSafariMobile   = `{"screenWidth":393,"screenHeight":852,"screenAvailWidth":393,"screenAvailHeight":852,"innerWidth":393,"innerHeight":659,"devicePixelRatio":3,"language":"ru-RU","languages":["ru-RU"],"webdriver":false,"hardwareConcurrency":4,"notificationsPermission":"denied"}`
)

// For строит канонический профиль для (family, platform). Неизвестный family -> Chrome.
func For(k Kind, p Platform) Profile {
	if p != Mobile {
		p = Desktop
	}
	switch k {
	case Safari:
		return safariProfile(p)
	case Firefox:
		return firefoxProfile(p)
	default:
		return chromeProfile(p)
	}
}

func chromeProfile(p Platform) Profile {
	if p == Mobile {
		return Profile{
			Family:          Chrome,
			Platform:        Mobile,
			UserAgent:       "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Mobile Safari/537.36",
			SecChUa:         `"Google Chrome";v="146", "Chromium";v="146", "Not)A;Brand";v="24"`,
			SecChUaMobile:   "?1",
			SecChUaPlatform: `"Android"`,
			AcceptLanguage:  "ru-RU,ru;q=0.9",
			DeviceJSON:      deviceChromeMobile,
		}
	}
	return Profile{
		Family:          Chrome,
		Platform:        Desktop,
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		SecChUa:         `"Google Chrome";v="146", "Chromium";v="146", "Not)A;Brand";v="24"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Windows"`,
		AcceptLanguage:  "ru-RU,ru;q=0.9",
		DeviceJSON:      deviceChromeDesktop,
	}
}

func firefoxProfile(p Platform) Profile {
	if p == Mobile {
		return Profile{
			Family:         Firefox,
			Platform:       Mobile,
			UserAgent:      "Mozilla/5.0 (Android 14; Mobile; rv:148.0) Gecko/148.0 Firefox/148.0",
			AcceptLanguage: "ru-RU,ru;q=0.9",
			DeviceJSON:     deviceFirefoxMobile,
		}
	}
	return Profile{
		Family:         Firefox,
		Platform:       Desktop,
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0",
		AcceptLanguage: "ru-RU,ru;q=0.9",
		DeviceJSON:     deviceFirefoxDesktop,
	}
}

func safariProfile(p Platform) Profile {
	if p == Mobile {
		return Profile{
			Family:         Safari,
			Platform:       Mobile,
			UserAgent:      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			AcceptLanguage: "ru-RU,ru;q=0.9",
			DeviceJSON:     deviceSafariMobile,
		}
	}
	return Profile{
		Family:         Safari,
		Platform:       Desktop,
		UserAgent:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Safari/605.1.15",
		AcceptLanguage: "ru-RU,ru;q=0.9",
		DeviceJSON:     deviceSafariDesktop,
	}
}

func Family(p Profile) Kind { return p.Family }

func IsMobile(p Profile) bool { return p.Platform == Mobile }

// ClientProfile - TLS/HTTP2-отпечаток персоны (JA3 + ALPN + h2 settings).
// Chrome/Firefox используют один TLS-стек на всех платформах, поэтому platform на
// них не влияет; Safari desktop и iOS различаются в ClientHello.
func (p Profile) ClientProfile() profiles.ClientProfile {
	switch p.Family {
	case Safari:
		if p.Platform == Mobile {
			return profiles.Safari_IOS_17_0
		}
		return profiles.Safari_16_0
	case Firefox:
		return profiles.Firefox_148
	default:
		return profiles.Chrome_146
	}
}

func ApplyFhttp(req *fhttp.Request, profile Profile) {
	req.Header.Set("User-Agent", profile.UserAgent)
	if profile.SecChUa != "" {
		req.Header.Set("sec-ch-ua", profile.SecChUa)
		req.Header.Set("sec-ch-ua-mobile", profile.SecChUaMobile)
		req.Header.Set("sec-ch-ua-platform", profile.SecChUaPlatform)
	}
	acceptLang := profile.AcceptLanguage
	if acceptLang == "" {
		acceptLang = "ru-RU,ru;q=0.9"
	}
	req.Header.Set("Accept-Language", acceptLang)
}
