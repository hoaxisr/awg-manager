import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import DnsRouteSettings from './DnsRouteSettings.svelte';
import type { Settings } from '$lib/types';

describe('DnsRouteSettings', () => {
	it('после неудачного сохранения показывает сохранённое значение, а не отклонённое', async () => {
		const settings = {
			dnsRoute: {
				autoRefreshEnabled: true,
				refreshMode: 'interval',
				refreshIntervalHours: 6,
				refreshDailyTime: '03:00',
			},
		} as unknown as Settings;
		// Страница не смогла сохранить: settings остаются прежними.
		const onSave = vi.fn(async () => {});
		const { container, getByRole } = render(DnsRouteSettings, {
			props: { settings, saving: false, onToggle: () => {}, onSave },
		});
		const input = container.querySelector('#dnsRefreshInterval') as HTMLInputElement;

		await fireEvent.input(input, { target: { value: '12' } });
		await fireEvent.click(getByRole('button', { name: /сохранить/i }));

		expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ refreshIntervalHours: 12 }));
		await vi.waitFor(() => expect(input.value).toBe('6'));
		expect(settings.dnsRoute.refreshIntervalHours).toBe(6);
	});
});
