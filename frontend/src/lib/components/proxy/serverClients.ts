// Решающая логика блока «Абоненты» (ia.md §3.3 часть А, спека §4.4).
// Чистые функции: компонент только рисует то, что здесь решено, а юниты
// проверяют матрицу кнопок и разбор отказов без рендера.
import type { WdttPanelUserEntry, WdttServerClientsReload } from '$lib/types';

/** Тексты берутся по ID микрокопии; своих строк в блоке нет. */
export const CLIENT_TEXT = {
	removeLastUsable:
		'Нельзя удалить последнего рабочего абонента: без единого рабочего пароля сервер не запустится.',
	/** SH-91: гейт «Запустить» у WDTT-сервера без рабочих абонентов. */
	startNoUsable: 'Сервер не запускается без единого рабочего пароля — добавьте абонента',
	passwordTaken: 'Пароль занят живым абонентом',
	/** Подпись поля «VK-хеш» там, где подставить нечего: мастер и модалка. */
	vkHashRequired: 'Обязательно — без него ссылка не заработает',
} as const;

/**
 * Рабочий абонент: непустой пароль. Главного пароля у сервера больше нет,
 * поэтому и сравнивать не с чем — все записи равноправны.
 *
 * Просрочка и деактивация в предикат не входят: оба признака ставит ТОЛЬКО
 * форк — телеграм-бот (`/new`, server.go) и его админ-API
 * (`admin_api.go`), а у нас нет ни поля ввода, ни ручки, чтобы назначить срок
 * или отключить абонента. Состояния недостижимы, поэтому и веток под них нет.
 * Сами поля продолжаем зеркалить и сохранять при слиянии passwords.json —
 * чужое не теряем.
 */
export function isUsable(user: WdttPanelUserEntry): boolean {
	return !!user.password.trim();
}

export function usableCount(users: WdttPanelUserEntry[]): number {
	return users.filter(isUsable).length;
}

/** SH-38: счётчик в подвале блока. */
export function counterLabel(users: WdttPanelUserEntry[]): string {
	return `Абонентов: ${users.length} · рабочих: ${usableCount(users)}`;
}

export interface RowActions {
	/** «Перевыпустить» — у любого абонента: исключений больше нет. */
	reissue: boolean;
	remove: 'yes' | 'blocked';
	removeHint: string;
}

/** Матрица кнопок строки — спека §4.4. */
export function rowActions(user: WdttPanelUserEntry, users: WdttPanelUserEntry[]): RowActions {
	// Рабочий: удаление запрещено, когда после него рабочих не останется —
	// та же вторая линия, что у бэкенда (`refuseLastUsableServerClient`).
	const lastUsable = isUsable(user) && usableCount(users) === 1;
	return {
		// Перевыпуск доступен и рабочему: смена скомпрометированного пароля —
		// обычная нужда, а не только починка просрочки. Прежде кнопку давала
		// одна ветка `isExpired`, и живому абоненту ключ было не сменить —
		// оставалось снести сервер или завести дубль и удалить оригинал.
		// Стража последнего рабочего перевыпуск не задевает: порядок шагов
		// (добавить нового → удалить старого) держит рабочего всё время.
		reissue: true,
		remove: lastUsable ? 'blocked' : 'yes',
		removeHint: lastUsable ? CLIENT_TEXT.removeLastUsable : '',
	};
}

/**
 * SH-77: после удаления рабочих не остаётся, и запустить сервер будет нельзя,
 * пока не добавят нового абонента. Абонента взамен никто не заводит: на путях
 * UI «Абонент 1» больше не рождается (Дополнение №5). Считается по тому же
 * предикату, что и страж последнего рабочего.
 */
export function noUsableAfterRemove(
	user: WdttPanelUserEntry,
	users: WdttPanelUserEntry[],
): boolean {
	return usableCount(users.filter((u) => u.password !== user.password)) === 0;
}

/**
 * Бейдж шапки (спека §2.2): судьбу решает `reload` последней мутации состава,
 * а когда его нет (чтение, переименование) — факт работы инстанса.
 * `failed` — тоже «применится при следующем запуске»: «применено сейчас»
 * обещать нельзя.
 */
export function headerApplied(reload: WdttServerClientsReload | undefined, running: boolean): boolean {
	if (reload) return reload === 'delivered';
	return running;
}

/** Укороченный пароль строки; полный живёт в `title`. */
export function shortPassword(pass: string): string {
	if (pass.length <= 16) return pass;
	return `${pass.slice(0, 8)}…${pass.slice(-6)}`;
}

const NOT_WRITTEN_PREFIX = 'абонент создан, но не записан в файл сервера';

/** Код отказа из конверта бэкенда: по нему различаются частичные успехи. */
export function apiErrorCode(e: unknown): string {
	return (e as { body?: { code?: string } })?.body?.code ?? '';
}

/**
 * Текст отказа добавления. Два частичных успеха отличаются КОДОМ
 * (`internal/api/wdtt_server.go:218-224`):
 * - `WDTT_SERVER_CLIENT_ADD_NOT_APPLIED` — SH-26 дословно, ошибка бэкенда
 *   подставляется в шаблон;
 * - `WDTT_SERVER_MAIN_PASSWORD_NOT_SAVED` — текст бэкенда дословно: своей
 *   строки в микрокопии нет, и «абонент не создан» утверждать нельзя.
 * Прочие отказы приходят одним кодом `WDTT_SERVER_CLIENT_ADD_FAILED` —
 * различает их только текст (TS-13..TS-16).
 */
export function addErrorText(code: string, message: string): string {
	const msg = message.trim();
	if (code === 'WDTT_SERVER_CLIENT_ADD_NOT_APPLIED') {
		const lower = msg.toLowerCase();
		const idx = lower.indexOf(NOT_WRITTEN_PREFIX);
		const tail = idx === 0 ? msg.slice(NOT_WRITTEN_PREFIX.length).replace(/^[:\s]+/, '') : msg;
		// SH-26
		return `Абонент создан, но не записан в файл сервера: ${tail}. Сервер подхватит его при следующем запуске.`;
	}
	const lower = msg.toLowerCase();
	if (lower.includes('занят живым абонентом')) return CLIENT_TEXT.passwordTaken;
	return msg;
}

/**
 * Имя нового абонента при перевыпуске: то же, что у старого. Старая запись
 * в момент добавления ещё жива, поэтому её саму коллизией не считаем —
 * только чужие записи с тем же именем (ia.md §3.3, «<имя> (2)»).
 */
export function reissueName(
	user: WdttPanelUserEntry,
	users: WdttPanelUserEntry[],
): string {
	const base = (user.comment ?? '').trim();
	if (!base) return base;
	const taken = new Set(
		users
			.filter((u) => u.password !== user.password)
			.map((u) => (u.comment ?? '').trim())
			.filter(Boolean),
	);
	if (!taken.has(base)) return base;
	let n = 2;
	while (taken.has(`${base} (${n})`)) n += 1;
	return `${base} (${n})`;
}

/**
 * Пароль записи, появившейся после добавления: бэкенд его не возвращает.
 * До-список берётся и из состава панели, и из `clients` конфига сервера —
 * сверяется только пароль.
 */
export function addedPassword(
	before: { password: string }[],
	after: WdttPanelUserEntry[],
): string {
	const known = new Set(before.map((u) => u.password));
	return after.find((u) => !known.has(u.password))?.password ?? '';
}

/** Разряды тысяч разделяются пробелом — как в LK-11 («1 340 байт»). */
export function groupDigits(n: number): string {
	return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
}
