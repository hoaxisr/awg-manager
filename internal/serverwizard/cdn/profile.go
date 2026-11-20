package cdn

import (
	"fmt"
	"net"
	"strings"
)

// Standard capability identifiers.
const (
	CapDirect         = "direct"
	CapGetOnly        = "get_only"
	CapGetPost        = "get_post"
	CapWebSocket      = "websocket"
	CapXHTTPStreaming = "xhttp_streaming"
)

// Standard Profile IDs.
const (
	ProfileDirectID  = "direct"
	ProfileCDNGetID  = "cdn_get"
	ProfileCDNWSID   = "cdn_ws"
	ProfileCDNFullID = "cdn_full"
)

type Profile struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
	Instructions string   `json:"instructions"`
	Recommended  bool     `json:"recommended"`
}

type DNSRecord struct {
	Type    string `json:"type"` // "A", "AAAA", "CNAME"
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
	Note    string `json:"note"`
}

var builtInProfiles = []Profile{
	{
		ID:           ProfileDirectID,
		Name:         "Прямое подключение (Direct / Port Forward)",
		Description:  "Прямой доступ к роутеру по белому IP или DDNS (без промежуточных CDN-прокси). Минимальная задержка.",
		Capabilities: []string{CapDirect, CapGetPost, CapWebSocket},
		Instructions: "Настройте проброс портов на роутере. В DNS укажите A-запись без проксирования (DNS only / серый значок).",
		Recommended:  false,
	},
	{
		ID:           ProfileCDNGetID,
		Name:         "CDN GET-only (CDN с агрессивным кешированием)",
		Description:  "Совместимо с бесплатными тарифами CDN, где разрешён только HTTP GET или POST блокируется.",
		Capabilities: []string{CapGetOnly},
		Instructions: "В панели CDN создайте DNS-запись (проксированную / включён режим CDN). Отключите кеширование для рабочего пути или создайте правило Cache Bypass.",
		Recommended:  true,
	},
	{
		ID:           ProfileCDNWSID,
		Name:         "CDN WebSocket (Стандартные CDN с поддержкой WS)",
		Description:  "Широкая совместимость со всеми CDN провайдерами, поддерживающими туннелирование WebSocket.",
		Capabilities: []string{CapWebSocket, CapGetPost},
		Instructions: "В настройках CDN включите поддержку WebSockets.",
		Recommended:  false,
	},
	{
		ID:           ProfileCDNFullID,
		Name:         "CDN Full / Advanced (XHTTP Streaming / HTTP/2)",
		Description:  "Максимальная скорость и низкий оверхед при наличии CDN с поддержкой HTTP/2, gRPC или потокового XHTTP.",
		Capabilities: []string{CapDirect, CapGetOnly, CapGetPost, CapWebSocket, CapXHTTPStreaming},
		Instructions: "В настройках CDN включите поддержку HTTP/2 или gRPC, а также отключите буферизацию ответов.",
		Recommended:  false,
	},
}

// ListProfiles returns all available CDN capability profiles.
func ListProfiles() []Profile {
	out := make([]Profile, len(builtInProfiles))
	copy(out, builtInProfiles)
	return out
}

// ListProfilesForServer returns available profiles tailored to a server kind.
// For VLESS (XHTTP) server ("xray" or "both"), direct and cdn_ws (WebSocket) profiles are excluded
// because awgm-xhttpd exclusively supports XHTTP/FiniteProto ingress.
func ListProfilesForServer(serverKind string) []Profile {
	all := ListProfiles()
	if serverKind == "xray" || serverKind == "both" {
		filtered := make([]Profile, 0, len(all))
		for _, p := range all {
			if p.ID != ProfileDirectID && p.ID != ProfileCDNWSID {
				filtered = append(filtered, p)
			}
		}
		return filtered
	}
	return all
}

// GetProfile returns a profile by its ID.
func GetProfile(id string) (Profile, bool) {
	for _, p := range builtInProfiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// GenerateDNSRecords calculates required DNS records based on the profile, domain, and origin target.
func GenerateDNSRecords(profileID, domain, originTarget string) ([]DNSRecord, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return nil, fmt.Errorf("domain cannot be empty")
	}
	originTarget = strings.TrimSpace(originTarget)
	if originTarget == "" {
		return nil, fmt.Errorf("origin target cannot be empty")
	}

	p, ok := GetProfile(profileID)
	if !ok {
		return nil, fmt.Errorf("unknown cdn profile: %s", profileID)
	}

	isProxied := p.ID != ProfileDirectID

	// Check if originTarget is an IP address or hostname
	ip := net.ParseIP(originTarget)
	if ip != nil {
		recType := "A"
		if ip.To4() == nil {
			recType = "AAAA"
		}
		note := "Проксированная запись через CDN"
		if !isProxied {
			note = "Прямая запись без проксирования CDN (DNS-only)"
		}
		return []DNSRecord{
			{
				Type:    recType,
				Name:    domain,
				Content: originTarget,
				Proxied: isProxied,
				TTL:     1, // Auto
				Note:    note,
			},
		}, nil
	}

	// Hostname -> CNAME record
	note := "Проксированная CNAME запись через CDN"
	if !isProxied {
		note = "Прямая CNAME запись без проксирования CDN (DNS-only)"
	}
	return []DNSRecord{
		{
			Type:    "CNAME",
			Name:    domain,
			Content: originTarget,
			Proxied: isProxied,
			TTL:     1, // Auto
			Note:    note,
		},
	}, nil
}
