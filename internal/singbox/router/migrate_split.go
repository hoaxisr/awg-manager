package router

import (
	"encoding/json"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
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
	Mode []byte
	// DNSRenames — пользовательские DNS-серверы, переименованные из
	// зарезервированных движком тегов. Пусто в подавляющем большинстве случаев.
	DNSRenames []DNSTagRename
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
func SplitLegacyRoutingSlot(data []byte, mode string) (LegacySlotSplit, error) {
	fakeip := ModeSlot(mode) == orchestrator.SlotFakeIP

	shared, err := parseRouterConfigBytes(data)
	if err != nil {
		return LegacySlotSplit{}, err
	}
	// Второй разбор вместо копии структуры: RouterConfig держит указатели
	// (*bool, *DomainResolver, *Experimental) и вложенные слайсы, поэтому
	// поверхностная копия дала бы двум слотам общие ячейки памяти.
	modeCfg, err := parseRouterConfigBytes(data)
	if err != nil {
		return LegacySlotSplit{}, err
	}

	system, user := splitLegacyRouteRules(shared.Route.Rules)

	// --- общий слот ---
	shared.Route.Rules = user
	if fakeip {
		// DNS fakeip-режима — это и есть механизм режима; он остаётся в
		// режимном слоте целиком (решение подэтапа 5D0). Копия в общем слоте
		// дала бы duplicate-dns на теги fakeip/real.
		shared.DNS = DNS{Servers: []DNSServer{}, Rules: []DNSRule{}}
	}
	renames := renameReservedDNSServers(shared)
	// WAN берём из самого файла — EnsureRouteWAN внутри перепишет поля теми же
	// значениями, то есть сохранит выбор пользователя.
	buildRoutingSlot(shared, RoutingSlotParams{
		Mode:          mode,
		WANAutoDetect: shared.Route.AutoDetectInterface != nil && *shared.Route.AutoDetectInterface,
		WANInterface:  shared.Route.DefaultInterface,
	})

	// --- режимный слот ---
	stripSharedFromModeSlot(modeCfg)
	modeCfg.Route.Rules = system
	// Разметка WAN — скаляры общего слота; в режимном они перебивали бы её
	// (режимный файл сливается первым).
	modeCfg.Route.AutoDetectInterface = nil
	modeCfg.Route.DefaultInterface = ""
	if !fakeip {
		// Вне fakeip DNS целиком уехал в общий слот: копия здесь = duplicate-dns.
		// experimental.cache_file и default_domain_resolver вне fakeip — мусор
		// прежней раскладки (ссылка на несуществующий резолвер роняет sing-box).
		modeCfg.DNS = DNS{}
		modeCfg.Experimental = nil
		modeCfg.Route.DefaultDomainResolver = nil
	}

	sharedRaw, err := json.MarshalIndent(shared, "", "  ")
	if err != nil {
		return LegacySlotSplit{}, fmt.Errorf("marshal routing slot: %w", err)
	}
	modeRaw, err := json.MarshalIndent(modeCfg, "", "  ")
	if err != nil {
		return LegacySlotSplit{}, fmt.Errorf("marshal mode slot: %w", err)
	}
	return LegacySlotSplit{Shared: sharedRaw, Mode: modeRaw, DNSRenames: renames}, nil
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
