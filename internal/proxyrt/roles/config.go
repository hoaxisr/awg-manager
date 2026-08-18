package roles

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Конфиги ролей — намерение и только намерение: ни одного кэшированного факта
// (RawClientIP старого мира — теперь наблюдение процесса). Нормализует
// писатель (план 5); Validate ОТКЛОНЯЕТ ненормализованное, а не чинит молча —
// две копии старых хранилищ нормализовали по-разному, и этот класс закрыт
// одним писателем.

// WdttClientConfig — клиент WDTT. Mode: "raw" | "wg".
type WdttClientConfig struct {
	Mode string
	// Name — человекочитаемое имя инстанса, данное пользователем: уходит в
	// хвост NDMS-description (ClientDescription) — паритет со старым
	// TunnelNameFromClient. НЕ DeviceID: тот у клиентов по умолчанию
	// одинаков и различимости не даёт (I4 ревью).
	Name        string
	Listen      string // 127.0.0.1:PORT из пула ListenPortMin..Max
	Peer        string
	Password    string
	VKHashes    string
	Workers     int
	Obfs        string
	Fingerprint string
	DeviceID    string
	CaptchaMode string // auto|rjs|wv
	VKAuthMode  string

	// Пин индекса (только raw): на имя OpkgTunN ссылаются permit'ы политик.
	NdmsIface string // OpkgTun17..49
	RawIface  string // opkgtun17..49

	// Policies — намерение членства в политиках доступа. Единственный
	// писатель — пользователь (спека §4.4).
	Policies []string
}

func (c WdttClientConfig) Validate() error {
	if c.Mode != "raw" && c.Mode != "wg" {
		return fmt.Errorf("mode %q: ожидали raw или wg (нормализует писатель конфига)", c.Mode)
	}
	if err := localListen(c.Listen); err != nil {
		return err
	}
	if strings.TrimSpace(c.Peer) == "" {
		return fmt.Errorf("не задан адрес сервера (-peer)")
	}
	if strings.TrimSpace(c.Password) == "" {
		// wt-client без -password ВЫХОДИТ (стенд mips 2026-08-17).
		return fmt.Errorf("не задан пароль подключения (-password)")
	}
	if strings.TrimSpace(c.VKHashes) == "" {
		return fmt.Errorf("не заданы VK-хеши (-vk)")
	}
	if c.Mode == "raw" && (c.NdmsIface == "" || c.RawIface == "") {
		return fmt.Errorf("raw-клиенту не выделен индекс OpkgTun (пин ставит писатель конфига)")
	}
	return nil
}

// NDMSNames — NDMS-интерфейсы, объявленные конфигом: из них строится ведомость
// для уборщика (instance.DeclaredNDMSNames). У wg-клиента имени нет — пустая
// строка, ведомость её отбрасывает.
//
// Метод обязан быть у КАЖДОГО конфига роли: ведомость собирается по интерфейсу,
// и конфиг без метода не соберётся вовсе — вместо того чтобы молча выпасть из
// ведомости и отдать свой живой интерфейс уборщику.
func (c WdttClientConfig) NDMSNames() []string { return []string{c.NdmsIface} }

// WdttServerConfig — сервер WDTT (обе половины: WG + raw).
type WdttServerConfig struct {
	Listen       string // DTLS, 0.0.0.0:56000
	WgPort       int
	ConfigDir    string
	Password     string
	AdminID      string
	BotToken     string
	NatIface     string
	WgIface      string // opkgtunN (пин)
	RawIface     string // opkgtunM (пин)
	NdmsIface    string // OpkgTunN
	RawNdmsIface string // OpkgTunM
	RawListen    string // пусто = DTLS+1 (конвенция qWDTT 1.4)
	DirectListen string
	RelayMode    string // wg|raw — режим генерации ссылки; на процесс влияет только через -dns
	NatMode      string // full|internet-only|none
	NatStaticWAN string // NDMS-имя WAN для internet-only (пишет план 5 по факту применения)
	Policy       string // none|<имя>
	LanSegments  []string
	// ExposeToPolicies — тумблер «использовать в политиках доступа»:
	// private → public + ip global (ndms_iface.go:101-110). Роутерный механизм
	// с осознанным выбором; routable_exit сервера УБРАН решением владельца.
	ExposeToPolicies bool
	OpenFirewall     bool
}

