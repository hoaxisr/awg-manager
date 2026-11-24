// Чистая логика карточки «Прокси Telegram»: пресеты доменов, выходные каналы,
// проверка секрета. Подписи — ленивыми вызовами m.*, чтобы следовать за языком.
import { m } from '$lib/i18n';
import {
	EXTERNAL_ACCESS_LABELS,
	cleanTunnelDisplayName,
	formatShortEgressSummary,
	formatTelegramEgressLabel,
	unavailableSuffix,
} from '../external-access/connectionLabels';
import type {
	TgWebProxyConfig,
	TgWebProxyStatus,
	TelegramUpstream,
	WebProxyEndpoint,
	TgNetworkPreset,
} from '$lib/types';
import type { EgressOption } from '$lib/types/serverWizard';

/** Выход для Telegram в виде, в котором его получает TelegramUpstreamsEditor. */
export interface EgressChoice {
	value: string;
	label: string;
	available: boolean;
}

/** Сырой выход из возможностей мастера; подпись собирается при показе. */
export interface EgressEntry {
	value: string;
	/** Имя туннеля; пусто у прямого выхода. */
	name: string;
	available: boolean;
	/** Интерфейс из текущей настройки, которого нет в списке возможностей. */
	current?: boolean;
}

export interface PresetMeta {
	id: TgNetworkPreset;
	label: () => string;
	badge: () => string;
	badgeClass: string;
	is_cdn: boolean;
	carrier_mode: string;
	subtitle: () => string;
	specs: () => string;
}

export const WEBPROXY_PRESETS: PresetMeta[] = [
	{
		id: 'cdn_keendns',
		label: () => m.servers_tg_preset_cdn_keendns_label(),
		badge: () => m.servers_tg_preset_cdn_keendns_badge(),
		badgeClass: 'badge-success',
		is_cdn: true,
		carrier_mode: 'https-lanes',
		subtitle: () => m.servers_tg_preset_cdn_keendns_subtitle(),
		specs: () => m.servers_tg_preset_specs({ mode: 'https-lanes', batch: m.servers_tg_size_mb({ n: 2 }), buffer: m.servers_tg_size_mb({ n: 32 }) }),
	},
	{
		id: 'cdn_static',
		label: () => m.servers_tg_preset_cdn_static_label(),
		badge: () => m.servers_tg_preset_cdn_static_badge(),
		badgeClass: 'badge-info',
		is_cdn: true,
		carrier_mode: 'https-lanes',
		subtitle: () => m.servers_tg_preset_cdn_static_subtitle(),
		specs: () => m.servers_tg_preset_specs({ mode: 'https-lanes', batch: m.servers_tg_size_kb({ n: 512 }), buffer: m.servers_tg_size_mb({ n: 64 }) }),
	},
	{
		id: 'keendns',
		label: () => m.servers_tg_preset_keendns_label(),
		badge: () => 'KeenDNS',
		badgeClass: 'badge-purple',
		is_cdn: false,
		carrier_mode: 'websocket',
		subtitle: () => m.servers_tg_preset_keendns_subtitle(),
		specs: () => m.servers_tg_preset_specs({ mode: 'websocket', batch: m.servers_tg_size_mb({ n: 1 }), buffer: m.servers_tg_size_mb({ n: 32 }) }),
	},
	{
		id: 'static_ip',
		label: () => m.servers_tg_preset_static_ip_label(),
		badge: () => m.servers_tg_preset_static_ip_badge(),
		badgeClass: 'badge-neutral',
		is_cdn: false,
		carrier_mode: 'websocket-lanes',
		subtitle: () => m.servers_tg_preset_static_ip_subtitle(),
		specs: () => m.servers_tg_preset_specs({ mode: 'websocket-lanes', batch: m.servers_tg_size_mb({ n: 2 }), buffer: m.servers_tg_size_mb({ n: 64 }) }),
	},
];

function isKeenCloudDomain(host?: string): boolean {
	const h = (host || '').toLowerCase();
	return (
		h.includes('.netcraze.club') ||
		h.includes('.keenetic.pro') ||
		h.includes('.keenetic.link') ||
		h.includes('.keenetic.name')
	);
}

