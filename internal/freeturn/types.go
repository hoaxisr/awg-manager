// Package freeturn manages the FreeTurn TURN-tunnel client and server as
// child processes: persisting their configuration, building CLI arguments
// from it, and tracking process lifecycle (start/stop/status).
//
// See https://github.com/samosvalishe/free-turn-proxy/blob/master/docs/flags.md
// for the upstream flag reference this package mirrors.
package freeturn

import "time"

// ClientConfig mirrors the flags accepted by the freeturn client binary.
type ClientConfig struct {
	Enabled bool `json:"enabled"`

	Listen   string `json:"listen"`          // -listen, local addr WG/Xray connects to (default 127.0.0.1:9000)
	Peer     string `json:"peer"`            // -peer, required: VPS server addr host:port
	Provider string `json:"provider"`        // -provider, default "vk"
	Links    string `json:"links,omitempty"` // -links, comma-separated VK Calls URLs; required when provider=vk
	// (-link, singular, is upstream's deprecated one-link alias — we always
	// emit -links so multiple call links, i.e. multiple credential pools,
	// work: see internal/config/config.go in samosvalishe/free-turn-proxy.)
	Streams   int    `json:"streams"`   // -n, parallel TURN streams *per link* (default 10)
	Transport string `json:"transport"` // -transport, tcp|udp (transport to TURN relay)
	Mode      string `json:"mode"`      // -mode, udp|tcp (tunnel mode)
	Bond      bool   `json:"bond"`      // -bond, only meaningful with mode=tcp

	TurnHost string `json:"turnHost,omitempty"` // -turn, override TURN server IP
	TurnPort int    `json:"turnPort,omitempty"` // -port, override TURN server port

	ObfProfile string `json:"obfProfile"`       // -obf-profile, none|rtpopus|rtpopus2|rtpopus3 (internal/config/config.go ObfProfile enum; upstream docs/flags.md lags behind)
	ObfKey     string `json:"obfKey,omitempty"` // -obf-key, 64 hex chars, required if obfProfile != none

	StreamsPerCred int    `json:"streamsPerCred"`                                       // -streams-per-cred, provider=vk only
	Platform       string `json:"platform" swaggertype:"string" enums:"desktop,mobile"` // -platform, provider=vk only (persona class)

	DNSMode    string `json:"dnsMode"`              // -dns-mode, plain|doh|auto
	DNSServers string `json:"dnsServers,omitempty"` // -dns-servers, ip[:port][,ip[:port]...]

	ClientID string `json:"clientId,omitempty"` // -client-id, auto-generated if empty
	Sub      string `json:"sub,omitempty"`      // -sub, subscription URL (see docs/sub.md)
	Debug    bool   `json:"debug"`
}

// DefaultClientConfig mirrors the binary's own flag defaults so an empty
// saved config behaves the same as `freeturn-client` with no flags at all.
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		Listen:         "127.0.0.1:9000",
		Provider:       "vk",
		Streams:        10,
		Transport:      "tcp",
		Mode:           "udp",
		ObfProfile:     "none",
		StreamsPerCred: 10,
		Platform:       "desktop",
		DNSMode:        "auto",
	}
}

// ServerConfig mirrors the flags accepted by the freeturn server binary.
type ServerConfig struct {
	Enabled bool `json:"enabled"`

	Listen      string `json:"listen"`                // -listen, default 0.0.0.0:56000
	Connect     string `json:"connect"`               // -connect, required: local backend host:port
	Mode        string `json:"mode"`                  // -mode, udp|tcp
	ObfProfile  string `json:"obfProfile"`            // -obf-profile, none|rtpopus|rtpopus2|rtpopus3
	ObfKey      string `json:"obfKey,omitempty"`      // -obf-key
	ClientsFile string `json:"clientsFile,omitempty"` // -clients-file, enables Client-ID allowlist auth
	Debug       bool   `json:"debug"`

	// OpenFirewall opens the server listen port in Keenetic INPUT (iptables).
	// nil / omitted → true (WAN relay works out of the box).
	OpenFirewall *bool `json:"openFirewall,omitempty"`
}

// DefaultServerConfig mirrors the binary's own flag defaults.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Listen:     "0.0.0.0:56000",
		Mode:       "udp",
		ObfProfile: "none",
	}
}

// ClientInstance is one freeturn-client profile (separate process + listen port).
type ClientInstance struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Config ClientConfig `json:"config"`
}

