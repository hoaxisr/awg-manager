import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { get } from 'svelte/store';

const {
	fircGetMigrationState,
	fircGetInstallStatus,
	fircInstall,
	singboxInstall,
	fircPurgeEngine,
	fircMigrateFromSingbox,
	fircReturnToSavedSingbox,
	fircReverseMigrateToSingbox,
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
		fircReturnToSavedSingbox: vi.fn(),
		fircReverseMigrateToSingbox: vi.fn(),
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
		fircReturnToSavedSingbox,
		fircReverseMigrateToSingbox,
		fircRestoreSnapshot,
		fircCreateSnapshot,
		fircDeleteSnapshot,
		fircUpdateMigrationState,
	},
}));

vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));
vi.mock('$lib/stores/firc', () => ({
	reloadFircAll: vi.fn().mockResolvedValue(undefined),
}));
vi.mock('$lib/stores/settings', () => ({
	reloadSettings: vi.fn().mockResolvedValue(undefined),
}));
vi.mock('$lib/stores/singboxRouter', () => ({
	singboxRouter: { reloadSettings: vi.fn().mockResolvedValue(undefined) },
}));
vi.mock('$lib/stores/singbox', () => ({
	singboxStatus: { refetch: vi.fn().mockResolvedValue(undefined) },
	singboxTunnels: { refetch: vi.fn().mockResolvedValue(undefined) },
}));
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
		savedSingbox: {
			available: false,
			source: 'none',
			tunnelsCount: 0,
			subscriptionsCount: 0,
			groupsCount: 0,
			filesCount: 0,
		},
		forwardPreview: {
			tunnels: [dummyTunnel],
			groups: [],
		},
		reversePreview: {
			tunnels: [],
			groups: [],
			outboundsCreated: 0,
			subscriptionsMigrated: 0,
			rulesMigrated: 0,
			ruleSetsMigrated: 0,
			clientRoutesMigrated: 0,
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
	fircReverseMigrateToSingbox.mockResolvedValue({
		tunnels: [],
		groups: [],
		outboundsCreated: 0,
		subscriptionsMigrated: 0,
		rulesMigrated: 0,
		ruleSetsMigrated: 0,
		clientRoutesMigrated: 0,
	});
	fircUpdateMigrationState.mockResolvedValue({ dismissedAutoPopup: false });
});

afterEach(() => {
	closeFircMigrationModal();
	cleanup();
});

