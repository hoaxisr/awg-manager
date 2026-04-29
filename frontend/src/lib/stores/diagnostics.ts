import { writable } from 'svelte/store';
import type { DiagTestEvent, DiagDoneSummary, TargetSummary, TunnelListItem } from '$lib/types';
import { GLOBAL_TARGET_ID } from '$lib/types';

interface DiagnosticsState {
	running: boolean;
	tests: DiagTestEvent[];
	currentPhase: string;
	summary: DiagDoneSummary | null;
	errorMessage: string;
	lastRunAt: Date | null;
	targets: TargetSummary[];
}

function emptyCounts(): TargetSummary['counts'] {
	return { pass: 0, warn: 0, fail: 0, error: 0, skip: 0, total: 0 };
}

function ledFromCounts(c: TargetSummary['counts']): TargetSummary['overallLed'] {
	if (c.total === 0) return 'gray';
	if (c.fail > 0 || c.error > 0) return 'red';
	if (c.warn > 0 || c.skip > 0) return 'yellow';
	return 'green';
}

function rebuildTargets(tests: DiagTestEvent[], tunnels: TunnelListItem[]): TargetSummary[] {
	const map = new Map<string, TargetSummary>();

	// Always seed Global pseudo-target first
	map.set(GLOBAL_TARGET_ID, {
		id: GLOBAL_TARGET_ID,
		name: 'Глобальные',
		isGlobal: true,
		counts: emptyCounts(),
		overallLed: 'gray',
	});

	// Seed every known tunnel so rail shows them even before tests arrive
	for (const t of tunnels) {
		map.set(t.id, {
			id: t.id,
			name: t.name,
			isGlobal: false,
			tunnelStatus: t.status === 'running' ? 'running' : 'stopped',
			counts: emptyCounts(),
			overallLed: 'gray',
		});
	}

	// Group tests by tunnelId (or GLOBAL if missing)
	for (const test of tests) {
		const id = test.tunnelId || GLOBAL_TARGET_ID;
		const target = map.get(id);
		if (!target) continue;
		target.counts.total++;
		switch (test.status) {
			case 'pass': target.counts.pass++; break;
			case 'warn': target.counts.warn++; break;
			case 'fail': target.counts.fail++; break;
			case 'error': target.counts.error++; break;
			case 'skip': target.counts.skip++; break;
		}
	}

	// Compute LEDs
	for (const target of map.values()) {
		target.overallLed = ledFromCounts(target.counts);
	}

	// Order: Global first, then tunnels in input order
	const arr: TargetSummary[] = [];
	const global = map.get(GLOBAL_TARGET_ID);
	if (global) arr.push(global);
	for (const t of tunnels) {
		const target = map.get(t.id);
		if (target) arr.push(target);
	}
	return arr;
}

function createDiagnosticsStore() {
	const { subscribe, update, set } = writable<DiagnosticsState>({
		running: false,
		tests: [],
		currentPhase: '',
		summary: null,
		errorMessage: '',
		lastRunAt: null,
		targets: [],
	});

	return {
		subscribe,
		seedTargets(tunnels: TunnelListItem[]) {
			update((s) => ({ ...s, targets: rebuildTargets(s.tests, tunnels) }));
		},
		start(tunnels: TunnelListItem[]) {
			update((s) => ({
				...s,
				running: true,
				tests: [],
				currentPhase: '',
				summary: null,
				errorMessage: '',
				targets: rebuildTargets([], tunnels),
			}));
		},
		setPhase(phase: string) {
			update((s) => ({ ...s, currentPhase: phase }));
		},
		addTest(test: DiagTestEvent, tunnels: TunnelListItem[]) {
			update((s) => {
				const tests = [...s.tests, test];
				return { ...s, tests, targets: rebuildTargets(tests, tunnels) };
			});
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
				targets: [],
			});
		},
	};
}

export const diagnosticsStore = createDiagnosticsStore();
