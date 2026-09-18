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

// PolicyPermit — permit нашего интерфейса в одной политике доступа.
//
// Order — позиция на СОЗДАНИИ permit'а (восстановление после апгрейда);
// существующую позицию никто не двигает (§4.4). Указатель, а не int, потому
// что состояний ТРИ, и ноль — не пустое место: NDMS нумерует permit'ы с нуля
// (ndms/query/policies.go:86), так что `order: 0` означает САМЫЙ ВЕРХ
// политики — ровно тот выход, который пользователь поднял выше провайдера, и
// ради которого перенос позиции вообще делается. nil — позиция не
// закреплена, permit уходит в хвост (appendOrder).
type PolicyPermit struct {
	Name  string `json:"name"`
	Order *int   `json:"order,omitempty"`
}

// WdttClientConfig — клиент WDTT. Mode: "raw" | "wg".
//
// json-теги — формат файла proxy-instances.json (план 5, Р2); имена — старые,
// канарейка TestStoreWireFormatCanary ловит дрейф.
type WdttClientConfig struct {
	Mode string `json:"connMode"`
	// Name — человекочитаемое имя инстанса, данное пользователем: уходит в
	// хвост NDMS-description (ClientDescription) — паритет со старым
	// TunnelNameFromClient. НЕ DeviceID: тот у клиентов по умолчанию
	// одинаков и различимости не даёт (I4 ревью).
	//
	// json:"-" — писатель имени один, Record.Name (Р3).
	Name        string `json:"-"`
	Listen      string `json:"listen"` // 127.0.0.1:PORT из пула ListenPortMin..Max
	Peer        string `json:"peer"`
	Password    string `json:"password"`
	VKHashes    string `json:"vkHashes"`
	Workers     int    `json:"workers"`
	Obfs        string `json:"obfs,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	DeviceID    string `json:"deviceId,omitempty"`
	CaptchaMode string `json:"captchaMode,omitempty"` // auto|rjs|wv
	VKAuthMode  string `json:"vkAuthMode,omitempty"`

	// Пин индекса (только raw): на имя OpkgTunN ссылаются permit'ы политик.
	// Номер приходит из ОБЩЕГО пула (internal/opkgtun), собственного окна у
	// прокси больше нет — оно разошлось бы с прошивкой (#891).
	NdmsIface string `json:"ndmsIface,omitempty"` // OpkgTunN
	RawIface  string `json:"rawIface,omitempty"`  // opkgtunN

	// Policies — намерение членства в политиках доступа. Единственный
	// писатель — пользователь (спека §4.4).
	Policies []PolicyPermit `json:"policies,omitempty"`
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

// DefaultWorkers — дефолт числа потоков (-n) клиента WDTT по архитектуре
// роутера. Замеры на KN-1010 (MT7621, mipsel) при сопоставимых условиях:
// 27 потоков — 10-15 Мбит/с, CPU до 90%, RSS 45 МБ; 9 потоков — 18.6 Мбит/с,
// CPU 43%, RSS 18.8 МБ (пик 26.1). На mips шифрование идёт софтовым путём и
// упирается в CPU раньше, чем в канал: лишние реле не добавляют полосу, а
// отнимают её. На arm64 шифрование идёт ассемблерным путём, стена дальше —
// там 27 потоков полосу реально дают.
//
// Кратность: клиент округляет -n ВНИЗ до кратного девяти и поднимает до
// девяти минимум (форк, go_client/main.go:260-263 при workersPerGroup = 9,
// group.go:14). Оба значения кратны девяти — дефолт доезжает до процесса
// без молчаливого урезания.
//
// Параметр goarch, а не runtime.GOARCH внутри: симметрично opkgtun.Ceiling,
// чтобы поведение проверялось тестом на всех архитектурах сразу.
func DefaultWorkers(goarch string) int {
	switch goarch {
	case "mips", "mipsle", "mips64", "mips64le":
		return 9
	default:
		return 27
	}
}

// NDMSNames — NDMS-интерфейсы, объявленные конфигом: из них строится ведомость
// для уборщика (instance.DeclaredNDMSNames). У wg-клиента имени нет — пустая
// строка, ведомость её отбрасывает.
//
// Метод обязан быть у КАЖДОГО конфига роли: ведомость собирается по интерфейсу,
// и конфиг без метода не соберётся вовсе — вместо того чтобы молча выпасть из
// ведомости и отдать свой живой интерфейс уборщику.
func (c WdttClientConfig) NDMSNames() []string { return []string{c.NdmsIface} }

// RawExit — выход, который конфиг объявляет для маршрутизации: всё, что
// реестру выходов (internal/proxyrt/exitreg, план 4) нужно от конфига, и
// ничего сверх. Только примитивы: roles не узнаёт ни про exitreg, ни про
// wdttclient — идентификатор выхода строит потребитель.
type RawExit struct {
	NDMSName    string // пин: OpkgTunN (номер из общего пула)
	KernelIface string // пин: opkgtunN
	Name        string // человеческое имя инстанса — в имя зеркальной записи
	Peer        string // адрес сервера — в эндпоинт карточки
}

// RawExiter — конфиг роли, объявляющий свой выход.
//
// Метод обязан быть у КАЖДОГО конфига роли — по той же причине, что и
// NDMSNames, и цена нарушения здесь выше. Ведомость выходов собирается по
// ЭТОМУ интерфейсу: конфиг без метода не соберётся вовсе — вместо того чтобы
// молча выпасть из ведомости и отдать свою зеркальную запись (с PingCheck и
// DefaultRoute пользователя, которых в конфиге нет) уборке.
//
// Методы объявлены на ЗНАЧЕНИИ, поэтому указатель на конфиг интерфейсу тоже
// удовлетворяет и даёт тот же ответ (метод-сет *T включает методы T).
type RawExiter interface {
	RawExit() (RawExit, bool)
}

// Проверка «метод у каждого» — здесь, а не только у потребителя: удаление
// метода у любого из ШЕСТИ конфигов ломает сборку пакета сразу.
//
// Границу гарантии называем честно: эти строки знают только про уже
// существующие типы. Седьмой конфиг они не поймают — его ловит поле
// InstanceConfig.Cfg у потребителя (exitreg/declared.go), типизированное этим
// интерфейсом, и ловит ровно до тех пор, пока конфиг не стёрли в any.
var (
	_ RawExiter = WdttClientConfig{}
	_ RawExiter = WdttServerConfig{}
	_ RawExiter = FreeTurnClientConfig{}
	_ RawExiter = FreeTurnServerConfig{}
	_ RawExiter = OpenFluxClientConfig{}
	_ RawExiter = OpenFluxServerConfig{}
)

// RawExit: выход объявляет ТОЛЬКО raw-клиент. У wg-режима ресурса
// routable_exit нет вовсе (wdttclient/role.go:141-152).
func (c WdttClientConfig) RawExit() (RawExit, bool) {
	if c.Mode != "raw" {
		return RawExit{}, false
	}
	return RawExit{
		NDMSName:    c.NdmsIface,
		KernelIface: c.RawIface,
		Name:        c.Name,
		Peer:        strings.TrimSpace(c.Peer),
	}, true
}

// RawExit: у сервера publication выхода УБРАНА решением владельца 2026-08-17
// (сервер — вход, а не выход; правило на него — ловушка).
func (c WdttServerConfig) RawExit() (RawExit, bool) { return RawExit{}, false }

// RawExit: у FreeTurn зеркальных записей не существует в принципе (связь с
// туннелем — поле FreeTurnClientID, storage/types.go:413).
func (c FreeTurnClientConfig) RawExit() (RawExit, bool) { return RawExit{}, false }

func (c FreeTurnServerConfig) RawExit() (RawExit, bool) { return RawExit{}, false }

// RawExit: у OpenFlux NDMS-зеркал нет — клиент слушает SOCKS5 на loopback,
// выходная нода вообще ничего не слушает (транспорты ходят НАРУЖУ к релею).
func (c OpenFluxClientConfig) RawExit() (RawExit, bool) { return RawExit{}, false }

func (c OpenFluxServerConfig) RawExit() (RawExit, bool) { return RawExit{}, false }

// WdttServerConfig — сервер WDTT (обе половины: WG + raw).
type WdttServerConfig struct {
	Listen       string `json:"listen"` // DTLS, 0.0.0.0:56000
	WgPort       int    `json:"wgPort,omitempty"`
	ConfigDir    string `json:"configDir,omitempty"`
	WgIface      string `json:"wgIface,omitempty"`      // opkgtunN (пин)
	RawIface     string `json:"rawIface,omitempty"`     // opkgtunM (пин)
	NdmsIface    string `json:"ndmsIface,omitempty"`    // OpkgTunN
	RawNdmsIface string `json:"rawNdmsIface,omitempty"` // OpkgTunM
	RawListen    string `json:"rawListen,omitempty"`    // пусто = DTLS+1 (конвенция qWDTT 1.4)
	// DirectListen — третий порт WG-половины: WRAP-обфускация БЕЗ слоя DTLS
	// (форк, `-listen-direct`). Меньше инкапсуляции — выше скорость, ценой
	// потери маскировки под DTLS. Пусто = выключено.
	DirectListen string `json:"directListen,omitempty"`
	RelayMode    string `json:"relayMode,omitempty"`    // wg|raw — только режим генерации ссылки; на процесс не влияет
	NatMode      string `json:"natMode,omitempty"`      // full|internet-only|none
	NatStaticWAN string `json:"natStaticWan,omitempty"` // legacy: одиночный WAN; читается через StaticNATList
	// NatStaticWANs — выходы static-NAT для internet-only. Их несколько:
	// при нескольких `ip global` static-NAT ставится на КАЖДЫЙ выход, иначе
	// после переключения провайдера трафик абонентов упирается в мёртвый
	// (PR #750).
	//
	// Кто пишет: миграционный посев (instancestore/seed.go) и тело PATCH —
	// фронт шлёт список, когда пользователь его не трогал (F61). Роль список
	// только читает, через StaticNATList. «По факту применения» его не пишет
	// никто — прежняя редакция комментария обещала писателя, которого нет.
	NatStaticWANs []string `json:"natStaticWans,omitempty"`
	Policy        string   `json:"policy,omitempty"` // none|<имя>
	LanSegments   []string `json:"lanSegments,omitempty"`
	// Debug — пользовательский тумблер старого мира (Г-1). В argv сервера не
	// эмитится — как и раньше, хранится намерение.
	Debug bool `json:"debug,omitempty"`
	// ExposeToPolicies — тумблер «использовать в политиках доступа»:
	// private → public + ip global (ndms_iface.go:101-110). Роутерный механизм
	// с осознанным выбором; routable_exit сервера УБРАН решением владельца.
	ExposeToPolicies bool `json:"exposeToPolicies,omitempty"`
	OpenFirewall     bool `json:"openFirewall"`
}

// StaticNATList — выходы static-NAT: новый список, иначе legacy-одиночка.
// Обе формы живут одновременно ради записей, созданных до перехода на список.
func (c WdttServerConfig) StaticNATList() []string {
	if len(c.NatStaticWANs) > 0 {
		return c.NatStaticWANs
	}
	if w := strings.TrimSpace(c.NatStaticWAN); w != "" {
		return []string{w}
	}
	return nil
}

func (c WdttServerConfig) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return fmt.Errorf("не задан listen сервера")
	}
	if err := c.validatePorts(); err != nil {
		return err
	}
	// Пароля здесь не проверяем, и поля под него в конфиге нет: форк падает
	// единственным условием — `serverWrapKeys.Count() == 0` (server.go:1969),
	// а ключи он собирает из passwords.json, то есть из состава абонентов.
	// Требование пароля в конфиге было строже форка и запирало сервер,
	// у которого абоненты есть.
	switch c.NatMode {
	case "full", "none":
	case "internet-only":
		// Молчаливая деградация internet-only в full-форму была багом H1
		// (PR #697); без выбранного WAN режим не имеет смысла — приговор
		// через cfgErr процесса, а не вечный waiting провайдера правил (I5).
		if len(c.StaticNATList()) == 0 {
			return fmt.Errorf("natMode internet-only: не выбран WAN (natStaticWANs)")
		}
	default:
		return fmt.Errorf("natMode %q: ожидали full|internet-only|none", c.NatMode)
	}
	switch c.RelayMode {
	case "wg", "raw":
	default:
		return fmt.Errorf("relayMode %q: ожидали wg|raw", c.RelayMode)
	}
	// Обе NDMS-половины обязательны: на каждой стоит DNAT :53 на её шлюз
	// OpkgTun-формы (10.66.0.1 / 10.70.0.1), а старый legacy-путь wdtt0
	// отвечал другим адресом (modes.go:72 → 10.66.66.1). Пустое имя означало
	// бы legacy-мир, которого новый рантайм не строит: отказ конфига честнее,
	// чем молча неверный резолвер у абонентов.
	if strings.TrimSpace(c.NdmsIface) == "" || strings.TrimSpace(c.WgIface) == "" {
		return fmt.Errorf("не заданы NDMS-имена WG-половины сервера (ndmsIface/wgIface)")
	}
	if strings.TrimSpace(c.RawNdmsIface) == "" || strings.TrimSpace(c.RawIface) == "" {
		return fmt.Errorf("не заданы NDMS-имена raw-половины сервера (rawNdmsIface/rawIface)")
	}
	return nil
}

// validatePorts — четыре сокета сервера не должны биться друг о друга.
//
// Сервер поднимает: DTLS (`-listen`), raw (`-listen-raw`, по умолчанию DTLS+1),
// direct (`-listen-direct`, если включён) и userspace-WireGuard (`-wg-port`).
// Последний слушает на ВСЕХ адресах (wireguard-go, `listen_port` в UAPI),
// поэтому сравниваются номера портов, а не пары адрес:порт — совпадение
// номера значит столкновение даже при разных хостах в конфиге.
//
// Пример достижимой коллизии, ради которой проверка и заведена: порт раздачи
// 56000 → raw получает 56001, а дефолт `-wg-port` — тоже 56001. Один из двух
// сокетов не поднимется, и причину пришлось бы искать в журнале форка.
//
// DirectListen, равный Listen, — это «выключено» (та же трактовка, что в
// argv, INPUT-портах и ведомости занятости), поэтому коллизией не считается.
func (c WdttServerConfig) validatePorts() error {
	type slot struct {
		name string
		port int
	}
	var slots []slot
	add := func(name, addr string) error {
		if strings.TrimSpace(addr) == "" {
			return nil
		}
		_, portStr, err := net.SplitHostPort(strings.TrimSpace(addr))
		if err != nil {
			return fmt.Errorf("%s: некорректный адрес %q", name, addr)
		}
		p, err := strconv.Atoi(portStr)
		if err != nil || p <= 0 || p > 65535 {
			return fmt.Errorf("%s: некорректный порт в %q", name, addr)
		}
		slots = append(slots, slot{name, p})
		return nil
	}
	if err := add("порт раздачи", c.Listen); err != nil {
		return err
	}
	if err := add("raw-порт", c.EffectiveRawListen()); err != nil {
		return err
	}
	if d := strings.TrimSpace(c.DirectListen); d != "" && d != strings.TrimSpace(c.Listen) {
		if err := add("direct-порт", d); err != nil {
			return err
		}
	}
	if c.WgPort > 0 {
		slots = append(slots, slot{"внутренний WG-порт", c.WgPort})
	}
	seen := map[int]string{}
	for _, s := range slots {
		if prev, dup := seen[s.port]; dup {
			return fmt.Errorf("порт %d занят дважды: %s и %s — задайте разные", s.port, prev, s.name)
		}
		seen[s.port] = s.name
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
	Listen         string `json:"listen"`
	Peer           string `json:"peer,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Links          string `json:"links,omitempty"`
	Streams        int    `json:"streams,omitempty"`
	Transport      string `json:"transport,omitempty"`
	Mode           string `json:"mode,omitempty"`
	ObfProfile     string `json:"obfProfile,omitempty"`
	ObfKey         string `json:"obfKey,omitempty"`
	StreamsPerCred int    `json:"streamsPerCred,omitempty"`
	Platform       string `json:"platform,omitempty"` // ""|desktop|mobile
	DNSMode        string `json:"dnsMode,omitempty"`
	DNSServers     string `json:"dnsServers,omitempty"`
	ClientID       string `json:"clientId,omitempty"`
	Sub            string `json:"sub,omitempty"`
	Debug          bool   `json:"debug,omitempty"`
	// KCP — профиль ARQ tcp-режима (freeturn 3.2+, F144): приезжает полем `kcp`
	// ссылки freeturn://, редактора в UI нет. Рендерится в -kcp-* только при
	// Mode tcp: вне tcp клиент отвергает любое отклонение от дефолта на старте.
	KCP *FreeTurnKCP `json:"kcp,omitempty"`
}

