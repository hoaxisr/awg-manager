// Package warpscout — встроенный сканер эндпоинтов Cloudflare WARP и
// автопилот, подменяющий эндпоинт туннеля на лету при отказе узла или
// «полиморфинге» Anycast-маршрута в запрещённый колокейшн (например DME).
//
// Массовый скан делает нативный C-модуль /opt/sbin/awgm-warpscout
// (c-core/warpscout): Go передаёт ему ключи и фильтры JSON-объектом в stdin
// и читает JSONL-поток событий из stdout. Сам пакет держит учётную запись
// WARP, профили автопилота, ручной скан и сторожей.
package warpscout

import "time"

// WarpPeerPublicKey — публичный ключ серверов Cloudflare WARP. Один на все
// эндпоинты и все учётные записи: по нему автопилот узнаёт WARP-туннель и
// отказывается трогать любой другой.
const WarpPeerPublicKey = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="

// TargetMode — тип целевого туннеля.
type TargetMode string

const (
	// TargetSystem — системный туннель Keenetic (WireguardN, раздел
	// «Системные туннели»), правится через NDMS RCI.
	TargetSystem TargetMode = "system"
	// TargetNative — туннель AWGM на нативном WireGuard NDMS (backend nativewg).
	TargetNative TargetMode = "native"
	// TargetKernel — туннель AWGM на модуле ядра amneziawg (backend kernel).
	TargetKernel TargetMode = "kernel"
)

// Valid сообщает, известен ли режим.
func (m TargetMode) Valid() bool {
	switch m {
	case TargetSystem, TargetNative, TargetKernel:
		return true
	}
	return false
}

// ProfileMode — сколько профилей автопилота может работать одновременно.
type ProfileMode string

const (
	// ProfileModeSingle — активен ровно один профиль (ActiveProfileID).
	ProfileModeSingle ProfileMode = "single"
	// ProfileModeMulti — каждый профиль со своим Enabled работает независимо.
	ProfileModeMulti ProfileMode = "multi"
)

// Protocol — режим рукопожатия при скане и в создаваемом конфиге.
const (
	ProtocolAWG = "awg" // мусорные пакеты Jc/Jmin/Jmax + сигнатура I1..I5
	ProtocolWG  = "wg"  // стандартный WireGuard

	// SignatureCustom — сигнатура I1..I5 скопирована из существующего туннеля
	// (или задана вручную) и не перегенерируется встроенными пресетами.
	SignatureCustom = "custom"

	DefaultMTU    = 1280
	NestedWarpMTU = 1220
)

// Account — учётная запись Cloudflare WARP в открытом виде (в памяти).
// На диске PrivateKey и Token лежат зашифрованными (см. store.go).
type Account struct {
	ID            string    `json:"id"`
	Token         string    `json:"-"`
	PrivateKey    string    `json:"-"`
	PublicKey     string    `json:"publicKey"`
	PeerPublicKey string    `json:"peerPublicKey"`
	AddressV4     string    `json:"addressV4"`
	AddressV6     string    `json:"addressV6,omitempty"`
	ClientID      string    `json:"clientId,omitempty"` // base64, 3 байта reserved
	AccountType   string    `json:"accountType,omitempty"`
	WarpPlus      bool      `json:"warpPlus"`
	HasToken      bool      `json:"hasToken"`
	Imported      bool      `json:"imported,omitempty"`
	License       string    `json:"-"`
	CreatedAt     time.Time `json:"createdAt"`
}

// TunnelAccount — учётная запись WARP, заведённая под один туннель.
type TunnelAccount struct {
	Account
	TunnelMode TargetMode
	TunnelID   string
}

// Reserved — 3 байта client_id для полей reserved заголовка WireGuard.
func (a *Account) Reserved() [3]int {
	var out [3]int
	b := decodeB64(a.ClientID)
	for i := 0; i < 3 && i < len(b); i++ {
		out[i] = int(b[i])
	}
	return out
}

