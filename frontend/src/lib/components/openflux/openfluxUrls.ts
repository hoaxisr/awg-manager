/**
 * Разбирает строку из одного или нескольких URL (через запятую, перенос строки, пробел или `|`).
 * Совместимо с openflux-core/transport/registry/group.go (`groupURLs`).
 */
export function splitTransportUrls(raw?: string | null): string[] {
	if (!raw) return [];
	return raw
		.split(/[\s,|]+/)
		.map((s) => s.trim())
		.filter(Boolean);
}

/** Отображает сохранённые через запятую ссылки по одной на строку для удобного редактирования. */
export function formatUrlsForTextarea(raw?: string | null): string {
	return splitTransportUrls(raw).join('\n');
}

/** Собирает несколько URL (в том числе по строкам) в компактную строку через запятую для бэкенда. */
export function normalizeTransportUrls(raw?: string | null): string {
	return splitTransportUrls(raw).join(',');
}

/** Человекочитаемая подпись количества параллельных линий страйпа. */
export function laneCountLabel(count: number): string {
	const mod10 = count % 10;
	const mod100 = count % 100;
	if (mod10 === 1 && mod100 !== 11) return `${count} линия · stripe`;
	if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return `${count} линии · stripe`;
	return `${count} линий · stripe`;
}

/** Проверяет, содержит ли вставленная строка/файл/JSON основную авторизационную cookie Mail.ru (`Mpop`). */
export function hasMailruLoginCookie(raw?: string | null): boolean {
	if (!raw) return false;
	const text = raw.trim();
	if (!text) return false;
	return (
		/(?:^|[;\s])Mpop=[^;\s]+/.test(text) ||
		/\tMpop\t\S+/.test(text) ||
		/"name"\s*:\s*"Mpop"/.test(text) ||
		/"Mpop"\s*:\s*"[^"]+"/.test(text)
	);
}
