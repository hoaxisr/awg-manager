import type { SingboxRouterWANInterface } from '$lib/types';

/** Подпись пункта выбора интерфейса привязки (sing-box direct). */
export function bindInterfaceLabel(i: SingboxRouterWANInterface): string {
	const state = i.up ? '' : i.absent ? ' (нет в системе)' : i.foreign ? ' (нет несущей)' : ' (down)';
	return `${i.label} · ${i.name}${state}`;
}
