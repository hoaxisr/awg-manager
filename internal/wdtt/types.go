// Package wdtt manages the WDTT/qWDTT headless client as a child process:
// persisting configuration, parsing share links, and tracking lifecycle.
package wdtt

import (
	"net"
	"strconv"
	"strings"
	"time"
)

// ClientConfig mirrors flags accepted by the wdtt-client binary.
type ClientConfig struct {
	Enabled bool `json:"enabled"`

	Listen      string `json:"listen"`               // -listen, local UDP addr (default 127.0.0.1:9000)
	Peer        string `json:"peer"`                 // -peer, VPS host:dtls_port
	Password    string `json:"password"`             // -password, WRAP key derivation
	VKHashes    string `json:"vkHashes"`             // -vk, comma-separated call hashes
	Workers     int    `json:"workers"`              // -n, parallel workers (multiple of 12)
	Obfs        string `json:"obfs"`                 // -obfs, audio|video
	Fingerprint string `json:"fingerprint"`          // -fingerprint
	DeviceID    string `json:"deviceId,omitempty"`   // -device-id
	CaptchaMode string `json:"captchaMode"`          // -captcha-mode: auto|rjs|wv (router default rjs)
	VKAuthMode  string `json:"vkAuthMode,omitempty"` // -vk-auth-mode
	Sub         string `json:"sub,omitempty"`        // subscription URL (metadata only)
	// ConnMode — wg (WireGuard + AWG-туннель) или raw (без WG, быстрее; нужен raw-сервер).
	ConnMode string `json:"connMode,omitempty"`
	PeerWg   string `json:"peerWg,omitempty"`  // VPS:DTLS для connMode=wg; Peer зеркалит активный слот (normalizePeers)
	PeerRaw  string `json:"peerRaw,omitempty"` // VPS:Raw для connMode=raw
	Debug    bool   `json:"debug"`

	// Raw client: OpkgTun17..49 в NDMS (маршрутизация LAN; NAT — на wdtt-server).
	NdmsIface    string `json:"ndmsIface,omitempty"`    // OpkgTun17..49
	RawIface     string `json:"rawIface,omitempty"`     // kernel opkgtunN
	RawClientIP  string `json:"rawClientIp,omitempty"`  // из RAWCONF VPS
	RawClientMTU int    `json:"rawClientMTU,omitempty"` // MTU из RAWCONF VPS

	// PolicyPermits — политики, где OpkgTun разрешён; восстанавливаются после рестарта awg-manager.
	PolicyPermits []OpkgPolicyPermit `json:"policyPermits,omitempty"`
}

// normalizePeers держит инвариант «Peer = слот активного режима».
//
// Главнее Peer, а не слот: адрес приходит и от тех, кто про слоты не знает —
// подписка, импорт ссылки, сторонний API-клиент, — и их свежий Peer не должен
// откатываться протухшим слотом. Слот активного режима зеркалит Peer, слот
// соседнего режима сохраняется нетронутым; пустой Peer восстанавливается из
// слота (конфиги, созданные до появления слотов).
//
// Подставить адрес соседнего режима ПРИ переключении — работа формы: только
// она знает, что режим меняет пользователь, а не приезжает из подписки.
func normalizePeers(cfg ClientConfig) ClientConfig {
	slot := &cfg.PeerWg
	if !cfg.UsesWireGuard() {
		slot = &cfg.PeerRaw
	}
	if peer := strings.TrimSpace(cfg.Peer); peer != "" {
		cfg.Peer = peer
		*slot = peer
		return cfg
	}
	cfg.Peer = strings.TrimSpace(*slot)
	return cfg
}

// OpkgPolicyPermit — permit global OpkgTun в одной политике (order как в NDMS).
type OpkgPolicyPermit struct {
	Name  string `json:"name"`
	Order int    `json:"order"`
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		Listen:      "127.0.0.1:9000",
		Workers:     24,
		Obfs:        "audio",
		Fingerprint: "chrome",
		CaptchaMode: "rjs",
		VKAuthMode:  "vkcalls",
		ConnMode:    ConnModeWG,
	}
}

type ClientInstance struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Config ClientConfig `json:"config"`
}

