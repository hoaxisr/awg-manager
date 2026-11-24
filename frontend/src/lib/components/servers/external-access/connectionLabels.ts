import type { TgNetworkPreset, XHTTPPreset, XrayConfig } from '$lib/types';
import type { EgressOption } from '$lib/types/serverWizard';
import { m } from '$lib/i18n';

// Подписи «Внешнего доступа». Геттеры, а не строки: текст вычисляется при
// обращении и поэтому следует за выбранным языком.
export const EXTERNAL_ACCESS_LABELS = {
	get sectionTitle() {
		return m.servers_ext_lbl_section_title();
	},
	get sectionSubtitle() {
		return m.servers_ext_lbl_section_subtitle();
	},
	vlessTitle: 'VLESS',
	get vlessService() {
		return m.servers_ext_lbl_vless_service();
	},
	vlessShort: 'VLESS',
	get vlessSubtitle() {
		return m.servers_ext_lbl_vless_subtitle();
	},
	telegramTitle: 'Telegram',
	get telegramService() {
		return m.servers_ext_lbl_telegram_service();
	},
	telegramShort: 'Telegram',
	get telegramSubtitle() {
		return m.servers_ext_lbl_telegram_subtitle();
	},
	get telegramMtproto() {
		return m.servers_ext_lbl_telegram_mtproto();
	},
	get telegramMtprotoBadge() {
		return m.servers_ext_lbl_telegram_mtproto();
	},
	get telegramWeb() {
		return m.servers_ext_lbl_telegram_web();
	},
	get telegramWebBadge() {
		return m.servers_ext_lbl_telegram_web();
	},
	get dispatcherTitle() {
		return m.servers_ext_lbl_dispatcher_title();
	},
	get dispatcherRailTitle() {
		return m.servers_ext_lbl_dispatcher_rail_title();
	},
	get dispatcherSubtitle() {
		return m.servers_ext_lbl_dispatcher_subtitle();
	},
	get wizardButton() {
		return m.servers_ext_lbl_wizard_button();
	},
	get statusRunning() {
		return m.servers_ext_lbl_status_running();
	},
	get statusPartial() {
		return m.servers_ext_lbl_status_partial();
	},
	get statusStopped() {
		return m.servers_ext_lbl_status_stopped();
	},
	get statusNeedsCheck() {
		return m.servers_ext_lbl_status_needs_check();
	},
	get egressDirect() {
		return m.servers_ext_lbl_egress_direct();
	},
	get egressFirc() {
		return m.servers_ext_lbl_egress_firc();
	},
	get egressTunnel() {
		return m.servers_ext_lbl_egress_tunnel();
	},
	get egressLegacyWarning() {
		return m.servers_ext_lbl_egress_legacy();
	},
	get legacyEgressWarning() {
		return m.servers_ext_lbl_egress_legacy();
	},
};

export interface VlessPresetLabelMeta {
	id: XHTTPPreset;
	title: string;
	shortBadge: string;
	description: string;
}

export const VLESS_PRESET_LABELS: Record<XHTTPPreset, VlessPresetLabelMeta> = {
	live_low_latency: {
		id: 'live_low_latency',
		get title() {
			return m.servers_ext_preset_fast_title();
		},
		shortBadge: 'HTTP POST',
		get description() {
			return m.servers_ext_preset_fast_desc();
		},
	},
	cdn_get: {
		id: 'cdn_get',
		get title() {
			return m.servers_ext_preset_cdn_get_title();
		},
		shortBadge: 'HTTP GET',
		get description() {
			return m.servers_ext_preset_cdn_get_desc();
		},
	},
	balanced: {
		id: 'balanced',
		get title() {
			return m.servers_ext_preset_balanced_title();
		},
		shortBadge: 'POST + Xmux',
		get description() {
			return m.servers_ext_preset_balanced_desc();
		},
	},
	custom: {
		id: 'custom',
		get title() {
			return m.servers_ext_preset_custom_title();
		},
		get shortBadge() {
			return m.servers_ext_preset_custom_title();
		},
		get description() {
			return m.servers_ext_preset_custom_desc();
		},
	},
};

export function getVlessPresetLabel(preset?: XHTTPPreset | string | null): string {
	if (!preset) return VLESS_PRESET_LABELS.custom.title;
	return VLESS_PRESET_LABELS[preset as XHTTPPreset]?.title ?? VLESS_PRESET_LABELS.custom.title;
}