// FreeTurnKCP — восемь параметров KCP в форме upstream (uri.KCP / -kcp-*).
type FreeTurnKCP struct {
	NoDelay    int  `json:"nodelay"`
	Interval   int  `json:"interval"`
	Resend     int  `json:"resend"`
	NC         int  `json:"nc"`
	SndWnd     int  `json:"sndwnd"`
	RcvWnd     int  `json:"rcvwnd"`
	MTU        int  `json:"mtu"`
	ACKNoDelay bool `json:"acknodelay"`
}

func (c FreeTurnClientConfig) Validate() error {
	if err := localListen(c.Listen); err != nil {
		return err
	}
	if strings.TrimSpace(c.Peer) == "" && strings.TrimSpace(c.Links) == "" && strings.TrimSpace(c.Sub) == "" {
		return fmt.Errorf("не задан адрес реле (-peer / -links / подписка)")
	}
	return c.KCP.validate()
}

// validate зеркалит validateKCP клиента freeturn (internal/config/kcp.go):
// частичный объект `kcp` из чужой ссылки даёт нули, и клиент падал бы на старте
// флагом `-kcp-mtu 0` — без редактора в UI выхода из этого нет.
func (k *FreeTurnKCP) validate() error {
	if k == nil {
		return nil
	}
	if k.NoDelay != 0 && k.NoDelay != 1 {
		return fmt.Errorf("kcp.nodelay %d: допустимо 0 | 1", k.NoDelay)
	}
	if k.NC != 0 && k.NC != 1 {
		return fmt.Errorf("kcp.nc %d: допустимо 0 | 1", k.NC)
	}
	if k.Interval <= 0 {
		return fmt.Errorf("kcp.interval %d: должен быть положительным", k.Interval)
	}
	if k.Resend < 0 {
		return fmt.Errorf("kcp.resend %d: не может быть отрицательным", k.Resend)
	}
	if k.SndWnd <= 0 || k.RcvWnd <= 0 {
		return fmt.Errorf("kcp окна %d/%d: sndwnd и rcvwnd должны быть положительными", k.SndWnd, k.RcvWnd)
	}
	if k.MTU < 300 || k.MTU > 1350 {
		return fmt.Errorf("kcp.mtu %d: допустимо 300..1350", k.MTU)
	}
	return nil
}

