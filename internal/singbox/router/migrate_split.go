package router

import (
	"encoding/json"
	"fmt"
)

// DNSTagRename описывает вынужденное переименование пользовательского
// DNS-сервера, тег которого занят движком fakeip-режима.
type DNSTagRename struct {
	From string
	To   string
}

// LegacySlotSplit — результат разбора одного файла прежней раскладки
// (20-router.json / 21-fakeip.json) на новую пару слотов.
type LegacySlotSplit struct {
	// Shared — содержимое общего слота 21-routing.json.
	Shared []byte
	// Mode — содержимое режимного слота (20-tproxy / 20-policytun / 20-fakeip).
	// nil при SharedOnly.
	Mode []byte
	// DNSRenames — пользовательские DNS-серверы, переименованные из
	// зарезервированных движком тегов. Пусто в подавляющем большинстве случаев.
	DNSRenames []DNSTagRename
	// Notes — готовые для журнала строки о правках, которые пользователю нужно
	// заметить: перецеленный или удалённый DNS-сервер. Пусто в штатном случае.
	Notes []string
}

// LegacySlotSplitParams — вход разбора.
type LegacySlotSplitParams struct {
	// Mode — режим, В КОТОРЫЙ идёт установка. От него зависит резолвер у
	// outbound'ов общего слота.
	Mode string
	// SourceIsFakeIP — разбирается 21-fakeip.json. Определяется ИМЕНЕМ файла, а
	// не режимом: DNS в нём — механизм fakeip-режима (серверы fakeip/real,
	// правила на них, dns.final), тогда как DNS в 20-router.json —
	// пользовательские серверы. На установке, где режим переключили, но движок
	// в новом режиме не поднимали, эти два случая расходятся.
	SourceIsFakeIP bool
	// SharedOnly — собрать только общий слот. Нужен там, где режимную часть
	// писать некуда: черновик staging (режимные слоты черновиков не имеют) и
	// запасной вариант, когда разбирается слот НЕ активного режима. В этом
	// случае движковый DNS из источника выбрасывается — иначе в общем слоте
	// повисли бы серверы, объявленные только режимом.
	SharedOnly bool
	// FallbackDNSResolver — тег DNS-сервера, на который можно перецелить
	// пользовательский сервер, потерявший свой резолвер вместе с режимным
	// слотом. Ожидается сервер базового слота (00-base.json), существующий
	// всегда; пустая строка означает «целить некуда» — тогда осиротевший
	// сервер удаляется, потому что висячая ссылка убивает sing-box на СТАРТЕ.
	FallbackDNSResolver string
}

