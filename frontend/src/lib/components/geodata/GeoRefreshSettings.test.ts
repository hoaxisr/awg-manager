import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import GeoRefreshSettings from './GeoRefreshSettings.svelte';
import type { GeoFileSettings } from '$lib/types';

function geoFile(p: Partial<GeoFileSettings>): GeoFileSettings {
	return { autoRefreshEnabled: false, refreshIntervalHours: 0, ...p };
}

describe('GeoRefreshSettings', () => {
	it('collapsed when disabled', () => {
		render(GeoRefreshSettings, {
			props: { value: geoFile({ autoRefreshEnabled: false }), saving: false, onToggle: vi.fn(), onSave: vi.fn() },
		});
		expect(screen.queryByText('Режим обновления:')).toBeNull();
	});

	it('shows interval controls when enabled (mode empty → interval default)', () => {
		render(GeoRefreshSettings, {
			props: { value: geoFile({ autoRefreshEnabled: true, refreshMode: undefined }), saving: false, onToggle: vi.fn(), onSave: vi.fn() },
		});
		expect(screen.getByText('Режим обновления:')).toBeTruthy();
		expect(screen.getByText('каждые N часов')).toBeTruthy();
	});

	it('после неудачного сохранения показывает сохранённое значение, а не отклонённое', async () => {
		const value = geoFile({ autoRefreshEnabled: true, refreshMode: 'interval', refreshIntervalHours: 6 });
		// Сохранить не удалось: value остаётся прежним.
		const onSave = vi.fn(async () => {});
		const { container } = render(GeoRefreshSettings, {
			props: { value, saving: false, onToggle: vi.fn(), onSave },
		});
		const input = container.querySelector('input[type="number"]') as HTMLInputElement;

		await fireEvent.input(input, { target: { value: '12' } });
		await fireEvent.click(screen.getByRole('button'));

		expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ refreshIntervalHours: 12 }));
		await vi.waitFor(() => expect(input.value).toBe('6'));
	});
});
