// Дедуп авто-ensure туннеля (W-19, W-20).
//
// Гварды живут в модуле, а не в компоненте детали: `{#key}` пересоздаёт деталь
// на каждый перевыбор инстанса, и гварды внутри неё сбрасывались бы — возврат
// на инстанс слал бы новый POST `ensure-*-tunnel` (идемпотентный, но при
// отказе ручки тост повторялся бы на каждый выбор). Так на инстанс приходится
// ровно один автоматический POST за жизнь страницы.

/** Инстансы, чей туннель уже есть — автозавод больше не нужен. */
const settled = new Set<string>();
/** Время последней попытки: на ошибке инстанс не settled, но и не долбится. */
const attempt = new Map<string, number>();

const COOLDOWN_MS = 20000;

/**
 * Можно ли слать ensure. Ручной вызов игнорирует и кулдаун, и settled:
 * пользователь нажал кнопку — значит, ждёт запроса.
 */
export function allowEnsure(id: string, manual: boolean): boolean {
	const now = Date.now();
	if (!manual) {
		if (settled.has(id)) return false;
		if (now - (attempt.get(id) ?? 0) < COOLDOWN_MS) return false;
	}
	attempt.set(id, now);
	return true;
}

/** Туннель инстанса есть — автозавод для него закончен. */
export function markEnsured(id: string): void {
	settled.add(id);
}
