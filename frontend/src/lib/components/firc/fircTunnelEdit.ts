// Чистые помощники окна правки туннеля FIRC (FircTunnelEditModal и его вкладки).
import { m, formatLocale } from '$lib/i18n';
import {
	stripFircEmojis,
	type FircTunnel,
	type FircTunnelFilteredNode,
	type FircTunnelState,
	type FircTunnelPreview,
	type FircTunnelProbeRow,
	type FircTunnelSubUsage,
} from '$lib/types/firc';

export interface DisplayNode {
	key: string;
	name: string;
	source: string;
	state: 'active' | 'down' | 'reserve' | 'excluded' | 'missing' | 'skipped';
	skipReason?: string;
	v6?: boolean;
}

function formatSubBytes(bytes?: number): string {
	if (bytes === undefined || bytes < 0) return '—';
	const units = [
		m.firc_tunnel_edit_unit_b(),
		m.firc_tunnel_edit_unit_kb(),
		m.firc_tunnel_edit_unit_mb(),
		m.firc_tunnel_edit_unit_gb(),
		m.firc_tunnel_edit_unit_tb(),
	];
	if (bytes === 0) return `0 ${units[0]}`;
	let val = bytes;
	let idx = 0;
	while (val >= 1024 && idx < units.length - 1) {
		val /= 1024;
		idx++;
	}
	return `${val >= 10 || idx === 0 ? val.toFixed(0) : val.toFixed(1)} ${units[idx]}`;
}

export function formatSubUsage(usage?: FircTunnelSubUsage): string {
	if (!usage) return '';
	const parts: string[] = [];
	if (usage.used !== undefined || usage.total !== undefined) {
		const usedStr = formatSubBytes(usage.used ?? 0);
		const totalStr = usage.total ? formatSubBytes(usage.total) : '∞';
		parts.push(`${usedStr} / ${totalStr}`);
	}
	if (usage.expire && usage.expire > 0) {
		const d = new Date(usage.expire * 1000);
		parts.push(m.firc_tunnel_edit_sub_expires({ date: d.toLocaleDateString(formatLocale()) }));
	}
	return parts.join(' · ');
}

export const SUB_INTERVALS = [
	{ value: 0, label: () => m.firc_tunnel_edit_interval_manual() },
	{ value: 3600, label: () => m.subscriptions_refresh_hourly() },
	{ value: 21600, label: () => m.subscriptions_refresh_6h() },
	{ value: 43200, label: () => m.subscriptions_refresh_12h() },
	{ value: 86400, label: () => m.subscriptions_refresh_daily() },
];

export const FINGERPRINTS: { value: '' | 'chrome' | 'firefox'; label: () => string }[] = [
	{ value: '', label: () => m.firc_tunnel_edit_fp_default() },
	{ value: 'chrome', label: () => 'Chrome' },
	{ value: 'firefox', label: () => 'Firefox' },
];

export function totalProbeLatencyMs(p?: FircTunnelProbeRow | null): number | null {
	if (!p || !p.ok) return null;
	if (p.firstByteMs != null) {
		return (p.handshakeMs ?? 0) + p.firstByteMs;
	}
	if (p.handshakeMs != null) {
		return p.handshakeMs;
	}
	return null;
}

export function parseHeadersTextToMap(text: string): Record<string, string> | undefined {
	const map: Record<string, string> = {};
	for (const rawLine of text.split(/\r?\n/)) {
		const line = rawLine.trim();
		if (!line || line.startsWith('#')) continue;
		const idx = line.indexOf(':');
		if (idx <= 0) continue;
		const k = line.slice(0, idx).trim();
		const v = line.slice(idx + 1).trim();
		if (k && v) map[k] = v;
	}
	return Object.keys(map).length > 0 ? map : undefined;
}

export function randomId(): string {
	const bytes = new Uint8Array(4);
	if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
		crypto.getRandomValues(bytes);
	} else {
		for (let i = 0; i < 4; i++) bytes[i] = Math.floor(Math.random() * 256);
	}
	return Array.from(bytes)
		.map((b) => b.toString(16).padStart(2, '0'))
		.join('');
}

