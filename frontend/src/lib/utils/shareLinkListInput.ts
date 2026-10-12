/**
 * Share-link schemes often pasted as a single space-separated line (e.g. from messengers).
 * Longest prefixes first so `naive+https://` wins over `naive+http://`.
 * Space/tab only before a scheme — not full `\\s`, so multiline YAML/JSON indents are not touched.
 */
const SPACE_BEFORE_SCHEME_PATTERN =
	'[ \\t]+(naive\\+https://|naive\\+http://|hysteria2://|vless://|hy2://|trojan://|ss://|mierus://|mieru://|tt://)';

const spaceBeforeShareSchemeRe = new RegExp(SPACE_BEFORE_SCHEME_PATTERN, 'g');

/** Space(s) before a known share scheme → newline so each link is on its own line. */
export function normalizeSpaceSeparatedShareLinks(text: string): string {
	return text.replace(spaceBeforeShareSchemeRe, '\n$1');
}

/**
 * Содержимое загруженного файла (share-link'и, Clash YAML, sing-box/mieru JSON)
 * в textarea импорта: пустое поле — заменяем целиком, непустое — дописываем
 * с новой строки. Текст файла не нормализуем и не тримим (кроме ведущего BOM):
 * многострочный JSON/YAML должен уйти на бэкенд как есть.
 */
export function appendImportedFileText(
	current: string,
	fileText: string,
): { text: string; rejected: boolean } {
	const text = fileText.replace(/^\uFEFF/, '');
	if (!current.trim()) return { text, rejected: false };
	// Смешивать JSON/YAML-конфиг с уже введёнными строками нельзя: автодетект
	// формата на бэкенде работает по ЦЕЛОМУ телу, и JSON-строки после ссылок
	// молча выпали бы при построчном разборе. Ошибка вместо тихой склейки.
	const fileIsConfig = /^\s*[{[]/.test(text) || /^\s*proxies\s*:/m.test(text);
	const currentIsConfig = /^\s*[{[]/.test(current);
	if (fileIsConfig || currentIsConfig) {
		return {
			text: current,
			rejected: true,
		};
	}
	return { text: current.replace(/\n?$/, '\n') + text, rejected: false };
}

export function mergePastedShareList(
	current: string,
	selectionStart: number,
	selectionEnd: number,
	pasted: string,
): { next: string; caret: number } {
	const normalized = normalizeSpaceSeparatedShareLinks(pasted);
	const next = current.slice(0, selectionStart) + normalized + current.slice(selectionEnd);
	return { next, caret: selectionStart + normalized.length };
}