// NDMSNames — у FreeTurn NDMS-интерфейсов нет: клиент слушает 127.0.0.1.
// Пустая декларация объявлена ЯВНО, а не отсутствием метода: без неё конфиг
// выпал бы из ведомости неотличимо от забытого.
func (c FreeTurnClientConfig) NDMSNames() []string { return nil }

// ── OpenFlux ─────────────────────────────────────────────────────

// Транспорты OpenFlux (upstream main.go:90). Клиент и выходная нода обязаны
// использовать ОДИН И ТОТ ЖЕ транспорт и один канал связи, поэтому список —
// контракт совместимости, а не украшение.
const (
	OpenFluxTransportYandex     = "yandex"     // Yandex.Docs (WS)
	OpenFluxTransportVyandex    = "vyandex"    // Yandex Volga (HTTP relay + WS)
	OpenFluxTransportOneme      = "oneme"      // MAX / OneMe (WebRTC DataChannel)
	OpenFluxTransportCupsOnline = "cupsonline" // Cups.online (Centrifugo)
	OpenFluxTransportMailru     = "mailru"     // Mail.ru Docs (WS)
)

// OpenFluxTransports — канонический перечень, порядок = порядок в UI.
var OpenFluxTransports = []string{
	OpenFluxTransportYandex,
	OpenFluxTransportVyandex,
	OpenFluxTransportOneme,
	OpenFluxTransportCupsOnline,
	OpenFluxTransportMailru,
}

