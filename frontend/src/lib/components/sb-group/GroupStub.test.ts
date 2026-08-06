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

	// До волны 5D2a заглушка уводила на «Движок»: он нёс содержимое старой
	// /sb/routing. Содержимое снесено, живого редактора нет нигде — ссылка
	// стала бы тупиком, поэтому её нет, а строка честно называет волну.
	it('никуда не уводит: до своей волны функции нет в интерфейсе', () => {
		const { queryByRole, getByText } = render(GroupStub, {
			props: { title: 'DNS', wave: '5D2c', source: 'DnsTab' },
		});
		expect(queryByRole('link')).toBeNull();
		expect(getByText(/До волны 5D2c этой функции нет в интерфейсе/)).toBeTruthy();
	});
});
