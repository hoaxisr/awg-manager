import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import ProxyBinariesCard, { type ProxyBinaryRow } from './ProxyBinariesCard.svelte';

const row = (key: ProxyBinaryRow['key'], label: string, present = false): ProxyBinaryRow => ({
	key,
	label,
	present,
	installAvailable: true,
	updateAvailable: false,
	busy: false,
	instances: 0,
	oninstall: vi.fn(),
	onuninstall: vi.fn(),
});
const phobosKernel = createRawSnippet(() => ({ render: () => '<div data-testid="kernel">Phobos в ядре</div>' }));

describe('ProxyBinariesCard', () => {
	// Пол ширины держится на селекторе :only-child: вторая кнопка в ветке
	// «не установлен» молча отвязала бы его.
	it('у неустановленной подсистемы «Установить» — единственная кнопка', () => {
		render(ProxyBinariesCard, { rows: [row('wdtt', 'WDTT'), row('freeturn', 'FreeTurn')] });
		const groups = document.querySelectorAll('.component-actions');
		expect(groups.length).toBe(2);
		for (const g of groups) {
			expect([...g.children].map((c) => c.textContent?.trim())).toEqual(['Установить']);
		}
	});

	it('выключатель ядра — под строкой Phobos', () => {
		render(ProxyBinariesCard, {
			rows: [row('obf-phobos', 'wg-obfuscator (Phobos)', true), row('obf-clusterm', 'wg-obfuscator (ClusterM)')],
			phobosKernel,
		});
		expect(screen.getByTestId('kernel').previousElementSibling?.textContent).toContain('Phobos');
	});

	it('строки Phobos нет (не наша арка) — выключатель ядра всё равно виден', () => {
		render(ProxyBinariesCard, { rows: [], phobosKernel });
		expect(screen.getAllByTestId('kernel')).toHaveLength(1);
	});

	it('показывать нечего — карточки нет', () => {
		const { container } = render(ProxyBinariesCard, { rows: [] });
		expect(container.querySelector('.card')).toBeNull();
	});
});
