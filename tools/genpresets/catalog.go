package main

// SagerNet rule-set branch raw root for new-preset .srs files.
const sagerNetSiteRoot = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/"

// aliasToCanonical maps SERVICE_PRESETS ids to the canonical (internalpresets)
// id. Canonical ids match the geosite-<id> tags already used in applied configs.
var aliasToCanonical = map[string]string{
	"twitter":        "x",
	"chatgpt":        "openai",
	"cloudflare-ips": "cloudflare",
}

func canonicalID(id string) string {
	if c, ok := aliasToCanonical[id]; ok {
		return c
	}
	return id
}

// droppedSourceIDs are SERVICE_PRESETS ids not carried over verbatim. "social"
// is a bundle (Instagram+Facebook+WhatsApp) — its members exist as separate
// presets sourced from internalpresets, so the bundle itself is discarded.
var droppedSourceIDs = map[string]bool{"social": true}

func isDroppedSourceID(id string) bool { return droppedSourceIDs[id] }

// dnsOnlyEntry is metadata for a DNS-only service that has no internalpresets
// (and thus no iconSlug/category there): assigned explicitly here.
type dnsOnlyEntry struct {
	name     string
	iconSlug string
	category string
}

// dnsOnlyMeta covers SERVICE_PRESETS-only ids (no .srs, DNS engine only).
var dnsOnlyMeta = map[string]dnsOnlyEntry{
	"atlassian":        {"Atlassian", "atlassian", "developer"},
	"ubisoft":          {"Ubisoft", "ubisoft", "gaming"},
	"dev-tools":        {"Dev tools", "lucide-cpu", "developer"},
	"tmdb":             {"TMDB", "lucide-film", "media"},
	"ip-checkers":      {"IP checkers", "lucide-globe", "developer"},
	"samsung":          {"Samsung", "samsung", "cloud"},
	"torrents":         {"Torrents", "bittorrent", "media"},
	"russian-services": {"Российские сервисы", "rkn", "block"},
	"all-blocked":      {"Всё заблокированное", "lucide-globe-lock", "block"},
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
	{"blizzard", "Blizzard", "blizzard", "gaming", srs("blizzard"), "tunnel"},
	{"duckduckgo", "DuckDuckGo", "duckduckgo", "social", srs("duckduckgo"), "tunnel"},
	{"paypal", "PayPal", "paypal", "cloud", srs("paypal"), "tunnel"},
	{"binance", "Binance", "binance", "cloud", srs("binance"), "tunnel"},
	{"patreon", "Patreon", "patreon", "social", srs("patreon"), "tunnel"},
	{"medium", "Medium", "medium", "social", srs("medium"), "tunnel"},
}
