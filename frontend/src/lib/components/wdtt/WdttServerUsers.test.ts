import { describe, it, expect, vi } from 'vitest';

// Компонент тянет barrel $lib/components/ui → theme-store читает matchMedia на импорте.
vi.hoisted(() => {
	Object.defineProperty(globalThis, 'matchMedia', {
		writable: true,
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			onchange: null,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
			dispatchEvent: () => false
		})
	});
});

const getWdttServerPanelUsers = vi.hoisted(() => vi.fn());
vi.mock('$lib/api/client', () => ({ api: { getWdttServerPanelUsers } }));

import { render, screen, waitFor } from '@testing-library/svelte';
import WdttServerUsers from './WdttServerUsers.svelte';

const MAIN = 'mainpass0000000000000000';

function status(available: boolean) {
	return {
		available,
		users: [
			{
				password: MAIN,
				comment: 'Основной',
				isMainPassword: true,
				isDeactivated: false,
				isExpired: false,
				isAuto: false
			},
			{
				password: '3f9a1c77b21e4d0a9c4e5b',
				comment: 'Клиент Иван',
				isMainPassword: false,
				isDeactivated: false,
				isExpired: false,
				isAuto: false
			}
		]
	};
}

describe('WdttServerUsers', () => {
	it('показывает основной пароль отдельной строкой и не даёт его удалить', async () => {
		getWdttServerPanelUsers.mockResolvedValueOnce(status(true));
		render(WdttServerUsers, { props: { serverInstanceId: 'default', serverMainPassword: MAIN } });

		await waitFor(() => expect(screen.getByText('Основной')).toBeTruthy());
		expect(screen.getByText('основной')).toBeTruthy();
		expect(screen.getByText('Клиент Иван')).toBeTruthy();
		// «Удалить» только у клиента с отдельным паролем.
		expect(screen.getAllByRole('button', { name: 'Удалить' })).toHaveLength(1);
	});

	it('предупреждает, когда panel.db недоступна, но список не прячет', async () => {
		getWdttServerPanelUsers.mockResolvedValueOnce(status(false));
		render(WdttServerUsers, { props: { serverInstanceId: 'default', serverMainPassword: MAIN } });

		await waitFor(() => expect(screen.getByText('Клиент Иван')).toBeTruthy());
		expect(screen.getByText(/panel\.db сейчас недоступна/)).toBeTruthy();
	});
});