// SplitLegacyRoutingSlot разбирает файл прежней раскладки на общий и режимный
// слоты. Живёт в пакете router, а не рядом с миграцией, потому что «что
// принадлежит общему слоту, а что режимному» уже описано генераторами
// (buildRoutingSlot / stripSharedFromModeSlot) и предикатами системных правил —
// второе описание тех же границ разъехалось бы с первым на первой же правке.
//
// Границы разбора:
//   - общий слот получает пользовательские правила, наборы, outbound'ы,
//     route.final, разметку WAN и (для НЕ-fakeip режимов) весь DNS-блок;
//   - режимный — инбаунды захвата, ведущие системные правила и режимную
//     специфику fakeip (серверы fakeip/real, DNS-правила режима,
//     default_domain_resolver, experimental.cache_file).
//
// Системные правила В ОБЩИЙ СЛОТ НЕ ПОПАДАЮТ: их пишет режимный генератор, и
// после первого же Reconcile в merged-конфиге оказалось бы два hijack-dns.
func SplitLegacyRoutingSlot(data []byte, p LegacySlotSplitParams) (LegacySlotSplit, error) {
	shared, err := parseRouterConfigBytes(data)
	if err != nil {
		return LegacySlotSplit{}, err
	}

	system, user := splitLegacyRouteRules(shared.Route.Rules)

	// --- общий слот ---
	shared.Route.Rules = user
	var notes []string
	switch {
	case p.SourceIsFakeIP && !p.SharedOnly:
		// DNS fakeip-режима — это и есть механизм режима; он остаётся в
		// режимном слоте целиком (решение подэтапа 5D0). Копия в общем слоте
		// дала бы duplicate-dns на теги fakeip/real.
		shared.DNS = DNS{Servers: []DNSServer{}, Rules: []DNSRule{}}
	case p.SourceIsFakeIP:
		// Режимного слота не будет, а значит серверов fakeip/real не объявит
		// никто: выбрасываем движковую часть DNS, пользовательскую оставляем —
		// иначе собственные DNS-серверы пользователя пропали бы вместе с ней.
		notes = append(notes, dropEngineDNS(shared)...)
	}
	// Порядок именно такой: сначала выбросить движковые серверы, потом лечить
	// пользовательские. Наоборот было бы хуже — переименование увело бы
	// движковый `fakeip` в `fakeip-user`, dropEngineDNS его бы уже не узнал, и
	// в конфиге без tun-инбаунда остался бы сервер, раздающий синтетические
	// адреса. На ветке с dropEngineDNS переименовывать после него нечего, и это
	// правильно: теги real/fakeip в 21-fakeip.json принадлежат движку, а не
	// пользователю.
	renames := renameReservedDNSServers(shared)
	// WAN берём из самого файла — EnsureRouteWAN внутри перепишет поля теми же
	// значениями, то есть сохранит выбор пользователя.
	buildRoutingSlot(shared, RoutingSlotParams{
		Mode:          p.Mode,
		WANAutoDetect: shared.Route.AutoDetectInterface != nil && *shared.Route.AutoDetectInterface,
		WANInterface:  shared.Route.DefaultInterface,
	})
	if p.SharedOnly {
		// Режимный слот не пишется — объявить движковый резолвер `real` некому.
		// Чинить обязательно и у outbound'ов, и у DNS-серверов: висячая ссылка
		// убивает sing-box (у outbound'а — сразу на check, у DNS-сервера —
		// только на СТАРТЕ, `check` её пропускает). Сюда же попадает резолвер,
		// который только что поставил buildRoutingSlot hostname-outbound'ам,
		// когда установка идёт в fakeip-режим.
		//
		// БЕЗУСЛОВНЫМ вызов делать нельзя, и цена ошибки — пользовательские
		// данные. healDanglingDomainResolvers считает ссылку живой, только
		// если тег есть в shared.DNS.Servers ЛИБО равен единственному внешнему
		// тегу, который ему передают, — FallbackDNSResolver (dns-bootstrap).
		// А базовый слот объявляет не только его: 00-base.json владеет ещё и
		// `dns-doh` (см. filterOutOurDNSServers в operator_baseconfig.go).
		// На пути, где режимный слот ПИШЕТСЯ (20-router.json → tproxy или
		// policy-tun), весь пользовательский DNS уезжает в общий слот, и
		// сервер с законным `domain_resolver: dns-doh` был бы объявлен
		// висячим: резолвер молча перецелен, а сервер, заданный именем хоста
		// и оставшийся без цели, — удалён. При обычном апгрейде, на ровном
		// месте.
		//
		// Попутно тот же обход снял бы `domain_resolver: real` с
		// hostname-outbound'ов, который только что поставил buildRoutingSlot:
		// эту ссылку объявляет режимный слот, а он этой функции не виден.
		// Сторож — TestMigrateSlotsSplitKeepsFakeIPOutboundResolver.
		notes = append(notes, healDanglingDomainResolvers(shared, p.FallbackDNSResolver)...)
	}
	sharedRaw, err := json.MarshalIndent(shared, "", "  ")
	if err != nil {
		return LegacySlotSplit{}, fmt.Errorf("marshal routing slot: %w", err)
	}
	if p.SharedOnly {
		return LegacySlotSplit{Shared: sharedRaw, DNSRenames: renames, Notes: notes}, nil
	}

	// --- режимный слот ---
	// Второй разбор вместо копии структуры: RouterConfig держит указатели
	// (*bool, *DomainResolver, *Experimental) и вложенные слайсы, поэтому
	// поверхностная копия дала бы двум слотам общие ячейки памяти.
	modeCfg, err := parseRouterConfigBytes(data)
	if err != nil {
		return LegacySlotSplit{}, err
	}
	stripSharedFromModeSlot(modeCfg)
	modeCfg.Route.Rules = system
	// Разметка WAN — скаляры общего слота; в режимном они перебивали бы её
	// (режимный файл сливается первым).
	modeCfg.Route.AutoDetectInterface = nil
	modeCfg.Route.DefaultInterface = ""
	if !p.SourceIsFakeIP {
		// Вне fakeip DNS целиком уехал в общий слот: копия здесь = duplicate-dns.
		// experimental.cache_file и default_domain_resolver вне fakeip — мусор
		// прежней раскладки (ссылка на несуществующий резолвер роняет sing-box).
		modeCfg.DNS = DNS{}
		modeCfg.Experimental = nil
		modeCfg.Route.DefaultDomainResolver = nil
	}
	modeRaw, err := json.MarshalIndent(modeCfg, "", "  ")
	if err != nil {
		return LegacySlotSplit{}, fmt.Errorf("marshal mode slot: %w", err)
	}
	return LegacySlotSplit{Shared: sharedRaw, Mode: modeRaw, DNSRenames: renames, Notes: notes}, nil
}

