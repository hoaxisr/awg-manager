import { m } from '$lib/i18n';
import type { StaticRouteList } from '$lib/types';

export interface PortableStaticRoute {
	name: string;
	subnets: string[];
	enabled: boolean;
	iconUrl?: string;
}

export function exportStaticRoutes(routes: StaticRouteList[]): PortableStaticRoute[] {
	return routes.map(r => ({
		name: r.name,
		subnets: r.subnets ?? [],
		enabled: r.enabled,
		iconUrl: r.iconUrl || undefined,
	}));
}

export function parseStaticRouteImport(json: string): PortableStaticRoute[] {
	const data = JSON.parse(json);
	if (!Array.isArray(data)) throw new Error(m.export_file_must_be_array());
	return data.filter(item =>
		typeof item.name === 'string' &&
		item.name.trim() !== '' &&
		Array.isArray(item.subnets) &&
		item.subnets.length > 0 &&
		(item.iconUrl === undefined || typeof item.iconUrl === 'string')
	);
}
