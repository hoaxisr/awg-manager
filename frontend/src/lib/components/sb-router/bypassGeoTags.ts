import type { GeoTag } from '$lib/types';

/**
 * Предел набора AWGM-BYPASS (bypassset.SetMaxElem). Больше записей ipset
 * просто не примет, поэтому выбор сверх предела бэкенд отвергает на PUT.
 */
export const BYPASS_SET_MAX_ELEM = 262144;

export interface GeoIPTagOption {
	/** Имя тега в нижнем регистре — в этом виде оно уезжает в настройки. */
	name: string;
	/** Записей в теге. Консервативно: .dat считает и IPv6, в набор идёт только IPv4. */
	count: number;
}

/**
 * Сводит теги всех geoip-файлов в один список: одноимённые теги из разных
 * файлов суммируются — ровно так их считает бэкенд (GeoIPTagCounts), поэтому
 * счётчик бюджета в UI совпадает с проверкой на PUT.
 */
export function aggregateGeoIPTags(perFile: GeoTag[][]): GeoIPTagOption[] {
	const totals = new Map<string, number>();
	for (const tags of perFile) {
		for (const t of tags) {
			const name = t.name.trim().toLowerCase();
			if (!name) continue;
			totals.set(name, (totals.get(name) ?? 0) + t.count);
		}
	}
	return [...totals]
		.map(([name, count]) => ({ name, count }))
		.sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * Сумма записей выбранных тегов. Неизвестный тег (пропал из .dat) считается
 * нулём — как и на бэкенде, где его просто нет в карте счётчиков.
 */
export function sumSelectedTags(selected: string[], options: GeoIPTagOption[]): number {
	const counts = new Map(options.map((o) => [o.name, o.count]));
	let total = 0;
	for (const tag of selected) {
		total += counts.get(tag.trim().toLowerCase()) ?? 0;
	}
	return total;
}