// AWGParams — обфускация AmneziaWG, совместимая с серверами WARP: мусорные
// пакеты, сигнатура I1..I5 и расширения AWG 3.0/3.1. S1..S4 и H1..H4 у WARP
// всегда штатные (0 и 1..4) — сервер на обычном WireGuard иначе отвергает
// рукопожатие.
type AWGParams struct {
	// SignatureProfile — профиль генератора сигнатуры (quic_initial, dns,
	// stun, dtls, sip), "custom" (взято из рабочего туннеля) или "none".
	SignatureProfile string `json:"signatureProfile"`
	// SignatureHostPreset — пул доменов для генератора I1 ("", "default",
	// "ru_whitelist", "cdn", "custom").
	SignatureHostPreset string `json:"signatureHostPreset,omitempty"`
	// SignatureDomain — пользовательский домен или список доменов через запятую.
	SignatureDomain string `json:"signatureDomain,omitempty"`
	// GeneratedDomain — фактический домен, зашитый в текущую сигнатуру I1.
	GeneratedDomain string `json:"generatedDomain,omitempty"`
	Jc              int    `json:"jc"`
	Jmin            int    `json:"jmin"`
	Jmax            int    `json:"jmax"`
	I1              string `json:"i1,omitempty"`
	I2              string `json:"i2,omitempty"`
	I3              string `json:"i3,omitempty"`
	I4              string `json:"i4,omitempty"`
	I5              string `json:"i5,omitempty"`
	// AWG 3.0 параметры (безопасны для WARP: не меняют S1..S4/H1..H4).
	// TimingJitter включает рандомизированные таймеры рукопожатия/rekey
	// (и PersistentKeepalive = 25-35 в kernel/.conf).
	TimingJitter bool `json:"timingJitter,omitempty"`
	// ContentPadding — ContentPaddingAddition (например "10-100" или "32").
	ContentPadding string `json:"contentPadding,omitempty"`
	// AWG 3.1 параметры (работают в kernel >= 3.1, ASC 3.x на 5.02.A.11+ и в .conf).
	RandomTrailers bool `json:"randomTrailers,omitempty"`
	DisableCookies bool `json:"disableCookies,omitempty"`
}

// ObfuscationSource — существующий на роутере туннель с параметрами AmneziaWG,
// из которого можно скопировать Jc/Jmin/Jmax и I1..I5 для скана или нового WARP.
type ObfuscationSource struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Iface   string     `json:"iface,omitempty"`
	Mode    TargetMode `json:"mode"`
	Running bool       `json:"running"`
	IsWarp  bool       `json:"isWarp"`
	AWG     AWGParams  `json:"awg"`
}

// Filters — требования к узлу. Общие у ручного скана и профилей.
type Filters struct {
	Adaptive         bool     `json:"adaptive,omitempty"`
	SearchSeconds    int      `json:"searchSeconds,omitempty"`
	Protocol         string   `json:"protocol"`
	ExcludeColos     []string `json:"excludeColos"`
	AllowedCountries []string `json:"allowedCountries"`
	MaxRTTMs         int      `json:"maxRttMs"`
	RequireTelegram  bool     `json:"requireTelegram"`
	MaxCandidates    int      `json:"maxCandidates,omitempty"`
	Ports            []int    `json:"ports,omitempty"`
}

// Profile — правило автопилота для одного туннеля.
type Profile struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Enabled          bool       `json:"enabled"`
	TargetMode       TargetMode `json:"targetMode"`
	TargetID         string     `json:"targetId"`
	Filters          Filters    `json:"filters"`
	CheckIntervalSec int        `json:"checkIntervalSec"`
	FailThreshold    int        `json:"failThreshold"`
	CooldownSec      int        `json:"cooldownSec"`
}

// Config — пользовательская конфигурация модуля.
type Config struct {
	ProfileMode     ProfileMode `json:"profileMode"`
	ActiveProfileID string      `json:"activeProfileId,omitempty"`
	// ScanInterface — явный интерфейс выхода для сканера и автопилота
	// (например "eth3", "ppp0" или "nwg1"); пусто — основной WAN по умолчанию.
	ScanInterface string `json:"scanInterface,omitempty"`
	// AutoRename разрешает обновлять код колокейшна (HEL, FRA, …) в названии
	// туннеля и профиля при проверках и смене endpoint'а. nil трактуется как
	// true для обратной совместимости со старыми файлами настроек.
	AutoRename *bool     `json:"autoRename,omitempty"`
	Profiles   []Profile `json:"profiles"`
	AWG        AWGParams `json:"awg"`
}

// TunnelAutoRename сообщает, включено ли переименование туннелей под локацию.
func (c Config) TunnelAutoRename() bool {
	return c.AutoRename == nil || *c.AutoRename
}

// TunnelWatchInfo — краткая сводка активного профиля автопилота для пометки
// привязанного туннеля в общих списках интерфейса.
type TunnelWatchInfo struct {
	ProfileID   string `json:"profileId"`
	ProfileName string `json:"profileName,omitempty"`
	State       string `json:"state"`
	Message     string `json:"message,omitempty"`
}

// RouteRef — доступный интерфейс выхода (WAN, внешний VPN или WARP для WARP-in-WARP).
type RouteRef struct {
	Name       string     `json:"name"`
	Label      string     `json:"label"`
	Kind       string     `json:"kind"` // "wan" | "tunnel" | "warp"
	Up         bool       `json:"up"`
	TunnelMode TargetMode `json:"tunnelMode,omitempty"`
	TunnelID   string     `json:"tunnelId,omitempty"`
}

