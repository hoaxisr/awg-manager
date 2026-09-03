import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, within, waitFor } from '@testing-library/svelte';
import McpCard from './McpCard.svelte';
import type { McpKey } from '$lib/types';

const keys: McpKey[] = [{ id: 'k1', name: 'laptop', createdAt: '2026-09-02T10:00:00Z' }];

const base = {
	saving: false,
	origin: 'http://192.168.1.1:2222',
	ontoggle: vi.fn(),
	oncreate: vi.fn(async (name: string) => ({ id: 'k9', name, createdAt: '2026-09-02T11:00:00Z', key: 'awgm_secret' })),
	onrevoke: vi.fn(async () => {}),
};

describe('McpCard', () => {
	it('выключен — ключи и адрес скрыты, тумблер виден', () => {
		render(McpCard, { ...base, enabled: false, keys });
		expect(screen.getByText('MCP-сервер')).toBeTruthy();
		expect(screen.queryByText('laptop')).toBeNull();
		expect(screen.queryByText(/\/mcp$/)).toBeNull();
	});

	it('включён — показывает адрес эндпоинта и список ключей', () => {
		render(McpCard, { ...base, enabled: true, keys });
		expect(screen.getByText('http://192.168.1.1:2222/mcp')).toBeTruthy();
		expect(screen.getByText('laptop')).toBeTruthy();
	});

	it('создание ключа показывает plaintext один раз и вызывает oncreate', async () => {
		render(McpCard, { ...base, enabled: true, keys: [] });
		await fireEvent.click(screen.getByText('Создать ключ'));
		const dialog = screen.getByRole('dialog');
		await fireEvent.input(within(dialog).getByPlaceholderText('Например, laptop'), { target: { value: 'phone' } });
		await fireEvent.click(within(dialog).getByText('Создать'));
		await waitFor(() => expect(base.oncreate).toHaveBeenCalledWith('phone'));
		await waitFor(() => expect(screen.getByText('awgm_secret')).toBeTruthy());
		expect(screen.getByText(/claude mcp add/)).toBeTruthy();
	});

	it('отзыв только после подтверждения', async () => {
		render(McpCard, { ...base, enabled: true, keys });
		await fireEvent.click(screen.getByText('Отозвать'));
		expect(base.onrevoke).not.toHaveBeenCalled();
		await fireEvent.click(within(screen.getByRole('dialog')).getByText('Отозвать'));
		await waitFor(() => expect(base.onrevoke).toHaveBeenCalledWith('k1'));
	});

	it('отзыв: если onrevoke отклоняется, диалог всё равно закрывается и не зависает в busy', async () => {
		const onrevoke = vi.fn(async () => {
			throw new Error('boom');
		});
		render(McpCard, { ...base, enabled: true, keys, onrevoke });
		await fireEvent.click(screen.getByText('Отозвать'));
		const dialog = screen.getByRole('dialog');
		await fireEvent.click(within(dialog).getByText('Отозвать'));
		await waitFor(() => expect(onrevoke).toHaveBeenCalledWith('k1'));
		// The confirm dialog must close even on failure — a hung dialog on a
		// destructive action leaves the user unable to tell what happened.
		await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
	});

	it('показанный один раз ключ можно скопировать явной кнопкой', async () => {
		render(McpCard, { ...base, enabled: true, keys: [] });
		await fireEvent.click(screen.getByText('Создать ключ'));
		const dialog = screen.getByRole('dialog');
		await fireEvent.input(within(dialog).getByPlaceholderText('Например, laptop'), { target: { value: 'phone' } });
		await fireEvent.click(within(dialog).getByText('Создать'));
		await waitFor(() => expect(screen.getByText('awgm_secret')).toBeTruthy());
		expect(within(dialog).getByLabelText('Скопировать ключ')).toBeTruthy();
	});
});
