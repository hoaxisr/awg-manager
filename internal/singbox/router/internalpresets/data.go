package internalpresets

const sagerNetSiteRoot = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/"
const sagerNetIPRoot = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/"

type Preset struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	IconSlug  string     `json:"iconSlug,omitempty"`
	RuleSets  []RuleRef  `json:"ruleSets"`
	Rules     []RuleLink `json:"rules"`
	Notice    string     `json:"notice,omitempty"`
	Featured  bool       `json:"featured,omitempty"`
	Sensitive bool       `json:"sensitive,omitempty"`
}

type RuleRef struct {
	Tag string `json:"tag"`
	URL string `json:"url"`
}

type RuleLink struct {
	RuleSetRef   string `json:"ruleSetRef"`
	ActionTarget string `json:"actionTarget"`
}

func All() []Preset {
	out := []Preset{
		{
			ID: "all-non-ru", Name: "Обход блокировок РФ (всё не-RU → VPN)",
			IconSlug: "lucide-shield-check",
			Featured: true,
			RuleSets: []RuleRef{{Tag: "geosite-geolocation-!ru", URL: sagerNetSiteRoot + "geosite-geolocation-!ru.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-geolocation-!ru", ActionTarget: "tunnel"}},
			Notice:   "Весь не-российский трафик через VPN. One-click сетап для обхода блокировок.",
		},
		{
			ID: "geoip-ru-direct", Name: "Российский трафик → мимо VPN",
			IconSlug: "lucide-globe",
			Featured: true,
			RuleSets: []RuleRef{{Tag: "geoip-ru", URL: sagerNetIPRoot + "geoip-ru.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geoip-ru", ActionTarget: "direct"}},
			Notice:   "Полезно когда final=tunnel (всё по умолчанию в VPN, а RU — мимо)",
		},
	}

	// Популярные соцсети / мессенджеры
	out = append(out,
		simpleGeosite("youtube", "YouTube", "youtube"),
		simpleGeosite("google", "Google", "google"),
		simpleGeosite("netflix", "Netflix", "netflix"),
		simpleGeosite("discord", "Discord", "discord"),
		simpleGeosite("telegram", "Telegram", "telegram"),
		simpleGeosite("twitter", "Twitter / X", "x"),
		simpleGeosite("facebook", "Facebook", "facebook"),
		simpleGeosite("instagram", "Instagram", "instagram"),
		simpleGeosite("tiktok", "TikTok", "tiktok"),
		simpleGeosite("whatsapp", "WhatsApp", "whatsapp"),
		simpleGeosite("signal", "Signal", "signal"),
		simpleGeosite("reddit", "Reddit", "reddit"),
		simpleGeosite("linkedin", "LinkedIn", "linkedin"),
		simpleGeosite("pinterest", "Pinterest", "pinterest"),
	)

	// Стриминг/медиа
	out = append(out,
		simpleGeosite("twitch", "Twitch", "twitch"),
		simpleGeosite("spotify", "Spotify", "spotify"),
		simpleGeosite("disney", "Disney+", "disney"),
		simpleGeosite("hbo", "HBO", "hbo"),
		Preset{
			ID: "category-media", Name: "Всё медиа",
			IconSlug: "lucide-film",
			RuleSets: []RuleRef{{Tag: "geosite-category-media", URL: sagerNetSiteRoot + "geosite-category-media.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-category-media", ActionTarget: "tunnel"}},
			Notice:   "Композитный список стриминговых сервисов",
		},
	)

	// AI
	out = append(out,
		simpleGeosite("openai", "OpenAI", "openai"),
		simpleGeosite("anthropic", "Anthropic", "anthropic"),
		simpleGeosite("gemini", "Gemini", "googlegemini"),
		simpleGeosite("perplexity", "Perplexity", "perplexity"),
		simpleGeosite("xai", "xAI / Grok", "xai"),
		Preset{
			ID: "category-ai", Name: "Все AI сервисы",
			IconSlug: "lucide-sparkles",
			RuleSets: []RuleRef{{Tag: "geosite-category-ai-!cn", URL: sagerNetSiteRoot + "geosite-category-ai-!cn.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-category-ai-!cn", ActionTarget: "tunnel"}},
			Notice:   "ChatGPT, Claude, Gemini, Perplexity и другие (кроме китайских)",
		},
	)

	// Developer
	out = append(out,
		simpleGeosite("github", "GitHub", "github"),
		simpleGeosite("gitlab", "GitLab", "gitlab"),
		simpleGeosite("stackoverflow", "Stack Overflow", "stackoverflow"),
		simpleGeosite("docker", "Docker", "docker"),
	)

	// Cloud / enterprise
	out = append(out,
		simpleGeosite("cloudflare", "Cloudflare", "cloudflare"),
		simpleGeosite("akamai", "Akamai", "akamai"),
		simpleGeosite("aws", "Amazon AWS", "amazonwebservices"),
		simpleGeosite("apple", "Apple", "apple"),
		simpleGeosite("microsoft", "Microsoft", "microsoft"),
	)

	// Gaming
	out = append(out,
		Preset{
			ID: "category-games", Name: "Все игры",
			IconSlug: "lucide-gamepad-2",
			RuleSets: []RuleRef{{Tag: "geosite-category-games", URL: sagerNetSiteRoot + "geosite-category-games.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-category-games", ActionTarget: "tunnel"}},
			Notice:   "Steam, Epic, PlayStation, Xbox, Nintendo, Blizzard и другие",
		},
		simpleGeosite("steam", "Steam", "steam"),
		simpleGeosite("epicgames", "Epic Games", "epicgames"),
		simpleGeosite("playstation", "PlayStation", "playstation"),
		simpleGeosite("xbox", "Xbox", "xbox"),
		simpleGeosite("nintendo", "Nintendo", "nintendo"),
		simpleGeosite("blizzard", "Blizzard", "blizzardentertainment"),
	)

	// Блокировка (action: reject)
	out = append(out,
		Preset{
			ID: "ads", Name: "Реклама и трекеры",
			IconSlug: "lucide-circle-slash",
			RuleSets: []RuleRef{{Tag: "geosite-category-ads-all", URL: sagerNetSiteRoot + "geosite-category-ads-all.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-category-ads-all", ActionTarget: "reject"}},
			Notice:   "Блокирует рекламу и трекеры через action:reject — выбор outbound не требуется",
		},
		// NOTE: presets `scam`, `cryptominers`, `tracking` removed —
		// the corresponding SagerNet rule-set URLs return 404 (the
		// sing-geosite repo no longer publishes those exact slugs).
		// Until upstream provides equivalents, only `ads-all` is
		// reliably reachable from the SagerNet root.
	)

	// Sensitive (hidden by default)
	out = append(out, Preset{
		ID: "porn", Name: "Adult content (18+)",
		IconSlug:  "lucide-lock",
		Sensitive: true,
		RuleSets:  []RuleRef{{Tag: "geosite-category-porn", URL: sagerNetSiteRoot + "geosite-category-porn.srs"}},
		Rules:     []RuleLink{{RuleSetRef: "geosite-category-porn", ActionTarget: "tunnel"}},
		Notice:    "Контент 18+ через VPN",
	})

	return out
}

func simpleGeosite(slug, name, iconSlug string) Preset {
	tag := "geosite-" + slug
	return Preset{
		ID:       slug,
		Name:     name,
		IconSlug: iconSlug,
		RuleSets: []RuleRef{{Tag: tag, URL: sagerNetSiteRoot + tag + ".srs"}},
		Rules:    []RuleLink{{RuleSetRef: tag, ActionTarget: "tunnel"}},
	}
}
