import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import AccessCard from './AccessCard.svelte';

// HttpServerCard внутри карточки ходит в API на монтировании — глушим.
vi.mock('$lib/api/client', () => ({
	api: {
		serverListenState: vi.fn(() => new Promise(() => {})),
		getAllInterfaces: vi.fn(async () => []),
	},
}));

describe('AccessCard', () => {
	it('вход выключен — поля времени сессии нет', () => {
		render(AccessCard, { authEnabled: false, sessionTtlHours: 24, ontoggleAuth: vi.fn(), onsaveSessionTtl: vi.fn() });
		expect(document.getElementById('sessionTtlHours')).toBeNull();
	});

	it('сохранение отдаёт наверх ограниченное число часов', async () => {
		const onsave = vi.fn();
		render(AccessCard, { authEnabled: true, sessionTtlHours: 24, ontoggleAuth: vi.fn(), onsaveSessionTtl: onsave });
		const input = document.getElementById('sessionTtlHours') as HTMLInputElement;
		expect(input.value).toBe('24');
		await fireEvent.input(input, { target: { value: '5000' } });
		await fireEvent.click(await screen.findByRole('button', { name: /сохранить/i }));
		expect(onsave).toHaveBeenCalledWith(720);
	});
});