// ServerInstance is one freeturn-server profile (separate process + listen port).
type ServerInstance struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Config ServerConfig `json:"config"`
}

// Config is the full persisted FreeTurn configuration. Multiple client and
// server instances can run concurrently on different listen ports.
type Config struct {
	Version int              `json:"version,omitempty"`
	Clients []ClientInstance `json:"clients"`
	Servers []ServerInstance `json:"servers"`

	// Legacy v1 fields — read during migration only, never written back.
	Client ClientConfig `json:"client,omitempty"`
	Server ServerConfig `json:"server,omitempty"`
}

// DefaultConfig returns a v2 config with one default client and server.
func DefaultConfig() Config {
	return Config{
		Version: ConfigVersion,
		Clients: []ClientInstance{{
			ID:     DefaultInstanceID,
			Name:   "Клиент",
			Config: DefaultClientConfig(),
		}},
		Servers: []ServerInstance{{
			ID:     DefaultInstanceID,
			Name:   "Сервер",
			Config: DefaultServerConfig(),
		}},
	}
}

// CreateClientInput is the body for POST /api/freeturn/clients.
type CreateClientInput struct {
	Name   string        `json:"name,omitempty"`
	Config *ClientConfig `json:"config,omitempty"`
}

// CreateServerInput is the body for POST /api/freeturn/servers.
type CreateServerInput struct {
	Name   string        `json:"name,omitempty"`
	Config *ServerConfig `json:"config,omitempty"`
}

// ProcessStatus describes the live state of one managed child process.
type ProcessStatus struct {
	Running   bool       `json:"running"`
	PID       int        `json:"pid,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// LastError holds the tail of stderr from the most recent exit, only
	// populated when the process is not currently running and the last
	// run ended in a failure (e.g. missing -peer, captcha required, etc).
	LastError string `json:"lastError,omitempty"`
	// Log is the tail of combined stdout+stderr from the current (or most
	// recent) run — e.g. TURN allocation / VK-auth progress, handshake
	// confirmations — so the panel can show whether the tunnel actually
	// connected instead of just "running".
	Log string `json:"log,omitempty"`
	// DtlsConnections is the number of freeturn streams with an established
	// DTLS session in the current log tail (0 when not running).
	DtlsConnections int `json:"dtlsConnections,omitempty"`
	// Binary is the configured binary path; BinaryPresent reports whether
	// an executable actually exists there. awg-manager does NOT ship the
	// freeturn binaries — the panel uses this to show an honest
	// «установите бинарь» hint instead of an opaque exec error on Start.
	Binary        string `json:"binary"`
	BinaryPresent bool   `json:"binaryPresent"`
	// OrphanedPID — процесс НАШ и живой, но pid-файл унаследован: startedAt
	// нет, потому что запускал его прошлый экземпляр демона. Лога и телеметрии
	// по такому процессу у нас не будет, health-надзор по нему слеп; лечится
	// обычным стартом — process.Start его усыновляет, и это же делает
	// супервизор (supervisor.go). Не путать со stalePid у HydraRoute: там
	// признак ровно обратный — pid-файл есть, а процесса уже нет.
	OrphanedPID bool `json:"orphanedPid,omitempty"`
}

// InstanceStatus pairs instance metadata with live process state.
type InstanceStatus struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Status ProcessStatus `json:"status"`
}

// Status is the combined status returned to the API/frontend.
type Status struct {
	Clients []InstanceStatus `json:"clients"`
	Servers []InstanceStatus `json:"servers"`
	// Client/Server mirror the default instance for legacy API consumers.
	Client ProcessStatus `json:"client"`
	Server ProcessStatus `json:"server"`
	// InstallAvailable: для этой архитектуры есть закреплённая сборка и
	// загрузчик — панель может предложить установку в один клик.
	InstallAvailable bool `json:"installAvailable"`
	// InstallVersion — версия freeturn, которую поставит установка.
	InstallVersion string `json:"installVersion,omitempty"`
	// InstalledVersion — версия, записанная после последней установки.
	InstalledVersion string `json:"installedVersion,omitempty"`
	// UpdateAvailable — бинари отсутствуют или устарели относительно InstallVersion.
	UpdateAvailable bool `json:"updateAvailable"`
	// Installing — установка сейчас идёт (кнопка блокируется).
	Installing bool `json:"installing"`
	// RouterClock — текущее время роутера для сверки с метками в логе freeturn.
	RouterClock string `json:"routerClock,omitempty"`
}
