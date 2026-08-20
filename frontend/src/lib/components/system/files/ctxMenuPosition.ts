/**
 * Прижимает контекстное меню к точке клика так, чтобы оно целиком попало во
 * вьюпорт. Если элемент ещё не смонтирован — отдаёт точку клика как есть.
 */
export function clampMenuPosition(
	el: HTMLElement | undefined,
	anchorX: number,
	anchorY: number,
): { x: number; y: number } {
	if (!el) return { x: anchorX, y: anchorY };

	const pad = 8;
	const rect = el.getBoundingClientRect();
	const vw = window.innerWidth;
	const vh = window.innerHeight;
	let x = anchorX;
	let y = anchorY;
	if (x + rect.width > vw - pad) x = vw - rect.width - pad;
	if (y + rect.height > vh - pad) y = anchorY - rect.height;
	if (x < pad) x = pad;
	if (y < pad) y = pad;
	return { x, y };
}
