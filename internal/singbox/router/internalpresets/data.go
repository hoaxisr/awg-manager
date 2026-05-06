package internalpresets

const sagerNetSiteRoot = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/"

type Preset struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Category  string     `json:"category,omitempty"`
	IconSlug  string     `json:"iconSlug,omitempty"`
	RuleSets  []RuleRef  `json:"ruleSets"`
	Rules     []RuleLink `json:"rules"`
	Notice    string     `json:"notice,omitempty"`
	Featured  bool       `json:"featured,omitempty"`
	Sensitive bool       `json:"sensitive,omitempty"`
}

// Category constants for presets.
//
// A preset with Category == "" is rendered outside the chip filter — it
// shows in the Featured row at the top, or, for the Sensitive preset, is
// hidden behind the existing Sensitive toggle. Featured: true and an
// empty Category are independent: a future featured preset could carry a
// category, but today none do.
const (
	CatSocial    = "social"
	CatMedia     = "media"
	CatAI        = "ai"
	CatDeveloper = "developer"
	CatCloud     = "cloud"
	CatGaming    = "gaming"
	CatBlock     = "block"
)

type RuleRef struct {
	Tag string `json:"tag"`
	URL string `json:"url"`
}

type RuleLink struct {
	RuleSetRef   string `json:"ruleSetRef"`
	ActionTarget string `json:"actionTarget"`
}