export interface TgWebPresetMeta {
	id: TgNetworkPreset;
	label: string;
	badge: string;
	badgeClass: string;
	is_cdn: boolean;
	carrier_mode: string;
	subtitle: string;
	specs: string;
}

export const TELEGRAM_WEB_PRESETS: TgWebPresetMeta[] = [
	{
		id: 'cdn_keendns',
		get label() {
			return m.servers_ext_tgp_cdn_keendns_label();
		},
		badge: 'CDN + KeenDNS',
		badgeClass: 'badge-success',
		is_cdn: true,
		carrier_mode: 'https-lanes',
		get subtitle() {
			return m.servers_ext_tgp_cdn_keendns_sub();
		},
		get specs() {
			return m.servers_ext_tgp_spec_2mb_32mb({ mode: 'https-lanes' });
		},
	},
	{
		id: 'cdn_static',
		get label() {
			return m.servers_ext_tgp_cdn_static_label();
		},
		get badge() {
			return m.servers_ext_tgp_cdn_static_badge();
		},
		badgeClass: 'badge-info',
		is_cdn: true,
		carrier_mode: 'https-lanes',
		get subtitle() {
			return m.servers_ext_tgp_cdn_static_sub();
		},
		get specs() {
			return m.servers_ext_tgp_spec_512kb_64mb({ mode: 'https-lanes' });
		},
	},
	{
		id: 'cdn_file_https',
		get label() {
			return m.servers_ext_tgp_cdn_file_label();
		},
		badge: 'CDN File HTTPS',
		badgeClass: 'badge-warning',
		is_cdn: true,
		carrier_mode: 'file-https-v1',
		get subtitle() {
			return m.servers_ext_tgp_cdn_file_sub();
		},
		get specs() {
			return m.servers_ext_tgp_spec_2kb_16mb({ mode: 'file-https-v1' });
		},
	},
	{
		id: 'keendns',
		get label() {
			return m.servers_ext_tgp_keendns_label();
		},
		badge: 'KeenDNS',
		badgeClass: 'badge-purple',
		is_cdn: false,
		carrier_mode: 'websocket',
		get subtitle() {
			return m.servers_ext_tgp_keendns_sub();
		},
		get specs() {
			return m.servers_ext_tgp_spec_1mb_32mb({ mode: 'websocket' });
		},
	},
	{
		id: 'static_ip',
		get label() {
			return m.servers_ext_tgp_static_label();
		},
		get badge() {
			return m.servers_ext_tgp_static_badge();
		},
		badgeClass: 'badge-neutral',
		is_cdn: false,
		carrier_mode: 'websocket-lanes',
		get subtitle() {
			return m.servers_ext_tgp_static_sub();
		},
		get specs() {
			return m.servers_ext_tgp_spec_2mb_64mb({ mode: 'websocket-lanes' });
		},
	},
];

export function formatTelegramCarrierLabel(carrierMode?: string | null): string {
	switch ((carrierMode || '').trim().toLowerCase()) {
		case 'https-lanes':
			return m.servers_ext_carrier_https_lanes();
		case 'file-https-v1':
			return m.servers_ext_carrier_file_https();
		case 'websocket':
			return 'WebSocket';
		case 'websocket-lanes':
			return m.servers_ext_carrier_ws_lanes();
		case 'https':
			return 'HTTPS POST';
		case 'get':
			return 'HTTP GET';
		default:
			return carrierMode ? String(carrierMode) : 'HTTPS / WebSocket';
	}
}

export function cleanTunnelDisplayName(name?: string | null): string {
	if (!name) return '';
	return name
		.replace(/\s*\[awg-manager:[^\]]+\]/gi, '')
		.replace(/\s{2,}/g, ' ')
		.trim();
}

export function isLegacySocksEgress(modeOrId?: string | null, socksPort?: number | null): boolean {
	const clean = (modeOrId || '').trim().toLowerCase();
	if (clean === 'socks' || clean === 'mihomo:1099') {
		return true;
	}
	if (!clean && (socksPort ?? 0) > 0) {
		return true;
	}
	return false;
}

export interface XrayEgressFormatInput {
	outbound_mode?: string;
	outbound_interface?: string;
	outbound_socks_port?: number;
	outboundMode?: string;
	outboundInterface?: string;
	outboundSocksPort?: number;
	tunnelName?: string;
}

