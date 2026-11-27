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
vi.mock('$lib/components/firc/FircGroupsPanel.svelte', () => ({ default: () => {} }));
vi.mock('$lib/components/firc/FircSettingsPanel.svelte', () => ({ default: () => {} }));
vi.mock('$lib/components/firc/FircMonitorPanel.svelte', () => ({ default: () => { throw new Error('FircMonitorPanel отрисован'); } }));
vi.mock('$lib/components/firc/FircJournalPanel.svelte', () => ({ default: () => { throw new Error('FircJournalPanel отрисован'); } }));

import FircRouterPanel from './FircRouterPanel.svelte';
import { fircForeign, fircStatus } from '$lib/stores/firc';
import { m } from '$lib/i18n';

beforeEach(() => vi.clearAllMocks());
afterEach(cleanup);

const setStatus = (v: unknown) => (fircStatus as never as { set(v: unknown): void }).set(v);

describe('вкладка AWGM Route без рантайма', () => {
	beforeEach(() => (fircForeign as never as { set(v: unknown): void }).set(null));

	it('вместо кнопок ядра — ссылка на установку в компонентах', () => {
		setStatus({ fircdInstalled: false, running: false, selected: false });
		render(FircRouterPanel);
		expect(screen.getByText(m.firc_router_not_installed_title())).toBeInTheDocument();
		expect(screen.getByRole('button', { name: m.firc_router_not_installed_action() })).toHaveAttribute(
			'href',
			'/settings?tab=components',
		);
		expect(screen.queryByText(m.firc_router_make_active())).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: m.firc_router_start() })).not.toBeInTheDocument();
	});

	it('рантайм установлен — кнопка «Сделать активным ядром» на месте', () => {
		setStatus({ fircdInstalled: true, running: false, selected: false });
		render(FircRouterPanel);
		expect(screen.getByText(m.firc_router_make_active())).toBeInTheDocument();
		expect(screen.queryByText(m.firc_router_not_installed_title())).not.toBeInTheDocument();
	});

	it('восстановление без бинаря — панель починки, не установка', () => {
		setStatus({ fircdInstalled: false, running: false, selected: true, recoveryRequired: true });
		render(FircRouterPanel);
		expect(screen.queryByText(m.firc_router_not_installed_title())).not.toBeInTheDocument();
		expect(screen.getByText(m.firc_router_recovery_title())).toBeInTheDocument();
	});
});
