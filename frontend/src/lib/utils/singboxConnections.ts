// frontend/src/lib/utils/singboxConnections.ts

import type {
	ClashConnectionsRaw,
	Connection,
	ConnectionBucket,
	ConnectionFilters,
	ConnectionsSnapshot,
} from '$lib/types/singboxConnections';

const OUTBOUND_LABELS: Record<string, string> = {
	DIRECT: 'Прямое',
	REJECT: 'Отклонено',
};

export function chainOutboundLabel(chains: string[]): string {
	if (chains.length === 0) return '—';
	const first = chains[0];
	return OUTBOUND_LABELS[first] ?? first;
}

export function parseSnapshot(
	raw: ClashConnectionsRaw,
	clientsByIP: Map<string, string>,
): ConnectionsSnapshot {
	const rawConns = raw.connections ?? [];
	const connections: Connection[] = rawConns.map((c) => {
		const ip = c.metadata.sourceIP.toLowerCase();
		const clientName = clientsByIP.get(ip);
		return {
			...c,
			clientName,
			outboundLabel: chainOutboundLabel(c.chains),
		};
	});
	return {
		connections,
		downloadTotal: raw.downloadTotal ?? 0,
		uploadTotal: raw.uploadTotal ?? 0,
		connectionsTotal: connections.length,
	};
}

export function matchFilters(c: Connection, f: ConnectionFilters): boolean {
	if (f.network !== 'all' && c.metadata.network !== f.network) return false;
	if (f.outbound && (c.chains[0] ?? '') !== f.outbound) return false;
	if (f.rule && c.rule !== f.rule) return false;
	if (f.search) {
		const needle = f.search.toLowerCase();
		const hay = [
			c.metadata.host,
			c.metadata.sourceIP,
			c.metadata.destinationIP,
			c.clientName ?? '',
		]
			.join(' ')
			.toLowerCase();
		if (!hay.includes(needle)) return false;
	}
	return true;
}

export function aggregateBy(
	conns: Connection[],
	keyFn: (c: Connection) => string,
	labelFn: (c: Connection) => string = keyFn,
): ConnectionBucket[] {
	const acc = new Map<string, ConnectionBucket>();
	let totalDown = 0;
	for (const c of conns) {
		const k = keyFn(c);
		totalDown += c.download;
		const cur = acc.get(k) ?? { key: k, label: labelFn(c), upload: 0, download: 0, count: 0, pct: 0 };
		cur.upload += c.upload;
		cur.download += c.download;
		cur.count += 1;
		acc.set(k, cur);
	}
	const out = Array.from(acc.values());
	for (const b of out) {
		b.pct = totalDown > 0 ? Math.round((b.download / totalDown) * 100) : 0;
	}
	out.sort((a, b) => b.download - a.download);
	return out;
}
