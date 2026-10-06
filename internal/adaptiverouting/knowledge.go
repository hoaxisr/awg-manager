package adaptiverouting

import (
	"net"
	"strings"
)

type DomainKnowledge struct {
	Title       string
	Description string
	Org         string
	Country     string
	CountryCode string
	Category    string
	Icon        string
}

type cidrKnowledge struct {
	net         *net.IPNet
	title       string
	org         string
	country     string
	countryCode string
	icon        string
}

type domainKnowledgeRule struct {
	patterns    []string
	title       string
	org         string
	country     string
	countryCode string
	icon        string
}

var (
	domainKnowledgeRules []domainKnowledgeRule
	cidrKnowledgeRules   []cidrKnowledge
)

func init() {
	domainKnowledgeRules = []domainKnowledgeRule{
		{patterns: []string{"youtube.com", "googlevideo.com", "ytimg.com"}, title: "YouTube", org: "Google LLC", country: "США", countryCode: "US", icon: "youtube"},
		{patterns: []string{"instagram.com", "cdninstagram.com"}, title: "Instagram", org: "Meta Platforms", country: "США", countryCode: "US", icon: "chat"},
		{patterns: []string{"facebook.com", "fbcdn.net"}, title: "Facebook", org: "Meta Platforms", country: "США", countryCode: "US", icon: "chat"},
		{patterns: []string{"twitter.com", "x.com", "twimg.com"}, title: "X (Twitter)", org: "X Corp.", country: "США", countryCode: "US", icon: "chat"},
		{patterns: []string{"discord.com", "discordapp.com", "discord.gg"}, title: "Discord", org: "Discord Inc.", country: "США", countryCode: "US", icon: "chat"},
		{patterns: []string{"telegram.org", "t.me", "telesco.pe"}, title: "Telegram", org: "Telegram FZ-LLC", country: "ОАЭ", countryCode: "AE", icon: "chat"},
		{patterns: []string{"rutracker.org", "rutracker.net"}, title: "RuTracker", org: "RuTracker", country: "Сейшелы", countryCode: "SC", icon: "globe"},
		{patterns: []string{"flibusta.is", "flibusta.site"}, title: "Flibusta", org: "Flibusta", country: "Нидерланды", countryCode: "NL", icon: "book"},
		{patterns: []string{"notion.so"}, title: "Notion", org: "Notion Labs", country: "США", countryCode: "US", icon: "cloud"},
		{patterns: []string{"openai.com", "chatgpt.com"}, title: "OpenAI / ChatGPT", org: "OpenAI", country: "США", countryCode: "US", icon: "bot"},
		{patterns: []string{"claude.ai", "anthropic.com"}, title: "Claude AI", org: "Anthropic", country: "США", countryCode: "US", icon: "bot"},
	}

	rawCIDRs := []struct {
		cidr    string
		title   string
		org     string
		country string
		cc      string
		icon    string
	}{
		{"149.154.160.0/20", "Telegram DC", "Telegram Messenger Network", "Нидерланды", "NL", "chat"},
		{"91.108.4.0/22", "Telegram DC", "Telegram Messenger Network", "Нидерланды", "NL", "chat"},
		{"91.108.8.0/22", "Telegram DC", "Telegram Messenger Network", "Сингапур", "SG", "chat"},
		{"91.108.16.0/22", "Telegram DC", "Telegram Messenger Network", "США", "US", "chat"},
		{"91.108.56.0/22", "Telegram DC", "Telegram Messenger Network", "Нидерланды", "NL", "chat"},
		{"173.194.0.0/16", "YouTube / Google", "Google LLC", "США", "US", "youtube"},
		{"142.250.0.0/15", "Google / YouTube", "Google LLC", "США", "US", "youtube"},
		{"108.177.0.0/17", "Google Video", "Google LLC", "США", "US", "youtube"},
		{"104.16.0.0/12", "Cloudflare CDN", "Cloudflare, Inc.", "США", "US", "cloud"},
		{"172.64.0.0/13", "Cloudflare CDN", "Cloudflare, Inc.", "США", "US", "cloud"},
		{"162.158.0.0/15", "Cloudflare Proxy", "Cloudflare, Inc.", "США", "US", "cloud"},
		{"31.13.64.0/18", "Meta / WhatsApp", "Meta Platforms, Inc.", "США", "US", "chat"},
		{"157.240.0.0/16", "Meta / Instagram", "Meta Platforms, Inc.", "США", "US", "chat"},
	}

	for _, item := range rawCIDRs {
		if _, ipnet, err := net.ParseCIDR(item.cidr); err == nil {
			cidrKnowledgeRules = append(cidrKnowledgeRules, cidrKnowledge{
				net:         ipnet,
				title:       item.title,
				org:         item.org,
				country:     item.country,
				countryCode: item.cc,
				icon:        item.icon,
			})
		}
	}
}

func FindDomainKnowledge(domain, ip string) *DomainKnowledge {
	target := strings.ToLower(domain)
	if target != "" {
		for _, rule := range domainKnowledgeRules {
			for _, pattern := range rule.patterns {
				if target == pattern || strings.HasSuffix(target, "."+pattern) {
					return &DomainKnowledge{
						Title:       rule.title,
						Org:         rule.org,
						Country:     rule.country,
						CountryCode: rule.countryCode,
						Icon:        rule.icon,
					}
				}
			}
		}
	}

	if ip != "" {
		if parsedIP := net.ParseIP(ip); parsedIP != nil {
			for _, rule := range cidrKnowledgeRules {
				if rule.net.Contains(parsedIP) {
					return &DomainKnowledge{
						Title:       rule.title,
						Org:         rule.org,
						Country:     rule.country,
						CountryCode: rule.countryCode,
						Icon:        rule.icon,
					}
				}
			}
		}
	}

	if target != "" && net.ParseIP(target) == nil {
		parts := strings.Split(target, ".")
		title := target
		if len(parts) >= 2 {
			base := parts[len(parts)-2]
			if len(base) > 1 {
				title = strings.ToUpper(base[:1]) + base[1:]
			}
		}
		return &DomainKnowledge{
			Title: title,
			Icon:  "globe",
		}
	}

	return nil
}