// ServerConfig mirrors flags accepted by the wdtt-server binary.
type ServerConfig struct {
	Enabled bool `json:"enabled"`

	Listen    string `json:"listen"`              // -listen, DTLS (default 0.0.0.0:56002)
	WgPort    int    `json:"wgPort"`              // -wg-port, internal WG (default 56001)
	Password  string `json:"password"`            // -password, WRAP key derivation
	ConfigDir string `json:"configDir,omitempty"` // -config-dir; empty → dataDir/wdtt/server/{id}
	AdminID   string `json:"adminId,omitempty"`   // -admin, optional Telegram user id
	BotToken  string `json:"botToken,omitempty"`  // -bot-token, optional Telegram bot
	Debug     bool   `json:"debug"`

	// Router integration (awg-manager + NDMS):
	NatIface       string   `json:"natIface,omitempty"`       // -nat-if when built-in NAT enabled manually
	NatMode        string   `json:"natMode"`                  // full | internet-only | none via managed service
	NatStaticWAN   string   `json:"natStaticWan,omitempty"`   // persisted WAN for internet-only teardown
	Policy         string   `json:"policy"`                   // NDMS hotspot policy or "none"
	LanSegments    []string `json:"lanSegments,omitempty"`    // LAN bridge names
	IngressEnabled bool     `json:"ingressEnabled,omitempty"` // sing-box ingress for iface:wgIface + wdttraw0

	// OpenFirewall opens the DTLS listen port in Keenetic INPUT (iptables).
	// nil / omitted → true (WAN relay works out of the box).
	OpenFirewall *bool `json:"openFirewall,omitempty"`

	// RelayMode — wg (WireGuard relay, совместимость) или raw (без WG на сервере; нужен редеплой).
	RelayMode string `json:"relayMode,omitempty"`
	// RawListen — -listen-raw (UDP, qWDTT 1.4+). Пусто → порт DTLS+1 на том же host.
	RawListen string `json:"rawListen,omitempty"`
	// DirectListen — -listen-direct (WRAP без DTLS для WG). Пусто → peer = DTLS-порт.
	DirectListen string `json:"directListen,omitempty"`

	// NdmsIface — NDMS id (OpkgTun17..49) when WDTT зарегистрирован в роутере.
	NdmsIface string `json:"ndmsIface,omitempty"`
	// WgIface — kernel WireGuard dev (opkgtunN); пусто → legacy wdtt0.
	WgIface string `json:"wgIface,omitempty"`
	// RawNdmsIface / RawIface — второй интерфейс сервера (raw-IP без WireGuard).
	// Заводится только с бинарём, знающим -raw-iface: NDMS выводит тип
	// интерфейса из имени, и wdttraw0 зарегистрировать нельзя. Пусто → legacy
	// wdttraw0 вне NDMS, на одних iptables менеджера.
	RawNdmsIface string `json:"rawNdmsIface,omitempty"`
	RawIface     string `json:"rawIface,omitempty"`

	// ExposeToPolicies показывает интерфейсы сервера роутеру как подключения:
	// security-level public + `ip global`. Выключено (по умолчанию) — private
	// без global, сервер остаётся внутренним интерфейсом. Применяется на
	// старте; смена на живом сервере вступает в силу с его перезапуска.
	ExposeToPolicies bool `json:"exposeToPolicies,omitempty"`

	// Clients — абоненты сервера. Источник правды: passwords.json собирается из
	// этого списка перед каждым стартом wdtt-server.
	Clients []ServerClient `json:"clients,omitempty"`

	// LinkPeer / LinkVKHashes — параметры последней сгенерированной ссылки,
	// чтобы wdtt:// восстанавливалась без повторного ввода WAN-адреса.
	LinkPeer     string `json:"linkPeer,omitempty"`
	LinkVKHashes string `json:"linkVkHashes,omitempty"`

	// StatsLog — server.log (JSON каждые ~2 с): "ram" (default), "off", "disk".
	StatsLog string `json:"statsLog,omitempty"`
}

// ServerClient is one WDTT client identity stored in wdtt.json.
type ServerClient struct {
	Password string `json:"password"`
	Comment  string `json:"comment,omitempty"`
	VkHash   string `json:"vkHash,omitempty"`
	// ExpiresAt — срок действия, назначенный САМИМ сервером (телеграм-бот и
	// admin-API выдают пароли только со сроком). Ноль — бессрочный, так
	// заводит абонентов менеджер. Поле хранится у нас, чтобы не снять срок с
	// отозванного доступа: янитор форка удаляет истёкшую запись из
	// passwords.json, и без этой памяти следующая синхронизация написала бы
	// её заново с нулевым сроком.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
	// Auto — абонента завёл инвариант непустоты, а не человек: «Абонент 1»
	// сохранения конфига (server.go) или записи файла (ensureUsableServerClient).
	// Признак ХРАНИТСЯ, потому что вычислить его нечем: имя пользователь меняет,
	// и матч по "Абонент 1" соврал бы сразу после переименования.
	Auto bool `json:"auto,omitempty"`
}