// healDanglingDomainResolvers чинит ссылки domain_resolver, указывающие в
// пустоту после того, как режимный слот перестал объявлять свои DNS-серверы.
//
// Поведение настоящего бинаря (проверено `check` И `run` — они расходятся, и
// это единственная причина, по которой функция выглядит именно так):
//
//	outbound → несуществующий тег          check: FATAL          run: FATAL
//	DNS-сервер → несуществующий тег        check: OK (!)         run: FATAL
//	                                       «start service: dependency[real]
//	                                        not found for server[user-dns]»
//	DNS-сервер с ИМЕНЕМ хоста без резолвера check: FATAL         —
//	outbound с именем хоста без резолвера   check: OK            run: OK
//
// Наш валидатор гоняет `check`, поэтому висячая ссылка у DNS-сервера прошла бы
// валидацию и убила движок на старте. Оставлять её нельзя ни в каком виде:
//
//   - у outbound'а и в route.default_domain_resolver ссылка просто снимается —
//     там резолвер необязателен;
//   - у DNS-сервера с АДРЕСОМ тоже снимается — резолвер ему не нужен;
//   - у DNS-сервера с ИМЕНЕМ хоста снять нельзя (тогда FATAL про missing domain
//     resolver), поэтому ссылка ПЕРЕЦЕЛИВАЕТСЯ на уцелевший резолвер: сначала
//     на пользовательский сервер, заданный адресом, потом на fallback базового
//     слота. Если целить некуда — сервер удаляется (с записью в журнал):
//     потерять один резолвер лучше, чем не поднять движок.
//
// Обход до фикс-пойнта: удаление сервера снимает ссылки на него у соседей
// (DeleteDNSServer), и осиротеть может уже они.
func healDanglingDomainResolvers(cfg *RouterConfig, fallbackResolver string) []string {
	var notes []string
	resolvable := func(tag string) bool {
		if tag == "" {
			return false
		}
		if tag == fallbackResolver {
			return true
		}
		for _, s := range cfg.DNS.Servers {
			if s.Tag == tag {
				return true
			}
		}
		return false
	}
	// route.default_domain_resolver здесь не проверяется: единственный вызов
	// идёт ПОСЛЕ buildRoutingSlot, а тот через stripModeOnlyFromRoutingSlot
	// обнуляет поле безусловно (в общем слоте оно всё равно инертно — скаляр
	// 00-base.json перебивает его при слиянии first-file-wins). Ветка была
	// мёртвой; сторож на висячую ссылку в этом поле — статический ассерт
	// assertNoDanglingDNSRefs в тестах миграции, а не код здесь.
	for i := range cfg.Outbounds {
		if r := cfg.Outbounds[i].DomainResolver; r != nil && !resolvable(r.Server) {
			cfg.Outbounds[i].DomainResolver = nil
		}
	}
	for {
		var orphaned []string
		for i := range cfg.DNS.Servers {
			s := &cfg.DNS.Servers[i]
			hasResolver := s.DomainResolver != nil && resolvable(s.DomainResolver.Server)
			if hasResolver {
				continue
			}
			if !isHostname(s.Server) {
				// Резолвер не нужен — снимаем висячую ссылку, если она была.
				s.DomainResolver = nil
				continue
			}
			target := pickDomainResolverFor(cfg, s.Tag, fallbackResolver)
			if target == "" {
				orphaned = append(orphaned, s.Tag)
				continue
			}
			s.DomainResolver = &DomainResolver{Server: target}
			notes = append(notes, fmt.Sprintf(
				"DNS-сервер %q задан именем хоста, а его резолвер объявлял режимный слот — резолвер переключён на %q",
				s.Tag, target))
		}
		if len(orphaned) == 0 {
			return notes
		}
		for _, tag := range orphaned {
			_ = cfg.DeleteDNSServer(tag, true)
			notes = append(notes, fmt.Sprintf(
				"DNS-сервер %q удалён: он задан именем хоста, а разрешать это имя стало некому — с висячей ссылкой sing-box не стартует",
				tag))
		}
	}
}

