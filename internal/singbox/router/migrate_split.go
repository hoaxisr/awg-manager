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
		dropEngineDNS(shared)
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
		// Режимный слот не пишется — объявить движковый резолвер `real` некому,
		// а ссылка на него у outbound'а роняет sing-box целиком («FATAL
		// initialize outbound: domain resolver not found: real»). Снимаем её,
		// в том числе ту, которую только что поставил buildRoutingSlot
		// hostname-outbound'ам, когда установка идёт в fakeip-режим. Первый же
		// Enable/Reconcile соберёт режимный слот и вернёт резолвер на место.
		dropEngineDomainResolvers(shared)
	}
	sharedRaw, err := json.MarshalIndent(shared, "", "  ")
	if err != nil {
		return LegacySlotSplit{}, fmt.Errorf("marshal routing slot: %w", err)
	}
	if p.SharedOnly {
		return LegacySlotSplit{Shared: sharedRaw, DNSRenames: renames}, nil
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
	return LegacySlotSplit{Shared: sharedRaw, Mode: modeRaw, DNSRenames: renames}, nil
}

// dropEngineDomainResolvers снимает ссылки на движковые DNS-серверы там, где
// висячая ссылка ломает загрузку конфига: route.default_domain_resolver и
// outbound'ы. Проверено настоящим бинарём:
//
//	outbound с domain_resolver на несуществующий тег
//	  → FATAL initialize outbound[1]: domain resolver not found: real
//	DNS-сервер с той же висячей ссылкой
//	  → грузится, sing-box её терпит
//	DNS-сервер, заданный ИМЕНЕМ хоста, у которого резолвер сняли
//	  → FATAL initialize DNS server[0]: missing domain resolver for domain server address
//
// Отсюда правило для DNS-серверов: ссылку снимаем только у сервера с
// АДРЕСОМ — там резолвер декоративен. У сервера с именем хоста ссылка
// остаётся висячей осознанно: конфиг грузится (интернет у пользователя есть), а
// цена — ErrDNSServerNotFound при правке ИМЕННО ЭТОГО сервера в интерфейсе,
// пока он не выберет резолвер заново. Снять ссылку означало бы обменять правку
// в интерфейсе на не поднимающийся sing-box.
func dropEngineDomainResolvers(cfg *RouterConfig) {
	if cfg.Route.DefaultDomainResolver != nil && engineDNSServerTags[cfg.Route.DefaultDomainResolver.Server] {
		cfg.Route.DefaultDomainResolver = nil
	}
	for i := range cfg.Outbounds {
		if cfg.Outbounds[i].DomainResolver != nil && engineDNSServerTags[cfg.Outbounds[i].DomainResolver.Server] {
			cfg.Outbounds[i].DomainResolver = nil
		}
	}
	for i := range cfg.DNS.Servers {
		s := &cfg.DNS.Servers[i]
		if s.DomainResolver != nil && engineDNSServerTags[s.DomainResolver.Server] && !isHostname(s.Server) {
			s.DomainResolver = nil
		}
	}
}

// dropEngineDNS выбрасывает из конфига DNS движка fakeip-режима: серверы с
// движковыми тегами, правила, которые на них ссылаются, режимные скаляры и
// ссылки на движковые резолверы. Пользовательские серверы и правила остаются —
// это их данные.
func dropEngineDNS(cfg *RouterConfig) {
	servers := make([]DNSServer, 0, len(cfg.DNS.Servers))
	for _, s := range cfg.DNS.Servers {
		if engineDNSServerTags[s.Tag] {
			continue
		}
		servers = append(servers, s)
	}
	cfg.DNS.Servers = servers
	rules := make([]DNSRule, 0, len(cfg.DNS.Rules))
	for _, r := range cfg.DNS.Rules {
		if engineDNSServerTags[r.Server] {
			continue
		}
		rules = append(rules, r)
	}
	cfg.DNS.Rules = rules
	if engineDNSServerTags[cfg.DNS.Final] {
		cfg.DNS.Final = ""
	}
	dropEngineDomainResolvers(cfg)
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
