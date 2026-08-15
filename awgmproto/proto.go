// Package awgmproto — протокол управляющего сокета между awg-manager и
// дочерними процессами прокси. Пакет — отдельный вложенный модуль: его тянут по
// require и три форка, и сам менеджер, поэтому четыре реализации не могут
// разъехаться.
package awgmproto

import (
	"encoding/json"
	"fmt"
)

// Version — мажорная версия протокола. Несовпадение — отказ без деградации.
const Version = 1

// maxLine — потолок длины кадра. Больше — ошибка протокола.
const maxLine = 64 * 1024

// Коды ошибок, обязательные к различению обеими сторонами.
const (
	CodeUnknownCommand = "unknown-command"
	CodeBadRequest     = "bad-request"
	CodeBusy           = "busy"
	CodeNotSupported   = "not-supported"
	CodeInternal       = "internal"
)

// Kind — вид разобранного сообщения.
type Kind int

const (
	KindRequest Kind = iota
	KindResponse
	KindEvent
)

// Request — команда от менеджера процессу.
type Request struct {
	V     int    `json:"v"`
	ID    uint64 `json:"id"`
	Cmd   string `json:"cmd"`
	Iface string `json:"iface,omitempty"`
}

// Response — ответ процесса на команду.
type Response struct {
	V     int    `json:"v"`
	ID    uint64 `json:"id"`
	OK    bool   `json:"ok"`
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
	State *State `json:"state,omitempty"`
}

// TunState — состояние прикрепления дескриптора.
type TunState struct {
	Iface    string `json:"iface"`
	Attached bool   `json:"attached"`
}

// WGState — конфиг WireGuard, полученный клиентом от сервера.
type WGState struct {
	Config string `json:"config"`
}

// ListenState — порты, которые слушает сервер. Адресов у него три (§5.2):
// -listen (DTLS), -listen-direct (клиенты без DTLS) и -listen-raw (raw-IP
// клиенты без WireGuard).
//
// omitempty здесь НЕТ намеренно: структура присутствует целиком или
// отсутствует целиком. Ноль означает «этот транспорт выключен» — законное
// значение, а не «неизвестно», и стирать его нельзя.
type ListenState struct {
	DTLS   int `json:"dtls"`
	Direct int `json:"direct"`
	Raw    int `json:"raw"`
}

// State — снимок состояния процесса.
//
// Все поля, кроме обязательных, помечены omitempty намеренно: отсутствие поля
// означает «неизвестно», и менеджер трактует это как unknown, а не как нулевое
// значение. На этом различии стоит правило «unknown не порождает шагов».
type State struct {
	Role         string       `json:"role"`
	Instance     string       `json:"instance"`
	PID          int          `json:"pid"`
	ConfigHash   string       `json:"config_hash"`
	BinarySHA256 string       `json:"binary_sha256"`
	UptimeS      int64        `json:"uptime_s"`
	LastError    string       `json:"last_error"`
	Mode         string       `json:"mode,omitempty"`
	Tun          *TunState    `json:"tun,omitempty"`
	Address      string       `json:"address,omitempty"`
	MTU          int          `json:"mtu,omitempty"`
	WG           *WGState     `json:"wg,omitempty"`
	Listen       *ListenState `json:"listen,omitempty"`
	// Clients — указатель, потому что ноль клиентов законен: `int` с omitempty
	// стёр бы его, и менеджер не отличил бы пустой сервер от «неизвестно».
	Clients *int `json:"clients,omitempty"`
}

// Event — push от процесса без запроса.
type Event struct {
	V        int    `json:"v"`
	Event    string `json:"event"`
	Impl     string `json:"impl,omitempty"`
	Role     string `json:"role,omitempty"`
	Instance string `json:"instance,omitempty"`
	PID      int    `json:"pid,omitempty"`

	ConfigHash   string `json:"config_hash,omitempty"`
	BinarySHA256 string `json:"binary_sha256,omitempty"`

	Address string `json:"address,omitempty"`
	MTU     int    `json:"mtu,omitempty"`

	Iface string `json:"iface,omitempty"`
	// Attached — указатель по той же причине, что Clients в State: у push tun
	// при ОТКРЕПЛЕНИИ значение ложно, и `bool` с omitempty стёр бы поле
	// целиком — push «tun» уехал бы без единого признака, чего именно с tun'ом.
	// Пример §5.4 спеки показывает поле в обоих случаях, и он прав.
	Attached *bool `json:"attached,omitempty"`

	Fatal   bool   `json:"fatal,omitempty"`
	Message string `json:"message,omitempty"`
	// Code — код выхода в push exit. omitempty стирает ноль, и это здесь
	// безвредно: exit — best effort и будильник, отсутствие кода менеджер
	// читает как нулевой, а решение принимает по закрытию соединения.
	Code int `json:"code,omitempty"`
}

// Имена push-событий.
const (
	EventHello    = "hello"
	EventAddress  = "address"
	EventWGConfig = "wg-config"
	EventTun      = "tun"
	EventError    = "error"
	EventEvicted  = "evicted"
	EventExit     = "exit"
)

// EncodeLine сериализует сообщение в один кадр с завершающим переводом строки.
// Переводы строки внутри полей экранируются кодировщиком JSON, поэтому кадр
// не может распасться.
func EncodeLine(msg any) ([]byte, error) {
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	if len(b)+1 > maxLine {
		return nil, fmt.Errorf("кадр длиной %d байт превышает потолок %d", len(b)+1, maxLine)
	}
	return append(b, '\n'), nil
}

// DecodeLine разбирает кадр и определяет его вид. Вид выводится по составу
// полей: у запроса есть cmd, у ответа — ok, у push — event.
func DecodeLine(line []byte) (Kind, any, error) {
	if len(line) > maxLine {
		return 0, nil, fmt.Errorf("кадр длиной %d байт превышает потолок %d", len(line), maxLine)
	}
	var probe struct {
		V     int    `json:"v"`
		Cmd   string `json:"cmd"`
		Event string `json:"event"`
		OK    *bool  `json:"ok"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return 0, nil, err
	}
	if probe.V != Version {
		return 0, nil, fmt.Errorf("версия протокола %d, поддерживается %d", probe.V, Version)
	}
	switch {
	case probe.Cmd != "":
		var r Request
		if err := json.Unmarshal(line, &r); err != nil {
			return 0, nil, err
		}
		return KindRequest, r, nil
	case probe.OK != nil:
		var r Response
		if err := json.Unmarshal(line, &r); err != nil {
			return 0, nil, err
		}
		return KindResponse, r, nil
	case probe.Event != "":
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return 0, nil, err
		}
		return KindEvent, e, nil
	}
	return 0, nil, fmt.Errorf("кадр не является ни запросом, ни ответом, ни событием")
}
