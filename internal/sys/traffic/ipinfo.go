package traffic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type ipInfoCacheEntry struct {
	knowledge *DomainKnowledge
	cachedAt  time.Time
}

var (
	ipInfoMu    sync.RWMutex
	ipInfoCache = make(map[string]ipInfoCacheEntry)
	httpClient  = &http.Client{Timeout: 2 * time.Second}
)

// LookupIPInfo queries ASN and Organization information for an IP address with in-memory caching.
func LookupIPInfo(ctx context.Context, ip string) *DomainKnowledge {
	if ip == "" || isLANIP(ip) {
		return nil
	}

	ipInfoMu.RLock()
	if entry, ok := ipInfoCache[ip]; ok {
		if time.Since(entry.cachedAt) < 24*time.Hour {
			ipInfoMu.RUnlock()
			return entry.knowledge
		}
	}
	ipInfoMu.RUnlock()

	// Try fast HTTP lookup from ip-api
	url := fmt.Sprintf("http://ip-api.com/json/%s?fields=status,country,countryCode,city,isp,org,asname", ip)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var raw struct {
		Status      string `json:"status"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		City        string `json:"city"`
		ISP         string `json:"isp"`
		Org         string `json:"org"`
		ASName      string `json:"asname"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil || raw.Status != "success" {
		return nil
	}

	orgName := raw.Org
	if orgName == "" {
		orgName = raw.ISP
	}
	if orgName == "" {
		orgName = raw.ASName
	}

	title := orgName
	if raw.ASName != "" && raw.ASName != orgName {
		title = fmt.Sprintf("%s (%s)", orgName, raw.ASName)
	}

	desc := fmt.Sprintf("Автономная система: %s · %s, %s", orgName, raw.City, raw.Country)
	if raw.City == "" {
		desc = fmt.Sprintf("Организация: %s · %s", orgName, raw.Country)
	}

	countryRu := translateCountry(raw.CountryCode, raw.Country)

	k := &DomainKnowledge{
		Title:       title,
		Description: desc,
		Org:         orgName,
		Country:     countryRu,
		CountryCode: raw.CountryCode,
		Category:    "cloud",
		Icon:        "server",
	}

	ipInfoMu.Lock()
	ipInfoCache[ip] = ipInfoCacheEntry{
		knowledge: k,
		cachedAt:  time.Now(),
	}
	ipInfoMu.Unlock()

	return k
}

func translateCountry(cc, fallback string) string {
	switch cc {
	case "RU":
		return "Россия"
	case "BY":
		return "Беларусь"
	case "KZ":
		return "Казахстан"
	case "UA":
		return "Украина"
	case "US":
		return "США"
	case "DE":
		return "Германия"
	case "NL":
		return "Нидерланды"
	case "FI":
		return "Финляндия"
	case "SE":
		return "Швеция"
	case "GB":
		return "Великобритания"
	case "FR":
		return "Франция"
	case "PL":
		return "Польша"
	case "AE":
		return "ОАЭ"
	case "JP":
		return "Япония"
	case "CN":
		return "Китай"
	case "SG":
		return "Сингапур"
	case "TR":
		return "Турция"
	}
	return fallback
}
