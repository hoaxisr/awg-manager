import { describe, it, expect, afterEach, vi } from 'vitest';

// Стор темы при импорте подписывается на prefers-color-scheme — в jsdom
// matchMedia нет, подставляем минимальную заглушку до импорта компонентов.
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
import { render, screen, fireEvent } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import UsageLevelCard from './UsageLevelCard.svelte';
import ThemeSchemeCard from './ThemeSchemeCard.svelte';
import { locale } from '$lib/i18n';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('UsageLevelCard i18n', () => {
	it('уровни и описание переключаются на английский без перемонтирования', async () => {
		render(UsageLevelCard, {
			props: { value: 'advanced', saving: false, onSelect: () => {}, initialExpanded: true },
		});
		expect(screen.getByText('Уровень использования')).toBeTruthy();
		expect(screen.getAllByText('Расширенный').length).toBeGreaterThan(0);

		locale.set('en');
		flushSync();

		expect(screen.getByText('Usage level')).toBeTruthy();
		expect(screen.getByRole('radio', { name: /Basic/ })).toBeTruthy();
		expect(screen.getAllByText('Advanced').length).toBeGreaterThan(0);

		await fireEvent.click(screen.getByRole('button', { name: 'More about the “Expert” level' }));
		expect(screen.getByText('Level: Expert')).toBeTruthy();
		expect(screen.getByText('The full feature set for fine-tuning')).toBeTruthy();
		expect(screen.getByText('Everything from the "Advanced" level')).toBeTruthy();
	});
});

describe('ThemeSchemeCard i18n', () => {
	it('карточка «Внешний вид» переключается на английский', async () => {
		render(ThemeSchemeCard);
		expect(screen.getByText('Внешний вид')).toBeTruthy();
		expect(screen.getByText('Окраска иконок')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Гармоничная' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Appearance')).toBeTruthy();
		expect(screen.getByText('Icon coloring')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Harmonious' })).toBeTruthy();
		expect(screen.getByText('Compact mode')).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { name: /Color scheme/ }));
		expect(
			screen.getByText('The classic AWGM theme with deep dark-blue shades.'),
		).toBeTruthy();
	});
});
