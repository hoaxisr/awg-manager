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

	it('очевидно заглушка: сообщает, что раздел ещё не переехал', () => {
		const { getByText } = render(GroupStub, {
			props: { title: 'Мастер', wave: '5D2b', source: 'FlowGraph' },
		});
		expect(getByText('Раздел ещё не переехал')).toBeTruthy();
	});

	// Без этой ссылки страница — тупик: легаси-закладка (`?chip=dns`) приводит
	// сюда редиректом, а живой редактор до волны 5D2 остаётся на «Движке».
	it('уводит на «Движок», где функция живёт до своей волны', () => {
		const { getByRole } = render(GroupStub, {
			props: { title: 'DNS', wave: '5D2c', source: 'DnsTab' },
		});
		const link = getByRole('link', { name: 'Движок' });
		expect(link.getAttribute('href')).toBe('/sb/engine');
		// Волна названа рядом со ссылкой: «до каких пор» — половина подсказки.
		expect(link.parentElement?.textContent).toContain('До волны 5D2c');
	});
});
