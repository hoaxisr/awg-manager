/** Ближайший прокручиваемый предок: overflow-y auto/scroll/overlay с реальным переполнением. */
export function findScrollContainer(start: HTMLElement | null): HTMLElement | null {
	let el = start?.parentElement ?? null;
	while (el) {
		const { overflowY } = getComputedStyle(el);
		if (
			(overflowY === 'auto' || overflowY === 'scroll' || overflowY === 'overlay')
			&& el.scrollHeight > el.clientHeight + 1
		) {
			return el;
		}
		el = el.parentElement;
	}
	return null;
}
