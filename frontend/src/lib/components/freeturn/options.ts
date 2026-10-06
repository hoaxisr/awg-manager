import { m } from '$lib/i18n';

// Общие option-списки клиентской и серверной панелей — единый источник,
// чтобы новый obf-профиль не появился только в одной из них.
export const modeOptions = [
	{ value: 'udp', label: 'udp' },
	{ value: 'tcp', label: 'tcp' }
];

export const transportOptions = [
	{ value: 'tcp', label: 'tcp' },
	{ value: 'udp', label: 'udp' }
];

export const obfOptions = [
	{ value: 'none', label: 'none' },
	{ value: 'rtpopus', label: 'rtpopus' },
	{ value: 'rtpopus2', label: 'rtpopus2' },
	{ value: 'rtpopus3', label: 'rtpopus3' }
];

/** VK-auth persona class for freeturn (-platform). */
export function platformOptions() {
	return [
		{ value: 'desktop', label: m.proxy_option_platform_desktop() },
		{ value: 'mobile', label: 'mobile' }
	];
}

export { dnsModeOptions } from '../proxy-panel/dnsOptions';

export function autoReconnectIntervalOptions() {
	return [
		{ value: 'on_failure', label: m.proxy_auto_reconnect_on_failure() },
		{ value: '30m', label: m.proxy_auto_reconnect_30m() },
		{ value: '1h', label: m.proxy_auto_reconnect_1h() },
		{ value: '2h', label: m.proxy_auto_reconnect_2h() },
		{ value: '4h', label: m.proxy_auto_reconnect_4h() },
		{ value: '8h', label: m.proxy_auto_reconnect_8h() },
		{ value: '12h', label: m.proxy_auto_reconnect_12h() },
		{ value: '24h', label: m.proxy_auto_reconnect_24h() }
	];
}

