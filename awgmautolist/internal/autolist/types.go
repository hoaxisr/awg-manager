package autolist

import (
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/judge"
)

// Константы поведения перенесены из autolist 0.6.x (src/backend-c/include/autolist/
// autolist.h, autolistprobes.h и src/api/app.c) как данные — это дизайн,
// а не код, лицензионных ограничений не несёт.

const (
	// Порог «домен кандидат»: столько отказов за FailWindow подряд.
	DefaultFailThreshold = 3
	DefaultFailWindow    = 60 // секунд

	// Сколько одинаковых ClientHello без ответа считают блокировкой TLS.
	// В Clash API ретрансляции не видны — этот порог применяет сборщик
	// dial-ошибок: столько ошибок dial на один host за окно.
	DefaultDialFailThreshold = 3

	// Одностороннее соединение: отправлено не меньше, чем получено,
	// получено меньше StallMaxBytes, соединение живёт дольше StallMinSecs.
	StallMinSecs        = 10
	StallMaxBytes       = 32768
	StallMinUplink      = 8192
	StallNotesThreshold = 2   // autolist AUTOLIST_NOTES
	StallNoteWindow     = 120 // секунд; autolist AUTOLIST_NOTE_SECS

	// Отдельное расширение AWGM: открытое соединение только направляет хост
	// в активную пробу, но не создаёт FIN-ноту и не становится кандидатом.
	OpenProbeMinSecs = 30
	OpenProbeMaxBPS  = 256

	// Очередь и её лимиты принадлежат probeq, без дублирующихся констант здесь.
	ProbeTickSec       = 120 // autolist AUTOLIST_PROBES_TICK_SECS
	ProbeTimeoutSec    = 30  // autolist_pump: o.timeout_s = 30
	ProbeStallSec      = 5   // autolist recheck.c PROBE_STALL_S
	DefaultWANCheckURL = "https://cp.cloudflare.com/generate_204"
	ProbeRedirectsMax  = 3     // autolist recheck.c PROBE_REDIRECTS
	ProbeBodyCap       = 65536 // autolist recheck.c PROBE_CAP

	// Добавления правил в режиме auto (autolist app.c AL_ADDS_PER_HOUR).
	AddsPerHour = 50

	// Dismiss прячет кандидата на сутки (autolistcand.h).
	DismissSec = 86400

	// Память наблюдателя (autolist.h).
	FlowsCap   = 1024
	HostsCap   = 512
	HintTTLSec = 600

	// Опрос Clash API и логов.
	PollIntervalSec = 15
	PollTimeoutSec  = 10

	// Packet visibility предшествует решению Clash. Короткий pending-буфер
	// закрывает только это окно; истёкший unknown не номинируется и не пробуется.
	PacketCorrelationTTL  = 45 * time.Second
	PacketPendingCapacity = 256
)

// Verdict — итог активной пробы (autolistcheck.h).
type Verdict int

const (
	VerdictUnknown Verdict = iota
	VerdictReachable
	VerdictBlocked
)

func (v Verdict) String() string {
	switch v {
	case VerdictReachable:
		return "reachable"
	case VerdictBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// Why — причина, по которой хост признан не открывающимся (autolistcand.h).
type Why int

const (
	WhyNone      Why = iota
	WhyNoAnswer      // TLS-рукопожатие без ответа (timeout после connect)
	WhyReset         // RST во время или сразу после рукопожатия
	WhyFreeze        // ответ встал после 8..32 КБ
	WhyNoConnect     // TCP connect не удался (SYN без ответа)
)

func (w Why) String() string {
	switch w {
	case WhyNoAnswer:
		return "no-answer"
	case WhyReset:
		return "reset"
	case WhyFreeze:
		return "freeze"
	case WhyNoConnect:
		return "no-connect"
	default:
		return ""
	}
}

// State — состояние кандидата в таблице (autolist_al_state_t).
type State int

const (
	StateCandidate State = iota
	StateChecking
)

func (s State) String() string {
	switch s {
	case StateChecking:
		return "checking"
	default:
		return "candidate"
	}
}

// Host — адрес назначения соединения из Clash API.
type Host struct {
	IP   string
	Name string // SNI/Host из метаданных sing-box, может быть пуст
}

// key — ключ атрибуции: имя, если sing-box его знает, иначе адрес.
func (h Host) key() string {
	if h.Name != "" {
		return strings.ToLower(strings.TrimSuffix(h.Name, "."))
	}
	return h.IP
}

// probeURL строит https-URL для активной пробы хоста. Для голых IP проба
// с SNI невозможна, поэтому они в пробы не попадают (фильтр в Judge).
func probeURL(name string) string {
	if name == "" {
		return ""
	}
	u := url.URL{Scheme: "https", Host: name, Path: "/"}
	return u.String()
}

// privateIP — адрес из приватных диапазонов (autolist judge.c local_v4 +
// autolist_local): такие в кандидаты и пробы не попадают.
func privateIP(ip string) bool {
	a := net.ParseIP(ip)
	if a == nil {
		return true // не IP — не наш случай
	}
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified() {
		return true
	}
	// CGNAT 100.64/10 и benchmark 198.18/15 (fakeip-пул!) — тоже локальные
	// для наших целей: autolist относится к 198.18.0.0/15 как к «никакому сайту».
	for _, cidr := range []string{"100.64.0.0/10", "198.18.0.0/15", "169.254.0.0/16", "fe80::/10", "fc00::/7"} {
		_, n, err := net.ParseCIDR(cidr)
		if err == nil && n.Contains(a) {
			return true
		}
	}
	return false
}

// registeredDomain использует единый алгоритм judge для таблицы, исключений
// и правил. Shared-хостинги по-прежнему не получают широкое доменное правило.
func registeredDomain(host string) string {
	domain, ok := judge.RegisteredDomain(host)
	if !ok || judge.IsSharedDomain(domain) {
		return ""
	}
	return domain
}
