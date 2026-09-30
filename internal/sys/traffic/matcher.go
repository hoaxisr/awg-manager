package traffic

import (
	"context"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/presets"
)

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
	},
	"google": {
		"google.com", "googleapis.com", "gstatic.com", "googleusercontent.com", "1e100.net", "ggpht.com",
	},
	"meta": {
		"facebook.com", "instagram.com", "whatsapp.com", "whatsapp.net", "fbcdn.net",
	},
	"vk": {
		"vk.com", "vk.me", "userapi.com", "mail.ru", "ok.ru", "mycdn.me",
	},
	"yandex": {
		"yandex.ru", "yandex.net", "ya.ru", "kinopoisk.ru", "dzen.ru",
	},
}

// MatchDomain checks if a domain matches any configured routing rules across engines.
func (s *Service) MatchDomain(ctx context.Context, domain string) []ItemRouteStatus {
	if domain == "" {
		return nil
	}
	target := strings.ToLower(domain)
	var matches []ItemRouteStatus

	s.mu.RLock()
	nativeStore := s.nativeStore
	routerSvc := s.routerSvc
	dnsRouteSvc := s.dnsRouteSvc
	hydraSvc := s.hydraSvc
	s.mu.RUnlock()

	// 1. Check Mihomo Rules
	if nativeStore != nil {
		rules := nativeStore.ListRules()
		for _, r := range rules {
			if !r.Enabled {
				continue
			}
			payload := strings.ToLower(strings.TrimSpace(r.Payload))
			matched := false
			matchPattern := r.Type

			switch strings.ToUpper(r.Type) {
			case "DOMAIN":
				if target == payload {
					matched = true
					matchPattern = "DOMAIN " + payload
				}
			case "DOMAIN-SUFFIX":
				if target == payload || strings.HasSuffix(target, "."+payload) {
					matched = true
					matchPattern = "DOMAIN-SUFFIX " + payload
				}
			case "DOMAIN-KEYWORD":
				if strings.Contains(target, payload) {
					matched = true
					matchPattern = "DOMAIN-KEYWORD " + payload
				}
			case "GEOSITE", "RULE-SET":
				if geositeHosts, ok := knownGeositeMap[payload]; ok {
					for _, gh := range geositeHosts {
						if target == gh || strings.HasSuffix(target, "."+gh) {
							matched = true
							matchPattern = "RULE-SET " + payload
							break
						}
					}
				} else if target == payload || strings.HasSuffix(target, "."+payload) {
					matched = true
					matchPattern = "RULE-SET " + payload
				}
			}

			if matched {
				matches = append(matches, ItemRouteStatus{
					Target:         "mihomo",
					TargetLabel:    "Mihomo",
					RuleName:       r.Outbound,
					RuleID:         r.ID,
					MatchedPattern: matchPattern,
					IsDirect:       strings.EqualFold(r.Outbound, "direct") || strings.EqualFold(r.Outbound, "direct-out"),
				})
			}
		}
	}

	// 2. Check Sing-box Rules
	if routerSvc != nil {
		if rules, err := routerSvc.ListRules(ctx); err == nil {
			for _, r := range rules {
				matched := false
				pattern := ""
				for _, d := range r.Domain {
					if target == strings.ToLower(d) {
						matched = true
						pattern = "domain: " + d
					}
					if matched {
						break
					}
				}
				if !matched {
					for _, d := range r.DomainSuffix {
						dl := strings.ToLower(d)
						if target == dl || strings.HasSuffix(target, "."+dl) {
							matched = true
							pattern = "domain_suffix: " + d
							break
						}
					}
				}
				if matched {
					matches = append(matches, ItemRouteStatus{
						Target:         "singbox",
						TargetLabel:    "Sing-box",
						RuleName:       r.Outbound,
						MatchedPattern: pattern,
						IsDirect:       strings.EqualFold(r.Outbound, "direct"),
					})
				}
			}
		}
	}

	// 3. Check Presets Catalog
	if builtins, err := presets.LoadBuiltins(); err == nil {
		for _, p := range builtins {
			if p.Engines.DNS != nil {
				for _, d := range p.Engines.DNS.Domains {
					dl := strings.ToLower(d)
					if target == dl || strings.HasSuffix(target, "."+dl) {
						matches = append(matches, ItemRouteStatus{
							Target:         "catalog",
							TargetLabel:    "Каталог",
							RuleName:       p.Name,
							RuleID:         p.ID,
							MatchedPattern: d,
							IsDirect:       false,
						})
						break
					}
				}
			}
		}
	}

	// 4. Check HydraRoute Neo
	if hydraSvc != nil {
		if hrRules, _, err := hydraSvc.ListRules(); err == nil {
			for _, r := range hrRules {
				if r.Disabled {
					continue
				}
				matched := false
				for _, d := range r.Domains {
					dl := strings.ToLower(d)
					if strings.HasPrefix(dl, "geosite:") {
						tag := strings.TrimPrefix(dl, "geosite:")
						if geositeHosts, ok := knownGeositeMap[tag]; ok {
							for _, gh := range geositeHosts {
								if target == gh || strings.HasSuffix(target, "."+gh) {
									matched = true
									break
								}
							}
						}
					} else if target == dl || strings.HasSuffix(target, "."+dl) {
						matched = true
					}
					if matched {
						matches = append(matches, ItemRouteStatus{
							Target:         "hydraroute",
							TargetLabel:    "HydraRoute Neo",
							RuleName:       r.Name,
							MatchedPattern: d,
							IsDirect:       false,
						})
						break
					}
				}
			}
		}
	}

	// 5. Check NDMS DNS Routes (only pure NDMS lists, not HydraRoute delegates)
	if dnsRouteSvc != nil {
		if dnsLists, err := dnsRouteSvc.List(ctx); err == nil {
			for _, l := range dnsLists {
				if !l.Enabled || l.Backend == "hydraroute" || strings.HasPrefix(l.ID, "hr_") {
					continue
				}
				for _, d := range l.Domains {
					dl := strings.ToLower(d)
					if target == dl || strings.HasSuffix(target, "."+dl) {
						matches = append(matches, ItemRouteStatus{
							Target:         "ndms",
							TargetLabel:    "NDMS DNS",
							RuleName:       l.Name,
							RuleID:         l.ID,
							MatchedPattern: d,
							IsDirect:       false,
						})
						break
					}
				}
			}
		}
	}

	return matches
}

