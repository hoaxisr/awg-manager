/**
 * Память «последний выбранный ребёнок меню» — общая для примитива Tabs
 * (выпадающая группа вкладок) и для SideNav (группа сайдбара).
 *
 * Пространство ключей одно на оба потребителя: пользователь воспринимает
 * «последний открытый раздел Sing-box» как одно свойство, независимо от того,
 * кликнул он по вкладке или по группе сайдбара.
 *
 * Ключ включает состав детей, иначе два меню с одинаковым label (например две
 * группы «Sing-box» — туннели и маршрутизация) делили бы одну ячейку.
 */
const PREFIX = 'ui.tabs.menuLast:';

export function menuMemoryKey(label: string, childIds: string[]): string {
	return `${PREFIX}${label}:${childIds.slice().sort().join(',')}`;
}

export function readMenuChild(label: string, childIds: string[]): string | null {
	if (typeof localStorage === 'undefined') return null;
	try {
		const raw = localStorage.getItem(menuMemoryKey(label, childIds));
		if (raw && childIds.includes(raw)) return raw;
	} catch {
		// приватный режим — память просто не работает
	}
	return null;
}

export function rememberMenuChild(label: string, childIds: string[], childId: string): void {
	if (!childIds.includes(childId)) return;
	if (typeof localStorage === 'undefined') return;
	try {
		localStorage.setItem(menuMemoryKey(label, childIds), childId);
	} catch {
		// приватный режим — память просто не работает
	}
}