// ValidOpenFluxTransport — транспорт из перечня upstream.
func ValidOpenFluxTransport(t string) bool {
	t = strings.ToLower(strings.TrimSpace(t))
	for _, ok := range OpenFluxTransports {
		if t == ok {
			return true
		}
	}
	return false
}

// openFluxTransportNeedsURL — транспорты, которым нужен документ-канал (-url).
// cupsonline комнаты выдаёт сам процесс (печатает base64-список), oneme ходит
// парой токен+uid; остальным нужен публичный документ.
func openFluxTransportNeedsURL(transport string) bool {
	switch transport {
	case OpenFluxTransportCupsOnline, OpenFluxTransportOneme:
		return false
	}
	return true
}

// OpenFluxCodecBatched/OpenFluxCodecLegacy — кодеки upstream. Провода НЕ
// совместимы: обе стороны обязаны быть на одном (README, «Выбор кодека»).
const (
	OpenFluxCodecBatched = "batched"
	OpenFluxCodecLegacy  = "legacy"
)

// OpenFluxClientConfig — клиент OpenFlux на роутере: SOCKS5-вход (на Linux
// других входов у upstream нет, tun_darwin.go — только macOS) на 127.0.0.1.
type OpenFluxClientConfig struct {
	Listen string `json:"listen"` // 127.0.0.1:PORT из пула ListenPortMin..Max
	// Transport — вид релея; URL — адрес документа-канала; обе стороны
	// туннеля обязаны сходиться в них (см. OpenFluxTransports).
	Transport string `json:"transport"`
	URL       string `json:"url,omitempty"`
	// MaxToken/MaxUid — учётные данные транспорта oneme (upstream --maxToken,
	// --maxUid). Не секреты API, но персональные идентификаторы — маскируются
	// как пароль (proxySecretsOf).
	MaxToken string `json:"maxToken,omitempty"`
	MaxUID   string `json:"maxUid,omitempty"`
	// Codec — batched|legacy; провода несовместимы, значение едет и на
	// выходную ноду. Пусто = batched (дефолт upstream).
	Codec string `json:"codec,omitempty"`
	// EncryptionKey — общий секрет AES-256-GCM (опционально). Едет в argv
	// форка (-encryption-key): файлов у наших ролей нет, паритет с -obf-key
	// freeturn и -password wdtt.
	EncryptionKey string `json:"encryptionKey,omitempty"`
	Debug         bool   `json:"debug,omitempty"`
}