func (c WdttServerConfig) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return fmt.Errorf("не задан listen сервера")
	}
	if strings.TrimSpace(c.Password) == "" {
		return fmt.Errorf("не задан пароль сервера (-password)")
	}
	switch c.NatMode {
	case "full", "none":
	case "internet-only":
		// Молчаливая деградация internet-only в full-форму была багом H1
		// (PR #697); без выбранного WAN режим не имеет смысла — приговор
		// через cfgErr процесса, а не вечный waiting провайдера правил (I5).
		if strings.TrimSpace(c.NatStaticWAN) == "" {
			return fmt.Errorf("natMode internet-only: не выбран WAN (natStaticWAN)")
		}
	default:
		return fmt.Errorf("natMode %q: ожидали full|internet-only|none", c.NatMode)
	}
	switch c.RelayMode {
	case "wg", "raw":
	default:
		return fmt.Errorf("relayMode %q: ожидали wg|raw", c.RelayMode)
	}
	// Обе NDMS-половины обязательны: argv ставит -dns шлюзом OpkgTun-формы
	// (10.66.0.1 / 10.70.66.1), а старый legacy-путь wdtt0 отвечал другим
	// адресом (modes.go:72 → 10.66.66.1). Пустое имя означало бы legacy-мир,
	// которого новый рантайм не строит: отказ конфига честнее, чем молча
	// неверный резолвер у абонентов.
	if strings.TrimSpace(c.NdmsIface) == "" || strings.TrimSpace(c.WgIface) == "" {
		return fmt.Errorf("не заданы NDMS-имена WG-половины сервера (ndmsIface/wgIface)")
	}
	if strings.TrimSpace(c.RawNdmsIface) == "" || strings.TrimSpace(c.RawIface) == "" {
		return fmt.Errorf("не заданы NDMS-имена raw-половины сервера (rawNdmsIface/rawIface)")
	}
	return nil
}

// NDMSNames — обе половины сервера: WG и raw.
func (c WdttServerConfig) NDMSNames() []string { return []string{c.NdmsIface, c.RawNdmsIface} }

// EffectiveRawListen — как ports.go:30: явный RawListen либо DTLS+1.
func (c WdttServerConfig) EffectiveRawListen() string {
	if a := strings.TrimSpace(c.RawListen); a != "" {
		return a
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(c.Listen))
	if err != nil {
		return "0.0.0.0:56003"
	}
	if strings.TrimSpace(host) == "" {
		host = "0.0.0.0"
	}
	// strconv.Atoi, а не Sscanf: Sscanf на "56000x" возвращает 56000 без
	// ошибки, и вместо фолбэка ports.go:10-27 получился бы порт из мусора.
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 || p >= 65535 {
		return net.JoinHostPort(host, "56003")
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", p+1))
}

// FreeTurnClientConfig — клиент FreeTurn (паритет с freeturn/service.go:876).
type FreeTurnClientConfig struct {
	Listen         string
	Peer           string
	Provider       string
	Links          string
	Streams        int
	Transport      string
	Mode           string
	Bond           bool
	TurnHost       string
	TurnPort       int
	ObfProfile     string
	ObfKey         string
	StreamsPerCred int
	Platform       string // ""|desktop|mobile
	DNSMode        string
	DNSServers     string
	ClientID       string
	Sub            string
	Debug          bool
}

func (c FreeTurnClientConfig) Validate() error {
	if err := localListen(c.Listen); err != nil {
		return err
	}
	if strings.TrimSpace(c.Peer) == "" && strings.TrimSpace(c.Links) == "" && strings.TrimSpace(c.Sub) == "" {
		return fmt.Errorf("не задан адрес реле (-peer / -links / подписка)")
	}
	return nil
}

// NDMSNames — у FreeTurn NDMS-интерфейсов нет: клиент слушает 127.0.0.1.
// Пустая декларация объявлена ЯВНО, а не отсутствием метода: без неё конфиг
// выпал бы из ведомости неотличимо от забытого.
func (c FreeTurnClientConfig) NDMSNames() []string { return nil }

// FreeTurnServerConfig — сервер FreeTurn.
type FreeTurnServerConfig struct {
	Listen       string
	Connect      string
	Mode         string // udp|tcp — он же протокол INPUT-правила
	ObfProfile   string
	ObfKey       string
	ClientsFile  string
	Debug        bool
	OpenFirewall bool
}

func (c FreeTurnServerConfig) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return fmt.Errorf("не задан listen сервера")
	}
	return nil
}

// NDMSNames — у FreeTurn NDMS-интерфейсов нет.
func (c FreeTurnServerConfig) NDMSNames() []string { return nil }

func localListen(addr string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("listen %q: %v", addr, err)
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("listen %q: клиент слушает только 127.0.0.1 (пул %d..%d)", addr, ListenPortMin, ListenPortMax)
	}
	return nil
}
