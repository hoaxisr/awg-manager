import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, within, waitFor } from '@testing-library/svelte';
import McpCard from './McpCard.svelte';
import type { McpKey } from '$lib/types';

const notify = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock('$lib/stores/notifications', () => ({ notifications: notify }));

const clipboardMock = vi.hoisted(() => vi.fn());
vi.mock('$lib/utils/clipboard', () => ({ copyToClipboard: clipboardMock }));

const keys: McpKey[] = [{ id: 'k1', name: 'laptop', createdAt: '2026-09-02T10:00:00Z' }];

const base = {
	saving: false,
	origin: 'http://192.168.1.1:2222',
	ontoggle: vi.fn(),
	oncreate: vi.fn(async (name: string) => ({ id: 'k9', name, createdAt: '2026-09-02T11:00:00Z', key: 'awgm_secret' })),
	onrevoke: vi.fn(async () => {}),
};

describe('McpCard', () => {
	beforeEach(() => {
		notify.success.mockClear();
		notify.error.mockClear();
		clipboardMock.mockReset();
	});

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

	// Владелец репозитория сообщил: адрес «выглядит редактируемым», и его
	// «нельзя скопировать». Оба дефекта были в одном и том же <button>
	// (текстовое поле по стилю, копирование без уведомления об успехе/ошибке).
	it('адрес эндпоинта — текст, а не кнопка/поле ввода, копируется явной кнопкой с уведомлением', async () => {
		clipboardMock.mockResolvedValue(true);
		render(McpCard, { ...base, enabled: true, keys });
		expect(screen.queryByRole('button', { name: 'http://192.168.1.1:2222/mcp' })).toBeNull();
		expect(screen.queryByRole('textbox', { name: /адрес/i })).toBeNull();

		const copyBtn = screen.getByRole('button', { name: 'Скопировать адрес эндпоинта' });
		await fireEvent.click(copyBtn);
		expect(clipboardMock).toHaveBeenCalledWith('http://192.168.1.1:2222/mcp');
		await waitFor(() => expect(notify.success).toHaveBeenCalledWith('Адрес скопирован в буфер обмена'));
		expect(notify.error).not.toHaveBeenCalled();
	});

	it('копирование адреса эндпоинта: ошибка буфера обмена уведомляет об ошибке, а не молчит', async () => {
		clipboardMock.mockResolvedValue(false);
		render(McpCard, { ...base, enabled: true, keys });
		await fireEvent.click(screen.getByRole('button', { name: 'Скопировать адрес эндпоинта' }));
		await waitFor(() => expect(notify.error).toHaveBeenCalledWith('Не удалось скопировать адрес'));
		expect(notify.success).not.toHaveBeenCalled();
	});

	// Метка «Beta» должна быть видна независимо от состояния тумблера — это
	// статус фичи, а не подсказка про включённое состояние.
	it('метка Beta видна и при выключенном, и при включённом MCP', () => {
		const { unmount } = render(McpCard, { ...base, enabled: false, keys });
		expect(screen.getByText('Beta')).toBeTruthy();
		unmount();

		render(McpCard, { ...base, enabled: true, keys });
		expect(screen.getByText('Beta')).toBeTruthy();
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

	it('показанный один раз ключ — текст, а не кнопка, копируется явной кнопкой с уведомлением', async () => {
		clipboardMock.mockResolvedValue(true);
		render(McpCard, { ...base, enabled: true, keys: [] });
		await fireEvent.click(screen.getByText('Создать ключ'));
		const dialog = screen.getByRole('dialog');
		await fireEvent.input(within(dialog).getByPlaceholderText('Например, laptop'), { target: { value: 'phone' } });
		await fireEvent.click(within(dialog).getByText('Создать'));
		await waitFor(() => expect(screen.getByText('awgm_secret')).toBeTruthy());
		expect(within(dialog).queryByRole('button', { name: 'awgm_secret' })).toBeNull();

		const copyBtn = within(dialog).getByLabelText('Скопировать ключ');
		expect(copyBtn).toBeTruthy();
		await fireEvent.click(copyBtn);
		expect(clipboardMock).toHaveBeenCalledWith('awgm_secret');
		await waitFor(() => expect(notify.success).toHaveBeenCalledWith('Ключ скопирован в буфер обмена'));
	});

	// Claude Desktop на Windows и Cursor режут каждый элемент args по
	// пробелам: "Authorization:Bearer <ключ>" одним элементом приехало бы
	// как два аргумента, токен потерялся бы, и пользователю сказали бы, что
	// только что вставленный ключ неверный. Заголовок передаётся через env.
	it('сниппет mcp-remote не содержит аргументов с пробелами, ключ уходит через env', async () => {
		render(McpCard, { ...base, enabled: true, keys: [] });
		await fireEvent.click(screen.getByText('Создать ключ'));
		const dialog = screen.getByRole('dialog');
		await fireEvent.input(within(dialog).getByPlaceholderText('Например, laptop'), { target: { value: 'phone' } });
		await fireEvent.click(within(dialog).getByText('Создать'));
		await waitFor(() => expect(screen.getByText('awgm_secret')).toBeTruthy());

		const summary = within(dialog).getByText('Claude Desktop (mcp-remote)');
		const pre = summary.parentElement?.querySelector('pre');
		expect(pre).toBeTruthy();
		const config = JSON.parse(pre!.textContent ?? '');
		const entry = config.mcpServers['awg-manager'];
		for (const arg of entry.args) {
			expect(typeof arg).toBe('string');
			expect(arg).not.toContain(' ');
		}
		expect(entry.args).toContain('Authorization:${AUTH_HEADER}');
		expect(entry.env.AUTH_HEADER).toBe('Bearer awgm_secret');
	});

	it('закрытие окна стирает показанный plaintext, а не откладывает до следующего создания', async () => {
		render(McpCard, { ...base, enabled: true, keys: [] });
		await fireEvent.click(screen.getByText('Создать ключ'));
		const dialog = screen.getByRole('dialog');
		await fireEvent.input(within(dialog).getByPlaceholderText('Например, laptop'), { target: { value: 'phone' } });
		await fireEvent.click(within(dialog).getByText('Создать'));
		await waitFor(() => expect(screen.getByText('awgm_secret')).toBeTruthy());

		await fireEvent.click(within(dialog).getByText('Готово'));
		await waitFor(() => expect(screen.queryByText('awgm_secret')).toBeNull());

		// Повторное открытие начинается с формы имени, а не с прежнего ключа.
		await fireEvent.click(screen.getByText('Создать ключ'));
		expect(within(screen.getByRole('dialog')).getByPlaceholderText('Например, laptop')).toBeTruthy();
		expect(screen.queryByText('awgm_secret')).toBeNull();
	});
});
