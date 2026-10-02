import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { readable } from 'svelte/store';
import AppHeader from './AppHeader.svelte';
import { locale } from '$lib/i18n';

vi.mock('$app/stores', () => ({
	page: readable({ url: new URL('http://router.local/') }),
}));
vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

const baseProps = {
	authenticated: true,
	username: 'admin',
	currentVersion: '2.20.0',
	onToggleThemeMode: () => {},
	onLogout: () => {},
	onOpenDonate: () => {},
};

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('AppHeader i18n', () => {
	it('меню и кнопки шапки переключаются на английский без перемонтирования', async () => {
		render(AppHeader, { props: baseProps });

		expect(screen.getByText('ТУННЕЛИ')).toBeTruthy();
		expect(screen.getByText('НАСТРОЙКИ')).toBeTruthy();
		expect(screen.getByRole('navigation', { name: 'Главная навигация' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Выйти' })).toBeTruthy();
		expect(screen.getByRole('link', { name: 'Терминал' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('TUNNELS')).toBeTruthy();
		expect(screen.getByText('SETTINGS')).toBeTruthy();
		expect(screen.queryByText('ТУННЕЛИ')).toBeNull();
		expect(screen.getByRole('navigation', { name: 'Main navigation' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Log out' })).toBeTruthy();
		expect(screen.getByRole('link', { name: 'Terminal' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Support the project' })).toBeTruthy();

		// Мобильное меню: подписи в обычном регистре, «Выйти» — тоже переведено.
		await fireEvent.click(screen.getByRole('button', { name: 'Menu' }));
		const mobileNav = screen.getByRole('navigation', { name: 'Mobile navigation' });
		expect(mobileNav.textContent).toContain('Tunnels');
		expect(mobileNav.textContent).toContain('Log out');
	});

	it('подпись кнопки темы — целая фраза под каждый случай', () => {
		const theme = {
			preset: 'legacy' as const,
			modePreference: 'dark' as const,
			mode: 'dark' as const,
			legacyMode: 'dark' as const,
			custom: { accent: '#8b5cf6', background: '#111827', text: '#f8fafc' },
			label: 'AWGM - Legacy',
			summary: '',
			supportsModeToggle: true,
		};
		const { rerender } = render(AppHeader, { props: { ...baseProps, theme } });
		expect(
			screen.getByRole('button', {
				name: 'Переключить AWGM - Legacy на светлую тему. Сейчас тёмная.',
			}),
		).toBeTruthy();

		locale.set('en');
		rerender({ ...baseProps, theme: { ...theme, modePreference: 'system' } });
		flushSync();
		expect(
			screen.getByRole('button', {
				name: 'Switch AWGM - Legacy from system to the light theme. Currently dark (system).',
			}),
		).toBeTruthy();
	});
});
