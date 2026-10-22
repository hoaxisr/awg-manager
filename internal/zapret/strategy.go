package zapret

import (
	"regexp"
	"strconv"
	"strings"
)

// maxProfiles — предохранитель на случай, если конфиг распарсился не так,
// как мы ожидали (например, кавычка не нашлась и весь хвост файла попал
// в одну «стратегию»). nfqws2 с таким числом профилей всё равно не
// стартует, но мы не должны этому способствовать.
const maxProfiles = 64

// maxOptBlockBytes ограничивает разбор значения переменной опций: файл
// config на клиентской машине — это текст в несколько килобайт, всё
// больше означает, что мы читаем не то.
const maxOptBlockBytes = 1 << 20

// Warning is a stable diagnostic code with optional dynamic detail. The UI
// owns the localized wording so API responses do not expose server-language
// prose to users of another locale.
type Warning struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// Strategy — разобранный профиль nfqws2: одна карточка в UI.
//
// Поля фильтров хранятся строками «как в конфиге» (--filter-tcp=443,2053),
// а не разложенными в структуры: пользователю нужно видеть и править то
// же самое, что лежит в файле, иначе редактор перепишет его в другую
// нотацию и следующее обновление z2k покажет неожиданный дифф.
type Strategy struct {
	// Index — позиция в файле. 0-based; приоритет = чем меньше, тем
	// раньше проверяется фильтр.
	Index int `json:"index"`
	// Raw — исходная строка опций профиля без завершающего --new.
	Raw string `json:"raw"`

	Name string `json:"name,omitempty"`

	FilterTCP  string `json:"filterTcp,omitempty"`
	FilterUDP  string `json:"filterUdp,omitempty"`
	FilterL7   string `json:"filterL7,omitempty"`
	FilterL3   string `json:"filterL3,omitempty"`
	FilterICMP string `json:"filterIcmp,omitempty"`
	FilterIPP  string `json:"filterIpp,omitempty"`
	InRange    string `json:"inRange,omitempty"`
	OutRange   string `json:"outRange,omitempty"`
	Payload    string `json:"payload,omitempty"`

	// Hostlists — подключённые к профилю списки доменов (--hostlist=).
	// Ключ — путь из конфига, значение — сколько доменов в файле.
	Hostlists []StrategyHostlist `json:"hostlists,omitempty"`
	// HostlistExcludes — списки исключений (--hostlist-exclude=).
	HostlistExcludes []StrategyHostlist `json:"hostlistExcludes,omitempty"`
	// HostlistAuto — автосписок (--hostlist-auto=). В отличие от
	// прочих, его пишет сам nfqws2 по итогам детекта блокировок.
	HostlistAuto []StrategyHostlist `json:"hostlistAuto,omitempty"`
	// Ipsets — списки адресов (--ipset=).
	Ipsets []StrategyHostlist `json:"ipsets,omitempty"`

	// Placeholders — маркеры <HOSTLIST> / <HOSTLIST_NOAUTO>, найденные в
	// профиле как отдельные токены. Их разворачивает init-скрипт zapret
	// при старте, поэтому на карточке они показываются как развёрнутые
	// в конкретные файлы, а не как путь.
	Placeholders []string `json:"placeholders,omitempty"`

	// LuaDesync — цепочка вызовов Lua, из которых и состоит собственно
	// обход. Порядок значит и здесь тоже.
	LuaDesync []LuaInstance `json:"luaDesync,omitempty"`

	// Warnings — предупреждения о конкретных граблях этой стратегии
	// (например, ipset-фильтр, который молча ничего не делает в
	// режиме MODE_FILTER=hostlist). Показываются прямо на карточке.
	Warnings []Warning `json:"warnings,omitempty"`
}

// StrategyHostlist — ссылка на файл списка плюс его текущее наполнение.
type StrategyHostlist struct {
	Path string `json:"path"`
	// Exists=false означает, что nfqws2 не запустится: RegisterHostlist
	// требует существующий непустой файл иначе завершает процесс.
	Exists   bool `json:"exists"`
	Entries  int  `json:"entries"`
	Editable bool `json:"editable"`
	// HasIPs — в списке есть адреса или подсети. Значимо только в паре с
	// Domains: если адреса есть, а доменов нет, список не сработает в
	// MODE_FILTER=hostlist (см. strategyWarnings).
	HasIPs bool `json:"hasIps"`
	// IPOnly — в списке только адреса, ни одного домена.
	IPOnly bool `json:"ipOnly"`
	// FromPlaceholder заполнен, когда ссылка пришла не явным путём, а
	// плейсхолдером <HOSTLIST>: его init-скрипт zapret разворачивает в
	// несколько реальных списков при старте. Пусто = путь указан явно.
	FromPlaceholder string `json:"fromPlaceholder,omitempty"`
	Note            string `json:"note,omitempty"`
}