func (c OpenFluxClientConfig) Validate() error {
	if err := localListen(c.Listen); err != nil {
		return err
	}
	if !ValidOpenFluxTransport(c.Transport) {
		return fmt.Errorf("transport %q: ожидали один из %s", c.Transport, strings.Join(OpenFluxTransports, "|"))
	}
	t := strings.ToLower(strings.TrimSpace(c.Transport))
	if openFluxTransportNeedsURL(t) && strings.TrimSpace(c.URL) == "" {
		return fmt.Errorf("не задан адрес канала (-url) для транспорта %s", t)
	}
	if t == OpenFluxTransportOneme && strings.TrimSpace(c.MaxToken) == "" {
		return fmt.Errorf("транспорту oneme нужен -maxToken")
	}
	switch c.Codec {
	case "", OpenFluxCodecBatched, OpenFluxCodecLegacy:
	default:
		return fmt.Errorf("codec %q: ожидали batched|legacy", c.Codec)
	}
	return nil
}

// NDMSNames — у OpenFlux NDMS-интерфейсов нет.
func (c OpenFluxClientConfig) NDMSNames() []string { return nil }

// OpenFluxModeL3/OpenFluxModeL4 — бэкенды выходной ноды (upstream --mode).
// l3 — сырой SNAT/DNAT, только Linux + root; l4 — gVisor proxy, без root.
const (
	OpenFluxModeL3 = "l3"
	OpenFluxModeL4 = "l4"
)