// pickDomainResolverFor выбирает, кем разрешать имя хоста DNS-сервера self:
// первым пользовательским сервером, заданным АДРЕСОМ (такому самому резолвер не
// нужен, цепочки не выйдет), иначе резолвером базового слота.
func pickDomainResolverFor(cfg *RouterConfig, self, fallbackResolver string) string {
	for _, s := range cfg.DNS.Servers {
		if s.Tag == self || s.Tag == "" || engineDNSServerTags[s.Tag] {
			continue
		}
		if !isHostname(s.Server) {
			return s.Tag
		}
	}
	return fallbackResolver
}

// dropEngineDNS выбрасывает из конфига DNS движка fakeip-режима: серверы с
// движковыми тегами, правила, которые на них ссылаются, и режимные скаляры.
// Пользовательские серверы и правила остаются — это их данные.
//
// Возвращает строки для журнала. Правило, ссылающееся на движковый сервер,
// уходит целиком — вместе с сужением, которое пользователь мог задать в нём
// руками (набор правил, адрес источника). Сохранить его нельзя: сервера, на
// который оно светит, в этой раскладке не существует. Молчать — тоже нельзя:
// со стороны это выглядит как пропавшая настройка.
func dropEngineDNS(cfg *RouterConfig) []string {
	servers := make([]DNSServer, 0, len(cfg.DNS.Servers))
	for _, s := range cfg.DNS.Servers {
		if engineDNSServerTags[s.Tag] {
			continue
		}
		servers = append(servers, s)
	}
	cfg.DNS.Servers = servers
	rules := make([]DNSRule, 0, len(cfg.DNS.Rules))
	dropped := 0
	for _, r := range cfg.DNS.Rules {
		if engineDNSServerTags[r.Server] {
			dropped++
			continue
		}
		rules = append(rules, r)
	}
	cfg.DNS.Rules = rules
	var notes []string
	if dropped > 0 {
		notes = append(notes, fmt.Sprintf(
			"DNS-правил режима fakeip отброшено: %d — они направляли запросы на движковые серверы, которых в этой раскладке нет; заданные в них вручную ограничения (набор правил, адрес источника) не сохранены",
			dropped))
	}
	if engineDNSServerTags[cfg.DNS.Final] {
		// dns.final — пользовательская настройка «куда идут запросы, не
		// подошедшие ни под одно правило». Она указывала на движковый сервер и
		// снимается вместе с ним; дальше запросы идут к первому серверу списка.
		notes = append(notes, fmt.Sprintf(
			"итоговый DNS-сервер (dns.final) был движковым (%q) и снят вместе с ним — задайте его заново, если он был выбран осознанно",
			cfg.DNS.Final))
		cfg.DNS.Final = ""
	}
	// Ссылки на удалённые серверы чинит healDanglingDomainResolvers — он
	// зовётся ПОСЛЕ buildRoutingSlot, который сам может навесить движковый
	// резолвер на outbound'ы.
	return notes
}