func All() []Preset {
	out := []Preset{}

	// Соцсети / мессенджеры
	out = append(out,
		simpleGeosite("youtube", "YouTube", CatSocial, "youtube"),
		simpleGeosite("google", "Google", CatSocial, "google"),
		simpleGeosite("discord", "Discord", CatSocial, "discord"),
		simpleGeosite("telegram", "Telegram", CatSocial, "telegram"),
		// twitter renamed to x — slug, name, icon all reflect the rebrand.
		simpleGeosite("x", "X (Twitter)", CatSocial, "x"),
		simpleGeosite("facebook", "Facebook", CatSocial, "facebook"),
		simpleGeosite("instagram", "Instagram", CatSocial, "instagram"),
		simpleGeosite("tiktok", "TikTok", CatSocial, "tiktok"),
		simpleGeosite("whatsapp", "WhatsApp", CatSocial, "whatsapp"),
		simpleGeosite("signal", "Signal", CatSocial, "signal"),
		simpleGeosite("reddit", "Reddit", CatSocial, "reddit"),
	)

	// Стриминг / медиа
	out = append(out,
		simpleGeosite("netflix", "Netflix", CatMedia, "netflix"),
		simpleGeosite("twitch", "Twitch", CatMedia, "twitch"),
		simpleGeosite("spotify", "Spotify", CatMedia, "spotify"),
		simpleGeosite("disney", "Disney+", CatMedia, "disney"),
		simpleGeosite("hbo", "HBO", CatMedia, "hbo"),
		// "wikimedia" is the SagerNet upstream rule-set slug; we display
		// "Wikipedia" since that is what users recognise.
		Preset{
			ID: "wikimedia", Name: "Wikipedia",
			Category: CatMedia,
			IconSlug: "wikipedia",
			RuleSets: []RuleRef{{Tag: "geosite-wikimedia", URL: sagerNetSiteRoot + "geosite-wikimedia.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-wikimedia", ActionTarget: "tunnel"}},
		},
		Preset{
			ID: "bbc", Name: "BBC",
			Category: CatMedia,
			IconSlug: "bbc",
			RuleSets: []RuleRef{{Tag: "geosite-bbc", URL: sagerNetSiteRoot + "geosite-bbc.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-bbc", ActionTarget: "tunnel"}},
		},
		Preset{
			ID: "category-media", Name: "Всё медиа",
			Category: CatMedia,
			IconSlug: "lucide-film",
			RuleSets: []RuleRef{{Tag: "geosite-category-media", URL: sagerNetSiteRoot + "geosite-category-media.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-category-media", ActionTarget: "tunnel"}},
			Notice:   "Композитный список стриминговых сервисов",
		},
	)

	// AI
	out = append(out,
		simpleGeosite("openai", "OpenAI", CatAI, "openai"),
		// anthropic preset covers claude.ai too — no separate "claude"
		// slot since SagerNet does not publish a geosite-claude.srs.
		simpleGeosite("anthropic", "Anthropic / Claude", CatAI, "anthropic"),
		// SagerNet upstream slug is "google-gemini"; our user-facing ID
		// stays "gemini" so the URL path is the only place the dash
		// appears.
		Preset{
			ID: "gemini", Name: "Gemini",
			Category: CatAI,
			IconSlug: "googlegemini",
			RuleSets: []RuleRef{{Tag: "geosite-google-gemini", URL: sagerNetSiteRoot + "geosite-google-gemini.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-google-gemini", ActionTarget: "tunnel"}},
		},
		simpleGeosite("perplexity", "Perplexity", CatAI, "perplexity"),
		Preset{
			ID: "category-ai", Name: "Все AI сервисы",
			Category: CatAI,
			IconSlug: "lucide-sparkles",
			RuleSets: []RuleRef{{Tag: "geosite-category-ai-!cn", URL: sagerNetSiteRoot + "geosite-category-ai-!cn.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-category-ai-!cn", ActionTarget: "tunnel"}},
			Notice:   "ChatGPT, Claude, Gemini, Perplexity и другие (кроме китайских)",
		},
	)

	// Developer
	out = append(out,
		simpleGeosite("github", "GitHub", CatDeveloper, "github"),
		simpleGeosite("gitlab", "GitLab", CatDeveloper, "gitlab"),
		simpleGeosite("docker", "Docker", CatDeveloper, "docker"),
	)

	// Cloud / enterprise
	out = append(out,
		simpleGeosite("cloudflare", "Cloudflare", CatCloud, "cloudflare"),
		simpleGeosite("akamai", "Akamai", CatCloud, "akamai"),
		simpleGeosite("aws", "Amazon AWS", CatCloud, "amazonwebservices"),
		simpleGeosite("apple", "Apple", CatCloud, "apple"),
		simpleGeosite("microsoft", "Microsoft", CatCloud, "microsoft"),
	)

	// Gaming
	out = append(out,
		Preset{
			ID: "category-games", Name: "Все игры",
			Category: CatGaming,
			IconSlug: "lucide-gamepad-2",
			RuleSets: []RuleRef{{Tag: "geosite-category-games", URL: sagerNetSiteRoot + "geosite-category-games.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-category-games", ActionTarget: "tunnel"}},
			Notice:   "Steam, Epic, PlayStation, Xbox, Nintendo, Blizzard и другие",
		},
		simpleGeosite("steam", "Steam", CatGaming, "steam"),
		simpleGeosite("playstation", "PlayStation", CatGaming, "playstation"),
		simpleGeosite("xbox", "Xbox", CatGaming, "xbox"),
		simpleGeosite("roblox", "Roblox", CatGaming, "roblox"),
	)

	// Блокировка (action: reject)
	out = append(out,
		Preset{
			ID: "ads", Name: "Реклама и трекеры",
			Category: CatBlock,
			IconSlug: "lucide-circle-slash",
			RuleSets: []RuleRef{{Tag: "geosite-category-ads-all", URL: sagerNetSiteRoot + "geosite-category-ads-all.srs"}},
			Rules:    []RuleLink{{RuleSetRef: "geosite-category-ads-all", ActionTarget: "reject"}},
			Notice:   "Блокирует рекламу и трекеры через action:reject — выбор outbound не требуется",
		},
	)

	// Sensitive (hidden by default; Category empty since the gallery
	// handles it through the existing Sensitive toggle, not through
	// category filtering).
	out = append(out, Preset{
		ID: "porn", Name: "Adult content (18+)",
		IconSlug:  "lucide-lock",
		Sensitive: true,
		RuleSets:  []RuleRef{{Tag: "geosite-category-porn", URL: sagerNetSiteRoot + "geosite-category-porn.srs"}},
		Rules:     []RuleLink{{RuleSetRef: "geosite-category-porn", ActionTarget: "tunnel"}},
		Notice:    "Контент 18+ через VPN",
	})

	// Regional default emphasis: keep the same preset behavior, but
	// surface the most common Russia-first starters in the featured row.
	featured := map[string]string{
		"telegram": "Рекомендуется как первый шаг для РФ-сценариев.",
		"youtube":  "Частый стартовый кейс для РФ-маршрутизации.",
		"openai":   "Популярный AI-маршрут для РФ-сетей.",
		"github":   "Базовый рабочий маршрут для разработчиков.",
	}
	for i := range out {
		if notice, ok := featured[out[i].ID]; ok {
			out[i].Featured = true
			if out[i].Notice == "" {
				out[i].Notice = notice
			}
		}
	}

	return out
}

func simpleGeosite(slug, name, category, iconSlug string) Preset {
	tag := "geosite-" + slug
	return Preset{
		ID:       slug,
		Name:     name,
		Category: category,
		IconSlug: iconSlug,
		RuleSets: []RuleRef{{Tag: tag, URL: sagerNetSiteRoot + tag + ".srs"}},
		Rules:    []RuleLink{{RuleSetRef: tag, ActionTarget: "tunnel"}},
	}
}
