import { m } from '$lib/i18n';
import type { DnsRoute } from '$lib/types';

export interface PortableDnsRoute {
	name: string;
	manualDomains: string[];
	subscriptions?: { url: string; name: string }[];
	excludes?: string[];
	subnets?: string[];
	enabled: boolean;
	iconUrl?: string;
	skipIPv6?: boolean;
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
		iconUrl: r.iconUrl || undefined,
		skipIPv6: r.skipIPv6 || undefined,
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
	if (!Array.isArray(data)) throw new Error(m.export_file_must_be_array());
	return data.filter(item =>
		typeof item.name === 'string' &&
		item.name.trim() !== '' &&
		(Array.isArray(item.manualDomains) || Array.isArray(item.subscriptions)) &&
		(item.iconUrl === undefined || typeof item.iconUrl === 'string')
	);
}