// LuaInstance — один вызов --lua-desync.
type LuaInstance struct {
	// Function — имя функции (fake, multisplit, circular, send, drop…).
	Function string `json:"function"`
	// Args — остальные аргументы после двоеточия, как есть.
	Args string `json:"args,omitempty"`
	// Strategy — значение :strategy=N, если есть. Именно по нему
	// circular перебирает варианты, поэтому это полезно показать.
	Slot string `json:"slot,omitempty"`
	// Key — значение :key=, метка пула для циклического перебора.
	Key string `json:"key,omitempty"`
}

var (
	optRe     = regexp.MustCompile(`--[a-z0-9-]+(?:=[^\s]+)?`)
	nameOptRe = regexp.MustCompile(`(?:^|\s)--name=([^\s]+)`)
	luaCallRe = regexp.MustCompile(`--lua-desync=([^\s]+)`)
	keyArgRe  = regexp.MustCompile(`(?:^|:)key=([^\s:]+)`)
	slotArgRe = regexp.MustCompile(`(?:^|:)strategy=([^\s:]+)`)
	// placeholderRe ловит маркеры, которые init-скрипт zapret разворачивает
	// в --hostlist / --hostlist-exclude при старте. Именно они несут основной
	// список доменов стратегии, поэтому пропустить их нельзя.
	placeholderRe = regexp.MustCompile(`<HOSTLIST(?:_NOAUTO)?>`)
)

// parseStrategies превращает тело переменной опций в карточки.
func parseStrategies(value string) []Strategy {
	items := splitProfiles(value)
	out := make([]Strategy, 0, len(items))
	for i, item := range items {
		out = append(out, parseStrategy(i, item))
	}
	return out
}

func parseStrategy(index int, raw string) Strategy {
	s := Strategy{Index: index, Raw: raw}

	for _, tok := range optRe.FindAllString(raw, -1) {
		key, val, hasVal := strings.Cut(tok, "=")
		switch key {
		case "--filter-tcp":
			s.FilterTCP = val
		case "--filter-udp":
			s.FilterUDP = val
		case "--filter-l7":
			s.FilterL7 = val
		case "--filter-l3":
			s.FilterL3 = val
		case "--filter-icmp":
			s.FilterICMP = val
		case "--filter-ipp":
			s.FilterIPP = val
		case "--in-range":
			s.InRange = val
		case "--out-range":
			s.OutRange = val
		case "--payload":
			s.Payload = val
		case "--hostlist":
			if hasVal {
				s.Hostlists = append(s.Hostlists, newHostlistRef(val, false))
			}
		case "--hostlist-exclude":
			if hasVal {
				s.HostlistExcludes = append(s.HostlistExcludes, newHostlistRef(val, true))
			}
		case "--hostlist-auto":
			if hasVal {
				s.HostlistAuto = append(s.HostlistAuto, newHostlistRef(val, false))
			}
		case "--ipset":
			if hasVal {
				s.Ipsets = append(s.Ipsets, newHostlistRef(val, false))
			}
		}
	}

	// Плейсхолдеры <HOSTLIST> / <HOSTLIST_NOAUTO> стоят отдельными токенами,
	// а не значением --hostlist: их разворачивает init-скрипт при старте
	// (filter_apply_hostlist_target в common/list.sh). Без отдельного
	// разбора мы бы пропустили основной список доменов стратегии.
	for _, ph := range placeholderRe.FindAllString(raw, -1) {
		s.Placeholders = append(s.Placeholders, ph)
	}

	// Имя профиля: явный --name= побеждает выведенное из --lua-desync.
	if m := nameOptRe.FindStringSubmatch(raw); len(m) == 2 {
		s.Name = m[1]
	} else {
		s.Name = defaultProfileName(s)
	}

	for _, call := range luaCallRe.FindAllStringSubmatch(raw, -1) {
		if len(call) != 2 {
			continue
		}
		inst := LuaInstance{}
		fn, args, _ := strings.Cut(call[1], ":")
		inst.Function = fn
		inst.Args = args
		if m := slotArgRe.FindStringSubmatch(args); len(m) == 2 {
			inst.Slot = m[1]
		}
		if m := keyArgRe.FindStringSubmatch(args); len(m) == 2 {
			inst.Key = m[1]
		}
		s.LuaDesync = append(s.LuaDesync, inst)
	}

	s.Warnings = strategyWarnings(s)
	return s
}

