package telemt

// Config describes the configuration for the Telegram MTProto proxy (telemt).
type Config struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode"` // "direct" (Fake-TLS) or "web" (Native Web Proxy)
	Port           int    `json:"port"`
	ListenIP       string `json:"listenIp"`
	Secret         string `json:"secret"`
	TLSDomain      string `json:"tlsDomain"`
	WebHost        string `json:"webHost,omitempty"`    // e.g. "proxy.example.com"
	WebCarrier     string `json:"webCarrier,omitempty"` // "https-lanes", "websocket", "https"
	WebDecoy       string `json:"webDecoy,omitempty"`   // e.g. "http://127.0.0.1:80"
	UpstreamDevice string `json:"upstreamDevice,omitempty"`
}

// Status represents the operational status of telemt.
type Status struct {
	Installed       bool    `json:"installed"`
	Running         bool    `json:"running"`
	PID             int     `json:"pid,omitempty"`
	Version         string  `json:"version,omitempty"`
	LatestVersion   string  `json:"latestVersion,omitempty"`
	UpdateAvailable bool    `json:"updateAvailable"`
	ArchSupported   bool    `json:"archSupported"`
	Binary          string  `json:"binary,omitempty"`
	Arch            string  `json:"arch,omitempty"`
	Source          string  `json:"source,omitempty"`
	Link            string  `json:"link,omitempty"`
	Error           string  `json:"error,omitempty"`
	Config          *Config `json:"config,omitempty"`
}
