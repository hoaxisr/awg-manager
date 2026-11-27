import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import { get } from 'svelte/store';

const {
	fircGetMigrationState,
	fircGetInstallStatus,
	fircInstall,
	singboxInstall,
	fircPurgeEngine,
	fircMigrateFromSingbox,
	fircRestoreSnapshot,
	fircCreateSnapshot,
	fircDeleteSnapshot,
	fircUpdateMigrationState,
} = vi.hoisted(() => {
	if (typeof window !== 'undefined' && typeof window.matchMedia !== 'function') {
		Object.defineProperty(window, 'matchMedia', {
			writable: true,
			value: (query: string) => ({
				matches: false,
				media: query,
				onchange: null,
				addListener: () => {},
				removeListener: () => {},
				addEventListener: () => {},
				removeEventListener: () => {},
				dispatchEvent: () => false,
			}),
		});
	}
	if (typeof globalThis.ResizeObserver === 'undefined') {
		globalThis.ResizeObserver = class {
			observe() {}
			unobserve() {}
			disconnect() {}
		};
	}
	return {
		fircGetMigrationState: vi.fn(),
		fircGetInstallStatus: vi.fn(),
		fircInstall: vi.fn(),
		singboxInstall: vi.fn(),
		fircPurgeEngine: vi.fn(),
		fircMigrateFromSingbox: vi.fn(),
		fircRestoreSnapshot: vi.fn(),
		fircCreateSnapshot: vi.fn(),
		fircDeleteSnapshot: vi.fn(),
		fircUpdateMigrationState: vi.fn(),
	};
});

vi.mock('$lib/api/client', () => ({
	api: {
		fircGetMigrationState,
		fircGetInstallStatus,
		fircInstall,
		singboxInstall,
		fircPurgeEngine,
		fircMigrateFromSingbox,
		fircRestoreSnapshot,
		fircCreateSnapshot,
		fircDeleteSnapshot,
		fircUpdateMigrationState,
	},
}));

vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));
vi.mock('$lib/stores/firc', async () => {
	const { writable } = await import('svelte/store');
	return {
		reloadFircAll: vi.fn().mockResolvedValue(undefined),
		fircStatus: writable(null),
		fircForeign: writable(undefined),
		acquireFircStatusWatch: () => () => {},
	};
});
vi.mock('$lib/stores/settings', () => ({
	reloadSettings: vi.fn().mockResolvedValue(undefined),
}));
vi.mock('$lib/stores/singboxRouter', () => ({
	singboxRouter: { reloadSettings: vi.fn().mockResolvedValue(undefined) },
}));
vi.mock('$lib/stores/singbox', async () => {
	const { readable } = await import('svelte/store');
	return {
		singboxStatus: {
			...readable({ data: { installed: false } }),
			refetch: vi.fn().mockResolvedValue(undefined),
		},
		singboxTunnels: { refetch: vi.fn().mockResolvedValue(undefined) },
	};
});
vi.mock('$lib/stores/subscriptions', () => ({
	subscriptionsStore: { refetch: vi.fn().mockResolvedValue(undefined) },
	subscriptionGroupsStore: { refetch: vi.fn().mockResolvedValue(undefined) },
}));
vi.mock('$lib/stores/awg3', () => ({
	awg3Tunnels: { refetch: vi.fn().mockResolvedValue(undefined) },
}));
vi.mock('$lib/stores/tunnels', () => ({
	tunnels: { refetch: vi.fn().mockResolvedValue(undefined) },
}));

import FircMigrationWizardModal from './FircMigrationWizardModal.svelte';
import {
	closeFircMigrationModal,
	fircMigrationModalOpen,
	openFircMigrationModal,
	setFircMigrationPopupLocallyDismissed,
} from '$lib/stores/fircMigrationModal';
import { notifications } from '$lib/stores/notifications';
import { m } from '$lib/i18n';
import type { FircInstallStatus, FircMigrationOverview, FircTunnel } from '$lib/types/firc';

const dummyTunnel: FircTunnel = {
	id: 'tun-1',
	device: 'tunvless0',
	description: 'tun-1',
	enable: true,
	active: 1,
	by: 'connection',
	interval: 60,
	silence: 0,
	filter: '',
	order: [],
	exclude: [],
	sources: [{ id: 's1', kind: 'link', link: 'vless://x@1.1.1.1:443#t' }],
	uplink: { kind: 'auto', ref: '' },
	advanced: { ca: '', insecure: false, timeout: 10, kernelTcp: true, fingerprint: '' },
	inbounds: [],
};

