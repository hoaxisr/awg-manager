package awgmproto

import (
	"encoding/json"
	"io"
	"strings"
)

// Имена флагов обвязки. Префикс awgm- принадлежит менеджеру, а не
// конфигурации: ConfigHash их отбрасывает, а форк о них не знает вовсе.
const (
	FlagProtocol = "awgm-protocol"
	FlagSocket   = "awgm-control-socket"
	FlagLogFile  = "awgm-log-file"
)

// Options — то, что менеджер передал обвязке.
type Options struct {
	Protocol bool   // напечатать строку пригодности и выйти
	Socket   string // путь управляющего сокета; пусто — сокета нет
	LogFile  string // путь журнала; пусто — писать в stdout, как раньше
}

// SplitArgs вырезает из argv флаги обвязки и возвращает остаток.
//
// Вырезать обязательно: парсеры форков (flag.Parse у wt-client,
// config.ParseClient у freeturn) на неизвестном флаге печатают usage и
// завершают процесс. Остаток отдаётся форку как есть, а ConfigHash считается
// по ПОЛНОМУ argv — он сам отбрасывает awgm-флаги, и обе стороны получают
// одинаковый отпечаток независимо от того, что кому досталось.
func SplitArgs(args []string) ([]string, Options) {
	var (
		rest []string
		opts Options
	)
	for i := 0; i < len(args); i++ {
		name, value, hasValue := splitArgFlag(args[i])
		switch name {
		case FlagProtocol:
			opts.Protocol = true
			continue
		case FlagSocket, FlagLogFile:
			if !hasValue {
				if i+1 < len(args) {
					value = args[i+1]
					i++
				}
			}
			if name == FlagSocket {
				opts.Socket = value
			} else {
				opts.LogFile = value
			}
			continue
		}
		rest = append(rest, args[i])
	}
	return rest, opts
}

// splitArgFlag — тот же разбор формы, что и в ident.go (splitFlag), но только
// для записей с ведущим дефисом.
//
// Разница нужна и не случайна: ConfigHash обязан узнавать имя флага и без
// дефисов (§5.5 п.2), а здесь argv настоящий, и значение вроде `rawtun` не
// должно притворяться флагом обвязки. Сам разбор `имя=значение` при этом
// остаётся в одном месте.
func splitArgFlag(arg string) (name, value string, hasValue bool) {
	if !strings.HasPrefix(arg, "-") {
		return "", "", false
	}
	return splitFlag(arg)
}

// FlagValue достаёт значение флага из argv, понимая обе формы записи:
// "-name value" и "--name=value". Нужен обвязкам, которым значение требуется
// раньше, чем форк разберёт свои аргументы, и которым нельзя опираться на
// внутренние переменные форка.
func FlagValue(args []string, name string) string {
	for i := 0; i < len(args); i++ {
		n, v, hasValue := splitArgFlag(args[i])
		if n != name {
			continue
		}
		if hasValue {
			return v
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			return args[i+1]
		}
		return ""
	}
	return ""
}

// InstanceFromPath достаёт идентификатор инстанса из имени управляющего
// сокета: <dir>/<impl>-<role>-<instance>.sock.
//
// Отдельного флага для идентификатора нет намеренно — набор флагов обвязки
// закрыт (§6), а имя сокета его уже содержит. Плата за это: поле instance в
// hello перестаёт быть независимой проверкой «мой ли это инстанс» и остаётся
// эхом того, что менеджер сам и назвал; самостоятельную проверку несут impl и
// role. Путь без ожидаемого префикса даёт пустую строку — менеджер увидит
// расхождение и откажет.
func InstanceFromPath(path, impl, role string) string {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".sock")
	prefix := impl + "-" + role + "-"
	if !strings.HasPrefix(base, prefix) {
		return ""
	}
	return base[len(prefix):]
}

// ProtocolInfo — ответ на --awgm-protocol.
type ProtocolInfo struct {
	V        int      `json:"v"`
	Impl     string   `json:"impl"`
	Role     string   `json:"role"`
	Modes    []string `json:"modes,omitempty"`
	Commands []string `json:"commands"`
}

// Команды протокола версии 1.
const (
	CmdState     = "state"
	CmdAttachTun = "attach-tun"
	CmdDetachTun = "detach-tun"
)

// PrintProtocol печатает РОВНО одну JSON-строку и ничего больше: менеджер
// разбирает stdout пробы целиком.
func PrintProtocol(w io.Writer, info ProtocolInfo) error {
	info.V = Version
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
