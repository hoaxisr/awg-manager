import { describe, it, expect, afterEach } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import LoginForm from './LoginForm.svelte';
import { locale } from '$lib/i18n';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

// Язык меняется в настройках, а они за авторизацией: англоязычный
// пользователь видел форму входа только по-русски. Переключатель на форме
// входа меняет язык сразу и запоминает выбор, как и в настройках.
describe('LoginForm: язык до входа', () => {
	it('переключает форму на английский и обратно', async () => {
		render(LoginForm);

		const en = screen.getByRole('button', { name: 'English' });
		expect(screen.getByRole('button', { name: 'Русский' }).getAttribute('aria-pressed')).toBe('true');
		expect(screen.getByRole('button', { name: 'Войти' })).toBeTruthy();

		await fireEvent.click(en);

		expect(locale.current).toBe('en');
		expect(localStorage.getItem('awg-manager-locale')).toBe('en');
		expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy();
		expect(screen.getByLabelText('Username')).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { name: 'Русский' }));

		expect(locale.current).toBe('ru');
		expect(screen.getByRole('button', { name: 'Войти' })).toBeTruthy();
	});

	it('подписывает группу переключателя для экранного диктора', () => {
		render(LoginForm);
		expect(screen.getByRole('group', { name: 'Язык интерфейса' })).toBeTruthy();
	});
});
