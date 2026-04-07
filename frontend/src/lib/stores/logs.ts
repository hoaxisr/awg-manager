import { writable } from 'svelte/store';
import type { LogEntry } from '$lib/types';
import type { LogEntryEvent, SnapshotLogsEvent } from '$lib/api/events';

const MAX_ENTRIES = 500;

function createLogStore() {
	const { subscribe, update, set } = writable<LogEntry[]>([]);
	const logsEnabled = writable(true);
	const logsTotal = writable(0);
	const loaded = writable(false);

	return {
		subscribe,
		enabled: { subscribe: logsEnabled.subscribe },
		total: { subscribe: logsTotal.subscribe },
		loaded: { subscribe: loaded.subscribe },
		append(entry: LogEntryEvent) {
			const logEntry: LogEntry = {
				...entry,
				subgroup: entry.subgroup ?? '',
			};
			update(entries => {
				const updated = [logEntry, ...entries];
				if (updated.length > MAX_ENTRIES) {
					updated.length = MAX_ENTRIES;
				}
				return updated;
			});
		},
		setSnapshot(data: SnapshotLogsEvent) {
			set(data.logs ?? []);
			logsEnabled.set(data.enabled);
			logsTotal.set(data.total);
			loaded.set(true);
		},
		clear() {
			set([]);
			logsTotal.set(0);
		},
		setEntries(entries: LogEntry[]) {
			set(entries);
		},
		setTotal(n: number) {
			logsTotal.set(n);
		}
	};
}

export const logEntries = createLogStore();
