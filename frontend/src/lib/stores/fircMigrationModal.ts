import { writable } from 'svelte/store';

export type FircMigrationWizardTab = 'forward' | 'reverse' | 'snapshots';

export const fircMigrationModalOpen = writable<boolean>(false);
export const fircMigrationModalTab = writable<FircMigrationWizardTab>('forward');
export const fircMigrationAppliedNonce = writable<number>(0);

export function notifyFircMigrationApplied(): void {
	fircMigrationAppliedNonce.update((n) => n + 1);
}

const LOCAL_DISMISS_KEY = 'awgm_firc_migration_popup_dismissed';

export function isFircMigrationPopupLocallyDismissed(): boolean {
	if (typeof window === 'undefined') return false;
	try {
		return window.localStorage.getItem(LOCAL_DISMISS_KEY) === '1';
	} catch {
		return false;
	}
}

export function setFircMigrationPopupLocallyDismissed(dismissed: boolean): void {
	if (typeof window === 'undefined') return;
	try {
		if (dismissed) {
			window.localStorage.setItem(LOCAL_DISMISS_KEY, '1');
		} else {
			window.localStorage.removeItem(LOCAL_DISMISS_KEY);
		}
	} catch {
		// ignore storage errors
	}
}

export function openFircMigrationModal(tab: FircMigrationWizardTab = 'forward'): void {
	fircMigrationModalTab.set(tab);
	fircMigrationModalOpen.set(true);
}

export function closeFircMigrationModal(): void {
	fircMigrationModalOpen.set(false);
}
