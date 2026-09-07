import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import ObfuscatorImportForm from './ObfuscatorImportForm.svelte';

describe('ObfuscatorImportForm', () => {
	it('phobos: поле ссылки установки и предупреждение о TLS', () => {
		render(ObfuscatorImportForm, { props: { flavor: 'phobos' } });
		expect(screen.getByLabelText(/Ссылка установки/)).toBeTruthy();
		expect(screen.getByText(/сертификат панели не проверяется/i)).toBeTruthy();
		expect(screen.queryByLabelText(/^Ключ/)).toBeNull();
	});
	it('clusterm: ручные поля релея, без MEDIA', () => {
		render(ObfuscatorImportForm, { props: { flavor: 'clusterm' } });
		expect(screen.getByLabelText(/Сервер \(host:port\)/)).toBeTruthy();
		expect(screen.getByLabelText(/^Ключ/)).toBeTruthy();
		const masking = screen.getByLabelText<HTMLSelectElement>(/Маскировка/);
		expect(Array.from(masking.options).map((o) => o.value)).toEqual(['STUN', 'AUTO', 'NONE']);
		expect(screen.queryByLabelText(/Ссылка установки/)).toBeNull();
	});
});
