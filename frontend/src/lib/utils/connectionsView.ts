import type { ConntrackConnection, RuleHit } from '$lib/types';
import { m } from '$lib/i18n';

/** Стабильный ключ соединения (в conntrack нет id). */
export function connKey(c: ConntrackConnection): string {
	return `${c.protocol}|${c.src}:${c.srcPort}|${c.dst}:${c.dstPort}`;
}

export interface ConnGroup {
	key: string;
	name: string;
	sub: string;
	rule?: RuleHit;
	conns: ConntrackConnection[];
	bytesIn: number;
	bytesOut: number;
}

/** Клиентская группировка текущей страницы (client: имя/срц, host: fqdn/dst). */
export function groupConnections(
	conns: ConntrackConnection[],
	mode: 'client' | 'host',
): ConnGroup[] {
	const map = new Map<string, ConnGroup>();
	for (const c of conns) {
		let key: string;
		let name: string;
		let sub: string;
		let rule: RuleHit | undefined;
		if (mode === 'client') {
			key = c.clientName || c.src;
			name = key;
			sub = c.clientName ? c.src : '';
		} else {
			const fqdn = c.rules?.[0]?.fqdn;
			key = fqdn || c.dst;
			name = key;
			sub = fqdn ? c.dst : '';
			rule = c.rules?.[0];
		}
		let g = map.get(key);
		if (!g) {
			g = { key, name, sub, rule, conns: [], bytesIn: 0, bytesOut: 0 };
			map.set(key, g);
		}
		g.conns.push(c);
		g.bytesIn += c.bytesIn;
		g.bytesOut += c.bytesOut;
	}
	return Array.from(map.values()).sort((a, b) => b.conns.length - a.conns.length);
}

export function dstFqdn(c: ConntrackConnection): string | undefined {
	return c.rules?.[0]?.fqdn || undefined;
}

export function routeLabel(c: ConntrackConnection): string {
	switch (c.routeClass) {
		case 'tunnel':
			return c.tunnelName;
		case 'singbox':
			return 'sing-box';
		case 'local':
			return m.diag_conn_filter_local();
		default:
			return c.interface || '—';
	}
}

export function routeVariant(c: ConntrackConnection): 'accent' | 'info' | 'muted' {
	if (c.routeClass === 'tunnel') return 'accent';
	if (c.routeClass === 'singbox') return 'info';
	return 'muted';
}

export function normProto(p: string): string {
	const low = p.toLowerCase();
	return low === 'icmpv6' ? 'icmp' : low;
}
