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
export const platformOptions = [
	{ value: 'desktop', label: 'desktop (роутер / ПК)' },
	{ value: 'mobile', label: 'mobile' }
];

export { dnsModeOptions } from '../proxy-panel/dnsOptions';

export const autoReconnectIntervalOptions = [
	{ value: 'on_failure', label: 'Только при сбое сессии' },
	{ value: '30m', label: 'Каждые 30 минут' },
	{ value: '1h', label: 'Каждый час' },
	{ value: '2h', label: 'Каждые 2 часа' },
	{ value: '4h', label: 'Каждые 4 часа' },
	{ value: '8h', label: 'Каждые 8 часов' },
	{ value: '12h', label: 'Каждые 12 часов' },
	{ value: '24h', label: 'Каждые 24 часа' }
];
