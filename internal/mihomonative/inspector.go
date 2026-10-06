package mihomonative

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

type DNSServerSpec struct {
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port,omitempty"`
	Detour     string `json:"detour,omitempty"`
	SNI        string `json:"sni,omitempty"`
}

type DNSRuleSpec struct {
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	RuleSet       []string `json:"rule_set,omitempty"`
	Server        string   `json:"server,omitempty"`
}

type InspectInput struct {
	Domain                string          `json:"domain"`
	Port                  int             `json:"port,omitempty"`
	Protocol              string          `json:"protocol,omitempty"`
	DNSServers            []DNSServerSpec `json:"-"`
	DNSRules              []DNSRuleSpec   `json:"-"`
	KeeneticCloudTunnel   bool            `json:"-"`
	KeeneticCloudOutbound string          `json:"-"`
}

type InspectDNSInfoDTO struct {
	MatchedRule   int    `json:"matchedRule"`
	Server        string `json:"server"`
	ServerAddress string `json:"serverAddress,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Policy        string `json:"policy,omitempty"`
	IsRemoteDNS   bool   `json:"isRemoteDNS,omitempty"`
}

type InspectMatchDTO struct {
	Index      int      `json:"index"`
	Matched    bool     `json:"matched"`
	Action     string   `json:"action"`
	Outbound   string   `json:"outbound,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

type InspectData struct {
	Input       string             `json:"input"`
	InputType   string             `json:"inputType"`
	Matches     []InspectMatchDTO  `json:"matches"`
	Destination string             `json:"destination"`
	MatchedRule int                `json:"matchedRule"`
	Final       string             `json:"final"`
	Note        string             `json:"note,omitempty"`
	DNS         *InspectDNSInfoDTO `json:"dns,omitempty"`
}

type InspectProgressDTO struct {
	Phase        string `json:"phase"`
	Message      string `json:"message"`
	RuleIndex    *int   `json:"ruleIndex,omitempty"`
	RuleTotal    *int   `json:"ruleTotal,omitempty"`
	RuleSetTag   string `json:"ruleSetTag,omitempty"`
	RuleSetIndex *int   `json:"ruleSetIndex,omitempty"`
	RuleSetTotal *int   `json:"ruleSetTotal,omitempty"`
	Final        string `json:"final,omitempty"`
	UsingDraft   bool   `json:"usingDraft,omitempty"`
}

type InspectStreamEventDTO struct {
	Type     string              `json:"type"`
	Progress *InspectProgressDTO `json:"progress,omitempty"`
	Result   *InspectData        `json:"result,omitempty"`
	Error    string              `json:"error,omitempty"`
}

var knownGeositeMap = map[string][]string{
	"telegram": {
		"t.me", "telegram.org", "telegram.me", "telesco.pe", "tdesktop.com",
		"telegra.ph", "tx.me", "telegram-cdn.org",
	},
	"youtube": {
		"youtube.com", "youtu.be", "googlevideo.com", "ytimg.com", "ggpht.com",
		"youtube-nocookie.com", "youtubeeducation.com", "youtubekids.com",
	},
	"roblox": {
		"roblox.com", "rbxcdn.com", "rbx.com", "robloxlabs.com", "rbximages.com",
		"rbxusercontent.com", "setup.rbxcdn.com", "versioncompatibility.api.roblox.com",
	},
	"discord": {
		"discord.com", "discordapp.com", "discord.gg", "discord.media", "discordapp.net",
		"discordstatus.com", "discord-attachments-uploads-prd.storage.googleapis.com",
	},
	"category-ai-!cn": {
		"openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com",
		"anthropic.com", "claude.ai", "cursor.sh", "cursor.com", "perplexity.ai",
		"midjourney.com",
	},
	"openai": {
		"openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com",
	},
	"instagram": {
		"instagram.com", "cdninstagram.com", "threads.net",
	},
	"facebook": {
		"facebook.com", "fbcdn.net", "fbsbx.com", "meta.com", "fb.me",
	},
	"twitter": {
		"twitter.com", "x.com", "twimg.com", "t.co",
	},
	"google": {
		"google.com", "googleapis.com", "gstatic.com", "googleusercontent.com", "googledrive.com",
	},
	"rutracker": {
		"rutracker.org", "rutracker.net", "rutracker.cc", "torstats.org",
	},
	"notion": {
		"notion.so", "notion.site", "notion.new",
	},
	"spotify": {
		"spotify.com", "scdn.co", "spotifycdn.com",
	},
	"netflix": {
		"netflix.com", "nflxvideo.net", "nflxext.com", "nflximg.net",
	},
	"github": {
		"github.com", "githubusercontent.com", "github.io", "githubassets.com",
	},
}

func matchesGeosite(domain string, category string) bool {
	cat := strings.ToLower(strings.TrimSpace(category))
	dom := strings.ToLower(strings.TrimSpace(domain))

	// Check if exact category in map
	if list, ok := knownGeositeMap[cat]; ok {
		for _, item := range list {
			if dom == item || strings.HasSuffix(dom, "."+item) {
				return true
			}
		}
	}

	// Sub-keywords check
	if strings.Contains(dom, cat) {
		return true
	}
	return false
}

func evaluateMihomoDNS(domain string, dnsRules []DNSRuleSpec, dnsServers []DNSServerSpec, routeDest string, winningRule *Rule) *InspectDNSInfoDTO {
	dom := strings.ToLower(strings.TrimSpace(domain))
	if dom == "" {
		return nil
	}

	dnsServerByTag := make(map[string]DNSServerSpec)
	for _, srv := range dnsServers {
		dnsServerByTag[srv.Tag] = srv
	}

	matchedRuleIdx := -1
	var matchedServerTag string
	var matchReason string
	var policyPattern string

	for i, r := range dnsRules {
		// 1. domain exact
		for _, d := range r.Domain {
			if strings.EqualFold(dom, strings.TrimSpace(d)) {
				matchedRuleIdx = i
				matchedServerTag = r.Server
				policyPattern = d
				matchReason = fmt.Sprintf("точное совпадение домена %s", d)
				break
			}
		}
		if matchedRuleIdx >= 0 {
			break
		}

		// 2. domain suffix
		for _, ds := range r.DomainSuffix {
			suf := strings.ToLower(strings.TrimSpace(ds))
			if suf != "" && (dom == suf || strings.HasSuffix(dom, "."+suf)) {
				matchedRuleIdx = i
				matchedServerTag = r.Server
				policyPattern = "+." + ds
				matchReason = fmt.Sprintf("совпало по суффиксу .%s", ds)
				break
			}
		}
		if matchedRuleIdx >= 0 {
			break
		}

		// 3. domain keyword
		for _, dk := range r.DomainKeyword {
			kw := strings.ToLower(strings.TrimSpace(dk))
			if kw != "" && strings.Contains(dom, kw) {
				matchedRuleIdx = i
				matchedServerTag = r.Server
				policyPattern = "domain:" + dk
				matchReason = fmt.Sprintf("содержит ключевое слово %q", dk)
				break
			}
		}
		if matchedRuleIdx >= 0 {
			break
		}

		// 4. rule_set / geosite
		for _, rs := range r.RuleSet {
			cleanRS := strings.TrimPrefix(strings.TrimPrefix(rs, "geosite-"), "geosite:")
			if matchesGeosite(dom, cleanRS) {
				matchedRuleIdx = i
				matchedServerTag = r.Server
				policyPattern = "geosite:" + cleanRS
				matchReason = fmt.Sprintf("домен входит в категорию geosite:%s", cleanRS)
				break
			}
		}
		if matchedRuleIdx >= 0 {
			break
		}
	}

	destUpper := strings.ToUpper(strings.TrimSpace(routeDest))
	srv, srvFound := dnsServerByTag[matchedServerTag]
	serverAddr := srv.Server
	if !srvFound || matchedServerTag == "" {
		// If no explicit DNS rule matched, check if route goes to a proxy/tunnel.
		// If so, Mihomo auto-routes its DNS to the tunnel DNS server.
		matchedRuleIdx = -1
		isProxied := destUpper != "" && !strings.HasPrefix(destUpper, "DIRECT") && !strings.HasPrefix(destUpper, "REJECT")
		if isProxied {
			for i := range dnsServers {
				if dnsServers[i].Detour != "" && !strings.EqualFold(dnsServers[i].Detour, "direct") {
					matchedServerTag = dnsServers[i].Tag
					serverAddr = dnsServers[i].Server
					if winningRule != nil && winningRule.Payload != "" {
						policyPattern = strings.ToLower(winningRule.Type) + ":" + winningRule.Payload
						matchReason = fmt.Sprintf("авто-маршрутизация DNS через VPN (%s)", routeDest)
					} else {
						policyPattern = "auto-proxy"
						matchReason = fmt.Sprintf("авто-маршрутизация DNS через VPN (%s)", routeDest)
					}
					break
				}
			}
		}

		if matchedServerTag == "" {
			matchedServerTag = "dns-direct"
			serverAddr = "77.88.8.8"
			for _, s := range dnsServers {
				if s.Tag == "dns-direct" || s.Tag == "bootstrap" || s.Detour == "" || s.Detour == "direct" {
					matchedServerTag = s.Tag
					serverAddr = s.Server
					break
				}
			}
			policyPattern = "dns.nameserver"
			matchReason = "применяется системный DNS по умолчанию"
		}
	}

	isRemote := false
	if destUpper != "" && !strings.HasPrefix(destUpper, "DIRECT") && !strings.HasPrefix(destUpper, "REJECT") {
		if winningRule != nil {
			wType := strings.ToUpper(winningRule.Type)
			if wType == "GEOSITE" || wType == "DOMAIN" || wType == "DOMAIN-SUFFIX" || wType == "DOMAIN-KEYWORD" || wType == "MATCH" || wType == "RULE-SET" {
				isRemote = true
			}
		} else {
			isRemote = true
		}
	}

	policyStr := ""
	if policyPattern != "" {
		policyStr = fmt.Sprintf("%s → %s", policyPattern, matchedServerTag)
	}

	return &InspectDNSInfoDTO{
		MatchedRule:   matchedRuleIdx,
		Server:        matchedServerTag,
		ServerAddress: serverAddr,
		Reason:        matchReason,
		Policy:        policyStr,
		IsRemoteDNS:   isRemote,
	}
}

func (s *Store) Inspect(ctx context.Context, input InspectInput) (InspectData, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	raw := strings.TrimSpace(input.Domain)
	if raw == "" {
		return InspectData{}, fmt.Errorf("domain is required")
	}

	inputType := "domain"
	parsedIP := net.ParseIP(raw)
	if parsedIP != nil {
		inputType = "ip"
	}

	var rules []*Rule
	if input.KeeneticCloudTunnel && strings.TrimSpace(input.KeeneticCloudOutbound) != "" {
		target := strings.TrimSpace(input.KeeneticCloudOutbound)
		rules = append(rules,
			&Rule{Type: "DOMAIN", Payload: "my.keenetic.net", Outbound: "DIRECT", Enabled: true},
			&Rule{Type: "DOMAIN", Payload: "my.netcraze.net", Outbound: "DIRECT", Enabled: true},
		)
		cloudDomains := []string{
			"keenetic.com", "keenetic.io", "keenetic.net", "keenetic.ru",
			"keenetic.pro", "keenetic.link", "keenetic.name", "keenetic.cloud",
			"netcraze.io", "netcraze.net", "netcraze.pro", "netcraze.ru", "netcraze.com", "netcraze.cloud",
			"crazedns.ru", "crazedns.com", "crazedns.net",
			"omni.ru", "knt9.xyz",
		}
		for _, d := range cloudDomains {
			rules = append(rules, &Rule{Type: "DOMAIN-SUFFIX", Payload: d, Outbound: target, Enabled: true})
		}
		cloudCIDRs := []string{
			"185.162.93.0/24",
			"95.213.212.0/24",
			"87.228.71.0/24",
			"91.92.241.0/24",
			"193.107.216.0/24",
			"178.250.154.0/24",
			"178.72.134.0/24",
			"85.198.119.0/24",
			"37.0.127.0/24",
			"5.35.2.0/24",
			"84.38.177.0/24",
			"49.12.59.0/24",
			"167.233.7.0/24",
		}
		for _, cidr := range cloudCIDRs {
			rules = append(rules, &Rule{Type: "IP-CIDR", Payload: cidr, Outbound: target, Enabled: true, NoResolve: true})
		}
		for _, p := range []string{"9", "3478", "3479", "4044", "5683"} {
			rules = append(rules, &Rule{Type: "DST-PORT", Payload: p, Outbound: target, Enabled: true})
		}
	}
	rules = append(rules, s.data.Rules...)
	matches := make([]InspectMatchDTO, 0, len(rules))
	matchedRule := -1
	var winningRule *Rule
	destination := ""
	final := "DIRECT"

	for i, r := range rules {
		if !r.Enabled {
			continue
		}

		matched := false
		reason := "не совпало"
		cond := fmt.Sprintf("%s, %s", r.Type, r.Payload)
		if r.NoResolve {
			cond += " (no-resolve)"
		}

		rType := strings.ToUpper(strings.TrimSpace(r.Type))
		payload := strings.TrimSpace(r.Payload)

		switch rType {
		case "MATCH":
			matched = true
			reason = "финальное правило MATCH"
		case "DOMAIN":
			if inputType == "domain" {
				matched = strings.EqualFold(raw, payload)
				if matched {
					reason = fmt.Sprintf("точное совпадение домена %s", payload)
				}
			}
		case "DOMAIN-SUFFIX":
			if inputType == "domain" {
				lowRaw := strings.ToLower(raw)
				lowPay := strings.ToLower(payload)
				matched = (lowRaw == lowPay || strings.HasSuffix(lowRaw, "."+lowPay))
				if matched {
					reason = fmt.Sprintf("совпало по суффиксу .%s", payload)
				}
			}
		case "DOMAIN-KEYWORD":
			if inputType == "domain" {
				matched = strings.Contains(strings.ToLower(raw), strings.ToLower(payload))
				if matched {
					reason = fmt.Sprintf("содержит ключевое слово %q", payload)
				}
			}
		case "GEOSITE":
			if inputType == "domain" {
				matched = matchesGeosite(raw, payload)
				if matched {
					reason = fmt.Sprintf("домен входит в категорию geosite:%s", payload)
				} else {
					reason = fmt.Sprintf("домен не входит в категорию geosite:%s", payload)
				}
			} else {
				reason = "geosite неприменим к IP-адресам"
			}
		case "GEOIP":
			if inputType == "ip" {
				matched = false
				reason = fmt.Sprintf("geoip:%s (IP не входит в базу)", payload)
			} else if r.NoResolve {
				matched = false
				reason = "пропущено (no-resolve для домена)"
			} else {
				matched = false
				reason = fmt.Sprintf("geoip:%s (домен не совпал)", payload)
			}
		case "IP-CIDR", "IP-CIDR6":
			if inputType == "ip" {
				_, ipnet, err := net.ParseCIDR(payload)
				if err == nil && ipnet.Contains(parsedIP) {
					matched = true
					reason = fmt.Sprintf("IP входит в подсеть %s", payload)
				} else {
					reason = fmt.Sprintf("IP не входит в подсеть %s", payload)
				}
			} else {
				reason = "IP-CIDR неприменим к домену без резолва"
			}
		case "SRC-IP-CIDR":
			matched = false
			reason = "источник IP не задан (пропущено)"
		case "DST-PORT":
			if input.Port > 0 {
				matched = (strconv.Itoa(input.Port) == payload)
				if matched {
					reason = fmt.Sprintf("порт назначения %d совпал", input.Port)
				}
			} else {
				reason = "порт назначения не указан"
			}
		case "RULE-SET":
			matched = matchesGeosite(raw, payload)
			if matched {
				reason = fmt.Sprintf("совпало по rule-set:%s", payload)
			}
		default:
			reason = fmt.Sprintf("тип %s не поддерживается для симуляции", rType)
		}

		matches = append(matches, InspectMatchDTO{
			Index:      i,
			Matched:    matched,
			Action:     "route",
			Outbound:   r.Outbound,
			Conditions: []string{cond},
			Reason:     reason,
		})

		if matched && matchedRule == -1 {
			matchedRule = i
			winningRule = r
			destination = r.Outbound
		}
	}

	if destination == "" {
		destination = final
	}

	var dnsInfo *InspectDNSInfoDTO
	if inputType == "domain" {
		dnsInfo = evaluateMihomoDNS(raw, input.DNSRules, input.DNSServers, destination, winningRule)
	}

	// Resolve destination group details
	resolvedDest := destination
	for _, g := range s.data.Groups {
		if strings.EqualFold(g.Name, destination) {
			if g.Type == "load-balance" {
				resolvedDest = fmt.Sprintf("%s (LOAD-BALANCE · %s)", g.Name, strings.Join(g.Proxies, ", "))
			} else if len(g.Proxies) > 0 {
				resolvedDest = fmt.Sprintf("%s → %s (%s)", g.Name, g.Proxies[0], strings.ToUpper(g.Type))
			}
			break
		}
	}

	note := "Маршрутизация ядра Mihomo (First-Match-Wins). Конфигурация применяется на лету."

	return InspectData{
		Input:       raw,
		InputType:   inputType,
		Matches:     matches,
		Destination: resolvedDest,
		MatchedRule: matchedRule,
		Final:       final,
		Note:        note,
		DNS:         dnsInfo,
	}, nil
}
