// Решающая логика блока «Абоненты» (ia.md §3.3 часть А, спека §4.4).
// Чистые функции: компонент только рисует то, что здесь решено, а юниты
// проверяют матрицу кнопок и разбор отказов без рендера.
import type { WdttPanelUserEntry, WdttServerClientsReload } from '$lib/types';

/** Тексты берутся по ID микрокопии; своих строк в блоке нет. */
export const CLIENT_TEXT = {
	/** SH-33 */
	linkExpired:
		'Абонент просрочен — ссылка не будет работать. Выдайте новую кнопкой «Перевыпустить».',
	/** SH-36 */
	removeMainPassword:
		'Пароль абонента совпадает с главным паролем сервера. Удаление в два хода: сначала смените главный пароль, потом удалите абонента.',
	/** SH-37 */
	removeLastUsable:
		'Нельзя удалить последнего рабочего абонента: без единого рабочего пароля сервер не запустится.',
	/** TS-13 */
	mainPasswordUnset: 'Сначала задайте главный пароль сервера',
	/** TS-14 */
	passwordIsMain:
		'Это главный пароль сервера — задайте абоненту другой или оставьте поле пустым, чтобы сгенерировать',
	/** TS-15 */
	passwordExpiredOwner: 'Пароль принадлежит просроченному абоненту, задайте новый',
	/** TS-16 */
	passwordTaken: 'Пароль занят живым абонентом',
} as const;

/**
 * Рабочий абонент — тот же предикат, что у бэкенда
 * (`ServerClientUnusableReason`, `passwords_json.go:206`): пустой пароль,
 * главный пароль, просрочка. `isDeactivated` в предикат НЕ входит — такая
 * запись пишется в `passwords.json` и держит стража последнего рабочего
 * (оговорка микрокопии SH-28/SH-38).
 */
export function isUsable(user: WdttPanelUserEntry): boolean {
	return !!user.password.trim() && !user.isMainPassword && !user.isExpired;
}

export function usableCount(users: WdttPanelUserEntry[]): number {
	return users.filter(isUsable).length;
}

/** SH-38: счётчик в подвале блока. */
export function counterLabel(users: WdttPanelUserEntry[]): string {
	return `Абонентов: ${users.length} · рабочих: ${usableCount(users)}`;
}

export interface RowActions {
	/** Ссылка: доступна, заблокирована с причиной или её нет вовсе. */
	link: 'yes' | 'blocked' | 'hidden';
	linkHint: string;
	/** «Перевыпустить» — только у просроченного. */
	reissue: boolean;
	remove: 'yes' | 'blocked';
	removeHint: string;
}

/**
 * Матрица кнопок строки — спека §4.4. Порядок веток важен: запись с главным
 * паролем разбирается первой, иначе просроченный главный пароль получил бы
 * кнопки просроченного абонента.
 */
export function rowActions(user: WdttPanelUserEntry, users: WdttPanelUserEntry[]): RowActions {
	if (user.isMainPassword) {
		return {
			link: 'hidden',
			linkHint: '',
			reissue: false,
			remove: 'blocked',
			removeHint: CLIENT_TEXT.removeMainPassword,
		};
	}
	if (user.isExpired) {
		return {
			link: 'blocked',
			linkHint: CLIENT_TEXT.linkExpired,
			reissue: true,
			// Просроченный не рабочий: страж последнего рабочего его не держит.
			remove: 'yes',
			removeHint: '',
		};
	}
	// Рабочий: удаление запрещено, когда после него рабочих не останется —
	// та же вторая линия, что у бэкенда (`refuseLastUsableServerClient`).
	const lastUsable = isUsable(user) && usableCount(users) === 1;
	return {
		link: 'yes',
		linkHint: '',
		reissue: false,
		remove: lastUsable ? 'blocked' : 'yes',
		removeHint: lastUsable ? CLIENT_TEXT.removeLastUsable : '',
	};
}

/**
 * SH-77: после удаления рабочих не остаётся — сервер заведёт «Абонент 1» сам,
 * иначе он не запустится. Считается по тому же предикату, что и страж.
 */
export function autoCreateAfterRemove(
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
	if (code === 'WDTT_SERVER_MAIN_PASSWORD_NOT_SAVED') return msg;
	const lower = msg.toLowerCase();
	if (lower.includes('сначала задайте пароль сервера')) return CLIENT_TEXT.mainPasswordUnset;
	if (lower.includes('совпадает с главным паролем')) return CLIENT_TEXT.passwordIsMain;
	if (lower.includes('просроченному абоненту')) return CLIENT_TEXT.passwordExpiredOwner;
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

/** Пароль записи, появившейся после добавления: бэкенд его не возвращает. */
export function addedPassword(
	before: WdttPanelUserEntry[],
	after: WdttPanelUserEntry[],
): string {
	const known = new Set(before.map((u) => u.password));
	return after.find((u) => !known.has(u.password))?.password ?? '';
}

/** Разряды тысяч разделяются пробелом — как в LK-11 («1 340 байт»). */
export function groupDigits(n: number): string {
	return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
}
