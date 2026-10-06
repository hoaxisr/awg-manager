import { describe, it, expect, afterEach, vi } from 'vitest';

// Сторы темы/настроек при импорте обращаются к matchMedia — в jsdom его нет.
vi.hoisted(() => {
	Object.defineProperty(window, 'matchMedia', {
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
		}),
	});
});
import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import SettingsFooter from './SettingsFooter.svelte';
import ObfuscatorRelayCard from './ObfuscatorRelayCard.svelte';
import LoggingSettings from './LoggingSettings.svelte';
import ExperimentalSettingsCard from './ExperimentalSettingsCard.svelte';
import { locale } from '$lib/i18n';
import type { Settings } from '$lib/types';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('SettingsFooter i18n', () => {
	it('ссылки и подписи футера переключаются на английский', () => {
		render(SettingsFooter);
		expect(screen.getByText('Документация:')).toBeTruthy();
		expect(screen.getByText('Пользовательское соглашение')).toBeTruthy();
		expect(screen.getByText('Сообщить о проблеме')).toBeTruthy();
		expect(screen.getByRole('link', { name: 'Открыть GitHub репозиторий AWG Manager' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Documentation:')).toBeTruthy();
		expect(screen.getByText('Terms of Use')).toBeTruthy();
		expect(screen.getByText('Report a problem')).toBeTruthy();
		expect(screen.getByRole('link', { name: 'Open the AWG Manager GitHub repository' })).toBeTruthy();
	});
});

describe('ObfuscatorRelayCard i18n', () => {
	it('карточка релея и причина отключения переключаются на английский', () => {
		render(ObfuscatorRelayCard, {
			props: { process: false, tripped: 'oom', ontoggle: () => {} },
		});
		expect(screen.getByText('Релей обфускатора')).toBeTruthy();
		expect(screen.getByText('Phobos в ядре')).toBeTruthy();
		expect(screen.getByText('Выключено автоматически: oom')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Obfuscator relay')).toBeTruthy();
		expect(screen.getByText('Phobos in kernel')).toBeTruthy();
		expect(screen.getByText('Disabled automatically: oom')).toBeTruthy();
	});
});

describe('LoggingSettings i18n', () => {
	it('подписи, описания и опции часов переключаются на английский', () => {
		const settings = {
			logging: {
				enabled: true,
				maxAge: 8,
				logLevel: 'info',
				singboxLogLevel: 'trace',
				appMaxEntries: 5000,
				singboxMaxEntries: 5000,
			},
		} as unknown as Settings;
		render(LoggingSettings, {
			props: { settings, saving: false, onToggle: () => {}, onSave: () => {} },
		});
		expect(screen.getByText('Логирование')).toBeTruthy();
		expect(screen.getByText('Уровень логирования AWGM')).toBeTruthy();
		expect(screen.getByText('Размер буфера приложения')).toBeTruthy();
		expect(screen.getByText('8 ч')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Logging')).toBeTruthy();
		expect(screen.getByText('AWGM log level')).toBeTruthy();
		expect(screen.getByText('Application buffer size')).toBeTruthy();
		expect(screen.getByText('8 h')).toBeTruthy();
	});
});

describe('ExperimentalSettingsCard i18n', () => {
	it('заголовок и кнопка переключаются на английский', () => {
		render(ExperimentalSettingsCard);
		expect(screen.getByText('Экспериментальное')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Вызвать пухосос' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Experimental')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Summon vacuum cleaner' })).toBeTruthy();
	});
});