export function inferEndpointPreset(
	ep: { hostname?: string; is_cdn?: boolean; preset?: string },
	cfg?: Partial<TgWebProxyConfig> | null
): TgNetworkPreset {
	const p = (ep.preset || '').toLowerCase();
	if (p === 'cdn_keendns' || p === 'cdn_static' || p === 'keendns' || p === 'static_ip') {
		return p;
	}
	const np = (cfg?.network_preset || '').toLowerCase();
	if (np === 'cdn_keendns' || np === 'keendns') {
		return ep.is_cdn ? 'cdn_keendns' : 'keendns';
	}
	if (np === 'cdn_static' || np === 'static_ip') {
		return ep.is_cdn ? 'cdn_static' : 'static_ip';
	}
	const hasKeenCloud =
		isKeenCloudDomain(ep.hostname) ||
		isKeenCloudDomain(cfg?.direct_host) ||
		isKeenCloudDomain(cfg?.public_hostname) ||
		(cfg?.endpoints || []).some((other) => isKeenCloudDomain(other.hostname));
	if (ep.is_cdn) {
		return hasKeenCloud ? 'cdn_keendns' : 'cdn_static';
	}
	return hasKeenCloud ? 'keendns' : 'static_ip';
}

export function getPresetMeta(presetId?: string): PresetMeta {
	return WEBPROXY_PRESETS.find((p) => p.id === presetId) || WEBPROXY_PRESETS[0];
}

export function buildEditableUpstreams(cfg?: Partial<TgWebProxyConfig> | null): TelegramUpstream[] {
	const list = cfg?.upstreams;
	if (list && list.length > 0) {
		return list.map((u, idx) => ({
			id: u.id || `up-${idx + 1}`,
			type: u.type === 'socks5' ? 'socks5' : 'direct',
			device: u.device || 'direct',
			address: u.address || '',
			username: u.username || '',
			password: '',
			password_set: Boolean(u.password_set),
			weight: u.weight && u.weight > 0 ? u.weight : 1,
			enabled: Boolean(u.enabled),
		}));
	}
	return [
		{
			id: 'up-1',
			type: 'direct',
			device: (cfg?.upstream_device || 'direct').trim() || 'direct',
			weight: 1,
			enabled: true,
		},
	];
}

export function validateUpstreamsDraft(list: TelegramUpstream[]): 'empty' | 'socks' | null {
	const enabled = list.filter((u) => u.enabled);
	if (enabled.length === 0) return 'empty';
	for (const u of enabled) {
		if (u.type === 'socks5') {
			const addr = (u.address || '').trim();
			const sep = addr.lastIndexOf(':');
			const host = sep > 0 ? addr.slice(0, sep).trim() : '';
			const portNum = sep > 0 ? Number(addr.slice(sep + 1)) : NaN;
			if (!host || !Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
				return 'socks';
			}
		}
	}
	return null;
}

/** Текст ошибки проверки черновика каналов; null — черновик годен. */
export function upstreamsDraftError(list: TelegramUpstream[]): string | null {
	const vErr = validateUpstreamsDraft(list);
	if (vErr === 'empty') return m.tg_upstreams_validation_empty();
	if (vErr === 'socks') return m.tg_upstreams_validation_socks();
	return null;
}

/** Черновик каналов в форме для сохранения: лишние для типа поля сброшены. */
export function cleanUpstreams(list: TelegramUpstream[]): TelegramUpstream[] {
	return list.map((u) => ({
		...u,
		device: u.type === 'direct' ? (u.device || '').trim() || 'direct' : '',
		address: u.type === 'socks5' ? (u.address || '').trim() : '',
		username: u.type === 'socks5' ? (u.username || '').trim() : '',
		password: u.type === 'socks5' ? u.password || '' : '',
		weight: u.weight && u.weight >= 1 ? Math.floor(u.weight) : 1,
	}));
}

