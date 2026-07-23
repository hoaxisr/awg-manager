// Package wdtt manages the WDTT/qWDTT headless client as a child process:
// persisting configuration, parsing share links, and tracking lifecycle.
package wdtt

import "time"

// ClientConfig mirrors flags accepted by the wdtt-client binary.
type ClientConfig struct {
	Enabled bool `json:"enabled"`

	Listen    string `json:"listen"`              // -listen, local UDP addr (default 127.0.0.1:9000)
	Peer      string `json:"peer"`                // -peer, VPS host:dtls_port
	Password  string `json:"password"`            // -password, WRAP key derivation
	VKHashes  string `json:"vkHashes"`            // -vk, comma-separated call hashes
	Workers   int    `json:"workers"`             // -n, parallel workers (multiple of 12)
	Obfs      string `json:"obfs"`                // -obfs, audio|video
	Fingerprint string `json:"fingerprint"`       // -fingerprint
	DeviceID  string `json:"deviceId,omitempty"`  // -device-id
	CaptchaMode string `json:"captchaMode"`         // -captcha-mode: auto|rjs|wv (router default rjs)
	VKAuthMode  string `json:"vkAuthMode,omitempty"` // -vk-auth-mode
	Sub       string `json:"sub,omitempty"`       // subscription URL (metadata only)
	Debug     bool   `json:"debug"`
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		Listen:      "127.0.0.1:9000",
		Workers:     24,
		Obfs:        "audio",
		Fingerprint: "chrome",
		CaptchaMode: "rjs",
		VKAuthMode:  "vkcalls",
	}
}

type ClientInstance struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Config ClientConfig `json:"config"`
}

type Config struct {
	Version int              `json:"version,omitempty"`
	Clients []ClientInstance `json:"clients"`
}

func DefaultConfig() Config {
	return Config{
		Version: ConfigVersion,
		Clients: []ClientInstance{{
			ID:     DefaultInstanceID,
			Name:   "Клиент",
			Config: DefaultClientConfig(),
		}},
	}
}

type CreateClientInput struct {
	Name   string        `json:"name,omitempty"`
	Config *ClientConfig `json:"config,omitempty"`
}

type ProcessStatus struct {
	Running       bool       `json:"running"`
	PID           int        `json:"pid,omitempty"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	Log           string     `json:"log,omitempty"`
	WgConfig          string `json:"wgConfig,omitempty"`
	DtlsConnections   int    `json:"dtlsConnections,omitempty"`
	Binary            string `json:"binary"`
	BinaryPresent bool       `json:"binaryPresent"`
}

type InstanceStatus struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Status ProcessStatus `json:"status"`
}

type Status struct {
	Clients []InstanceStatus `json:"clients"`
	Client  ProcessStatus    `json:"client"`
	InstallAvailable bool   `json:"installAvailable"`
	InstallVersion   string `json:"installVersion,omitempty"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	UpdateAvailable  bool   `json:"updateAvailable"`
	Installing       bool   `json:"installing"`
	RouterClock      string `json:"routerClock,omitempty"`
}

// ImportPayload is the normalized result of wdtt://, qwdtt:// or subscription import.
type ImportPayload struct {
	Name      string   `json:"name,omitempty"`
	Peer      string   `json:"peer"`
	Password  string   `json:"password"`
	VKHashes  []string `json:"vkHashes"`
	Workers   int      `json:"workers,omitempty"`
	Listen    string   `json:"listen,omitempty"`
	SubURL    string   `json:"subUrl,omitempty"`
	DeviceID  string   `json:"deviceId,omitempty"`
	WG        string   `json:"wg,omitempty"` // optional bundled WireGuard client config
}
