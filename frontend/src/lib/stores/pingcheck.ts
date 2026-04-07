import { writable } from 'svelte/store';
import type { PingCheckStateEvent, SnapshotPingcheckEvent, PingCheckLogEvent } from '$lib/api/events';
import type { TunnelPingStatus, PingLogEntry } from '$lib/types';

export interface PingCheckStatus {
	tunnelId: string;
	status: string;
	failCount: number;
	successCount: number;
}

function createPingCheckStore() {
	const { subscribe, update } = writable<Map<string, PingCheckStatus>>(new Map());
	const statusesList = writable<TunnelPingStatus[]>([]);
	const logsList = writable<PingLogEntry[]>([]);
	const loaded = writable(false);

	return {
		subscribe,
		statuses: { subscribe: statusesList.subscribe },
		logs: { subscribe: logsList.subscribe },
		loaded: { subscribe: loaded.subscribe },
		updateStatus(data: PingCheckStateEvent) {
			update(map => {
				map.set(data.tunnelId, {
					tunnelId: data.tunnelId,
					status: data.status,
					failCount: data.failCount,
					successCount: data.successCount,
				});
				return new Map(map);
			});
			// Also merge into statuses list so UI stays in sync
			statusesList.update(list =>
				list.map(t =>
					t.tunnelId === data.tunnelId
						? { ...t, status: data.status as TunnelPingStatus['status'], failCount: data.failCount, successCount: data.successCount }
						: t
				)
			);
		},
		setSnapshot(data: SnapshotPingcheckEvent) {
			statusesList.set(data.statuses ?? []);
			logsList.set(data.logs ?? []);
			loaded.set(true);
			// Also update the status map from statuses list
			update(() => {
				const newMap = new Map<string, PingCheckStatus>();
				for (const s of (data.statuses ?? [])) {
					newMap.set(s.tunnelId, {
						tunnelId: s.tunnelId,
						status: s.status,
						failCount: s.failCount,
						successCount: s.successCount ?? 0,
					});
				}
				return newMap;
			});
		},
		setTunnelEnabled(id: string, enabled: boolean) {
			statusesList.update(list =>
				list.map(t =>
					t.tunnelId === id
						? { ...t, enabled, status: enabled ? (t.status === 'disabled' ? 'alive' : t.status) : 'disabled' }
						: t
				)
			);
		},
		appendLog(entry: PingCheckLogEvent) {
			logsList.update(list => {
				const logEntry = entry as unknown as PingLogEntry;
				return [logEntry, ...list].slice(0, 200);
			});
		},
		clearLogs() {
			logsList.set([]);
		},
		clear() {
			update(() => new Map());
		}
	};
}

export const pingCheckStatus = createPingCheckStore();
