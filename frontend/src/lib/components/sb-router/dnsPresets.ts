import type { SingboxRouterDNSServer } from '$lib/types';

/** Транспорт выходного DNS в простом режиме. */
export type DnsPresetProto = 'udp' | 'dot' | 'doh';

export interface DnsPreset {
	id: string;
	label: string;
	/** IPv4-литерал. Подключаемся по нему, имя идёт только в SNI — bootstrap-резолв не нужен. */
	ip: string;
	/** Имя для SNI и проверки сертификата (DoT/DoH). */
	sni: string;
	path: string;
	/** Провайдер фильтрует ответы — подписывается в списке. */
	note?: string;
}

/**
 * Пары IP + SNI подтверждены живыми запросами (openssl s_client, dig +tls,
 * curl --resolve): сертификат проверяется, DoT отдаёт NOERROR, DoH — 200.
 *
 * Гочи, из-за которых значения нельзя «поправить по памяти»:
 *   - у Яндекса DoH живёт на том же хосте, что DoT: common.DOT.dns.yandex.net.
 *     Расхожий common.dns.yandex.net не существует — имя не резолвится и в
 *     сертификате его нет;
 *   - у Cloudflare профиль фильтрации выбирается ещё и через SNI: 1.1.1.1 с
 *     family.cloudflare-dns.com уже режет контент. Чистая пара — только
 *     cloudflare-dns.com (либо one.one.one.one);
 *   - у AdGuard legacy-имя dns-unfiltered.adguard.com отдаёт сертификат
 *     *.adguard.com вообще без IP в SAN — использовать нельзя.
 *
 * 9.9.9.9 и 94.140.14.14 — фильтрующие профили по умолчанию у своих
 * провайдеров; берём их (это те самые «девятки» из issue #560), но
 * подписываем note.
 */
export const DNS_PRESETS: readonly DnsPreset[] = [
	{ id: 'yandex', label: 'Яндекс', ip: '77.88.8.8', sni: 'common.dot.dns.yandex.net', path: '/dns-query' },
	{ id: 'quad9', label: 'Quad9', ip: '9.9.9.9', sni: 'dns.quad9.net', path: '/dns-query', note: 'блокирует malware' },
	{ id: 'cloudflare', label: 'Cloudflare', ip: '1.1.1.1', sni: 'cloudflare-dns.com', path: '/dns-query' },
	{ id: 'google', label: 'Google', ip: '8.8.8.8', sni: 'dns.google', path: '/dns-query' },
	{ id: 'adguard', label: 'AdGuard', ip: '94.140.14.14', sni: 'dns.adguard-dns.com', path: '/dns-query', note: 'блокирует рекламу' },
];

export function findDnsPresetByIp(ip: string): DnsPreset | undefined {
	const addr = ip.trim();
	return DNS_PRESETS.find((p) => p.ip === addr);
}

export function protoOfDnsServer(server: Pick<SingboxRouterDNSServer, 'type'>): DnsPresetProto {
	if (server.type === 'tls') return 'dot';
	if (server.type === 'https') return 'doh';
	return 'udp';
}

/**
 * Переход на UDP уничтожит содержательные TLS-настройки: блок tls на сервере
 * типа udp бэкенд отвергает (config_dns.go:158). server_name не считаем — он
 * перетирается при любой смене пресета.
 */
export function udpDropsTls(base: SingboxRouterDNSServer): boolean {
	const t = base.tls;
	if (!t) return false;
	return !!(
		t.certificate_public_key_sha256?.length
		|| t.alpn?.length
		|| t.insecure
		|| t.min_version
		|| t.max_version
	);
}

const PROTO_TYPE: Record<DnsPresetProto, SingboxRouterDNSServer['type']> = {
	udp: 'udp',
	dot: 'tls',
	doh: 'https',
};

/**
 * Собирает DTO DNS-сервера с нуля: из base переносится только то, что не
 * зависит от выбранного провайдера. path и server_port пересчитываются по
 * протоколу — иначе при DoH → UDP остался бы висеть /dns-query (бэкенд path
 * не валидирует вообще).
 */
export function buildDnsServer(
	base: SingboxRouterDNSServer,
	addr: string,
	sni: string,
	proto: DnsPresetProto,
): SingboxRouterDNSServer {
	const out: SingboxRouterDNSServer = {
		tag: base.tag,
		type: PROTO_TYPE[proto],
		server: addr.trim(),
	};
	if (base.detour) out.detour = base.detour;
	if (base.domain_strategy) out.domain_strategy = base.domain_strategy;
	if (base.domain_resolver) out.domain_resolver = { ...base.domain_resolver };

	if (proto === 'udp') return out;

	out.server_port = proto === 'dot' ? 853 : 443;
	if (proto === 'doh') out.path = '/dns-query';
	// Остальной TLS-хвост (пины, alpn, версии) сохраняем: терять пины
	// сертификата одним кликом в простом режиме — тихий даунгрейд.
	out.tls = { ...base.tls, server_name: sni.trim() };
	return out;
}
