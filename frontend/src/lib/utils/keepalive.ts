/**
 * effectiveKeepalive — значение keepalive, которое реально уходит на прошивку:
 * одиночное как есть, диапазон AWG 3.0 — по нижней границе. Пусто, 0, вне u16
 * и мусор дают null: слать нечего, и показывать в карточке тоже нечего.
 *
 * Зеркало storage.Keepalive.Effective() (internal/storage/types.go). Правка
 * одной стороны без другой означает, что карточка врёт про поведение роутера.
 */
export function effectiveKeepalive(raw: string | number | null | undefined): number | null {
	const lower = String(raw ?? '').split('-')[0];
	// JS trim() снимает U+FEFF, а Go strings.TrimSpace пробелом его не считает.
	// Без этой строки карточка показала бы «применяется 25 с» на значении,
	// которое бэкенд отвергает целиком.
	if (lower.includes('\uFEFF')) return null;
	const trimmed = lower.trim();
	if (!/^\d+$/.test(trimmed)) return null;
	const n = Number(trimmed);
	if (n === 0 || n > 65535) return null;
	return n;
}

/**
 * keepaliveHint — число для подписи «применяется N с» в карточке туннеля, или
 * null, когда подписи быть не должно. Подпись объясняет ровно одну ситуацию:
 * NativeWG отдаёт keepalive прошивке числом и схлопывает диапазон в нижнюю
 * границу. Kernel понимает диапазон целиком, одиночное значение не меняется
 * нигде, а на неизвестном бэкенде утверждать нечего.
 */
export function keepaliveHint(
	backend: string | null | undefined,
	raw: string | number | null | undefined,
): number | null {
	if (backend !== 'nativewg') return null;
	if (!String(raw ?? '').includes('-')) return null;
	return effectiveKeepalive(raw);
}