const (
	DefaultWdttIface   = "wdtt0"
	DefaultWdttAddress = "10.66.66.1"
	DefaultWdttMask    = "255.255.255.0"
	// DefaultWdttServerGateway* — NDMS OpkgTun: шлюз в сети пула клиентов monolith
	// (10.66.0.0/16). Совпадение сети интерфейса с пулом обязательно: NDMS
	// NAT/policy/ACL кроют только сеть интерфейса (PR #697, F2). 10.66.0.1 не
	// достаётся клиентам: reserveGatewayIPInDevices держит слот в passwords.json
	// (старый бинарь), getNextIP-патч скипает оба шлюза (новый бинарь).
	DefaultWdttServerGatewayAddr = "10.66.0.1"
	DefaultWdttServerGatewayMask = "255.255.0.0"
	DefaultRawServerIface        = "wdttraw0"
	DefaultRawServerAddr         = "10.70.66.1"
	DefaultRawServerMask         = "255.255.0.0"
	// DefaultRawServerGatewayAddr — адрес NDMS на raw-OpkgTun, паритет с
	// 10.66.0.1 на WG-половине: свой адрес нужен, потому что 10.70.66.1 уже
	// висит на интерфейсе от самого wdtt-server. Абоненту не достаётся —
	// getNextRawIP форка его пропускает (флаг -raw-iface и пропуск приехали
	// одним релизом), а записи старых устройств с этим адресом снимает
	// sanitizePasswordsDevices.
	DefaultRawServerGatewayAddr = "10.70.0.1"
	DefaultRawClientTun         = "wdtturn0"
	DefaultRawClientMask        = "255.255.255.255"
	// DefaultWdttClientPoolCIDR — пул qWDTT monolith (getNextIP → 10.66.0.x).
	DefaultWdttClientPoolCIDR = "10.66.0.0/16"
)

func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Listen:    "0.0.0.0:56002",
		WgPort:    56001,
		NatMode:   "full",
		Policy:    "none",
		RelayMode: ConnModeWG,
	}
}

type ServerInstance struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Config ServerConfig `json:"config"`
}

type Config struct {
	Version int              `json:"version,omitempty"`
	Clients []ClientInstance `json:"clients"`
	Servers []ServerInstance `json:"servers"`
}

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

type CreateClientInput struct {
	Name   string        `json:"name,omitempty"`
	Config *ClientConfig `json:"config,omitempty"`
}

type CreateServerInput struct {
	Name   string        `json:"name,omitempty"`
	Config *ServerConfig `json:"config,omitempty"`
}

type ProcessStatus struct {
	Running     bool       `json:"running"`
	PID         int        `json:"pid,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	Log         string     `json:"log,omitempty"`
	WgConfig    string     `json:"wgConfig,omitempty"`
	RawClientIP string     `json:"rawClientIp,omitempty"`
	RawIface    string     `json:"rawIface,omitempty"`
	NdmsIface   string     `json:"ndmsIface,omitempty"`
	// RawNdmsIface — NDMS-имя raw-интерфейса сервера. У клиента раздельных
	// имён нет (интерфейс один), у сервера их два, и в веб-морде роутера
	// человек ищет оба.
	RawNdmsIface    string `json:"rawNdmsIface,omitempty"`
	DtlsConnections int    `json:"dtlsConnections,omitempty"`
	Binary          string `json:"binary"`
	BinaryPresent   bool   `json:"binaryPresent"`
	// OrphanedPID — процесс НАШ и живой, но pid-файл унаследован: startedAt
	// нет, потому что запускал его прошлый экземпляр демона. Лога и телеметрии
	// по такому процессу у нас не будет, health-надзор по нему слеп; лечится
	// обычным стартом — process.Start его усыновляет, и это же делает
	// супервизор (supervisor.go). Не путать со stalePid у HydraRoute: там
	// признак ровно обратный — pid-файл есть, а процесса уже нет.
	OrphanedPID bool `json:"orphanedPid,omitempty"`
}

type InstanceStatus struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Status ProcessStatus `json:"status"`
}

type Status struct {
	Clients []InstanceStatus `json:"clients"`
	Servers []InstanceStatus `json:"servers"`
	Client  ProcessStatus    `json:"client"`
	Server  ProcessStatus    `json:"server"`
	// ServerSupported — собирается ли wdtt-server под эту архитектуру.
	// На mips/mipsel его нет (см. internal/wdtt/install.go), UI прячет вкладку «Сервер».
	ServerSupported  bool   `json:"serverSupported"`
	InstallAvailable bool   `json:"installAvailable"`
	InstallVersion   string `json:"installVersion,omitempty"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	UpdateAvailable  bool   `json:"updateAvailable"`
	Installing       bool   `json:"installing"`
	RouterClock      string `json:"routerClock,omitempty"`
}

