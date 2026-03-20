import type { StaticRouteList } from '$lib/types';

export interface PortableStaticRoute {
	name: string;
	subnets: string[];
	enabled: boolean;
}

export function exportStaticRoutes(routes: StaticRouteList[]): PortableStaticRoute[] {
	return routes.map(r => ({
		name: r.name,
		subnets: r.subnets ?? [],
		enabled: r.enabled,
	}));
}

export function parseStaticRouteImport(json: string): PortableStaticRoute[] {
	const data = JSON.parse(json);
	if (!Array.isArray(data)) throw new Error('Файл должен содержать JSON массив');
	return data.filter(item =>
		typeof item.name === 'string' &&
		item.name.trim() !== '' &&
		Array.isArray(item.subnets) &&
		item.subnets.length > 0
	);
}
