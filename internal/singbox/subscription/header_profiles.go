package subscription

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// HeaderProfile is one client fingerprint — the header set a given client app
// sends to a subscription provider. The same list feeds header auto-detection
// (DetectHeaders) and the preset dropdown in the UI, so the fingerprints live
// in one place instead of one copy per side of the API.
type HeaderProfile struct {
	Kind    string   `json:"kind"`
	Label   string   `json:"label"`
	Headers []Header `json:"headers"`
}

// HeadersText renders the profile in the "Name: value" form the UI edits.
func (p HeaderProfile) HeadersText() string {
	lines := make([]string, 0, len(p.Headers))
	for _, h := range p.Headers {
		lines = append(lines, fmt.Sprintf("%s: %s", h.Name, h.Value))
	}
	return strings.Join(lines, "\n")
}

// randIndex returns a random index in [0,n). Bias from the modulo is
// irrelevant for picking a cosmetic device model.
func randIndex(n int) int {
	if n <= 1 {
		return 0
	}
	b := make([]byte, 1)
	if _, err := rand.Read(b); err != nil {
		return 0
	}
	return int(b[0]) % n
}

// singboxProfileKind is the fallback profile: what we send when nothing was
// detected, and the default the UI starts with.
const singboxProfileKind = "singbox"

// HeaderProfiles returns the known client fingerprints. Values that real
// clients randomize per install (HWID, device model, build stamp) are
// regenerated on every call.
func HeaderProfiles() []HeaderProfile {
	models := []string{
		"iPhone 15 Pro", "iPhone 15 Pro Max",
		"iPhone 16 Pro", "iPhone 16 Pro Max",
		"iPhone 17 Pro", "iPhone 17 Pro Max",
	}
	model := models[randIndex(len(models))]
	minor := randIndex(5)

	return []HeaderProfile{
		{
			Kind:  "happ",
			Label: "HAPP iOS",
			Headers: []Header{
				{Name: "User-Agent", Value: fmt.Sprintf("Happ/4.6.%d/ios/%d", minor, time.Now().Unix())},
				{Name: "X-Device-OS", Value: "iOS"},
				{Name: "X-HWID", Value: randomHex(16)},
				{Name: "X-Device-Locale", Value: "ru"},
				{Name: "X-Ver-OS", Value: fmt.Sprintf("18.%d", minor+1)},
				{Name: "X-App-Version", Value: fmt.Sprintf("4.6.%d", minor)},
				{Name: "X-Device-Model", Value: model},
			},
		},
		{
			Kind:  "mihomo",
			Label: "Clash / mihomo",
			Headers: []Header{
				{Name: "User-Agent", Value: "Clash-Verge/1.7.0 (Clash.Meta)"},
			},
		},
		{
			Kind:  singboxProfileKind,
			Label: "sing-box",
			Headers: []Header{
				{Name: "User-Agent", Value: "sing-box/v1.14.20"},
			},
		},
		{
			Kind:  "v2rayn",
			Label: "v2rayN",
			Headers: []Header{
				{Name: "User-Agent", Value: "v2rayN/6.42 (Windows NT 10.0; Win64; x64)"},
			},
		},
	}
}

// defaultHeaderProfile is the profile used when detection found nothing.
func defaultHeaderProfile() HeaderProfile {
	for _, p := range HeaderProfiles() {
		if p.Kind == singboxProfileKind {
			return p
		}
	}
	return HeaderProfiles()[0]
}