export function formatXrayEgressLabel(
	config: XrayEgressFormatInput | Partial<XrayConfig> | null | undefined,
	tunnelInterfaces: EgressOption[] = []
): string {
	if (!config) return EXTERNAL_ACCESS_LABELS.egressDirect;
	const c = config as XrayEgressFormatInput;
	const rawMode = c.outbound_mode ?? c.outboundMode;
	const rawIface = c.outbound_interface ?? c.outboundInterface;
	const rawSocksPort = c.outbound_socks_port ?? c.outboundSocksPort;

	const mode =
		rawMode ||
		(rawSocksPort && rawSocksPort > 0
			? 'socks'
			: rawIface
			? 'interface'
			: 'direct');

	if (mode === 'router') {
		return EXTERNAL_ACCESS_LABELS.egressFirc;
	}
	if (mode === 'socks') {
		return EXTERNAL_ACCESS_LABELS.egressLegacyWarning;
	}
	if (mode === 'interface' || mode === 'outbound') {
		const iface = (rawIface || '').trim();
		if (!iface || iface === 'direct') return EXTERNAL_ACCESS_LABELS.egressDirect;
		if (c.tunnelName) {
			return m.servers_ext_egress_tunnel({ name: cleanTunnelDisplayName(c.tunnelName) });
		}
		const found = tunnelInterfaces.find((t) => t.iface === iface || t.id === iface);
		return found
			? m.servers_ext_egress_tunnel({ name: cleanTunnelDisplayName(found.name) })
			: m.servers_ext_egress_iface({ name: iface });
	}
	return EXTERNAL_ACCESS_LABELS.egressDirect;
}

export function formatTelegramEgressLabel(
	upstreamDevice?: string | null,
	options: { value: string; label: string }[] = []
): string {
	const dev = (upstreamDevice || '').trim();
	if (!dev || dev === 'direct') {
		return EXTERNAL_ACCESS_LABELS.egressDirect;
	}
	if (isLegacySocksEgress(dev)) {
		return EXTERNAL_ACCESS_LABELS.egressLegacyWarning;
	}
	const found = options.find((o) => o.value === dev);
	if (found) {
		return cleanTunnelDisplayName(stripUnavailableSuffix(found.label));
	}
	return m.servers_ext_egress_iface({ name: dev });
}

/** Подпись «(недоступен)» у недоступного выхода — общая с карточками, которые её дописывают. */
export function unavailableSuffix(): string {
	return ` (${m.servers_ext_unavailable()})`;
}

function stripUnavailableSuffix(label: string): string {
	const suffix = unavailableSuffix();
	return label.endsWith(suffix) ? label.slice(0, -suffix.length).trimEnd() : label;
}

// Префикс подписи «Туннель: …» / «Интерфейс: …» на текущем языке: имя — в конце
// сообщения, поэтому подстановка пустого имени даёт ровно префикс.
const tunnelPrefix = () => m.servers_ext_egress_tunnel({ name: '' });
const ifacePrefix = () => m.servers_ext_egress_iface({ name: '' });

export function formatShortEgressSummary(label: string): string {
	if (!label || label === 'direct' || label === EXTERNAL_ACCESS_LABELS.egressDirect) {
		return m.servers_ext_short_direct();
	}
	if (label === 'firc:router' || label === 'router' || label === EXTERNAL_ACCESS_LABELS.egressFirc) {
		return m.servers_ext_short_router();
	}
	if (label === 'mihomo:1099' || label === EXTERNAL_ACCESS_LABELS.egressLegacyWarning) {
		return m.servers_ext_short_other({ text: EXTERNAL_ACCESS_LABELS.egressLegacyWarning });
	}
	const tp = tunnelPrefix();
	if (label.startsWith(tp)) {
		const cleanName = cleanTunnelDisplayName(label.slice(tp.length))
			.replace(/\s*\([^()]+\)\s*$/, '')
			.trim();
		return m.servers_ext_short_via({ name: cleanName || m.servers_ext_short_tunnel_word() });
	}
	const ip = ifacePrefix();
	if (label.startsWith(ip)) {
		return m.servers_ext_short_via_iface({ name: label.slice(ip.length).trim() });
	}
	const cleanLabel = cleanTunnelDisplayName(label).replace(/\s*\([^()]+\)\s*$/, '').trim();
	return m.servers_ext_short_via({ name: cleanLabel || label });
}