export function cloneTunnel(t: FircTunnel): FircTunnel {
	const copy = JSON.parse(JSON.stringify(t)) as FircTunnel;
	copy.filter = copy.filter ?? '';
	copy.filterExclude = copy.filterExclude ?? '';
	return copy;
}

export function buildFilteredNodes(
	preview: FircTunnelPreview | null,
	liveState: FircTunnelState | undefined,
): FircTunnelFilteredNode[] {
	const raw =
		preview !== null ? (preview.filteredNodes ?? []) : (liveState?.filteredNodes ?? []);
	return raw.map((n) => ({
		...n,
		name: stripFircEmojis(n.name || n.key),
		source: stripFircEmojis(n.source || ''),
	}));
}

/** Узлы туннеля в порядке приоритета: живое состояние + предпросмотр + сохранённый order. */
export function buildDisplayNodes(
	tun: FircTunnel,
	liveState: FircTunnelState | undefined,
	preview: FircTunnelPreview | null,
	filteredNodes: FircTunnelFilteredNode[],
): DisplayNode[] {
	const live = liveState?.nodes ?? [];
	const prev = preview?.nodes ?? [];
	const hiddenKeys = new Set(filteredNodes.map((n) => n.key));
	const byKey = new Map<string, DisplayNode>();

	if (preview !== null) {
		const liveByKey = new Map(live.map((ln) => [ln.key, ln]));
		for (const pn of prev) {
			const ln = liveByKey.get(pn.key);
			byKey.set(pn.key, {
				key: pn.key,
				name: stripFircEmojis(pn.name || pn.key),
				source: stripFircEmojis(pn.source || ''),
				state: ln
					? ln.state
					: pn.excluded
						? 'excluded'
						: pn.skipReason
							? 'skipped'
							: pn.missing
								? 'missing'
								: 'reserve',
				skipReason: pn.skipReason ?? ln?.skipReason,
				v6: ln?.v6,
			});
		}
	} else {
		for (const ln of live) {
			byKey.set(ln.key, {
				key: ln.key,
				name: stripFircEmojis(ln.name || ln.key),
				source: stripFircEmojis(ln.source || ''),
				state: ln.state,
				skipReason: ln.skipReason,
				v6: ln.v6,
			});
		}

		for (const pn of prev) {
			if (!byKey.has(pn.key)) {
				byKey.set(pn.key, {
					key: pn.key,
					name: stripFircEmojis(pn.name || pn.key),
					source: stripFircEmojis(pn.source || ''),
					state: pn.excluded
						? 'excluded'
						: pn.skipReason
							? 'skipped'
							: pn.missing
								? 'missing'
								: 'reserve',
					skipReason: pn.skipReason,
				});
			}
		}
	}

	for (const key of tun.order ?? []) {
		if (hiddenKeys.has(key)) continue;
		if (!byKey.has(key)) {
			const rawName = key.includes(':') ? key.slice(key.indexOf(':') + 1) : key;
			let decoded = rawName;
			try {
				decoded = decodeURIComponent(rawName);
			} catch {
				// keep raw
			}
			byKey.set(key, {
				key,
				name: stripFircEmojis(decoded),
				source: key.includes(':') ? key.slice(0, key.indexOf(':')) : '',
				state: (tun.exclude ?? []).includes(key) ? 'excluded' : 'reserve',
			});
		}
	}

	const orderIdx = new Map((tun.order ?? []).map((k, i) => [k, i]));
	return Array.from(byKey.values()).sort((a, b) => {
		const ia = orderIdx.get(a.key);
		const ib = orderIdx.get(b.key);
		if (ia !== undefined && ib !== undefined) return ia - ib;
		if (ia !== undefined) return -1;
		if (ib !== undefined) return 1;
		return a.name.localeCompare(b.name);
	});
}
