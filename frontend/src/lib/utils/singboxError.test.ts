import { describe, it, expect } from 'vitest';
import { m, uiText } from '$lib/i18n';
import { singboxErrorText } from './singboxError';

describe('singboxErrorText', () => {
	it('NO_SPACE → отложенная строка каталога, а не текст бэкенда', () => {
		const err = Object.assign(new Error('текст бэкенда'), { status: 409, body: { code: 'NO_SPACE' } });
		const t = singboxErrorText(err);
		expect(typeof t).toBe('function');
		expect(uiText(t)).toBe(m.singbox_error_no_space());
	});

	it('другой код — сообщение как есть', () => {
		const err = Object.assign(new Error('текст бэкенда'), { status: 409, body: { code: 'INSTALL_IN_PROGRESS' } });
		expect(singboxErrorText(err)).toBe('текст бэкенда');
	});
});
