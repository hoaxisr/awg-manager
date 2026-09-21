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
): { text: string; error: string } {
	const text = fileText.replace(/^\uFEFF/, '');
	if (!current.trim()) return { text, error: '' };
	// \u0421\u043C\u0435\u0448\u0438\u0432\u0430\u0442\u044C JSON/YAML-\u043A\u043E\u043D\u0444\u0438\u0433 \u0441 \u0443\u0436\u0435 \u0432\u0432\u0435\u0434\u0451\u043D\u043D\u044B\u043C\u0438 \u0441\u0442\u0440\u043E\u043A\u0430\u043C\u0438 \u043D\u0435\u043B\u044C\u0437\u044F: \u0430\u0432\u0442\u043E\u0434\u0435\u0442\u0435\u043A\u0442
	// \u0444\u043E\u0440\u043C\u0430\u0442\u0430 \u043D\u0430 \u0431\u044D\u043A\u0435\u043D\u0434\u0435 \u0440\u0430\u0431\u043E\u0442\u0430\u0435\u0442 \u043F\u043E \u0426\u0415\u041B\u041E\u041C\u0423 \u0442\u0435\u043B\u0443, \u0438 JSON-\u0441\u0442\u0440\u043E\u043A\u0438 \u043F\u043E\u0441\u043B\u0435 \u0441\u0441\u044B\u043B\u043E\u043A
	// \u043C\u043E\u043B\u0447\u0430 \u0432\u044B\u043F\u0430\u043B\u0438 \u0431\u044B \u043F\u0440\u0438 \u043F\u043E\u0441\u0442\u0440\u043E\u0447\u043D\u043E\u043C \u0440\u0430\u0437\u0431\u043E\u0440\u0435. \u041E\u0448\u0438\u0431\u043A\u0430 \u0432\u043C\u0435\u0441\u0442\u043E \u0442\u0438\u0445\u043E\u0439 \u0441\u043A\u043B\u0435\u0439\u043A\u0438.
	const fileIsConfig = /^\s*[{[]/.test(text) || /^\s*proxies\s*:/m.test(text);
	const currentIsConfig = /^\s*[{[]/.test(current);
	if (fileIsConfig || currentIsConfig) {
		return {
			text: current,
			error:
				'JSON/YAML-\u043A\u043E\u043D\u0444\u0438\u0433 \u0438\u043C\u043F\u043E\u0440\u0442\u0438\u0440\u0443\u0435\u0442\u0441\u044F \u0442\u043E\u043B\u044C\u043A\u043E \u0446\u0435\u043B\u0438\u043A\u043E\u043C: \u043E\u0447\u0438\u0441\u0442\u0438\u0442\u0435 \u043F\u043E\u043B\u0435 \u043F\u0435\u0440\u0435\u0434 \u0437\u0430\u0433\u0440\u0443\u0437\u043A\u043E\u0439 \u0444\u0430\u0439\u043B\u0430 \u0438\u043B\u0438 \u0441\u043E\u0437\u0434\u0430\u0439\u0442\u0435 \u043E\u0442\u0434\u0435\u043B\u044C\u043D\u0443\u044E \u043F\u043E\u0434\u043F\u0438\u0441\u043A\u0443',
		};
	}
	return { text: current.replace(/\n?$/, '\n') + text, error: '' };
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
