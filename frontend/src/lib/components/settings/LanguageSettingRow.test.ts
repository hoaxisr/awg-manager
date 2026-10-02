import { describe, it, expect, afterEach } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import LanguageSettingRow from './LanguageSettingRow.svelte';
import { locale } from '$lib/i18n';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('LanguageSettingRow', () => {
	it('показывает текущий язык и переключает на английский', async () => {
		render(LanguageSettingRow);

		const ru = screen.getByRole('button', { name: 'Русский' });
		const en = screen.getByRole('button', { name: 'English' });
		expect(ru.getAttribute('aria-pressed')).toBe('true');
		expect(en.getAttribute('aria-pressed')).toBe('false');
		expect(screen.getByText('Язык интерфейса')).toBeTruthy();

		await fireEvent.click(en);

		expect(locale.current).toBe('en');
		expect(localStorage.getItem('awg-manager-locale')).toBe('en');
		expect(en.getAttribute('aria-pressed')).toBe('true');
		expect(screen.getByText('Interface language')).toBeTruthy();
	});
});
