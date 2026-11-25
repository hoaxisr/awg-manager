import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen } from '@testing-library/svelte';

vi.mock('$lib/api/client', () => ({ api: {} }));
vi.mock('$lib/stores/notifications', () => ({ notifications: { success: vi.fn(), error: vi.fn() } }));
vi.mock('$lib/stores/singboxRouter', () => ({ singboxRouter: { reloadSettings: vi.fn() } }));
vi.mock('$lib/stores/singbox', async () => {
	const { readable } = await import('svelte/store');
	return { singboxStatus: readable({ data: { installed: true } }) };
});
vi.mock('$lib/stores/firc', async () => {
	const { readable, writable } = await import('svelte/store');
	return {
		fircStatus: writable(null),
		fircForeign: writable(undefined),
		acquireFircStatusWatch: () => () => {},
		fircGroups: readable([]),
		fircTunnels: readable([]),
		fircTunnelsState: readable([]),
		reloadFircAll: vi.fn().mockResolvedValue(undefined),
	};
});
vi.mock('$lib/components/firc/FircGroupsPanel.svelte', () => ({ default: () => { throw new Error('FircGroupsPanel отрисован'); } }));
vi.mock('$lib/components/firc/FircSettingsPanel.svelte', () => ({ default: () => { throw new Error('FircSettingsPanel отрисован'); } }));
vi.mock('$lib/components/firc/FircMonitorPanel.svelte', () => ({ default: () => { throw new Error('FircMonitorPanel отрисован'); } }));
vi.mock('$lib/components/firc/FircJournalPanel.svelte', () => ({ default: () => { throw new Error('FircJournalPanel отрисован'); } }));

import FircRouterPanel from './FircRouterPanel.svelte';
import { fircForeign, fircStatus } from '$lib/stores/firc';
import { m } from '$lib/i18n';

beforeEach(() => vi.clearAllMocks());
afterEach(cleanup);

describe('вкладка FIRC при стороннем firc', () => {
	it('до статуса — спиннер без кнопок', () => {
		const { container } = render(FircRouterPanel);
		expect(container.querySelector('.animate-spin')).not.toBeNull();
		expect(screen.queryAllByRole('button')).toHaveLength(0);
	});

	it('вместо разделов — пояснение и единственная кнопка ухода с движка', () => {
		(fircStatus as never as { set(v: unknown): void }).set({ running: false, selected: true, foreign: { package: 'firc' } });
		(fircForeign as never as { set(v: unknown): void }).set({ package: 'firc' });
		render(FircRouterPanel);
		expect(screen.getByText(m.firc_foreign_conflict())).toBeInTheDocument();
		expect(screen.getAllByRole('button').map((b) => b.textContent?.trim())).toEqual([m.firc_foreign_leave()]);
		expect(screen.queryByText(m.firc_router_make_active())).not.toBeInTheDocument();
	});
});
