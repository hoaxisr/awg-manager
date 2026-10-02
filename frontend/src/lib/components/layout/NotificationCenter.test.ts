import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import NotificationCenter from './NotificationCenter.svelte';
import { notificationCenter } from '$lib/stores/notificationCenter';
import { locale } from '$lib/i18n';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

afterEach(() => {
	notificationCenter.clearAll();
	locale.set('ru');
	localStorage.clear();
});

describe('NotificationCenter i18n', () => {
	it('колокольчик и панель переключаются на английский без перемонтирования', async () => {
		notificationCenter.record({ type: 'warning', message: 'msg', ts: Date.now() });
		render(NotificationCenter, { props: { authenticated: true } });

		await fireEvent.click(screen.getByRole('button', { name: 'Уведомления, непрочитанных: 1' }));
		expect(screen.getByText('Сегодня')).toBeTruthy();
		expect(screen.getByText('Прочитать всё')).toBeTruthy();
		expect(screen.getByText('Открыть журнал →')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByRole('button', { name: 'Notifications, unread: 1' })).toBeTruthy();
		expect(screen.getByText('Today')).toBeTruthy();
		expect(screen.getByText('Mark all as read')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Remove notification' })).toBeTruthy();
		expect(screen.getByText('Kept for 7 days · up to 100')).toBeTruthy();
		expect(screen.getByText('Open log →')).toBeTruthy();
		// Текст самого уведомления приходит готовым и не переводится.
		expect(screen.getByText('msg')).toBeTruthy();
	});

	it('пустая панель', async () => {
		locale.set('en');
		render(NotificationCenter, { props: { authenticated: true } });
		await fireEvent.click(screen.getByRole('button', { name: 'Notifications' }));
		expect(screen.getByText('No notifications')).toBeTruthy();
	});
});