// ServerClientEntry — один абонент сервера: личность из wdtt.json,
// живые признаки — из passwords.json.
type ServerClientEntry struct {
	Password      string `json:"password"`
	Comment       string `json:"comment,omitempty"`
	VkHash        string `json:"vkHash,omitempty"`
	IsDeactivated bool   `json:"isDeactivated"`
	// IsExpired — срок, назначенный сервером, истёк. Такой абонент в
	// passwords.json не пишется и подключиться не может; в списке он остаётся,
	// чтобы пользователь понимал, почему доступ пропал.
	IsExpired bool `json:"isExpired"`
	// IsMainPassword — пароль абонента совпадает с главным паролем сервера.
	// Сам главный пароль в списке не передаётся намеренно (это ключ
	// администрирования), поэтому сравнить пароли на фронте нечем — отдаём
	// готовый признак. Такой абонент сервером не принимается и одним ходом не
	// удаляется: RemoveServerClient на него отвечает отказом.
	IsMainPassword bool `json:"isMainPassword"`
	// IsAuto — абонента завёл инвариант непустоты (ServerClient.Auto).
	IsAuto bool `json:"isAuto"`
}

// ServerClientsReload — судьба SIGHUP после изменения состава абонентов:
// узнал ли живой сервер об изменении ПРЯМО СЕЙЧАС. Пустое значение — сигнал не
// посылался (чтение списка, переименование: имя серверу безразлично).
type ServerClientsReload string

const (
	// ReloadDelivered — SIGHUP доставлен живому серверу: passwords.json
	// перечитан, новый состав действует сейчас.
	ReloadDelivered ServerClientsReload = "delivered"
	// ReloadServerStopped — сервер не запущен: файл записан, состав вступит в
	// силу при следующем запуске.
	ReloadServerStopped ServerClientsReload = "serverStopped"
	// ReloadFailed — сигнал не доставлен (процесс есть, но kill не прошёл).
	// Файл записан, поэтому доступ появится при следующем запуске; «применено
	// сейчас» обещать нельзя.
	ReloadFailed ServerClientsReload = "failed"
)

// ServerClientsStatus is returned by the server clients API.
type ServerClientsStatus struct {
	// Available — passwords.json прочитан. Пусто до первого старта сервера:
	// список при этом всё равно отдаётся, из wdtt.json.
	Available bool                `json:"available"`
	Users     []ServerClientEntry `json:"users"`
	// Reload — результат доставки SIGHUP для ЭТОЙ мутации. Заполняют только
	// ручки, переписывающие passwords.json (добавление, удаление); у чтения и
	// переименования пусто.
	Reload ServerClientsReload `json:"reload,omitempty"`
}

// ImportPayload is the normalized result of wdtt://, qwdtt:// or subscription import.
type ImportPayload struct {
	Name     string   `json:"name,omitempty"`
	Peer     string   `json:"peer"`
	Password string   `json:"password"`
	VKHashes []string `json:"vkHashes"`
	Workers  int      `json:"workers,omitempty"`
	Listen   string   `json:"listen,omitempty"`
	SubURL   string   `json:"subUrl,omitempty"`
	DeviceID string   `json:"deviceId,omitempty"`
	WG       string   `json:"wg,omitempty"` // optional bundled WireGuard client config
	ConnMode string   `json:"connMode,omitempty"`
}

// wgServerPeerCIDR — подсеть клиентов qWDTT monolith (10.66.0.x, не 10.66.66.x).
func wgServerPeerCIDR() string {
	_, n, err := net.ParseCIDR(DefaultWdttClientPoolCIDR)
	if err != nil {
		return "10.66.0.0/16"
	}
	return n.String()
}

// wdttPeerCIDR — alias для entware NAT/MSS/LAN (monolith client pool).
func wdttPeerCIDR() string {
	return wgServerPeerCIDR()
}

func maskBits(mask string) string {
	ones, _ := net.IPMask(net.ParseIP(mask).To4()).Size()
	if ones == 0 {
		return "24"
	}
	return strconv.Itoa(ones)
}