// OpenFluxServerConfig — выходная нода OpenFlux на роутере. Слушающего сокета
// у неё НЕТ: клиент и выход соединяются ЧЕРЕЗ релей (документ), входящие
// порты открывать не нужно.
type OpenFluxServerConfig struct {
	Transport string `json:"transport"`
	URL       string `json:"url,omitempty"`
	MaxToken  string `json:"maxToken,omitempty"`
	MaxUID    string `json:"maxUid,omitempty"`
	// Mode — l3|l4. l3 на роутере требует root и правило против kernel-RST
	// (README, «l3 и kernel-RST»); правило ставит роль, но адрес выхода
	// (--local-ip) обязан быть явным — авто-детект меняется при смене WAN.
	Mode string `json:"mode,omitempty"`
	// LocalIP — egress IP для l3 (upstream --local-ip). Обязателен в l3:
	// scoped RST-drop и SNAT строятся по нему.
	LocalIP string `json:"localIp,omitempty"`
	Codec   string `json:"codec,omitempty"`
	// EncryptionKey — общий секрет AES-256-GCM; обязан совпадать у клиента.
	EncryptionKey string `json:"encryptionKey,omitempty"`
	// DNS — резолверы процесса через запятую (fork -dns). Системный
	// 127.0.0.1 на Keenetic отвечает не всегда, и первая же ошибка
	// транспорта убивает процесс (upstream: log.Fatalf) — поле даёт
	// пользователю выход без правки resolv.conf.
	DNS string `json:"dns,omitempty"`
	// SingboxRoute — «через sing-box»: данные-сокеты ноды (l4) метятся
	// fwmark'ом (fork -fwmark), и OUTPUT-правило роли направляет их в
	// цепочку AWGM-REDIRECT sing-box — дальше действует его маршрутизация.
	// TCP-трафик абонентов; UDP и транспорт до релея идут напрямую.
	SingboxRoute bool `json:"singboxRoute,omitempty"`
	Debug        bool `json:"debug,omitempty"`
}