// MatchIP checks if an IP belongs to any CIDRs in active routing rules or catalog presets.
func (s *Service) MatchIP(ctx context.Context, ipStr string) []ItemRouteStatus {
	if ipStr == "" || isLANIP(ipStr) {
		return nil
	}
	parsedIP := net.ParseIP(ipStr)
	if parsedIP == nil {
		return nil
	}

	var matches []ItemRouteStatus

	s.mu.RLock()
	nativeStore := s.nativeStore
	routerSvc := s.routerSvc
	staticRouteSvc := s.staticRouteSvc
	dnsRouteSvc := s.dnsRouteSvc
	hydraSvc := s.hydraSvc
	s.mu.RUnlock()

	// 1. Check Mihomo Rules for IP-CIDR
	if nativeStore != nil {
		rules := nativeStore.ListRules()
		for _, r := range rules {
			if !r.Enabled {
				continue
			}
			t := strings.ToUpper(r.Type)
			if t == "IP-CIDR" || t == "IP-CIDR6" {
				payload := strings.TrimSpace(r.Payload)
				if _, cidrNet, err := net.ParseCIDR(payload); err == nil {
					if cidrNet.Contains(parsedIP) {
						matches = append(matches, ItemRouteStatus{
							Target:         "mihomo",
							TargetLabel:    "Mihomo",
							RuleName:       r.Outbound,
							RuleID:         r.ID,
							MatchedPattern: "IP-CIDR " + payload,
							IsDirect:       strings.EqualFold(r.Outbound, "direct") || strings.EqualFold(r.Outbound, "direct-out"),
						})
					}
				}
			}
		}
	}

	// 2. Check Sing-box Rules for ip_cidr
	if routerSvc != nil {
		if rules, err := routerSvc.ListRules(ctx); err == nil {
			for _, r := range rules {
				for _, cidr := range r.IPCIDR {
					if _, cidrNet, err := net.ParseCIDR(cidr); err == nil {
						if cidrNet.Contains(parsedIP) {
							matches = append(matches, ItemRouteStatus{
								Target:         "singbox",
								TargetLabel:    "Sing-box",
								RuleName:       r.Outbound,
								MatchedPattern: "ip_cidr: " + cidr,
								IsDirect:       strings.EqualFold(r.Outbound, "direct"),
							})
							break
						}
					}
				}
			}
		}
	}

	// 3. Check Presets Catalog for Subnets / IPs
	if builtins, err := presets.LoadBuiltins(); err == nil {
		for _, p := range builtins {
			if p.Engines.DNS != nil {
				for _, pSubnet := range p.Engines.DNS.Subnets {
					if strings.Contains(pSubnet, "/") {
						if _, cidrNet, err := net.ParseCIDR(pSubnet); err == nil {
							if cidrNet.Contains(parsedIP) {
								matches = append(matches, ItemRouteStatus{
									Target:         "catalog",
									TargetLabel:    "Каталог",
									RuleName:       p.Name,
									RuleID:         p.ID,
									MatchedPattern: pSubnet,
									IsDirect:       false,
								})
								break
							}
						}
					} else if pSubnet == ipStr {
						matches = append(matches, ItemRouteStatus{
							Target:         "catalog",
							TargetLabel:    "Каталог",
							RuleName:       p.Name,
							RuleID:         p.ID,
							MatchedPattern: pSubnet,
							IsDirect:       false,
						})
						break
					}
				}
			}
		}
	}

	// 4. Check Static IP Routes (NDMS)
	if staticRouteSvc != nil {
		if staticLists, err := staticRouteSvc.List(); err == nil {
			for _, rl := range staticLists {
				if !rl.Enabled {
					continue
				}
				for _, s := range rl.Subnets {
					s = strings.TrimSpace(s)
					if strings.Contains(s, "/") {
						if _, cidrNet, err := net.ParseCIDR(s); err == nil {
							if cidrNet.Contains(parsedIP) {
								matches = append(matches, ItemRouteStatus{
									Target:         "static_route",
									TargetLabel:    "Статический IP",
									RuleName:       rl.Name,
									RuleID:         rl.ID,
									MatchedPattern: s,
									IsDirect:       false,
								})
								break
							}
						}
					} else if s == ipStr {
						matches = append(matches, ItemRouteStatus{
							Target:         "static_route",
							TargetLabel:    "Статический IP",
							RuleName:       rl.Name,
							RuleID:         rl.ID,
							MatchedPattern: s,
							IsDirect:       false,
						})
						break
					}
				}
			}
		}
	}

	// 5. Check NDMS DNS Route Subnets (only pure NDMS lists, not HydraRoute delegates)
	if dnsRouteSvc != nil {
		if dnsLists, err := dnsRouteSvc.List(ctx); err == nil {
			for _, l := range dnsLists {
				if !l.Enabled || l.Backend == "hydraroute" || strings.HasPrefix(l.ID, "hr_") {
					continue
				}
				for _, s := range l.Subnets {
					s = strings.TrimSpace(s)
					if strings.Contains(s, "/") {
						if _, cidrNet, err := net.ParseCIDR(s); err == nil {
							if cidrNet.Contains(parsedIP) {
								matches = append(matches, ItemRouteStatus{
									Target:         "ndms",
									TargetLabel:    "NDMS DNS",
									RuleName:       l.Name,
									RuleID:         l.ID,
									MatchedPattern: s,
									IsDirect:       false,
								})
								break
							}
						}
					} else if s == ipStr {
						matches = append(matches, ItemRouteStatus{
							Target:         "ndms",
							TargetLabel:    "NDMS DNS",
							RuleName:       l.Name,
							RuleID:         l.ID,
							MatchedPattern: s,
							IsDirect:       false,
						})
						break
					}
				}
			}
		}
	}

	// 6. Check HydraRoute Neo Subnets
	if hydraSvc != nil {
		if hrRules, _, err := hydraSvc.ListRules(); err == nil {
			for _, r := range hrRules {
				if r.Disabled {
					continue
				}
				for _, s := range r.Subnets {
					s = strings.TrimSpace(s)
					if strings.Contains(s, "/") {
						if _, cidrNet, err := net.ParseCIDR(s); err == nil {
							if cidrNet.Contains(parsedIP) {
								matches = append(matches, ItemRouteStatus{
									Target:         "hydraroute",
									TargetLabel:    "HydraRoute Neo",
									RuleName:       r.Name,
									MatchedPattern: s,
									IsDirect:       false,
								})
								break
							}
						}
					} else if s == ipStr {
						matches = append(matches, ItemRouteStatus{
							Target:         "hydraroute",
							TargetLabel:    "HydraRoute Neo",
							RuleName:       r.Name,
							MatchedPattern: s,
							IsDirect:       false,
						})
						break
					}
				}
			}
		}
	}

	return matches
}