// Hit — найденный рабочий эндпоинт.
type Hit struct {
	AWG         *AWGParams `json:"awg,omitempty"`
	Endpoint    string     `json:"endpoint"`
	HandshakeMs int        `json:"handshakeMs"`
	PingMs      int        `json:"pingMs"`
	Colo        string     `json:"colo"`
	Loc         string     `json:"loc"`
	Warp        string     `json:"warp"`
	TgOK        bool       `json:"tgOk"`
}

// SwapEvent — запись журнала автоподмен.
type SwapEvent struct {
	SearchMs      int        `json:"searchMs,omitempty"`
	ScanInterface string     `json:"scanInterface,omitempty"`
	AWG           *AWGParams `json:"awg,omitempty"`
	At            time.Time  `json:"at"`
	ProfileID     string     `json:"profileId"`
	ProfileName   string     `json:"profileName"`
	TargetMode    TargetMode `json:"targetMode"`
	TargetID      string     `json:"targetId"`
	Reason        string     `json:"reason"`
	OldEndpoint   string     `json:"oldEndpoint"`
	NewEndpoint   string     `json:"newEndpoint"`
	Colo          string     `json:"colo"`
	Loc           string     `json:"loc"`
	PingMs        int        `json:"pingMs"`
	OK            bool       `json:"ok"`
	Error         string     `json:"error,omitempty"`
}

// TraceResult — разбор http://…/cdn-cgi/trace через интерфейс туннеля.
type TraceResult struct {
	LatencyKind string `json:"latencyKind,omitempty"`
	HTTPSMs     int    `json:"httpsMs,omitempty"`
	TelegramOK  bool   `json:"telegramOK,omitempty"`
	Colo        string `json:"colo"`
	Loc         string `json:"loc"`
	Warp        string `json:"warp"`
	IP          string `json:"ip"`
	LatencyMs   int    `json:"latencyMs"`
}

// CheckSample — одна точка живой серии проверок автопилота (кольцевой буфер в RAM).
type CheckSample struct {
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
	LatencyMs int       `json:"latencyMs,omitempty"`
	Colo      string    `json:"colo,omitempty"`
	Loc       string    `json:"loc,omitempty"`
	Swapped   bool      `json:"swapped,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// ProfileStats — накопленная статистика работы одного профиля автопилота.
type ProfileStats struct {
	ProfileID       string         `json:"profileId"`
	StartedAt       time.Time      `json:"startedAt,omitempty"`
	EndpointSince   time.Time      `json:"endpointSince,omitempty"`
	CurrentEndpoint string         `json:"currentEndpoint,omitempty"`
	ChecksTotal     int64          `json:"checksTotal"`
	ChecksOK        int64          `json:"checksOk"`
	ChecksFail      int64          `json:"checksFail"`
	SwapsOK         int64          `json:"swapsOk"`
	SwapsFail       int64          `json:"swapsFail"`
	LatencySumMs    int64          `json:"latencySumMs,omitempty"`
	LatencyCount    int64          `json:"latencyCount,omitempty"`
	MinLatencyMs    int            `json:"minLatencyMs,omitempty"`
	MaxLatencyMs    int            `json:"maxLatencyMs,omitempty"`
	AvgLatencyMs    int            `json:"avgLatencyMs,omitempty"`
	Colos           map[string]int `json:"colos,omitempty"`
	Samples         []CheckSample  `json:"samples,omitempty"`
}

// TunnelRef — целевой туннель глазами автопилота.
type TunnelRef struct {
	Mode           TargetMode `json:"mode"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Iface          string     `json:"iface"`
	Endpoint       string     `json:"endpoint"`
	AddressV4      string     `json:"addressV4,omitempty"`
	LocalPublicKey string     `json:"-"`
	PeerPublicKey  string     `json:"-"`
	IsWarp         bool       `json:"isWarp"`
	Running        bool       `json:"running"`
	Protected      bool       `json:"protected"`
	ProtectReason  string     `json:"protectReason,omitempty"`
	ViaMode        TargetMode `json:"viaMode,omitempty"`
	ViaTunnelID    string     `json:"viaTunnelId,omitempty"`
	ViaIface       string     `json:"viaIface,omitempty"`
	ViaLabel       string     `json:"viaLabel,omitempty"`
	ViaEndpoint    string     `json:"viaEndpoint,omitempty"`
	ViaRunning     bool       `json:"viaRunning,omitempty"`
}