const installedFircStatus: FircInstallStatus = {
	state: 'installed',
	available: true,
	fircdPresent: true,
	tunvlessPresent: true,
	installedVersion: '0.7.0',
	busy: false,
} as FircInstallStatus;

function baseOverview(overrides: Partial<FircMigrationOverview> = {}): FircMigrationOverview {
	return {
		shouldShowPopup: false,
		wizardState: { dismissedAutoPopup: false },
		activeEngine: 'sing-box',
		fircRunning: false,
		fircInstalled: true,
		singboxRunning: true,
		singboxInstalled: true,
		singboxTunnelsCount: 2,
		singboxSubscriptionsCount: 1,
		fircTunnelsCount: 1,
		fircGroupsCount: 1,
		snapshots: [],
		forwardPreview: {
			tunnels: [dummyTunnel],
			groups: [],
		},
		...overrides,
	};
}

beforeEach(() => {
	vi.clearAllMocks();
	setFircMigrationPopupLocallyDismissed(false);
	closeFircMigrationModal();
	fircGetInstallStatus.mockResolvedValue(installedFircStatus);
	fircGetMigrationState.mockResolvedValue(baseOverview());
	fircMigrateFromSingbox.mockResolvedValue({ tunnels: [], groups: [] });
	fircUpdateMigrationState.mockResolvedValue({ dismissedAutoPopup: false });
});

afterEach(() => {
	closeFircMigrationModal();
	cleanup();
});

