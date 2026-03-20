import type { DnsRoute } from '$lib/types';

export interface PortableDnsRoute {
	name: string;
	manualDomains: string[];
	subscriptions?: { url: string; name: string }[];
	excludes?: string[];
	subnets?: string[];
	enabled: boolean;
}

export function exportRoutes(routes: DnsRoute[]): PortableDnsRoute[] {
	return routes.map(r => ({
		name: r.name,
		manualDomains: r.manualDomains ?? [],
		subscriptions: r.subscriptions?.length
			? r.subscriptions.map(s => ({ url: s.url, name: s.name }))
			: undefined,
		excludes: r.excludes?.length ? r.excludes : undefined,
		subnets: r.subnets?.length ? r.subnets : undefined,
		enabled: r.enabled,
	}));
}

export function downloadJson(data: unknown, filename: string) {
	const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
	const url = URL.createObjectURL(blob);
	const a = document.createElement('a');
	a.href = url;
	a.download = filename;
	a.click();
	URL.revokeObjectURL(url);
}

export function parseImportFile(json: string): PortableDnsRoute[] {
	const data = JSON.parse(json);
	if (!Array.isArray(data)) throw new Error('Файл должен содержать JSON массив');
	return data.filter(item =>
		typeof item.name === 'string' &&
		item.name.trim() !== '' &&
		(Array.isArray(item.manualDomains) || Array.isArray(item.subscriptions))
	);
}
