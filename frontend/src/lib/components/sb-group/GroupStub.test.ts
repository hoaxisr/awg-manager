import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import GroupStub from './GroupStub.svelte';

describe('GroupStub', () => {
	it('показывает подпись пункта заголовком страницы', () => {
		const { getByRole } = render(GroupStub, {
			props: { title: 'Rule sets', wave: '5D2c', source: 'RuleSetsTable' },
		});
		expect(getByRole('heading', { level: 1 }).textContent).toBe('Rule sets');
	});

	it('называет волну 5D2 и источник содержимого', () => {
		const { getByText } = render(GroupStub, {
			props: { title: 'DNS', wave: '5D2c', source: 'DnsTab' },
		});
		// Волна и источник — единственное, ради чего заглушка существует: волна
		// 5D2 не должна искать, откуда приедет содержимое.
		expect(getByText(/волна 5D2c/)).toBeTruthy();
		expect(getByText(/DnsTab/)).toBeTruthy();
	});

	it('очевидно заглушка: сообщает, что страница пустая', () => {
		const { getByText } = render(GroupStub, {
			props: { title: 'Мастер', wave: '5D2b', source: 'FlowGraph' },
		});
		expect(getByText('Страница пока пустая')).toBeTruthy();
	});
});