// GetActiveEngines returns the list of available routing engines on the system.
func (s *Service) GetActiveEngines() []ActiveEngineInfo {
	engines := []ActiveEngineInfo{
		{
			ID:          "catalog",
			Label:       "Каталог сервисов",
			Description: "Универсальный каталог сервисов и правил для всех движков",
			Active:      true,
		},
	}

	routingEngine := "sing-box"

	isMihomo := routingEngine == "mihomo" || routingEngine == "both"
	isSingbox := routingEngine == "sing-box" || routingEngine == "both"

	if isMihomo {
		engines = append(engines, ActiveEngineInfo{
			ID:          "mihomo",
			Label:       "Mihomo",
			Description: "Правила маршрутизации и прокси-группы ядра Mihomo",
			Active:      true,
		})
	}

	if isSingbox {
		engines = append(engines, ActiveEngineInfo{
			ID:          "singbox",
			Label:       "Sing-box",
			Description: "Правила маршрутизации ядра Sing-box",
			Active:      true,
		})
	}

	engines = append(engines, ActiveEngineInfo{
		ID:          "hydraroute",
		Label:       "HydraRoute Neo",
		Description: "Списки доменной маршрутизации Keenetic",
		Active:      true,
	})

	engines = append(engines, ActiveEngineInfo{
		ID:          "static_route",
		Label:       "Статический IP",
		Description: "Системные маршруты роутера Keenetic",
		Active:      true,
	})

	return engines
}