describe('FircMigrationWizardModal engine purge & recovery', () => {
	it('offers switch_and_uninstall in Tab 1 and enforces mandatory snapshot on submit', async () => {
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
					savedSingboxPreserved: true,
				},
			};
		});

		render(FircMigrationWizardModal);
		openFircMigrationModal('forward');

		const cardText = await screen.findByText('Переключить на FIRC и полностью удалить Sing-box и его туннели');
		const radio = screen.getByDisplayValue('switch_and_uninstall');
		expect(cardText).toBeInTheDocument();

		const snapChk = screen.getByLabelText(/Автоматически создать проверенный/i);
		await fireEvent.click(snapChk);
		expect(snapChk).not.toBeChecked();

		await fireEvent.click(radio);
		await waitFor(() => {
			expect(snapChk).toBeChecked();
			expect(snapChk).toBeDisabled();
		});

		const submitBtn = screen.getByRole('button', { name: 'Выполнить миграцию в FIRC' });
		await fireEvent.click(submitBtn);

		await waitFor(() => {
			expect(fircMigrateFromSingbox).toHaveBeenCalledWith(
				false,
				expect.objectContaining({
					postSingboxAction: 'switch_and_uninstall',
					createSnapshot: true,
				}),
			);
			expect(notifications.success).toHaveBeenLastCalledWith(
				expect.stringContaining('Sing-box и его туннели удалены'),
			);
			expect(get(fircMigrationModalOpen)).toBe(false);
		});
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
			savedSingboxPreserved: true,
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

		const purgeBtn = await screen.findByRole('button', { name: 'Удалить Sing-box и туннели' });
		expect(screen.queryByRole('button', { name: 'Удалить FIRC и туннели' })).toBeNull();

		await fireEvent.click(purgeBtn);
		const confirmBtn = await screen.findByRole('button', { name: 'Подтвердить удаление' });
		await fireEvent.click(confirmBtn);

		await waitFor(() => {
			expect(fircPurgeEngine).toHaveBeenCalledWith('sing-box');
		});
		await screen.findByText(/Движок Sing-box и его туннели удалены/i);
	});

	it('allows reinstalling removed Sing-box in Tab 2 and returning with switch_and_uninstall_firc', async () => {
		fircGetMigrationState.mockResolvedValueOnce(
			baseOverview({
				activeEngine: 'firc',
				fircRunning: true,
				singboxRunning: false,
				singboxInstalled: false,
				savedSingbox: {
					available: true,
					source: 'saved_bundle',
					tunnelsCount: 1,
					subscriptionsCount: 0,
					groupsCount: 1,
					filesCount: 4,
				},
			}),
		);
		singboxInstall.mockResolvedValue({ installed: true, running: false });
		fircGetMigrationState.mockResolvedValue(
			baseOverview({
				activeEngine: 'firc',
				fircRunning: true,
				singboxRunning: false,
				singboxInstalled: true,
				savedSingbox: {
					available: true,
					source: 'saved_bundle',
					tunnelsCount: 1,
					subscriptionsCount: 0,
					groupsCount: 1,
					filesCount: 4,
				},
			}),
		);
		fircReturnToSavedSingbox.mockResolvedValue({
			source: 'saved_bundle',
			restoredFiles: 4,
			postActionApplied: 'switch_and_uninstall_firc',
			snapshotCreated: {
				id: 'snap-pre-ret',
				createdAt: 1794900020,
				label: 'pre-ret',
				trigger: 'auto-pre-return-singbox',
				activeEngine: 'firc',
				singboxTunnelsCount: 0,
				singboxSubscriptionsCount: 0,
				singboxGroupsCount: 0,
				fircTunnelsCount: 1,
				fircGroupsCount: 1,
				filesCount: 4,
			},
			removedEngine: {
				engine: 'firc',
				tunnelsRemoved: 1,
				groupsRemoved: 1,
				runtimeRemoved: true,
				preservedSnapshots: 2,
				savedSingboxPreserved: true,
			},
		});

		render(FircMigrationWizardModal);
		openFircMigrationModal('reverse');

		await screen.findByText('Бинарный файл Sing-box не установлен');
		const returnBtn = screen.getByRole('button', { name: 'Вернуться к сохранённому Sing-box' });
		expect(returnBtn).toBeDisabled();

		const installBtns = screen.getAllByRole('button', { name: 'Установить Sing-box' });
		await fireEvent.click(installBtns[0]);
		await waitFor(() => {
			expect(singboxInstall).toHaveBeenCalled();
		});

		await waitFor(() => {
			expect(screen.queryByText('Бинарный файл Sing-box не установлен')).toBeNull();
			expect(returnBtn).not.toBeDisabled();
		});

		const uninstallFircRadio = screen.getByDisplayValue('switch_and_uninstall_firc');
		await fireEvent.click(uninstallFircRadio);
		await fireEvent.click(returnBtn);

		await waitFor(() => {
			expect(fircReturnToSavedSingbox).toHaveBeenCalledWith({
				createSnapshot: true,
				postFircAction: 'switch_and_uninstall_firc',
			});
			expect(notifications.success).toHaveBeenLastCalledWith(
				expect.stringContaining('FIRC и его туннели удалены'),
			);
			expect(get(fircMigrationModalOpen)).toBe(false);
		});
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

		await screen.findByText('Авто до удаления');
		await screen.findByText('Обнаружены удалённые движки маршрутизации');
		const restoreBtn = screen.getByRole('button', { name: 'Восстановить' });
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
		await screen.findByText('Переключить на FIRC и полностью удалить Sing-box и его туннели');
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

	it('shows pending Sing-box badge for saved bundle when validator_unavailable and filesCount > 0', async () => {
		fircGetMigrationState.mockResolvedValue(
			baseOverview({
				activeEngine: 'firc',
				fircRunning: true,
				singboxRunning: false,
				singboxInstalled: false,
				savedSingbox: {
					available: false,
					verificationStatus: 'validator_unavailable',
					tunnelsCount: 1,
					subscriptionsCount: 0,
					groupsCount: 5,
					filesCount: 6,
				},
			}),
		);

		render(FircMigrationWizardModal);
		openFircMigrationModal('reverse');

		const cardBadges = await screen.findAllByText('Ожидает Sing-box');
		expect(cardBadges.length).toBeGreaterThanOrEqual(1);
		expect(screen.queryByText('Нет сохранённой копии')).toBeNull();
		const scenario1Radio = screen.getByDisplayValue('saved');
		await fireEvent.click(scenario1Radio);
		await waitFor(() => {
			expect(screen.getAllByText('Ожидает Sing-box').length).toBeGreaterThanOrEqual(2);
		});
	});
});