// OpenFluxSingboxMark — fwmark данных-сокетов при SingboxRoute (roles/args.go
// кладёт его в -fwmark, singboxJump-ресурс ролей по нему матчит OUTPUT).
// Значение выбрано в стороне от типовых меток tproxy (1, 0xff) — конфликт
// означал бы перехват чужого трафика.
const OpenFluxSingboxMark = 20294

func (c OpenFluxServerConfig) Validate() error {
	if !ValidOpenFluxTransport(c.Transport) {
		return fmt.Errorf("transport %q: ожидали один из %s", c.Transport, strings.Join(OpenFluxTransports, "|"))
	}
	t := strings.ToLower(strings.TrimSpace(c.Transport))
	if openFluxTransportNeedsURL(t) && strings.TrimSpace(c.URL) == "" {
		return fmt.Errorf("не задан адрес канала (-url) для транспорта %s", t)
	}
	if t == OpenFluxTransportOneme && strings.TrimSpace(c.MaxToken) == "" {
		return fmt.Errorf("транспорту oneme нужен -maxToken")
	}
	switch c.Mode {
	case OpenFluxModeL3:
		if c.SingboxRoute {
			// В l3 пакеты уходят сырым SOCK_RAW мимо данных-сокетов, метка
			// на них не действует — тумблер дал бы молчаливое «ниcharger».
			return fmt.Errorf("mode l3: направление через sing-box работает только в l4")
		}
		if strings.TrimSpace(c.LocalIP) == "" {
			return fmt.Errorf("mode l3: не задан egress-адрес (localIp) — по нему строится RST-drop и SNAT")
		}
		if net.ParseIP(strings.TrimSpace(c.LocalIP)) == nil {
			return fmt.Errorf("localIp %q: не IP-адрес", c.LocalIP)
		}
	case OpenFluxModeL4:
	case "":
	default:
		return fmt.Errorf("mode %q: ожидали l3|l4", c.Mode)
	}
	switch c.Codec {
	case "", OpenFluxCodecBatched, OpenFluxCodecLegacy:
	default:
		return fmt.Errorf("codec %q: ожидали batched|legacy", c.Codec)
	}
	// DNS — только IP-адреса через запятую: форк валидирует тем же правилом,
	// и расхождение дало бы приговор конфига уже применением.
	for _, part := range strings.Split(c.DNS, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if net.ParseIP(part) == nil {
			return fmt.Errorf("dns %q: не IP-адрес", part)
		}
	}
	return nil
}

