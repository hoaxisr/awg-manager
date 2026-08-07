import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import Dropdown from './Dropdown.svelte';

// Dropdown пересчитывает позицию панели через ResizeObserver, которого нет в jsdom.
class ResizeObserverStub {
	observe(): void {}
	disconnect(): void {}
}
vi.stubGlobal('ResizeObserver', ResizeObserverStub);

const OPTIONS = [
	{ value: '', label: 'Автоматически' },
	{ value: 'ppp0', label: 'ppp0 — Letai (PPPoE)' },
];

function trigger(container: HTMLElement): HTMLButtonElement {
	return container.querySelector('.trigger') as HTMLButtonElement;
}

async function pick(label: string): Promise<void> {
	const option = [...document.querySelectorAll('.dropdown-panel .option')].find((el) =>
		(el.textContent ?? '').trim().startsWith(label),
	);
	if (!option) throw new Error(`пункт «${label}» не найден`);
	await fireEvent.click(option);
}

describe('Dropdown', () => {
	it('по умолчанию коммитит выбор оптимистично', async () => {
		const onchange = vi.fn();
		const { container } = render(Dropdown, { props: { value: '', options: OPTIONS, onchange } });
		await fireEvent.click(trigger(container));
		await pick('ppp0');
		expect(onchange).toHaveBeenCalledWith('ppp0');
		expect(trigger(container).textContent).toContain('ppp0');
	});

	// controlled: значением владеет родитель. Ровно та же роль, что у
	// `controlled` у Toggle, и нужна она по той же причине: при провале
	// сохранения родитель проп НЕ меняет, а самовольно записанный пункт остаётся
	// на экране навсегда (Svelte снимает локальную запись $bindable-пропа только
	// при СМЕНЕ значения сверху — перезагрузка стора тем же значением её не
	// снимает).
	it('controlled: зовёт onchange, но пункт не меняет — его выбирает родитель', async () => {
		const onchange = vi.fn();
		const { container } = render(Dropdown, {
			props: { value: '', options: OPTIONS, controlled: true, onchange },
		});
		await fireEvent.click(trigger(container));
		await pick('ppp0');
		expect(onchange).toHaveBeenCalledWith('ppp0');
		expect(trigger(container).textContent).toContain('Автоматически');
	});

	// Обратная сторона: удачное сохранение обязано доехать до экрана, иначе
	// список замер бы навсегда.
	it('controlled: принятый родителем выбор показывает', async () => {
		const props = { value: '', options: OPTIONS, controlled: true };
		const { container, rerender } = render(Dropdown, { props });
		await fireEvent.click(trigger(container));
		await pick('ppp0');
		await rerender({ ...props, value: 'ppp0' });
		expect(trigger(container).textContent).toContain('ppp0');
	});
});