export function primaryDirectDevice(list: TelegramUpstream[]): string {
	const enabled = list.filter((u) => u.enabled);
	if (enabled.length === 0 || enabled[0].type !== 'direct') return '';
	return (enabled[0].device || '').trim() || 'direct';
}

export function resolveEgressIface(opt: EgressOption): string {
	if (opt.id === 'direct' || opt.kind === 'direct') return 'direct';
	if (opt.iface) return opt.iface;
	const match = /\(([^()]+)\)\s*$/.exec(opt.name || '');
	if (match && match[1]) return match[1].trim();
	return opt.id;
}

export function isValidSecretInput(raw: string): boolean {
	const s = raw.trim().toLowerCase();
	if (!/^[0-9a-f]+$/.test(s)) return false;
	if (s.length === 32) return true;
	if (s.length === 34 && s.startsWith('dd')) return true;
	return s.length > 34 && s.startsWith('ee') && s.length % 2 === 0;
}

/** Описание домена от бэкенда/старых версий в человеческих словах. */
export function cleanEndpointDescription(desc?: string): string {
	if (!desc) return '';
	const external = m.servers_tg_external_service();
	return desc
		.replace(/^Primary\s+Web\s+Proxy$/i, m.servers_tg_primary_web_proxy())
		.replace(/Selectel\s*CDN\s*\+\s*Static\s*IP/gi, m.servers_tg_preset_cdn_static_label())
		.replace(/Selectel\s*CDN/gi, external)
		.replace(/KeenDNS\s*Static\s*IP/gi, 'KeenDNS')
		.replace(/\bSelectel\b/gi, external)
		.replace(/\bCDN\b/gi, external);
}

/** Новый домен настроек с портами по порядку и пресетом по сценарию. */
export function newEndpoint(editConfig: Partial<TgWebProxyConfig>, nextIdx: number): WebProxyEndpoint {
	const isCDN = editConfig.scenario === 'cdn_http' || editConfig.scenario === 'dual';
	const meta = getPresetMeta(inferEndpointPreset({ is_cdn: isCDN }, editConfig));
	return {
		id: `ep-${Date.now().toString(36)}`,
		hostname: '',
		listen_port: nextIdx === 0 ? 8084 : 18084 + (nextIdx - 1) * 3,
		admin_port: nextIdx === 0 ? 8086 : 18086 + (nextIdx - 1) * 3,
		description: '',
		preset: meta.id,
		carrier_mode: meta.carrier_mode,
		is_cdn: meta.is_cdn,
		enabled: true,
	};
}

export type OverallState = 'running' | 'partial' | 'stopped';

/** Итоговое состояние сервера с учётом сценария и флага «включено». */
export function overallState(
	config: TgWebProxyConfig | null,
	status: TgWebProxyStatus | null,
	expectsMt: boolean,
	expectsWeb: boolean
): OverallState {
	const mtOnline = Boolean(status?.direct_online);
	const webOnline = Boolean(status?.webproxy_online);
	const cfgEnabled = config ? Boolean(config.enabled) : Boolean(status?.running);
	if (!cfgEnabled) {
		if (!status?.running && !mtOnline && !webOnline) return 'stopped';
		return 'partial';
	}
	if (!status?.running && !mtOnline && !webOnline) return 'stopped';
	if (expectsMt && expectsWeb) {
		if (mtOnline && webOnline) return 'running';
		if (mtOnline || webOnline) return 'partial';
		return 'stopped';
	}
	if (expectsMt) return mtOnline ? 'running' : 'stopped';
	if (expectsWeb) return webOnline ? 'running' : 'stopped';
	return status?.running ? 'running' : 'stopped';
}

/** Почему сервер работает частично; пусто, если состояние не partial. */
export function partialReason(
	state: OverallState,
	config: TgWebProxyConfig | null,
	status: TgWebProxyStatus | null,
	expectsMt: boolean,
	expectsWeb: boolean
): string {
	if (state !== 'partial') return '';
	if (!config?.enabled && (status?.running || status?.direct_online || status?.webproxy_online)) {
		return m.servers_tg_partial_disabled_running();
	}
	if (expectsMt && !status?.direct_online) return m.servers_tg_partial_no_mtproto();
	if (expectsWeb && !status?.webproxy_online) return m.servers_tg_partial_no_web();
	return m.servers_tg_partial_one_down();
}

