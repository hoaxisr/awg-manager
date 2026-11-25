import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import LogBufferSettings from './LogBufferSettings.svelte';
import type { Settings } from '$lib/types';

describe('LogBufferSettings', () => {
	it('после неудачного сохранения показывает сохранённое значение, а не отклонённое', async () => {
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
		// Страница не смогла сохранить: settings остаются прежними.
		const onSave = vi.fn(async () => {});
		const { container } = render(LogBufferSettings, {
			props: { settings, saving: false, onSave },
		});
		const input = container.querySelector('.logging-buffer-row input') as HTMLInputElement;

		await fireEvent.input(input, { target: { value: '7000' } });
		await fireEvent.blur(input);

		// Только свои поля: снимок целиком откатил бы уровень, сохранённый рядом.
		expect(onSave).toHaveBeenCalledWith({ appMaxEntries: 7000, singboxMaxEntries: 5000 });
		await vi.waitFor(() => expect(input.value).toBe('5000'));
		expect(settings.logging.appMaxEntries).toBe(5000);
	});
});
