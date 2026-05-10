import { writable } from 'svelte/store';
import type { WizardState, WizardStep, ApplyLogEntry } from '$lib/types';

// Default state. `policyName` here is the description we PASS to NDMS when
// creating the policy on first run; the actual NDMS-assigned name comes back
// from singboxRouterCreatePolicy and is persisted to settings — see
// wizardOrchestrator.ts Phase 1.
const initialState = (): WizardState => ({
	step: 'presets',
	presetIds: [],
	tunnelTag: null,
	deviceMacs: [],
	policyName: 'SBRouter',
	dnsServer: null,
	applyLog: [],
	error: null,
});

function createSingboxWizardStore() {
	const open = writable(false);
	const state = writable<WizardState>(initialState());

	function reset(): void {
		state.set(initialState());
	}

	function start(): void {
		reset();
		open.set(true);
	}

	function close(): void {
		open.set(false);
	}

	function setStep(step: WizardStep): void {
		state.update((s) => ({ ...s, step }));
	}

	function setPresetIds(ids: string[]): void {
		state.update((s) => ({ ...s, presetIds: ids }));
	}

	function togglePresetId(id: string): void {
		state.update((s) => {
			const set = new Set(s.presetIds);
			if (set.has(id)) set.delete(id);
			else set.add(id);
			return { ...s, presetIds: Array.from(set) };
		});
	}

	function setTunnelTag(tag: string | null): void {
		state.update((s) => ({ ...s, tunnelTag: tag }));
	}

	function setDeviceMacs(macs: string[]): void {
		state.update((s) => ({ ...s, deviceMacs: macs }));
	}

	function setDnsServer(addr: string | null): void {
		state.update((s) => ({ ...s, dnsServer: addr }));
	}

	function pushLog(entry: ApplyLogEntry): void {
		state.update((s) => ({ ...s, applyLog: [...s.applyLog, entry] }));
	}

	function updateLastLog(patch: Partial<ApplyLogEntry>): void {
		state.update((s) => {
			if (s.applyLog.length === 0) return s;
			const copy = [...s.applyLog];
			copy[copy.length - 1] = { ...copy[copy.length - 1], ...patch };
			return { ...s, applyLog: copy };
		});
	}

	function setError(phase: string, message: string): void {
		state.update((s) => ({ ...s, error: { phase, message }, step: 'error' as WizardStep }));
	}

	function clearError(): void {
		state.update((s) => ({ ...s, error: null }));
	}

	return {
		open: { subscribe: open.subscribe },
		state: { subscribe: state.subscribe },
		start,
		close,
		reset,
		setStep,
		setPresetIds,
		togglePresetId,
		setTunnelTag,
		setDeviceMacs,
		setDnsServer,
		pushLog,
		updateLastLog,
		setError,
		clearError,
	};
}

export const singboxWizard = createSingboxWizardStore();