// splitLegacyRouteRules делит route.rules прежнего слота на режимный префикс и
// пользовательский хвост. Режимными считаются ТОЛЬКО ведущие правила (как их
// ставит EnsureSystemRules) — правило в середине списка пользователь завёл сам,
// и переносить его в режимный слот, который перегенерируется из настроек,
// значило бы его потерять.
func splitLegacyRouteRules(rules []Rule) (system, user []Rule) {
	head := true
	for _, r := range rules {
		if head && isMigratableSystemRule(r) {
			system = append(system, r)
			continue
		}
		head = false
		user = append(user, r)
	}
	return system, user
}

// isMigratableSystemRule — системное правило В ТОЧНО ТОЙ ФОРМЕ, в которой его
// порождает режимный генератор. Для обхода приватных адресов форма проверяется
// строже, чем в isSystemPrivateRule: пользователь мог направить приватные сети
// в свой LAN-выход, и такое правило — его данные, а не наш префикс.
func isMigratableSystemRule(r Rule) bool {
	switch {
	case isSystemSniffRule(r), isSystemHijackRule(r), isSystemUDPTimeoutRule(r):
		return true
	case isSystemPrivateRule(r):
		return r.Outbound == "direct" && r.Action == "" && len(r.DomainSuffix) == 0 &&
			len(r.Domain) == 0 && len(r.IPCIDR) == 0 && len(r.SourceIPCIDR) == 0 &&
			len(r.Port) == 0 && len(r.RuleSet) == 0 && len(r.Inbound) == 0
	}
	return false
}

// renameReservedDNSServers уводит пользовательский DNS-сервер с движкового тега
// (`real` / `fakeip`) на свободное имя и переписывает ВСЕ ссылки на него.
//
// Зачем: запрет на эти теги (checkDNSServerTagReserved) закрывает только новые
// установки. Сервер, заведённый до этой версии, после миграции оказывается в
// общем слоте, активном во всех режимах; переключение в fakeip добавляет
// движковый сервер с тем же тегом в режимный слот, кросс-слот валидатор отдаёт
// duplicate-dns и reload блокируется НАВСЕГДА — движок молча работает на старом
// конфиге, а пользователь видит «применено» и нулевой эффект.
func renameReservedDNSServers(cfg *RouterConfig) []DNSTagRename {
	var renames []DNSTagRename
	for i := range cfg.DNS.Servers {
		oldTag := cfg.DNS.Servers[i].Tag
		if !engineDNSServerTags[oldTag] {
			continue
		}
		newTag := freeDNSServerTag(cfg, oldTag+"-user")
		cfg.DNS.Servers[i].Tag = newTag
		cfg.renameDNSServerReferences(oldTag, newTag)
		// renameDNSServerReferences знает только про DNS-блок; ссылки из
		// route живут отдельно и без них тег повис бы.
		if cfg.Route.DefaultDomainResolver != nil && cfg.Route.DefaultDomainResolver.Server == oldTag {
			cfg.Route.DefaultDomainResolver.Server = newTag
		}
		for j := range cfg.Outbounds {
			if cfg.Outbounds[j].DomainResolver != nil && cfg.Outbounds[j].DomainResolver.Server == oldTag {
				cfg.Outbounds[j].DomainResolver.Server = newTag
			}
		}
		renames = append(renames, DNSTagRename{From: oldTag, To: newTag})
	}
	return renames
}

// freeDNSServerTag подбирает незанятое имя, начиная с want.
func freeDNSServerTag(cfg *RouterConfig, want string) string {
	taken := make(map[string]bool, len(cfg.DNS.Servers))
	for _, s := range cfg.DNS.Servers {
		taken[s.Tag] = true
	}
	if !taken[want] && !engineDNSServerTags[want] {
		return want
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", want, n)
		if !taken[candidate] && !engineDNSServerTags[candidate] {
			return candidate
		}
	}
}