describe('FircMigrationWizardModal engine purge & recovery', () => {
	it('до ответа — без кнопок и вкладок, сбой installStatus не оставляет прошлый ответ', async () => {
		fircGetMigrationState.mockResolvedValue(baseOverview());
		let answer!: (v: FircInstallStatus) => void;
		fircGetInstallStatus.mockReturnValue(new Promise((r) => (answer = r)));
		render(FircMigrationWizardModal);
		openFircMigrationModal('forward');
		await waitFor(() => expect(fircGetInstallStatus).toHaveBeenCalled());
		expect(screen.queryByText(m.firc_migration_tab_snapshots())).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: m.firc_migration_fwd_apply() })).not.toBeInTheDocument();
		answer(installedFircStatus);
		expect((await screen.findAllByText(m.firc_migration_tab_snapshots())).length).toBeGreaterThan(0); // признак не холостой
		expect(await screen.findByRole('button', { name: m.firc_migration_fwd_apply() })).toBeInTheDocument();

		closeFircMigrationModal();
		await tick();
		fircGetInstallStatus.mockRejectedValue(new Error('сеть'));
		openFircMigrationModal('forward');
		expect(await screen.findByRole('alert')).toHaveTextContent(m.firc_migration_load_failed());
		expect(screen.queryByRole('button', { name: m.firc_migration_fwd_apply() })).not.toBeInTheDocument();
	});

	it('повторное открытие не показывает пояснение с прошлого раза', async () => {
		fircGetMigrationState.mockResolvedValue(baseOverview());
		fircGetInstallStatus.mockResolvedValue({ ...installedFircStatus, state: 'foreign', foreign: { package: 'firc' } });
		render(FircMigrationWizardModal);
		openFircMigrationModal('forward');
		expect(await screen.findByText(m.firc_foreign_text())).toBeInTheDocument();
		closeFircMigrationModal();
		await tick();
		fircGetInstallStatus.mockReturnValue(new Promise(() => {}));
		openFircMigrationModal('forward');
		await waitFor(() => expect(fircGetInstallStatus).toHaveBeenCalledTimes(2));
		expect(screen.queryByText(m.firc_foreign_text())).not.toBeInTheDocument();
	});

	it('сторонний firc ушёл при открытом окне — данные перечитываются', async () => {
		const { fircForeign } = await import('$lib/stores/firc');
		const store = fircForeign as unknown as { set(v: unknown): void };
		store.set({ package: 'firc' });
		fircGetMigrationState.mockResolvedValue(baseOverview());
		fircGetInstallStatus.mockResolvedValue({ ...installedFircStatus, state: 'foreign', foreign: { package: 'firc' } });
		render(FircMigrationWizardModal);
		openFircMigrationModal('forward');
		expect(await screen.findByText(m.firc_foreign_text())).toBeInTheDocument();
		fircGetInstallStatus.mockResolvedValue(installedFircStatus);
		store.set(null);
		await waitFor(() => expect(fircGetInstallStatus).toHaveBeenCalledTimes(2));
		expect(await screen.findByRole('button', { name: m.firc_migration_fwd_apply() })).toBeInTheDocument();
		store.set(undefined);
	});

	it('при стороннем firc — пояснение вместо разделов, без предпросмотра и миграции', async () => {
		fircGetMigrationState.mockResolvedValue(baseOverview({ activeEngine: 'firc', forwardPreview: null as never }));
		fircGetInstallStatus.mockResolvedValue({
			...installedFircStatus,
			state: 'foreign',
			fircdPresent: false,
			tunvlessPresent: false,
			foreign: { binary: '/opt/bin/fircd' },
		});
		render(FircMigrationWizardModal);
		openFircMigrationModal('forward');
		expect(await screen.findByText(m.firc_foreign_binary({ path: '/opt/bin/fircd' }))).toBeInTheDocument();
		// sing-box не установлен: уход с движка означает «выключить».
		expect(screen.getByRole('button', { name: m.firc_foreign_turn_off() })).toBeInTheDocument();
		expect(screen.queryByText(m.firc_migration_tab_snapshots())).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: m.firc_migration_fwd_apply() })).not.toBeInTheDocument();
		expect(fircMigrateFromSingbox).not.toHaveBeenCalled();
	});

	it('импорт с заменой: подтверждение при существующих данных, снимок без флажка', async () => {
		fircMigrateFromSingbox.mockImplementation(async (dryRun: boolean) => {
			if (dryRun) {
				return baseOverview().forwardPreview;
			}
			return {
				tunnels: [dummyTunnel],
				groups: [],
				configMigrated: true,
				fircStarted: true,
				engineSwitched: true,
				postActionApplied: 'switch_and_uninstall',
				snapshotCreated: {
					id: 'snap-pre-mig',
					createdAt: 1794900000,
					label: 'pre',
					trigger: 'auto-pre-migrate',
					activeEngine: 'sing-box',
					singboxTunnelsCount: 1,
					singboxSubscriptionsCount: 1,
					singboxGroupsCount: 0,
					fircTunnelsCount: 0,
					fircGroupsCount: 0,
					filesCount: 4,
				},
				removedEngine: {
					engine: 'sing-box',
					tunnelsRemoved: 2,
					groupsRemoved: 0,
					subscriptionsRemoved: 1,
					runtimeRemoved: true,
					preservedSnapshots: 1,
				},
			};
		});

		render(FircMigrationWizardModal);
		openFircMigrationModal('forward');

		await screen.findByText(m.firc_migration_fwd_post_uninstall());
		expect(document.getElementById('wiz-fwd-mode')).toBeNull();
		await fireEvent.click(screen.getByDisplayValue('switch_and_uninstall'));

		// В AWGM Route уже есть туннель и группа: без подтверждения замены кнопка закрыта.
		const submitBtn = screen.getByRole('button', { name: m.firc_migration_fwd_apply() });
		expect(submitBtn).toBeDisabled();
		await fireEvent.click(screen.getByRole('checkbox', { name: /будут заменены/i }));
		expect(submitBtn).not.toBeDisabled();
		await fireEvent.click(submitBtn);

		await waitFor(() => {
			expect(fircMigrateFromSingbox).toHaveBeenCalledWith(false, expect.objectContaining({ postSingboxAction: 'switch_and_uninstall' }));
			expect(notifications.success).toHaveBeenLastCalledWith(expect.stringContaining('Sing-box и его туннели удалены'));
			expect(get(fircMigrationModalOpen)).toBe(false);
		});
		const sent = fircMigrateFromSingbox.mock.calls.find((c) => c[0] === false)?.[1];
		expect(sent).not.toHaveProperty('mode');
		expect(sent).not.toHaveProperty('createSnapshot');
		// Группы не выбраны — их категория не трогается.
		expect(sent).toMatchObject({ includeTunnels: true, includeGroups: false, includeClientRoutes: false });
	});

	it('пустой AWGM Route — импорт без подтверждения замены', async () => {
		fircGetMigrationState.mockResolvedValue(baseOverview({ fircTunnelsCount: 0, fircGroupsCount: 0 }));
		render(FircMigrationWizardModal);
		openFircMigrationModal('forward');
		const submitBtn = await screen.findByRole('button', { name: m.firc_migration_fwd_apply() });
		expect(screen.queryByRole('checkbox', { name: /будут заменены/i })).toBeNull();
		expect(submitBtn).not.toBeDisabled();
	});

	it('allows 2-click purge of inactive Sing-box from engine strip when FIRC is active', async () => {
		fircGetMigrationState.mockResolvedValueOnce(
			baseOverview({
				activeEngine: 'firc',
				fircRunning: true,
				singboxRunning: false,
				singboxInstalled: true,
				singboxTunnelsCount: 2,
				singboxSubscriptionsCount: 1,
			}),
		);
		fircPurgeEngine.mockResolvedValue({
			engine: 'sing-box',
			tunnelsRemoved: 2,
			groupsRemoved: 0,
			subscriptionsRemoved: 1,
			runtimeRemoved: true,
			snapshotCreated: {
				id: 'snap-pre-purge',
				createdAt: 1794900010,
				label: 'pre-purge',
				trigger: 'auto-pre-purge',
				activeEngine: 'firc',
				singboxTunnelsCount: 2,
				singboxSubscriptionsCount: 1,
				singboxGroupsCount: 0,
				fircTunnelsCount: 1,
				fircGroupsCount: 1,
				filesCount: 5,
			},
			preservedSnapshots: 1,
		});
		fircGetMigrationState.mockResolvedValue(
			baseOverview({
				activeEngine: 'firc',
				fircRunning: true,
				singboxRunning: false,
				singboxInstalled: false,
				singboxTunnelsCount: 0,
				singboxSubscriptionsCount: 0,
			}),
		);

		render(FircMigrationWizardModal);
		openFircMigrationModal('forward');

		const purgeBtn = await screen.findByRole('button', { name: m.firc_migration_purge_singbox() });
		expect(screen.queryByRole('button', { name: m.firc_migration_purge_firc() })).toBeNull();

		await fireEvent.click(purgeBtn);
		const confirmBtn = await screen.findByRole('button', { name: m.firc_migration_purge_confirm() });
		await fireEvent.click(confirmBtn);

		await waitFor(() => {
			expect(fircPurgeEngine).toHaveBeenCalledWith('sing-box');
		});
		await screen.findByText(/Движок Sing-box и его туннели удалены/i);
	});

	it('shows auto-pre-purge trigger badge and missing-engine restore guard in Tab 3', async () => {
		fircGetMigrationState.mockResolvedValue(
			baseOverview({
				activeEngine: 'firc',
				fircRunning: true,
				singboxRunning: false,
				singboxInstalled: false,
				snapshots: [
					{
						id: 'snap-purge-1',
						createdAt: 1794900030,
						label: 'Перед удалением Sing-box',
						trigger: 'auto-pre-purge',
						activeEngine: 'sing-box',
						singboxTunnelsCount: 2,
						singboxSubscriptionsCount: 1,
						singboxGroupsCount: 1,
						fircTunnelsCount: 1,
						fircGroupsCount: 1,
						filesCount: 6,
					},
				],
			}),
		);

		render(FircMigrationWizardModal);
		openFircMigrationModal('snapshots');

		await screen.findByText(m.firc_migration_trigger_pre_purge());
		await screen.findByText(m.firc_migration_snap_missing_title());
		const restoreBtn = screen.getByRole('button', { name: m.firc_migration_snap_restore() });
		expect(restoreBtn).toBeDisabled();
	});

	it('auto-opens when backend returns shouldShowPopup=true and not dismissed', async () => {
		fircGetMigrationState.mockResolvedValue(
			baseOverview({
				shouldShowPopup: true,
				wizardState: { dismissedAutoPopup: false },
				activeEngine: 'sing-box',
				fircTunnelsCount: 0,
				singboxTunnelsCount: 1,
				singboxSubscriptionsCount: 1,
			}),
		);

		render(FircMigrationWizardModal);

		await waitFor(() => {
			expect(get(fircMigrationModalOpen)).toBe(true);
		});
		await screen.findByText(m.firc_migration_fwd_post_uninstall());
		expect(fircGetMigrationState).toHaveBeenCalledTimes(1);
	});

	it('does not auto-open when dismissedAutoPopup is true', async () => {
		fircGetMigrationState.mockResolvedValue(
			baseOverview({
				shouldShowPopup: true,
				wizardState: { dismissedAutoPopup: true },
			}),
		);

		render(FircMigrationWizardModal);

		await waitFor(() => {
			expect(fircGetMigrationState).toHaveBeenCalledTimes(1);
		});
		expect(get(fircMigrationModalOpen)).toBe(false);
	});

});
