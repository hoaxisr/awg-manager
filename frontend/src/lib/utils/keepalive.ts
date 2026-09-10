/**
 * effectiveKeepalive — значение keepalive, которое реально уходит на прошивку:
 * одиночное как есть, диапазон AWG 3.0 — по нижней границе. Пусто, 0, вне u16
 * и мусор дают null: слать нечего, и показывать в карточке тоже нечего.
 *
 * Зеркало storage.Keepalive.Effective() (internal/storage/types.go). Правка
 * одной стороны без другой означает, что карточка врёт про поведение роутера.
 */
export function effectiveKeepalive(raw: string | number | null | undefined): number | null {
	const lower = String(raw ?? '').split('-')[0].trim();
	if (!/^\d+$/.test(lower)) return null;
	const n = Number(lower);
	if (n === 0 || n > 65535) return null;
	return n;
}
