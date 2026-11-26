import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import TgProxyUsersSection from './TgProxyUsersSection.svelte';
import { m } from '$lib/i18n';

const user = { id: 'u1', remark: 'Основной', enabled: true, secret: '' };

function setup() {
	const onrename = vi.fn();
	render(TgProxyUsersSection, {
		props: { users: [user], busyId: '', onadd: vi.fn(), ontoggle: vi.fn(), onrename, onreveal: vi.fn(), ondelete: vi.fn() },
	});
	return onrename;
}

describe('TgProxyUsersSection: имя пользователя', () => {
	it('переименование отдаёт новое имя без пробелов по краям', async () => {
		const onrename = setup();
		await fireEvent.click(screen.getByRole('button', { name: m.servers_tg_user_rename() }));
		await fireEvent.input(screen.getByRole('textbox', { name: m.servers_tg_user_rename() }), { target: { value: '  Телефон ' } });
		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));
		expect(onrename).toHaveBeenCalledWith(user, 'Телефон');
	});

	it('то же имя или отмена ничего не шлют', async () => {
		const onrename = setup();
		await fireEvent.click(screen.getByRole('button', { name: m.servers_tg_user_rename() }));
		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));
		await fireEvent.click(screen.getByRole('button', { name: m.servers_tg_user_rename() }));
		await fireEvent.click(screen.getByRole('button', { name: m.common_cancel() }));
		expect(onrename).not.toHaveBeenCalled();
		expect(screen.getByText('Основной')).toBeTruthy();
	});
});
