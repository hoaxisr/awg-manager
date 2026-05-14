/**
 * Share-link schemes often pasted as a single space-separated line (e.g. from messengers).
 * Longest prefixes first so `naive+https://` wins over `naive+http://`.
 * Space/tab only before a scheme — not full `\\s`, so multiline YAML/JSON indents are not touched.
 */
const SPACE_BEFORE_SCHEME_PATTERN =
	'[ \\t]+(naive\\+https://|naive\\+http://|hysteria2://|vless://|hy2://|trojan://|ss://)';

const spaceBeforeShareSchemeRe = new RegExp(SPACE_BEFORE_SCHEME_PATTERN, 'g');

/** Space(s) before a known share scheme → newline so each link is on its own line. */
export function normalizeSpaceSeparatedShareLinks(text: string): string {
	return text.replace(spaceBeforeShareSchemeRe, '\n$1');
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

