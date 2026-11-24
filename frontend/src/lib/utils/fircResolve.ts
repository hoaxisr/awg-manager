// DNS-серверы группы FIRC (resolve.server) и группы «только DNS». Правила те же, что у
// fircd и internal/firc/resolve_check.go; пул подставных адресов проверяет только сервер.
import { m } from '$lib/i18n';
import { isIPv4, isIPv6 } from '$lib/utils/cidr';
import type { FircGroup, FircInterfaceOption } from '$lib/types/firc';

/** Псевдо-интерфейс группы «только DNS». */
export const DNS_ONLY_IFACE = 'dns';
export const MAX_RESOLVE_SERVERS = 4;
const MAX_URL_BYTES = 255;

export const DOH_PRESETS: { name: string; url: string }[] = [
	{ name: 'Cloudflare', url: 'https://1.1.1.1/dns-query' },
	{ name: 'Google', url: 'https://dns.google/dns-query' },
	{ name: 'Quad9', url: 'https://9.9.9.9/dns-query' },
	{ name: 'AdGuard', url: 'https://dns.adguard-dns.com/dns-query' },
];

/** fircd умеет DoH и группы «только DNS»: старый демон псевдо-интерфейс dns не отдаёт. */
export function supportsDnsOnly(interfaces: FircInterfaceOption[]): boolean {
	return interfaces.some((i) => i.id === DNS_ONLY_IFACE);
}

export function isDnsOnlyGroup(group: Pick<FircGroup, 'interface'>): boolean {
	const iface = Array.isArray(group.interface) ? group.interface : [group.interface];
	return iface.length === 1 && iface[0] === DNS_ONLY_IFACE;
}

export function isSubnetRuleType(type: string): boolean {
	return type === 'subnet' || type === 'subnet6';
}

/** Список серверов из resolve.server, без пустых записей. */
export function splitServers(server: string | undefined): string[] {
	return (server ?? '').split(',').map((s) => s.trim()).filter(Boolean);
}

export function isDohUrl(server: string): boolean {
	return server.includes('://');
}

/** Для бейджей: у адреса DoH только хост, целиком он показывается в подсказке. */
export function shortServer(server: string): string {
	return isDohUrl(server) ? (server.match(/^[a-z]+:\/\/([^/]+)/i)?.[1] ?? server) : server;
}

interface ParsedServer {
	host: string;
	port: string;
	bracketed: boolean;
}

function splitHostPort(s: string): ParsedServer | null {
	const v6 = s.match(/^\[([^\]]*)\](?::(.+))?$/);
	if (v6) return { host: v6[1], port: v6[2] ?? '', bracketed: true };
	const colons = s.split(':').length - 1;
	if (s.startsWith('[')) return null;
	if (colons !== 1) return { host: s, port: '', bracketed: false };
	const [host, port] = s.split(':');
	return port ? { host, port, bracketed: false } : null;
}

function validPort(port: string): boolean {
	return /^\d+$/.test(port) && Number(port) >= 1 && Number(port) <= 65535;
}

/** Адрес, с которого резолвер ответить не может: 0.0.0.0, 127/8, ::, ::1, ::ffff:x. */
function isSinkAddress(ip: string): boolean {
	return ip === '0.0.0.0' || (isIPv4(ip) && ip.startsWith('127.')) ||
		(ip.includes(':') && (/^[0:]+1?$/.test(ip) || /^::ffff:/i.test(ip)));
}

/** Ошибка одной записи списка или пустая строка. allowDoh — демон понимает адреса DoH. */
export function serverError(server: string, allowDoh = true): string {
	const s = server.trim();
	if (!isDohUrl(s)) {
		const p = splitHostPort(s);
		if (!p) return m.firc_resolve_err_addr();
		if (isSinkAddress(p.host)) return m.firc_resolve_err_sink();
		if (!(isIPv6(p.host) || (!p.bracketed && isIPv4(p.host)))) return m.firc_resolve_err_addr();
		return p.port && (!validPort(p.port) || p.port.length > 5) ? m.firc_resolve_err_port() : '';
	}
	if (!allowDoh) return m.firc_resolve_err_doh_unsupported();
	const url = s.match(/^https:\/\/([^/]+)\/(.*)$/i);
	if (!url || /[\x00-\x20\x7f?#]/.test(s)) return m.firc_resolve_err_url();
	if (new TextEncoder().encode(s).length > MAX_URL_BYTES) return m.firc_resolve_err_url_long();
	const p = splitHostPort(url[1]);
	if (!p || !p.host || /[@[\]]/.test(p.host)) return m.firc_resolve_err_url();
	if (isSinkAddress(p.host)) return m.firc_resolve_err_sink();
	if (p.bracketed ? !isIPv6(p.host) : p.host.includes(':')) return m.firc_resolve_err_url();
	return p.port && !validPort(p.port) ? m.firc_resolve_err_port() : '';
}

function serverKey(server: string): string {
	const s = server.trim();
	const p = isDohUrl(s) ? null : splitHostPort(s);
	return p ? `${p.host.toLowerCase()} ${Number(p.port || 53)}` : s;
}

/** Повтор уже указанного выше сервера. */
export function isDuplicateServer(servers: string[], index: number): boolean {
	const key = serverKey(servers[index]);
	return servers.some((s, i) => i < index && s.trim() !== '' && serverKey(s) === key);
}

/** Почему группу не сохранить — или пустая строка. */
export function groupResolveError(group: FircGroup, allowDoh = true): string {
	const servers = splitServers(group.resolve?.server);
	if (servers.length > MAX_RESOLVE_SERVERS) return m.firc_resolve_err_too_many({ max: MAX_RESOLVE_SERVERS });
	for (const [i, s] of servers.entries()) {
		const err = serverError(s, allowDoh);
		if (err) return `${s}: ${err}`;
		if (isDuplicateServer(servers, i)) return `${s}: ${m.firc_resolve_err_duplicate()}`;
	}
	if (!isDnsOnlyGroup(group)) return '';
	if (servers.length === 0) return m.firc_dns_only_server_required();
	return group.rules.some((r) => isSubnetRuleType(r.type)) ? m.firc_dns_only_no_subnets() : '';
}

/** Почему группе нельзя назначить выход «Только DNS» — или пустая строка. */
export function dnsOnlyBlocker(group: FircGroup, interfaces: FircInterfaceOption[]): string {
	if (isDnsOnlyGroup(group)) return '';
	if (!supportsDnsOnly(interfaces)) return m.firc_dns_only_need_update();
	if (splitServers(group.resolve?.server).length === 0) return m.firc_dns_only_need_server();
	return group.rules.some((r) => isSubnetRuleType(r.type)) ? m.firc_dns_only_no_subnets() : '';
}

/** Приводит группу к её выходу: у «только DNS» нет туннеля, Cloudflare и проверки, у прочих — запасного DNS. */
export function fitGroupToOutbound(group: FircGroup) {
	const server = group.resolve?.server ?? '';
	if (isDnsOnlyGroup(group)) {
		group.resolve = { server, fallback: group.resolve?.fallback ?? true };
		group.cloudflare = undefined;
		if (group.check) group.check = { ...group.check, mode: 'none' };
	} else if (group.resolve) {
		group.resolve = { tunnel: group.resolve.tunnel ?? true, server };
	}
}
