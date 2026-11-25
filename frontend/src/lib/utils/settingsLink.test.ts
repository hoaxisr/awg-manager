import { describe, it, expect, vi } from 'vitest';
import { DOWNLOAD_SETTINGS_HREF } from './downloadError';
import {
	LOGGING_SETTINGS_HREF,
	UPDATE_SETTINGS_HREF,
	resolveSettingsLink,
	revealSettingsTarget,
	settingsTabHref,
	type SettingsLink,
} from './settingsLink';

const resolve = (href: string) => resolveSettingsLink(new URL(href, 'http://router:2222'));
const TOKEN = '0123456789abcdef0123456789abcdef';

describe('resolveSettingsLink', () => {
	it.each<[string, SettingsLink]>([
		['/settings?mode', { tab: 'general', anchor: 'usage-level' }],
		['/settings?feedbackFab', { tab: 'updates', anchor: 'feedback-fab' }],
		['/settings?highlight=downloads#downloads', { tab: 'updates', anchor: 'downloads' }],
		['/settings#downloads', { tab: 'updates', anchor: 'downloads' }],
		['/settings?highlight=downloads', { tab: 'updates', anchor: 'downloads' }],
		['/settings#logging', { tab: 'logs', anchor: 'logging' }],
		['/settings#awgm-update', { tab: 'updates', anchor: 'awgm-update' }],
		[`/settings#listen-confirm=${TOKEN}`, { tab: 'access', listenConfirm: TOKEN }],
		// Ссылка, открытая поверх уже выбранной вкладки, важнее неё.
		['/settings?tab=logs&feedbackFab', { tab: 'updates', anchor: 'feedback-fab' }],
		[`/settings?tab=system#listen-confirm=${TOKEN}`, { tab: 'access', listenConfirm: TOKEN }],
	])('%s', (href, expected) => {
		expect(resolve(href)).toEqual(expected);
	});

	it.each(['/settings', '/settings?tab=logs', '/settings#nope', '/settings#listen-confirm=zz'])(
		'%s — не глубокая ссылка',
		(href) => {
			expect(resolve(href)).toBeNull();
		},
	);

	it('ссылки, которые панель ставит сама, разбираются', () => {
		expect(resolve(DOWNLOAD_SETTINGS_HREF)?.anchor).toBe('downloads');
		expect(resolve(UPDATE_SETTINGS_HREF)?.anchor).toBe('awgm-update');
		expect(resolve(LOGGING_SETTINGS_HREF)?.anchor).toBe('logging');
	});

	it('адрес вкладки сам глубокой ссылкой не считается', () => {
		expect(settingsTabHref('general')).toBe('/settings');
		expect(resolve(settingsTabHref('updates'))).toBeNull();
	});
});

describe('revealSettingsTarget', () => {
	it('прокручивает и подсвечивает блок; без блока молчит', () => {
		document.body.innerHTML = '<div id="logging"><span></span></div>';
		const el = document.getElementById('logging')!;
		el.scrollIntoView = vi.fn();
		revealSettingsTarget('logging');
		expect(el.scrollIntoView).toHaveBeenCalledOnce();
		expect(el.classList.contains('highlighted')).toBe(true);
		// Анимация потомка всплывает, но подсветку не гасит.
		el.querySelector('span')!.dispatchEvent(new Event('animationend', { bubbles: true }));
		expect(el.classList.contains('highlighted')).toBe(true);
		el.dispatchEvent(new Event('animationend'));
		expect(el.classList.contains('highlighted')).toBe(false);
		expect(() => revealSettingsTarget('absent')).not.toThrow();
	});
});
