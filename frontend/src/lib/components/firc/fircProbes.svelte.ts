/**
 * Замер отклика интерфейсов Fallback: какие интерфейсы и группы сейчас
 * замеряются, запуск замера и тосты с результатом. Результаты пишутся в общий
 * стор fircInterfaceProbeResults.
 */

import { api } from '$lib/api/client';
import { m } from '$lib/i18n';
import { notifications } from '$lib/stores/notifications';
import { fircInterfaceProbeResults } from '$lib/stores/firc';
import { isSpecialIface, type FircProbeOptions } from '$lib/utils/fircGroups';
import type { FircInterfaceProbeResult } from '$lib/types/firc';

export interface FircProbesController {
	/** Интерфейсы, замер которых идёт сейчас. */
	readonly ifaces: Record<string, boolean>;
	/** Ключи групп замера (`grp:<id>`, `pg:<id>`, …), идущих сейчас. */
	readonly groups: Record<string, boolean>;
	probe(ids: string[], opts?: FircProbeOptions): Promise<void>;
	/** Панель размонтирована: ответы, пришедшие позже, игнорируются. */
	destroy(): void;
}

/** label — человекочитаемое имя интерфейса для тоста. */
export function createFircProbes(label: (id: string) => string): FircProbesController {
	let ifaces = $state<Record<string, boolean>>({});
	let groups = $state<Record<string, boolean>>({});
	let destroyed = false;

	async function probe(ids: string[], opts?: FircProbeOptions): Promise<void> {
		const uniqueIds = Array.from(
			new Set(ids.map((x) => x.trim()).filter((x) => x.length > 0 && !isSpecialIface(x))),
		);
		if (uniqueIds.length === 0) return;
		const gKey = opts?.groupKey;
		if (gKey && groups[gKey]) return;

		const nextProbing = { ...ifaces };
		for (const id of uniqueIds) nextProbing[id] = true;
		ifaces = nextProbing;
		if (gKey) {
			groups = { ...groups, [gKey]: true };
		}

		try {
			const allResults: Record<string, FircInterfaceProbeResult> = {};
			for (let i = 0; i < uniqueIds.length; i += 16) {
				const chunk = uniqueIds.slice(i, i + 16);
				const res = await api.fircProbeInterfaces(chunk, opts?.url);
				if (destroyed) return;
				const ts = res?.checkedAt ?? Math.floor(Date.now() / 1000);
				for (const [k, r] of Object.entries(res?.results ?? {})) {
					allResults[k] = { ...r, checkedAt: ts };
				}
			}
			fircInterfaceProbeResults.update((prev) => ({
				...prev,
				...allResults,
			}));

			if (!opts?.silent) {
				if (uniqueIds.length === 1) {
					const onlyId = uniqueIds[0];
					const r = allResults[onlyId];
					const lbl = label(onlyId);
					if (r?.ok) {
						notifications.success(
							m.firc_fallback_toast_latency({ name: lbl, ms: Math.round(r.latencyMs) }),
						);
					} else {
						notifications.warning(
							m.firc_fallback_toast_timeout({
								name: lbl,
								error: r?.error ? ` (${r.error})` : '',
							}),
						);
					}
				} else {
					const okCount = uniqueIds.filter((id) => allResults[id]?.ok).length;
					notifications.success(
						m.firc_fallback_probe_updated({ ok: okCount, total: uniqueIds.length }),
					);
				}
			}
		} catch (e) {
			if (!opts?.silent && !destroyed) {
				notifications.error(
					m.firc_fallback_probe_error({
						error: e instanceof Error ? e.message : String(e),
					}),
				);
			}
		} finally {
			if (!destroyed) {
				const cleared = { ...ifaces };
				for (const id of uniqueIds) delete cleared[id];
				ifaces = cleared;
				if (gKey) {
					const nextG = { ...groups };
					delete nextG[gKey];
					groups = nextG;
				}
			}
		}
	}

	return {
		get ifaces() {
			return ifaces;
		},
		get groups() {
			return groups;
		},
		probe,
		destroy() {
			destroyed = true;
		},
	};
}
