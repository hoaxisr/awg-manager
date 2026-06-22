import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import HrNeoGeoRefreshSettings from './HrNeoGeoRefreshSettings.svelte';
import type { GeoFileSettings } from '$lib/types';

function geoFile(p: Partial<GeoFileSettings>): GeoFileSettings {
	return { autoRefreshEnabled: false, refreshIntervalHours: 0, ...p };
}

describe('HrNeoGeoRefreshSettings', () => {
	it('collapsed when disabled', () => {
		render(HrNeoGeoRefreshSettings, {
			props: { value: geoFile({ autoRefreshEnabled: false }), saving: false, onToggle: vi.fn(), onSave: vi.fn() },
		});
		expect(screen.queryByText('Режим обновления:')).toBeNull();
	});

	it('shows interval controls when enabled (mode empty → interval default)', () => {
		render(HrNeoGeoRefreshSettings, {
			props: { value: geoFile({ autoRefreshEnabled: true, refreshMode: undefined }), saving: false, onToggle: vi.fn(), onSave: vi.fn() },
		});
		expect(screen.getByText('Режим обновления:')).toBeTruthy();
		expect(screen.getByText('каждые N часов')).toBeTruthy();
	});
});
