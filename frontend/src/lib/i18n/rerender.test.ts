import { describe, it, expect, afterEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import LoginForm from '$lib/components/LoginForm.svelte';
import ConfirmModal from '$lib/components/ui/ConfirmModal.svelte';
import { locale } from './index';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('переведённые компоненты перерисовываются при смене языка', () => {
	it('LoginForm', () => {
		render(LoginForm);
		expect(screen.getByRole('button', { name: 'Войти' })).toBeTruthy();
		expect(screen.getByLabelText('Логин')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Роутер' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy();
		expect(screen.getByLabelText('Username')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Router' })).toBeTruthy();
		expect(screen.getByRole('link', { name: 'terms of use' })).toBeTruthy();
	});

	it('ConfirmModal: дефолтные подписи следуют языку, переданные — нет', () => {
		const props = { open: true, title: 'T', message: 'M', onConfirm: () => {}, onClose: () => {} };
		render(ConfirmModal, { props });
		render(ConfirmModal, { props: { ...props, confirmLabel: 'Сбросить' } });
		expect(screen.getAllByRole('button', { name: 'Отмена' })).toHaveLength(2);
		expect(screen.getByRole('button', { name: 'Удалить' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getAllByRole('button', { name: 'Cancel' })).toHaveLength(2);
		expect(screen.getByRole('button', { name: 'Delete' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Сбросить' })).toBeTruthy();
	});
});
