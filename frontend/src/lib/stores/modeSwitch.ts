// Shared mode-switch controller. Both routing-mode tabs (FakeIP, TProxy) drive
// their on/off toggle through here, so there is ONE confirm + progress flow and
// one source of truth for the in-flight transition. Reuses `fakeipTransition`
// (the global SSE/progress reducer) unchanged; this store holds only the UI
// phase, leaving the tested progress reducer free of modal/intent state.
import { get, writable } from 'svelte/store';
import { api } from '$lib/api/client';
import { fakeipTransition, type FakeIPMode } from '$lib/stores/fakeipTransition';
import { singboxRouter } from '$lib/stores/singboxRouter';
import type { SingboxRouterStatus, SingboxRouterSettings } from '$lib/types';

export type ModeSwitchPhase = 'idle' | 'confirming' | 'running';

export interface ModeSwitchState {
	phase: ModeSwitchPhase;
	target: FakeIPMode;
	from: FakeIPMode;
}

/**
 * Honest current routing mode. CRITICAL: gate on `enabled`, never bare
 * `routingMode` — after SwitchMode('off') the backend leaves
 * routingMode='fakeip-tun' with enabled=false, so bare routingMode would lie.
 * `enabled` comes from `status` (live SSE); `routingMode` from `settings`.
 */
export function currentMode(
	status: SingboxRouterStatus | null,
	settings: SingboxRouterSettings | null,
): FakeIPMode {
	if (!status?.enabled) return 'off';
	return (settings?.routingMode as FakeIPMode | undefined) ?? 'tproxy';
}

/** Busy = a switch is being confirmed or is running → disables both toggles + tab nav. */
export function modeSwitchBusy(s: ModeSwitchState): boolean {
	return s.phase !== 'idle';
}

function createModeSwitch() {
	const store = writable<ModeSwitchState>({ phase: 'idle', target: 'off', from: 'off' });

	function request(target: FakeIPMode): void {
		// One-shot snapshot of `from`. Safe: the busy-guard (modeSwitchBusy) blocks a
		// second request mid-transition, so the two-store read can't race.
		const from = currentMode(get(singboxRouter.status), get(singboxRouter.settings));
		if (target === from) return; // no-op (also guards a fast double-click)
		store.set({ phase: 'confirming', target, from });
	}

	function cancel(): void {
		// Confirm-dialog only: ignore unless we're awaiting confirmation, so a stray
		// call can't abandon a running transition mid-flight (would desync from
		// fakeipTransition). Leaving 'running' is exclusively closeProgress's job.
		store.update((s) => (s.phase === 'confirming' ? { ...s, phase: 'idle' } : s));
	}

	async function confirm(): Promise<void> {
		const { from, target } = get(store);
		store.update((s) => ({ ...s, phase: 'running' }));
		fakeipTransition.begin(from, target);
		try {
			await api.singboxRouterSwitchMode(target);
		} catch (e) {
			// POST failed: STAY in 'running' — the progress modal surfaces the error
			// (via fakeipTransition.fail below) and the user dismisses it through
			// closeProgress → 'idle'. Mirrors the success path's modal lifecycle.
			fakeipTransition.fail(e instanceof Error ? e.message : 'Не удалось переключить режим');
		}
	}

	function closeProgress(): void {
		store.update((s) => ({ ...s, phase: 'idle' }));
		fakeipTransition.reset();
		void singboxRouter.loadAll();
	}

	return { subscribe: store.subscribe, request, cancel, confirm, closeProgress };
}

export const modeSwitch = createModeSwitch();