/** Выходы из возможностей мастера плюс текущий интерфейс, если его там нет. */
export function egressEntriesFrom(options: EgressOption[], currentUpstream?: string): EgressEntry[] {
	const seen = new Set<string>();
	const list: EgressEntry[] = [];
	for (const opt of options) {
		if (opt.kind === 'socks' || opt.id === 'firc:router') continue;
		const val = resolveEgressIface(opt);
		if (!val || seen.has(val)) continue;
		seen.add(val);
		list.push({ value: val, name: cleanTunnelDisplayName(opt.name), available: opt.available });
	}
	if (list.length === 0) {
		list.push({ value: 'direct', name: '', available: true });
		seen.add('direct');
	}
	const cur = (currentUpstream || '').trim();
	if (cur && !seen.has(cur)) {
		list.push({ value: cur, name: cur, available: false, current: true });
	}
	return list;
}

/** Подписи выходов на текущем языке. */
export function egressChoices(entries: EgressEntry[]): EgressChoice[] {
	return entries.map((e) => ({
		value: e.value,
		available: e.available,
		label: !e.name
			? EXTERNAL_ACCESS_LABELS.egressDirect
			: e.current
				? m.servers_tg_egress_current_iface({ name: e.name })
				: e.available
					? e.name
					: `${e.name}${unavailableSuffix()}`, // formatTelegramEgressLabel срезает именно этот суффикс
	}));
}

function summarizeUpstream(u: TelegramUpstream, options: EgressChoice[]): string {
	if (u.type === 'socks5') {
		return m.tg_upstreams_summary_socks({ addr: (u.address || '').trim() || 'SOCKS5' });
	}
	return formatTelegramEgressLabel(u.device || 'direct', options);
}

/** Включённые каналы, а если таких нет — все. */
function activeUpstreams(list: TelegramUpstream[]): TelegramUpstream[] {
	const enabled = list.filter((u) => u.enabled);
	return enabled.length > 0 ? enabled : list;
}

/** Полная подпись выхода (для диагностики). */
export function egressSummary(config: TgWebProxyConfig | null, options: EgressChoice[]): string {
	const list = config?.upstreams;
	if (list && list.length > 0) {
		const active = activeUpstreams(list);
		if (active.length === 1) return summarizeUpstream(active[0], options);
		return m.tg_upstreams_summary_multi({
			primary: summarizeUpstream(active[0], options),
			extra: active.length - 1,
		});
	}
	return formatTelegramEgressLabel(config?.upstream_device, options);
}

/** Короткая подпись выхода (для сводки карточки). */
export function shortEgressSummary(config: TgWebProxyConfig | null, options: EgressChoice[]): string {
	const list = config?.upstreams;
	if (list && list.length > 0) {
		const active = activeUpstreams(list);
		const first = active[0];
		const firstShort =
			first.type === 'socks5'
				? summarizeUpstream(first, options)
				: formatShortEgressSummary(summarizeUpstream(first, options));
		if (active.length > 1) {
			return m.tg_upstreams_summary_multi({ primary: firstShort, extra: active.length - 1 });
		}
		return firstShort;
	}
	return formatShortEgressSummary(egressSummary(config, options));
}

/** Ни один включённый канал не может вывести трафик. */
export function egressUnavailable(config: TgWebProxyConfig | null, options: EgressChoice[]): boolean {
	const deviceDown = (device?: string) => {
		const dev = (device || 'direct').trim();
		if (!dev || dev === 'direct') return false;
		const found = options.find((o) => o.value === dev);
		return Boolean(found && !found.available);
	};
	const list = config?.upstreams;
	if (list && list.length > 0) {
		const enabled = list.filter((u) => u.enabled);
		if (enabled.length === 0) return true;
		return enabled.every((u) => (u.type === 'socks5' ? !(u.address || '').trim() : deviceDown(u.device)));
	}
	return deviceDown(config?.upstream_device);
}
