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
	NdmsIface string `json:"ndmsIface,omitempty"` // OpkgTun17..49
	RawIface  string `json:"rawIface,omitempty"`  // opkgtun17..49

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
// Параметр goarch, а не runtime.GOARCH внутри: симметрично OpkgIndexRange,
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
	NDMSName    string // пин: OpkgTun17..49
	KernelIface string // пин: opkgtun17..49
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
// метода у любого из ЧЕТЫРЁХ конфигов ломает сборку пакета сразу.
//
// Границу гарантии называем честно: эти строки знают только про уже
// существующие типы. Пятый конфиг они не поймают — его ловит поле
// InstanceConfig.Cfg у потребителя (exitreg/declared.go), типизированное этим
// интерфейсом, и ловит ровно до тех пор, пока конфиг не стёрли в any.
var (
	_ RawExiter = WdttClientConfig{}
	_ RawExiter = WdttServerConfig{}
	_ RawExiter = FreeTurnClientConfig{}
	_ RawExiter = FreeTurnServerConfig{}
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

// WdttServerConfig — сервер WDTT (обе половины: WG + raw).
type WdttServerConfig struct {
	Listen       string `json:"listen"` // DTLS, 0.0.0.0:56000
	WgPort       int    `json:"wgPort,omitempty"`
	ConfigDir    string `json:"configDir,omitempty"`
	Password     string `json:"password"`
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
	// (PR #750). Пишет план 5 по факту применения.
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
	// Пароль владельца (-password) НЕ обязателен. Форк падает единственным
	// условием — `serverWrapKeys.Count() == 0`, то есть когда нет НИ ОДНОГО
	// пароля: главного или абонентского (server.go:1969). Абонентского
	// достаточно. Требование главного было строже форка и запирало сервер,
	// у которого абоненты есть, а «пароля владельца» никто не задавал;
	// починить его из UI было нечем. Непустоту паролей держит гейт запуска
	// по составу абонентов, а не эта проверка.
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
	Bond           bool   `json:"bond,omitempty"`
	ObfProfile     string `json:"obfProfile,omitempty"`
	ObfKey         string `json:"obfKey,omitempty"`
	StreamsPerCred int    `json:"streamsPerCred,omitempty"`
	Platform       string `json:"platform,omitempty"` // ""|desktop|mobile
	DNSMode        string `json:"dnsMode,omitempty"`
	DNSServers     string `json:"dnsServers,omitempty"`
	ClientID       string `json:"clientId,omitempty"`
	Sub            string `json:"sub,omitempty"`
	Debug          bool   `json:"debug,omitempty"`
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
	Listen       string `json:"listen"`
	Connect      string `json:"connect,omitempty"`
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
