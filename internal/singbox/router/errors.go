package router

import "errors"

var (
	ErrNetfilterComponentMissing = errors.New("kernel module xt_TPROXY.ko not found: install the router firmware 'Netfilter kernel modules' component")
	ErrIPTablesModTProxyMissing  = errors.New("iptables-mod-tproxy package not installed")
	ErrRuleSetReferenced         = errors.New("rule set is referenced by one or more rules")
	ErrOutboundReferenced        = errors.New("outbound is referenced by one or more rules")
	ErrInvalidMatchers           = errors.New("rule must have at least one matcher")
	ErrRuleIndexOutOfRange       = errors.New("rule index out of range")
	// ErrBulkEmptyIndices / ErrBulkEmptyTags reject an empty selection passed
	// to a bulk rule/ruleset mutation (nothing to do — client error). Mapped
	// to 400 by the API.
	ErrBulkEmptyIndices = errors.New("empty indices")
	ErrBulkEmptyTags    = errors.New("empty tags")
	// ErrBulkInvalidSelection rejects a non-empty bulk selection that is
	// itself invalid (duplicate index/tag, a rule that isn't a route rule,
	// an unknown outbound tag, or a rule set that isn't type=remote) — a
	// client error just like the empty-selection cases above. Mapped to 400
	// by the API.
	ErrBulkInvalidSelection = errors.New("invalid bulk selection")
	ErrRuleSetTagConflict   = errors.New("rule set with this tag already exists")
	ErrRuleSetNotFound      = errors.New("rule set not found")
	// ErrRuleSetTagUnsafe — тег inline-набора не совпадает со своим именем
	// файла артефакта (F434, #941). Ошибка ввода, API отвечает 400.
	ErrRuleSetTagUnsafe    = errors.New("inline rule set tag must equal its artifact file name")
	ErrDatRuleSetForbidden = errors.New("dat rule set token is invalid")
	ErrOutboundTagConflict = errors.New("outbound with this tag already exists")
	// ErrCompositeMemberUnknown — член selector/urltest ссылается на несуществующий выход (#567).
	ErrCompositeMemberUnknown = errors.New("неизвестный член композита")
	// ErrOutboundInvalid — форма outbound отвергнута validateOutbound (тип,
	// участники, direct в urltest, ссылка на себя). Ввод, не сбой: API — 400.
	ErrOutboundInvalid          = errors.New("некорректный outbound")
	ErrOutboundNotFound         = errors.New("outbound not found")
	ErrDNSServerTagConflict     = errors.New("dns server with this tag already exists")
	ErrDNSServerReferenced      = errors.New("dns server is referenced by one or more dns rules or used as final/default")
	ErrDNSServerNotFound        = errors.New("dns server not found")
	ErrDNSRuleIndexOutOfRange   = errors.New("dns rule index out of range")
	ErrDNSServerIndexOutOfRange = errors.New("dns server index out of range")
	ErrDNSInvalidServer         = errors.New("dns rule references unknown server tag")
	// ErrDNSServerDetour — detour сервера отвергнут SetDNSServerDetour:
	// неизвестный outbound или сервер, который никуда не подключается. Ввод,
	// не сбой.
	ErrDNSServerDetour = errors.New("недопустимый detour DNS-сервера")

	// ErrIfaceAbsent — интерфейса нет в свежем списке NDMS; команда по нему не
	// уходила (адаптеры подтверждения cmd/awg-manager, F546).
	ErrIfaceAbsent = errors.New("нет в NDMS")
	// ErrWANAbsent — выхода static NAT нет в свежем списке NDMS; команда не
	// уходила. Не ErrIfaceAbsent: включение отвечает ошибкой, восстановление —
	// «возвращать не на что» (F563).
	ErrWANAbsent = errors.New("выхода нет в NDMS")

	ErrPolicyNotConfigured = errors.New("router policy not configured (settings.policyName is empty)")
	ErrPolicyMissing       = errors.New("policy has no fwmark in NDMS (deleted or has no permitted interface)")

	// ErrSingboxNotReady is returned by enable when sing-box did not
	// become ready within the boot-wait window. Callers should surface
	// this as 503 Service Unavailable — the iptables/policy install was
	// deliberately skipped to avoid orphaning DNS:53 redirects at a
	// torn-down sing-box port (issue #221).
	ErrSingboxNotReady = errors.New("sing-box did not become ready within boot-wait window — iptables install skipped")

	// ErrFakeIPLockedField is returned when a fakeip-tun config edit collides with
	// an engine-locked field (the fakeip/real DNS servers, dns.final,
	// default_domain_resolver, or the hijack-dns rule). Surfaced as 4xx.
	ErrFakeIPLockedField = errors.New("fakeip-tun config field is engine-locked")

	// ErrFakeIPRealServerInvalid rejects an edit of the "real" DNS server whose
	// new upstream is not a plain IP address (the fakeip topology resolves every
	// domain through "real" itself, so a domain upstream could never bootstrap).
	// Mapped to 400 by the API.
	ErrFakeIPRealServerInvalid = errors.New("upstream of dns server \"real\" must be an IP address")

	// ErrQoSClassesInvalid wraps every QoS-class validation failure from
	// NormalizeSingboxRouterSettings (DSCP out of 0-63, duplicate DSCP,
	// class limit exceeded, empty outbound, name too long) and the
	// UpdateSettings outbound-existence check so the API can map them to
	// 400 with the detailed Russian message intact.
	ErrQoSClassesInvalid = errors.New("некорректные классы QoS")

	// ErrIP6TablesMissing: в настройках выбран перехват IPv6, а ip6tables на
	// роутере нет (в Entware это отдельный пакет). Mapped to 400 by the API.
	ErrIP6TablesMissing = errors.New("для перехвата IPv6 нужен пакет ip6tables: opkg install ip6tables")

	// ErrIPv6BypassInactive: в исключениях по подсетям есть IPv6-адреса, а
	// v6-слой при этих настройках не работает (режим не TProxy или семейство
	// без IPv6) — правило под них поставить некому. Mapped to 400 by the API.
	ErrIPv6BypassInactive = errors.New("IPv6-адреса в исключениях по подсетям действуют только при перехвате IPv6 (режим TProxy, семейства «IPv4 и IPv6» или «Только IPv6»)")

	// ErrTransitionInProgress: PUT настроек лёг в окно живого SwitchRoutingMode.
	// Save отсюда откатил бы persistMode(target), и Enable перехода поднялся бы
	// по чужому режиму. Не ждём (переход держит transitionMu минуты) — честно
	// отказываем, клиент повторит после завершения перехода.
	ErrTransitionInProgress = errors.New("смена режима роутинга выполняется — повторите после её завершения")

	// ErrTunAddrLocked rejects a fakeipTunAddr4 change while a tun mode
	// (fakeip-tun / policy-tun) holds a provisioned OpkgTun: the address is
	// applied only on enable.
	ErrTunAddrLocked = errors.New("адрес tun меняется только при выключенном режиме")

	// ErrDNSChainTagReserved rejects a user DNS rule that claims (or references)
	// the awgm-dns-* tag namespace owned by the DNS-preset overlay: such a rule
	// would be wiped by the next ensureDNSChainOverlay. Mapped to 400 by the API.
	ErrDNSChainTagReserved = errors.New("dns rule: теги awgm-dns-* зарезервированы для DNS-пресета")

	// ErrDNSRuleManaged rejects an update/delete/move of a DNS rule that belongs
	// to the DNS-preset overlay: the preset owns those rules and re-asserts them
	// on every write, so a manual edit could never stick. Mapped to 400 by the API.
	ErrDNSRuleManaged = errors.New("dns rule: правило управляется DNS-пресетом")

	// ErrReservedInboundTag rejects user route rules whose inbound matcher
	// references the reserved tproxy-qos-*/redirect-qos-* namespace. The
	// managed QoS rules live in 18-qos-routes.json and merge BEFORE the user
	// slot, so such a user rule could never match — it would only sit in the
	// UI as an inert, confusing shadow rule. Mapped to 400 by the API.
	ErrReservedInboundTag = errors.New("теги qos-* зарезервированы для QoS-классов")

	// ErrBindNotSelectable — bind_interface вне каталога выбираемых
	// интерфейсов: ошибка ввода (400), в отличие от сбоя чтения каталога
	// (500) (F429).
	ErrBindNotSelectable = errors.New("bind_interface is not a selectable interface")

	// ErrDNSBindAddress — адрес отправителя DNS-сервера неприменим: не IPv4,
	// не назначен роутеру или задан там, где sing-box его не использует.
	// Ошибка ввода (400); сбой чтения адресов роутера идёт мимо него (500).
	ErrDNSBindAddress = errors.New("адрес отправителя DNS-сервера неприменим")
)