// defaultProfileName подписывает карточку так, как её увидит человек.
// Явного имени обычно нет, поэтому собираем из того, что реально
// различает стратегии: порты и протокол.
func defaultProfileName(s Strategy) string {
	proto := "TCP"
	switch {
	case s.FilterUDP != "":
		proto = "UDP"
	case s.FilterTCP != "":
		proto = "TCP"
	case s.FilterICMP != "":
		proto = "ICMP"
	}
	ports := s.FilterTCP
	if ports == "" {
		ports = s.FilterUDP
	}
	if ports == "" {
		if s.FilterL7 != "" {
			return proto + " " + s.FilterL7
		}
		return proto + " (без фильтра)"
	}
	return proto + " " + ports
}

// strategyWarnings собирает грабли, которые не видно из значений полей,
// но которые ломают стратегию «молча».
func strategyWarnings(s Strategy, nfqws2 ...bool) []Warning {
	var w []Warning
	usesLuaDesync := len(nfqws2) == 0 || nfqws2[0]
	countFailureReported := false
	for _, refs := range [][]StrategyHostlist{s.Hostlists, s.HostlistExcludes, s.HostlistAuto, s.Ipsets} {
		for _, h := range refs {
			if strings.HasPrefix(h.Note, hostlistCountErrorNotePrefix) {
				w = append(w, Warning{Code: "hostlist_count_failed", Detail: strings.TrimPrefix(h.Note, hostlistCountErrorNotePrefix)})
				countFailureReported = true
				break
			}
		}
		if countFailureReported {
			break
		}
	}

	// Главная ловушка zapret: в MODE_FILTER=hostlist/ipset демон
	// применяет списки как есть и адреса в них ИГНОРИРУЕТ. Список с
	// CIDR в hostlist выглядит рабочим, а не делает ничего.
	if len(s.Hostlists) > 0 || len(s.HostlistAuto) > 0 {
		for _, h := range s.Hostlists {
			if h.Exists && h.HasIPs && h.IPOnly {
				w = append(w, Warning{Code: "hostlist_ip_only"})
				break
			}
		}
	}

	// circular без key= не запоминает, на какой стратегии остановился:
	// перебор начинается заново на каждом новом хосте.
	if usesLuaDesync {
		for _, inst := range s.LuaDesync {
			if inst.Function == "circular" && inst.Key == "" {
				w = append(w, Warning{Code: "circular_missing_key"})
				break
			}
		}

		// Empty Lua chain: the profile matches the filter but performs no desync.
		if len(s.LuaDesync) == 0 {
			w = append(w, Warning{Code: "strategy_no_lua_desync"})
		}
	}

	return w
}

// strategiesToBody собирает тело переменной опций обратно из карточек,
// сохраняя порядок слайса как приоритет.
func strategiesToBody(items []Strategy) string {
	raw := make([]string, 0, len(items))
	for _, it := range items {
		raw = append(raw, it.Raw)
	}
	return serializeProfiles(raw)
}

// reindex перенумеровывает карточки после перестановки: индекс — это
// позиция, а не идентичность, и UI показывает его как «приоритет».
func reindex(items []Strategy) []Strategy {
	for i := range items {
		items[i].Index = i
	}
	return items
}

// slotCount считает, сколько вариантов перебирает circular в стратегии:
// максимальный :strategy=N плюс один, потому что нумерация с единицы.
func slotCount(s Strategy) int {
	maxSlot := 0
	for _, inst := range s.LuaDesync {
		if inst.Slot == "" {
			continue
		}
		if n, err := strconv.Atoi(inst.Slot); err == nil && n > maxSlot {
			maxSlot = n
		}
	}
	return maxSlot
}
