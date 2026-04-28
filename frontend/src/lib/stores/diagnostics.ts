import { writable } from 'svelte/store';
import type { DiagTestEvent, DiagDoneSummary } from '$lib/types';

interface DiagnosticsState {
	running: boolean;
	tests: DiagTestEvent[];
	currentPhase: string;
	summary: DiagDoneSummary | null;
	errorMessage: string;
	lastRunAt: Date | null;
}

function createDiagnosticsStore() {
	const { subscribe, update, set } = writable<DiagnosticsState>({
		running: false,
		tests: [],
		currentPhase: '',
		summary: null,
		errorMessage: '',
		lastRunAt: null,
	});

	return {
		subscribe,
		start() {
			update((s) => ({
				...s,
				running: true,
				tests: [],
				currentPhase: '',
				summary: null,
				errorMessage: '',
			}));
		},
		setPhase(phase: string) {
			update((s) => ({ ...s, currentPhase: phase }));
		},
		addTest(test: DiagTestEvent) {
			update((s) => ({ ...s, tests: [...s.tests, test] }));
		},
		finish(summary: DiagDoneSummary) {
			update((s) => ({
				...s,
				running: false,
				summary,
				currentPhase: '',
				lastRunAt: new Date(),
			}));
		},
		fail(message: string) {
			update((s) => ({ ...s, running: false, errorMessage: message, currentPhase: '' }));
		},
		reset() {
			set({
				running: false,
				tests: [],
				currentPhase: '',
				summary: null,
				errorMessage: '',
				lastRunAt: null,
			});
		},
	};
}

export const diagnosticsStore = createDiagnosticsStore();
