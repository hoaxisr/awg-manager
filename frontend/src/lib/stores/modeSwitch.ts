// Shared mode-switch controller. Both routing-mode tabs (FakeIP, TProxy) drive
// their on/off toggle through here, so there is ONE confirm + progress flow and
// one source of truth for the in-flight transition. Reuses `fakeipTransition`
// (the global SSE/progress reducer) unchanged; this store holds only the UI
// phase, leaving the tested progress reducer free of modal/intent state.
import { get, writable } from 'svelte/store';
import { api } from '$lib/api/client';
import { fakeipTransition, type FakeIPMode } from '$lib/stores/fakeipTransition';
import { singboxRouter } from '$lib/stores/singboxRouter';
import { selectiveBypass } from '$lib/stores/selectiveBypass';
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

/**
 * Смена режима реально ИДЁТ (запрос ушёл, финал не пришёл) — в отличие от
 * `modeSwitchBusy`, который включает и открытый диалог подтверждения, когда с
 * движком ещё ничего не произошло. Нужно состоянию движка: пока переключение в
 * полёте, промежуточные enabled/active врут (см. EngineStatusInput.switching).
 */
export function modeSwitchInFlight(s: ModeSwitchState): boolean {
	return s.phase === 'running';
}

/** How often the SSE-loss watchdog re-polls backend status. */
const WATCHDOG_POLL_MS = 5000;
/** Hard cap: a switch that reported nothing for this long is declared failed. */
const WATCHDOG_TIMEOUT_MS = 4 * 60 * 1000;

function createModeSwitch() {
	const store = writable<ModeSwitchState>({ phase: 'idle', target: 'off', from: 'off' });
	let watchdog: ReturnType<typeof setInterval> | null = null;

	function stopWatchdog(): void {
		if (watchdog !== null) {
			clearInterval(watchdog);
			watchdog = null;
		}
	}

	/**
	 * SSE-loss fallback. The transition finale normally arrives as a
	 * `singbox-router:transition` SSE event, but a reconnect can swallow the
	 * terminal event — without a fallback the modal spins forever. The watchdog
	 * re-polls backend status and declares success only after TWO consecutive
	 * polls show the target mode (a single read can catch a transient state
	 * mid-teardown); after WATCHDOG_TIMEOUT_MS of no finale it fails the modal.
	 */
	function startWatchdog(target: FakeIPMode): void {
		stopWatchdog();
		const startedAt = Date.now();
		let confirmations = 0;
		let polling = false;
		watchdog = setInterval(async () => {
			if (polling) return;
			const t = get(fakeipTransition);
			if (get(store).phase !== 'running' || t === null || t.done) {
				stopWatchdog();
				return;
			}
			polling = true;
			try {
				await singboxRouter.loadAll();
			} catch {
				/* transient fetch failure — retry next tick */
			} finally {
				polling = false;
			}
			// Re-check after the await: the SSE finale may have landed meanwhile.
			const fresh = get(fakeipTransition);
			if (get(store).phase !== 'running' || fresh === null || fresh.done) {
				stopWatchdog();
				return;
			}
			const mode = currentMode(get(singboxRouter.status), get(singboxRouter.settings));
			confirmations = mode === target ? confirmations + 1 : 0;
			if (confirmations >= 2) {
				fakeipTransition.completeSuccess(target);
				stopWatchdog();
				return;
			}
			if (Date.now() - startedAt > WATCHDOG_TIMEOUT_MS) {
				fakeipTransition.fail(
					'Не получен финальный статус переключения — проверьте состояние роутера и обновите страницу',
				);
				stopWatchdog();
			}
		}, WATCHDOG_POLL_MS);
	}

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
		selectiveBypass.resetProgress();
		try {
			await api.singboxRouterSwitchMode(target);
			// Переключение асинхронное: прогресс и финал приходят по SSE,
			// watchdog подстраховывает потерянный терминальный event.
			startWatchdog(target);
		} catch (e) {
			fakeipTransition.fail(e instanceof Error ? e.message : 'Не удалось переключить режим');
		}
	}

	function closeProgress(): void {
		stopWatchdog();
		store.update((s) => ({ ...s, phase: 'idle' }));
		fakeipTransition.reset();
		selectiveBypass.resetProgress();
		void singboxRouter.loadAll();
	}

	return { subscribe: store.subscribe, request, cancel, confirm, closeProgress };
}

export const modeSwitch = createModeSwitch();
