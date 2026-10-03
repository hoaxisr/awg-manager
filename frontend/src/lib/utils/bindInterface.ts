import { m } from '$lib/i18n';
import type { SingboxRouterOutbound, SingboxRouterWANInterface } from '$lib/types';

/** Подпись пункта выбора интерфейса привязки (sing-box direct). */
export function bindInterfaceLabel(i: SingboxRouterWANInterface): string {
	const args = { label: i.label, name: i.name };
	if (i.up) return m.bind_interface_label(args);
	if (i.absent) return m.bind_interface_label_absent(args);
	if (i.foreign) return m.bind_interface_label_foreign(args);
	return m.bind_interface_label_down(args);
}

/**
 * Пункты пикера привязки direct-outbound'а. Скрыты интерфейсы, занятые
 * ДРУГИМИ outbound'ами того же конфига (tproxy и fakeip — разные конфиги). Своя
 * привязка редактируемого outbound'а остаётся всегда, а пропавшая из списка
 * роутера — заглушкой «нет в системе», чтобы поле не выглядело пустым (#961).
 */
export function directBindChoices(
	list: SingboxRouterWANInterface[],
	outbounds: SingboxRouterOutbound[],
	editing?: SingboxRouterOutbound
): SingboxRouterWANInterface[] {
	const own = editing?.bind_interface ?? '';
	const taken = new Set(outbounds.map((o) => o.bind_interface).filter(Boolean));
	taken.delete(own);
	const out = list.filter((i) => !taken.has(i.name));
	if (own && !out.some((i) => i.name === own)) {
		out.push({ name: own, id: '', label: own, up: false, priority: 0, absent: true });
	}
	return out;
}
