import { m } from '$lib/i18n';
import type {
	AWGTagInfo,
	RouterInterface,
	SingboxRouterDNSServer,
	SingboxRouterDNSGlobals,
	SingboxTunnel,
	Subscription,
} from '$lib/types';

export interface AutolistTunnelOption {
	value: string;
	label: string;
	description?: string;
}

export interface AutolistDnsOption {
	value: string;
	label: string;
	description?: string;
	disabled?: boolean;
}

interface TunnelCatalogInput {
	awgTagInfos: AWGTagInfo[];
	singboxTunnels: SingboxTunnel[];
	subscriptions: Subscription[];
	interfaces: RouterInterface[];
	/** Текущее значение поля: остаётся видимым, даже если источник недоступен. */
	current: string;
}

/**
 * Список «Интерфейс туннеля»: каталог туннелей (те же три источника, что
 * у TunnelCatalog сателлита, awgm/catalog.go) плюс WAN из all-interfaces.
 * Managed AWG-туннели (opkgtun*) вырезаны из all-interfaces (isOwnTunnel),
 * поэтому приходят только из getAWGTags. Дедуп по kernel-имени интерфейса:
 * первый источник выигрывает, у дубликата забираем человеческое имя, если у
 * первого его не было.
 */
export function buildTunnelInterfaceOptions(input: TunnelCatalogInput): AutolistTunnelOption[] {
	/** Накопитель по kernel-имени; label — человеческое имя или ''. */
	const byValue = new Map<string, { value: string; label: string; description?: string }>();
	const add = (value: string, human: string, description?: string) => {
		const existing = byValue.get(value);
		if (!existing) byValue.set(value, { value, label: human, description });
		else if (!existing.label && human) existing.label = human;
	};
	const fmt = (value: string, human: string) => (human ? `${human} (${value})` : value);
	// Managed AWG: enabled-поля нет, берём все с непустым iface.
	for (const t of input.awgTagInfos) {
		if (!t.iface) continue;
		add(t.iface, t.label, m.autolist_cfg_target_kind_awg());
	}
	// Standalone sing-box: интерфейс существует, только пока процесс поднят.
	for (const t of input.singboxTunnels) {
		if (!t.running || !t.kernelInterface) continue;
		add(t.kernelInterface, t.tag, m.autolist_cfg_target_kind_singbox());
	}
	// Подписки: синтетический интерфейс t2s<proxyIndex>.
	for (const s of input.subscriptions) {
		if (!s.enabled || s.proxyIndex < 0) continue;
		add(`t2s${s.proxyIndex}`, s.label, m.autolist_cfg_target_kind_subscription());
	}
	// WAN/прочие из all-interfaces: LAN/bridge не предлагаем (private).
	for (const iface of input.interfaces) {
		if (!iface.up) continue;
		if (iface.securityLevel && iface.securityLevel !== 'public') continue;
		add(iface.name, iface.label, m.autolist_cfg_target_kind_wan());
	}
	const opts = [...byValue.values()].map((o) => ({ ...o, label: fmt(o.value, o.label) }));
	if (input.current && !byValue.has(input.current)) {
		opts.push({ value: input.current, label: input.current });
	}
	return opts;
}

interface DnsCatalogInput {
	dnsServers: SingboxRouterDNSServer[];
	dnsGlobals: SingboxRouterDNSGlobals | null;
	/** Текущее значение поля: неизвестное значение остаётся в списке. */
	current: string;
}

/** DNS для проверки покрытия: dns.final + поддерживаемые numeric-IP серверы. */
export function buildDnsOptions(input: DnsCatalogInput): AutolistDnsOption[] {
	const finalTag = input.dnsGlobals?.final ?? '';
	const opts: AutolistDnsOption[] = [];
	if (finalTag) {
		opts.push({ value: '', label: m.autolist_dns_final(), description: finalTag });
	}
	for (const server of input.dnsServers) {
		const supportedTransport = server.type === 'udp' || server.type === 'https' || server.type === 'tls';
		const disabled = !supportedTransport;
		const description = !supportedTransport
			? m.autolist_dns_unsupported()
			: server.detour
				? m.autolist_dns_detour()
				: `${server.type.toUpperCase()} · ${server.server}`;
		opts.push({ value: `sb:${server.tag}`, label: server.tag, description, disabled });
	}
	if (input.current && !opts.some((o) => o.value === input.current)) {
		opts.push({ value: input.current, label: input.current });
	}
	return opts;
}