// NDMSNames — у OpenFlux NDMS-интерфейсов нет.
func (c OpenFluxServerConfig) NDMSNames() []string { return nil }

// NormalizedMode — режим с дефолтом l4 (безопасный бэкенд без root).
func (c OpenFluxServerConfig) NormalizedMode() string {
	if strings.TrimSpace(c.Mode) == OpenFluxModeL3 {
		return OpenFluxModeL3
	}
	return OpenFluxModeL4
}

// FreeTurnServerConfig — сервер FreeTurn.
type FreeTurnServerConfig struct {
	Listen  string `json:"listen"`
	Connect string `json:"connect,omitempty"`
	// LinkPeer — адрес, который панель кладёт в ссылку абоненту (#933):
	// ВАЛИДАЦИЯ ЭТОГО ПОЛЯ ЖИВЁТ НЕ ЗДЕСЬ. Ошибка из Validate() уезжает в
	// cfgErr ресурса процесса (procres/proc.go) и означает «инстанс не
	// запускать»: косметический адрес ссылки не смеет быть приговором
	// раздаче. Отказ обязан случиться ДО записи — см. gateCheck
	// (internal/api/proxy_instances.go), там же и правило.
	//
	// DNS-имя роутера или его внешний IP, при желании с портом. Пусто —
	// сборщик ссылки спросит внешний IP, как делал всегда.
	//
	// Своё поле конфига, а НЕ общий Record.LinkPeer (как у wdtt-сервера): там
	// это память о последней выдаче, которую молча перебивает любой
	// одноразовый peer запроса, и править её снаружи нечем — в теле PATCH
	// инстанса полей записи пять, linkPeer среди них нет. Здесь нужна
	// НАСТРОЙКА с одним писателем — пользователем.
	LinkPeer     string `json:"linkPeer,omitempty"`
	Mode         string `json:"mode,omitempty"` // udp|tcp — он же протокол INPUT-правила
	ObfProfile   string `json:"obfProfile,omitempty"`
	ObfKey       string `json:"obfKey,omitempty"`
	ClientsFile  string `json:"clientsFile,omitempty"`
	Debug        bool   `json:"debug,omitempty"`
	OpenFirewall bool   `json:"openFirewall"`
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
