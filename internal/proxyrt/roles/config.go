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
