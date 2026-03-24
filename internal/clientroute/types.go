package clientroute

// ClientRoute represents a per-device VPN routing rule.
type ClientRoute struct {
	ID             string `json:"id"`
	ClientIP       string `json:"clientIp"`
	ClientHostname string `json:"clientHostname"`
	TunnelID       string `json:"tunnelId"`
	Fallback       string `json:"fallback"` // "drop" | "bypass"
	Enabled        bool   `json:"enabled"`
}
