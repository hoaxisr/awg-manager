// Компактное форматирование счётчика для чип-badge (мокап «184k»).
// Большие значения соединений сжимаем, чтобы чип не распухал. До 1000 — как есть.

/**
 * Форматирует неотрицательное целое в компактную строку:
 *   0..999       → «0».. «999»
 *   1_000..      → «1k», «12k», «184k», «1.2M», …
 * Дробь показываем одним знаком только когда < 10 единиц масштаба (12k, не 12.3k;
 * 1.2k — да), чтобы badge оставался узким и стабильным.
 */
export function formatCompactCount(n: number): string {
	if (!Number.isFinite(n) || n <= 0) return '0';
	const v = Math.floor(n);
	if (v < 1000) return String(v);

	const units = [
		{ d: 1_000_000_000, s: 'B' },
		{ d: 1_000_000, s: 'M' },
		{ d: 1_000, s: 'k' },
	];
	for (const { d, s } of units) {
		if (v >= d) {
			const scaled = v / d;
			const text = scaled < 10 ? scaled.toFixed(1).replace(/\.0$/, '') : String(Math.floor(scaled));
			return `${text}${s}`;
		}
	}
	return String(v);
}
