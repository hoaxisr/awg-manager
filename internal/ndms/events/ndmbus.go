package events

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// DefaultBusPath — UNIX-сокет шины событий ndm (libndm core.c:
// NDM_CORE_EVENT_SOCKET_). Подписки в протоколе нет: подключённый клиент
// получает все широковещательные события белого списка (AuditLog,
// ConfigurationSaved, Neighbour, …); интерфейсных событий на шине нет —
// хуки остаются (event-bus-evidence.md §1).
const DefaultBusPath = "/var/run/ndm.event.socket"

// BusClassConfigurationSaved — класс события «конфигурация записана во
// флеш»: приходит в момент строки `configuration saved` журнала ndm, и при
// неизменном содержимом тоже (save-signal-research.md §6).
const BusClassConfigurationSaved = "Event::Type::ConfigurationSaved"

// maxBusString — потолок длины строки кадра. Самое длинное, что видел стенд,
// — known-host Neighbour (49 байт); длина больше — порванный поток или не
// тот протокол: ошибка и переподключение, а не выделение по чужой длине.
const maxBusString = 64 << 10

// BusEvent — одно событие шины: класс и raise_time (uptime ndm, с, тот же
// домен часов, что /proc/uptime — ReadUptime).
type BusEvent struct {
	Class     string
	RaiseTime float64
}

// Кадр шины (libndm-1.1.30 core.c:567-775, __ndm_core_read_xml_children):
// поток без длины кадра; шаг — байт ctrl<<6|type, у NODE/ATTR/SIBL следом две
// строки «u32 BE длина + байты» (name, value), у END данных нет.
const (
	busCtrlNode = 0
	busCtrlAttr = 1
	busCtrlSibl = 2
	busCtrlEnd  = 3
)

// busParser — потоковый разбор документов шины. Автомат по глубине: узел
// DOCUMENT — уровень 1, корневой `event` — уровень 2; документ завершён,
// когда END возвращает глубину к уровню 1. У листового события END один (он
// закрывает `event`), у события с детьми (Neighbour) — по одному на
// последнего ребёнка каждого уровня плюс корень (event-bus-evidence.md,
// поправка к §6 research). Атрибуты class/raise_time берутся только с узла
// уровня 2; дети пропускаются. Тип узла на разбор не влияет: строки есть у
// любого NODE/ATTR/SIBL, поэтому неизвестный тип рассинхрона не даёт.
type busParser struct {
	r *bufio.Reader
}

func newBusParser(r io.Reader) *busParser {
	return &busParser{r: bufio.NewReaderSize(r, 4096)}
}

// next читает документ целиком. Ошибка — обрыв или нарушение протокола:
// вызывающий переподключается (поток после ошибки не синхронизировать).
func (p *busParser) next() (BusEvent, error) {
	var ev BusEvent
	depth := 0
	isEvent := false // узел уровня 2 — элемент `event`
	for {
		b, err := p.r.ReadByte()
		if err != nil {
			return BusEvent{}, err
		}
		ctrl := b >> 6
		if ctrl == busCtrlEnd {
			if depth < 2 {
				return BusEvent{}, fmt.Errorf("ndm bus: END на глубине %d", depth)
			}
			depth--
			if depth == 1 {
				return ev, nil
			}
			continue
		}
		name, err := p.str()
		if err != nil {
			return BusEvent{}, err
		}
		value, err := p.str()
		if err != nil {
			return BusEvent{}, err
		}
		switch ctrl {
		case busCtrlNode:
			depth++
			if depth == 2 {
				isEvent = name == "event"
			}
		case busCtrlSibl:
			if depth < 2 {
				return BusEvent{}, fmt.Errorf("ndm bus: SIBL на глубине %d", depth)
			}
			if depth == 2 {
				isEvent = name == "event"
			}
		case busCtrlAttr:
			if depth != 2 || !isEvent {
				continue
			}
			switch name {
			case "class":
				ev.Class = value
			case "raise_time":
				// Нечисловое — 0: такое событие не закроет ни один полёт
				// (raise ≥ t0 > 0), то есть fail-closed.
				ev.RaiseTime, _ = strconv.ParseFloat(value, 64)
			}
		}
	}
}

func (p *busParser) str() (string, error) {
	var l [4]byte
	if _, err := io.ReadFull(p.r, l[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint32(l[:])
	if n > maxBusString {
		return "", fmt.Errorf("ndm bus: строка %d байт больше %d", n, maxBusString)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(p.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// BusReader — клиент шины событий ndm: держит соединение, разбирает
// документы и отдаёт их onEvent по одному в порядке потока из одной
// горутины; onState(true/false) — подключение и его потеря. Обработчики
// обязаны быть O(1) без блокировки: пока они работают, сокет не читается, а
// поведение ndm при медленном читателе стендом не проверено.
//
// Поздний клиент историю не получает, переподключившийся теряет события
// промежутка (event-bus-evidence.md §3) — потребитель обязан считать
// состояние после onState(false) неизвестным.
type BusReader struct {
	path    string
	onEvent func(BusEvent)
	onState func(connected bool)
	log     Logger

	// backoffMin/backoffMax — пауза между попытками подключения (1, 2, 4 … ≤ 10 с).
	backoffMin, backoffMax time.Duration

	mu   sync.Mutex
	conn net.Conn
	stop chan struct{}
	done chan struct{}
}

// NewBusReader создаёт клиента; подключается Start.
func NewBusReader(path string, onEvent func(BusEvent), onState func(connected bool), log Logger) *BusReader {
	if log == nil {
		log = NopLogger()
	}
	return &BusReader{
		path: path, onEvent: onEvent, onState: onState, log: log,
		backoffMin: time.Second, backoffMax: 10 * time.Second,
	}
}
