import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { locale } from '$lib/i18n';
import TunnelListActions from './TunnelListActions.svelte';
import ChipMultiSelect from './ChipMultiSelect.svelte';
import ConnectionsGroupBar from '../connections/ConnectionsGroupBar.svelte';
import ConnectionsTotalsBar from '../connections/ConnectionsTotalsBar.svelte';
import { routeLabel } from '$lib/utils/connectionsView';
import type { ConntrackConnection } from '$lib/types';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('ui / connections i18n', () => {
	it('TunnelListActions: прежние русские умолчания проп-значений переключаются на английский', () => {
		render(TunnelListActions, {
			props: { variant: 'labeled', onEdit: vi.fn(), onTest: vi.fn(), onDelete: vi.fn() },
		});
		expect(screen.getByRole('button', { name: 'Изменить' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Тест' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Удалить' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByRole('button', { name: 'Edit' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Test' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Delete' })).toBeTruthy();
	});

	it('TunnelListActions: явно переданные подписи имеют приоритет над текстом по умолчанию', () => {
		render(TunnelListActions, {
			props: { onTest: vi.fn(), testTitle: 'Проверить туннель' },
		});
		expect(screen.getByRole('button', { name: 'Проверить туннель' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByRole('button', { name: 'Проверить туннель' })).toBeTruthy();
	});

	it('ChipMultiSelect: плейсхолдер по умолчанию следует за языком, переданный — нет', () => {
		const { unmount } = render(ChipMultiSelect, {
			props: { values: [], options: [{ value: 'a', label: 'A' }], onchange: vi.fn() },
		});
		expect(screen.getByText('Не выбрано')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Not selected')).toBeTruthy();
		unmount();

		render(ChipMultiSelect, {
			props: { values: [], options: [], placeholder: 'Свой текст', onchange: vi.fn() },
		});
		expect(screen.getByText('Свой текст')).toBeTruthy();
	});

	it('ConnectionsGroupBar: сегменты группировки и счётчик переключаются на английский', () => {
		render(ConnectionsGroupBar, {
			props: { group: 'none', visible: 3, total: 10, onChange: vi.fn() },
		});
		expect(screen.getByText('Группировка:')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'По клиенту' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Group:')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'By client' })).toBeTruthy();
		expect(screen.getByText(/Visible:/)).toBeTruthy();
	});

	it('ConnectionsTotalsBar: слово «соединение» склоняется по числу в обоих языках', () => {
		const props = {
			bytesOut: 0,
			bytesIn: 0,
			fetchedAt: '',
			loading: false,
			progress: 0,
			onRefresh: vi.fn(),
		};
		const stats = (total: number) => ({ total, protocols: { tcp: 0, udp: 0, icmp: 0 } });
		const { container, rerender } = render(ConnectionsTotalsBar, {
			props: { ...props, stats: stats(1) } as never,
		});
		const seg = () => container.querySelector('.seg')?.textContent?.replace(/\s+/g, ' ').trim();
		expect(seg()).toBe('Всего: 1 соединение');

		rerender({ ...props, stats: stats(3) } as never);
		flushSync();
		expect(seg()).toBe('Всего: 3 соединения');

		rerender({ ...props, stats: stats(7) } as never);
		flushSync();
		expect(seg()).toBe('Всего: 7 соединений');

		locale.set('en');
		flushSync();
		expect(seg()).toBe('Total: 7 connections');

		rerender({ ...props, stats: stats(1) } as never);
		flushSync();
		expect(seg()).toBe('Total: 1 connection');
	});

	it('routeLabel: подпись локального маршрута берётся из словаря при вызове', () => {
		const conn = { routeClass: 'local' } as ConntrackConnection;
		expect(routeLabel(conn)).toBe('Локально');

		locale.set('en');
		expect(routeLabel(conn)).toBe('Local');
	});
});
