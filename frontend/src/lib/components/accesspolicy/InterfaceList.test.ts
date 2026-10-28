import { describe, it, expect, vi } from 'vitest';

// Сторы темы при импорте обращаются к matchMedia — в jsdom его нет.
vi.hoisted(() => {
	Object.defineProperty(window, 'matchMedia', {
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
		}),
	});
});
import { render, fireEvent } from '@testing-library/svelte';
import InterfaceList from './InterfaceList.svelte';

const props = (held?: string[]) => ({
	interfaces: [{ name: 'PPPoE0', order: 0, denied: false }],
	availableInterfaces: [
		{ name: 'PPPoE0', label: 'WAN', up: true },
		{ name: 'OpkgTun10', label: 'warp', up: true },
		{ name: 'Wireguard1', label: 'wg', up: true },
	],
	held,
	onpermit: () => {},
	ondeny: () => {},
	onreorder: () => {},
	onupdate: () => {},
});

// Туннель, снятый мониторингом, виден в политике с пометкой и не предлагается
// к добавлению: монитор вернёт его сам (F752).
describe('InterfaceList: снятый мониторингом туннель', () => {
	it('показан с пометкой и исключён из «Добавить»', async () => {
		const { container, getByText } = render(InterfaceList, { props: props(['OpkgTun10']) });
		const held = container.querySelectorAll('.iface-row.held');
		expect(held).toHaveLength(1);
		expect(held[0].querySelector('.iface-label')?.getAttribute('title')).toBe('OpkgTun10');

		await fireEvent.click(getByText(/Добавить|Add/));
		const offered = [...container.querySelectorAll('.dropdown-item .iface-name')].map((e) => e.textContent);
		expect(offered.join('|')).not.toContain('warp');
		expect(offered.join('|')).toContain('wg');
	});

	it('без удержаний пометки нет', () => {
		const { container } = render(InterfaceList, { props: props() });
		expect(container.querySelectorAll('.iface-row.held')).toHaveLength(0);
	});
});
