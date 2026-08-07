// Выбор внешнего интерфейса (WAN) карточки «Здоровье» — чистые функции.
//
// ОДИН СПИСОК ВМЕСТО «ТУМБЛЕР + ЗАВИСИМЫЙ СЕЛЕКТ». Пара полей у бэкенда
// жёстко связана: NormalizeSingboxRouterSettings (service_settings.go)
// требует либо auto=true && iface=="", либо auto=false && iface!="", и гейт
// стоит и на storage, и на apply. Тумблер сам по себе выражал невалидную
// половину этой пары, поэтому его выключение вообще ничего не сохраняло: тумблер
// гас, бэкенд оставался на авто-определении, и страница врала про закреплённый
// WAN — включая перезагрузку списка после чужого сохранения. На мультивановом
// роутере это отправляет прямой трафик sing-box не в тот интерфейс.
//
// Со списком невалидного состояния не существует: каждый пункт — одно готовое
// валидное сохранение.
import type { DropdownOption } from '$lib/components/ui';
import type { SingboxRouterSettings, SingboxRouterWANInterface } from '$lib/types';

/**
 * Значение пункта «Автоматически».
 *
 * Пустая строка, а не сентинел вроде 'auto': ровно её несёт `wanInterface`
 * при включённом авто-определении, и совпасть с именем интерфейса она не
 * может.
 */
export const WAN_AUTO = '';

/** Выбранный пункт списка по сохранённым настройкам. */
export function wanSelectValue(cfg: SingboxRouterSettings | null | undefined): string {
	// Отсутствующий wanAutoDetect — легаси-payload, дефолт бэкенда = авто.
	if (cfg?.wanAutoDetect === false) return cfg.wanInterface || WAN_AUTO;
	return WAN_AUTO;
}

/**
 * Пункты списка: «Автоматически», затем интерфейсы роутера.
 *
 * `selected` дописывается отдельным пунктом, если его нет в списке — список
 * не загрузился или интерфейс исчез из системы. Без этого `Dropdown` (он ищет
 * `options.find(o => o.value === value)`) показал бы плейсхолдер, то есть
 * сохранённое закрепление молча выглядело бы как «Автоматически».
 */
export function wanSelectOptions(
	interfaces: SingboxRouterWANInterface[],
	selected: string,
): DropdownOption[] {
	const options: DropdownOption[] = [
		{ value: WAN_AUTO, label: 'Автоматически' },
		...interfaces.map((iface) => ({
			value: iface.name,
			label: iface.label ? `${iface.name} — ${iface.label}` : iface.name,
		})),
	];
	if (selected !== WAN_AUTO && !options.some((o) => o.value === selected)) {
		options.push({ value: selected, label: selected });
	}
	return options;
}

/**
 * Что сохранить при выборе пункта; null — сохранять нечего.
 *
 * Пара уходит целиком: половину бэкенд отвергнет. Повторный выбор текущего
 * пункта — no-op: `Dropdown` зовёт `onchange` и на нём, а каждое сохранение
 * тянет GET + PUT + перезагрузку конфига движка.
 */
export function planWanSelection(
	next: string,
	current: string,
): Partial<SingboxRouterSettings> | null {
	if (next === current) return null;
	return next === WAN_AUTO
		? { wanAutoDetect: true, wanInterface: '' }
		: { wanAutoDetect: false, wanInterface: next };
}
