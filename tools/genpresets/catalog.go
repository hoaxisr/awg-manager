package main

import "strings"

// SagerNet rule-set branch raw root for new-preset .srs files.
const sagerNetSiteRoot = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/"

// Upstream .srs are decompiled from these immutable commits so generation is
// deterministic. Bump intentionally, then re-run the generator + commit.
const (
	sagerNetPinSHA = "4fe72acfd29178e56c9d4699a12062097a16f755" // pinned 2026-06-02 (SagerNet/sing-geosite rule-set)
	vernettePinSHA = "1e1fd57f2ff0533f09ca95da895ee2ea367e2720" // pinned 2026-06-02 (vernette/rulesets master)
)

// pinnedFetchURL rewrites a moving-branch .srs URL to its pinned-commit form,
// used ONLY for the deterministic decompile fetch. The catalog keeps the branch
// URL (runtime: sing-box re-fetches fresh per update_interval). A URL matching
// neither pattern is returned unchanged.
func pinnedFetchURL(url string) string {
	url = strings.Replace(url,
		"raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/",
		"raw.githubusercontent.com/SagerNet/sing-geosite/"+sagerNetPinSHA+"/", 1)
	url = strings.Replace(url,
		"github.com/vernette/rulesets/raw/master/",
		"github.com/vernette/rulesets/raw/"+vernettePinSHA+"/", 1)
	return url
}

// addition is a new preset (meta/oculus + popular) sourced from SagerNet.
type addition struct {
	id, name, iconSlug, category, srsURL, action string
}

func srs(slug string) string { return sagerNetSiteRoot + "geosite-" + slug + ".srs" }

// additions: ~29 new presets. iconSlug values must exist in brandIcons after
// the icon task (simple-icons) or be added as MANUAL (primevideo,hulu,slack,
// canva,adobe,blizzard). geosite slug differs from id only for npm (geosite-npmjs).
var additions = []addition{
	{"meta", "Meta (все сервисы)", "meta", "social", srs("meta"), "tunnel"},
	{"oculus", "Oculus / Quest", "oculus", "gaming", srs("oculus"), "tunnel"},
	{"threads", "Threads", "threads", "social", srs("threads"), "tunnel"},
	{"bluesky", "Bluesky", "bluesky", "social", srs("bluesky"), "tunnel"},
	{"pinterest", "Pinterest", "pinterest", "social", srs("pinterest"), "tunnel"},
	{"soundcloud", "SoundCloud", "soundcloud", "media", srs("soundcloud"), "tunnel"},
	{"deezer", "Deezer", "deezer", "media", srs("deezer"), "tunnel"},
	{"tidal", "Tidal", "tidal", "media", srs("tidal"), "tunnel"},
	{"vimeo", "Vimeo", "vimeo", "media", srs("vimeo"), "tunnel"},
	{"primevideo", "Prime Video", "primevideo", "media", srs("primevideo"), "tunnel"},
	{"hulu", "Hulu", "hulu", "media", srs("hulu"), "tunnel"},
	{"notion", "Notion", "notion", "developer", srs("notion"), "tunnel"},
	{"zoom", "Zoom", "zoom", "developer", srs("zoom"), "tunnel"},
	{"figma", "Figma", "figma", "developer", srs("figma"), "tunnel"},
	{"vercel", "Vercel", "vercel", "developer", srs("vercel"), "tunnel"},
	{"nvidia", "NVIDIA", "nvidia", "cloud", srs("nvidia"), "tunnel"},
	{"jetbrains", "JetBrains", "jetbrains", "developer", srs("jetbrains"), "tunnel"},
	{"npm", "npm", "npm", "developer", srs("npmjs"), "tunnel"},
	{"slack", "Slack", "slack", "developer", srs("slack"), "tunnel"},
	{"canva", "Canva", "canva", "developer", srs("canva"), "tunnel"},
	{"adobe", "Adobe", "adobe", "developer", srs("adobe"), "tunnel"},
	{"epicgames", "Epic Games", "epicgames", "gaming", srs("epicgames"), "tunnel"},
	{"ea", "EA", "ea", "gaming", srs("ea"), "tunnel"},
	{"blizzard", "Blizzard", "lucide-gamepad-2", "gaming", srs("blizzard"), "tunnel"},
	{"duckduckgo", "DuckDuckGo", "duckduckgo", "social", srs("duckduckgo"), "tunnel"},
	{"paypal", "PayPal", "paypal", "cloud", srs("paypal"), "tunnel"},
	{"binance", "Binance", "binance", "cloud", srs("binance"), "tunnel"},
	{"patreon", "Patreon", "patreon", "social", srs("patreon"), "tunnel"},
	{"medium", "Medium", "medium", "social", srs("medium"), "tunnel"},
}
