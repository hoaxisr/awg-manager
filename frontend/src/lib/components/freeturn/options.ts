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
