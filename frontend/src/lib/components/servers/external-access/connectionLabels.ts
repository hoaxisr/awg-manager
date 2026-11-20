import type { TgNetworkPreset, XHTTPPreset, XrayConfig } from '$lib/types';
import type { EgressOption } from '$lib/types/serverWizard';

export const EXTERNAL_ACCESS_LABELS = {
	sectionTitle: 'Внешний доступ',
	sectionSubtitle:
		'Управление входящими подключениями VLESS и Telegram через ваш роутер',
	vlessTitle: 'VLESS',
	vlessService: 'Сервер VLESS',
	vlessShort: 'VLESS',
	vlessSubtitle: 'Подключение телефона, ноутбука или другого роутера через ваш Keenetic',
	telegramTitle: 'Telegram',
	telegramService: 'Прокси Telegram',
	telegramShort: 'Telegram',
	telegramSubtitle: 'Прокси и веб-подключение для Telegram через ваш роутер',
	telegramMtproto: 'Прокси для приложения Telegram (MTProto)',
	telegramMtprotoBadge: 'Прокси для приложения Telegram (MTProto)',
	telegramWeb: 'Веб-ссылка / мост через домен',
	telegramWebBadge: 'Веб-ссылка / мост через домен',
	dispatcherTitle: 'Входящие подключения (:9009)',
	dispatcherRailTitle: 'Входящие подключения',
	dispatcherSubtitle:
		'Приём внешнего HTTP-трафика на порту 9009 и распределение между VLESS и Telegram',
	wizardButton: 'Мастер настройки',
	statusRunning: 'Сервер запущен',
	statusPartial: 'Частично работает',
	statusStopped: 'Остановлен',
	statusNeedsCheck: 'Требует проверки',
	egressDirect: 'Напрямую в интернет',
	egressFirc: 'По правилам роутера',
	egressTunnel: 'Через выбранный туннель',
	egressLegacyWarning: 'Устаревший выход — требуется выбор',
	legacyEgressWarning: 'Устаревший выход — требуется выбор',
} as const;

export interface VlessPresetLabelMeta {
	id: XHTTPPreset;
	title: string;
	shortBadge: string;
	description: string;
}

export const VLESS_PRESET_LABELS: Record<XHTTPPreset, VlessPresetLabelMeta> = {
	live_low_latency: {
		id: 'live_low_latency',
		title: 'Быстрый (HTTP POST)',
		shortBadge: 'HTTP POST',
		description: 'Рекомендуется для KeenDNS (белый и серый IP) и быстрых CDN с поддержкой HTTP/2. Пакеты до 1 МБ, минимальная задержка.',
	},
	cdn_get: {
		id: 'cdn_get',
		title: 'Совместимый CDN (HTTP GET)',
		shortBadge: 'HTTP GET',
		description: 'Для строгих прокси и CDN, ограничивающих потоковые POST-запросы. Передача данных через HTTP GET (интервал 5–10 мс).',
	},
	balanced: {
		id: 'balanced',
		title: 'Мультиплексирование (Xmux)',
		shortBadge: 'POST + Xmux',
		description: 'Мультиплексирование потоков (Xmux) поверх HTTP POST для снижения числа TLS-соединений.',
	},
	custom: {
		id: 'custom',
		title: 'Вручную',
		shortBadge: 'Вручную',
		description: 'Ручная настройка метода отправки, буферов, заголовков и параметров потока.',
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
		label: 'Через CDN и KeenDNS',
		badge: 'CDN + KeenDNS',
		badgeClass: 'badge-success',
		is_cdn: true,
		carrier_mode: 'https-lanes',
		subtitle: 'Через внешний CDN поверх облачного домена KeenDNS (роутер за NAT / серый IP)',
		specs: 'https-lanes · батч 2 МБ · буфер 32 МБ',
	},
	{
		id: 'cdn_static',
		label: 'Через CDN на белый IP',
		badge: 'CDN + белый IP',
		badgeClass: 'badge-info',
		is_cdn: true,
		carrier_mode: 'https-lanes',
		subtitle: 'Через внешний CDN напрямую к публичному IP роутера',
		specs: 'https-lanes · батч 512 КБ · буфер 64 МБ',
	},
	{
		id: 'cdn_file_https',
		label: 'Через CDN (файловый GET)',
		badge: 'CDN File HTTPS',
		badgeClass: 'badge-warning',
		is_cdn: true,
		carrier_mode: 'file-https-v1',
		subtitle: 'CDN через безопасные GET-запросы с расширениями (.bin/.html) для жестких фильтров',
		specs: 'file-https-v1 · батч 2 КБ · буфер 16 МБ',
	},
	{
		id: 'keendns',
		label: 'Напрямую через KeenDNS',
		badge: 'KeenDNS',
		badgeClass: 'badge-purple',
		is_cdn: false,
		carrier_mode: 'websocket',
		subtitle: 'Прямое подключение к облачному домену KeenDNS без промежуточного CDN',
		specs: 'websocket · батч 1 МБ · буфер 32 МБ',
	},
	{
		id: 'static_ip',
		label: 'Напрямую по белому IP',
		badge: 'Прямой IP',
		badgeClass: 'badge-neutral',
		is_cdn: false,
		carrier_mode: 'websocket-lanes',
		subtitle: 'Прямое подключение к публичному IP или домену роутера без CDN',
		specs: 'websocket-lanes · батч 2 МБ · буфер 64 МБ',
	},
];

export function formatTelegramCarrierLabel(carrierMode?: string | null): string {
	switch ((carrierMode || '').trim().toLowerCase()) {
		case 'https-lanes':
			return 'HTTPS-потоки (https-lanes)';
		case 'file-https-v1':
			return 'Файловый HTTPS GET (file-https-v1)';
		case 'websocket':
			return 'WebSocket';
		case 'websocket-lanes':
			return 'Параллельные WebSocket (websocket-lanes)';
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
			return `Туннель: ${cleanTunnelDisplayName(c.tunnelName)}`;
		}
		const found = tunnelInterfaces.find((t) => t.iface === iface || t.id === iface);
		return found ? `Туннель: ${cleanTunnelDisplayName(found.name)}` : `Интерфейс: ${iface}`;
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
		return cleanTunnelDisplayName(found.label.replace(/\s*\(недоступен\)\s*$/i, ''));
	}
	return `Интерфейс: ${dev}`;
}

export function formatShortEgressSummary(label: string): string {
	if (!label || label === 'direct' || label === EXTERNAL_ACCESS_LABELS.egressDirect) {
		return 'Интернет: напрямую';
	}
	if (label === 'firc:router' || label === 'router' || label === EXTERNAL_ACCESS_LABELS.egressFirc) {
		return 'Интернет: по правилам роутера';
	}
	if (label === 'mihomo:1099' || label === EXTERNAL_ACCESS_LABELS.egressLegacyWarning) {
		return `Интернет: ${EXTERNAL_ACCESS_LABELS.egressLegacyWarning}`;
	}
	if (label.startsWith('Туннель: ')) {
		const cleanName = cleanTunnelDisplayName(label.slice('Туннель: '.length))
			.replace(/\s*\([^()]+\)\s*$/, '')
			.trim();
		return `Интернет: через ${cleanName || 'туннель'}`;
	}
	if (label.startsWith('Интерфейс: ')) {
		return `Интернет: через туннель (${label.slice('Интерфейс: '.length).trim()})`;
	}
	const cleanLabel = cleanTunnelDisplayName(label).replace(/\s*\([^()]+\)\s*$/, '').trim();
	return `Интернет: через ${cleanLabel || label}`;
}
