// Тексты итогов мастера миграции Sing-box ⇄ FIRC. Итог хранится в $state,
// поэтому поля — UiText: сообщение i18n вычисляется при показе и следует за
// языком, а значения от бэкенда (id, ошибки, коды действий) подставляются как есть.
import { m, formatLocale, type UiText } from '$lib/i18n';
import type {
	FircEnginePurgeResult,
	FircGroup,
	FircMigrationSummary,
	FircReverseMigrationSummary,
	FircSavedSingboxRestoreResult,
	FircSnapshotMeta,
	FircTunnel,
} from '$lib/types/firc';

export type MigrationEngine = 'sing-box' | 'firc';

export interface ExecNotice {
	kind: 'success' | 'warning' | 'error';
	configStatus: UiText;
	snapshotStatus: UiText;
	fircStartStatus?: UiText;
	engineStatus: UiText;
	recoveryRequired?: boolean;
}

export const engineLabel = (engine: MigrationEngine): string => (engine === 'sing-box' ? 'Sing-box' : 'AWGM Route');

export function isSubscriptionTunnel(tun: FircTunnel): boolean {
	return (tun.sources ?? []).some((s) => s.kind === 'subscription');
}

export function isClientRouteGroup(grp: FircGroup): boolean {
	return (grp.devices?.allow?.length ?? 0) > 0 && grp.id.startsWith('cr');
}

/** Добавляет id в выбор или убирает его оттуда. */
export function toggleId(list: string[], id: string): string[] {
	return list.includes(id) ? list.filter((x) => x !== id) : [...list, id];
}

export function formatTs(unixSec?: number): string {
	if (!unixSec) return '—';
	try {
		return new Date(unixSec * 1000).toLocaleString(formatLocale(), {
			day: '2-digit',
			month: '2-digit',
			year: 'numeric',
			hour: '2-digit',
			minute: '2-digit',
			second: '2-digit',
		});
	} catch {
		return String(unixSec);
	}
}

export function snapTriggerLabel(trigger: string): string {
	switch (trigger) {
		case 'manual':
			return m.firc_migration_trigger_manual();
		case 'auto-pre-purge':
			return m.firc_migration_trigger_pre_purge();
		case 'auto-pre-reverse':
			return m.firc_migration_trigger_pre_reverse();
		case 'auto-pre-return-singbox':
			return m.firc_migration_trigger_pre_return();
		default:
			return m.firc_migration_trigger_pre_migrate();
	}
}

export function purgeItemsSummary(engine: MigrationEngine, res: FircEnginePurgeResult): string {
	return engine === 'sing-box'
		? m.firc_migration_purge_items_singbox({ tunnels: res.tunnelsRemoved, subs: res.subscriptionsRemoved ?? 0 })
		: m.firc_migration_purge_items_firc({ tunnels: res.tunnelsRemoved, groups: res.groupsRemoved });
}

export function purgeNotice(engine: MigrationEngine, res: FircEnginePurgeResult, activeEngine: string): ExecNotice {
	const label = engineLabel(engine);
	const items = () => purgeItemsSummary(engine, res);
	const snap = res.snapshotCreated;
	return {
		kind: 'success',
		configStatus: res.runtimeRemoved
			? () => m.firc_migration_purge_config_runtime({ engine: label, items: items() })
			: () => m.firc_migration_purge_config({ engine: label, items: items() }),
		snapshotStatus: snap
			? () => m.firc_migration_purge_snapshot_created({ id: snap.id, count: res.preservedSnapshots })
			: () => m.firc_migration_purge_snapshots_kept({ count: res.preservedSnapshots }),
		engineStatus: () =>
			(res.savedSingboxPreserved ? m.firc_migration_purge_engine_saved : m.firc_migration_purge_engine)({
				engine: activeEngine,
				label,
				tab: m.firc_migration_tab_snapshots(),
			}),
	};
}

function snapshotStatus(effective: boolean, createdId: string | undefined, strict: boolean): UiText {
	if (!effective) return strict ? () => m.firc_migration_snap_disabled_by_user() : () => m.firc_migration_snap_disabled();
	if (createdId) return () => m.firc_migration_snap_created({ id: createdId });
	return strict ? () => m.firc_migration_snap_unconfirmed_error() : () => m.firc_migration_snap_unconfirmed();
}

type FwdPostAction = 'switch_standby' | 'switch_and_stop' | 'switch_and_uninstall' | 'keep_current';

export function forwardNotice(
	res: FircMigrationSummary,
	opts: { effectiveSnap: boolean; postAction: FwdPostAction; activeEngine: string; hasPostErr: boolean },
): ExecNotice {
	const tunnels = res.tunnels?.length ?? res.tunnelsCreated ?? 0;
	const groups = res.groups?.length ?? res.groupsCreated ?? 0;
	const startError = res.startError;
	let fircStartStatus: UiText;
	if (opts.postAction === 'keep_current') {
		fircStartStatus = res.fircStarted ? () => m.firc_migration_fwd_start_already() : () => m.firc_migration_fwd_start_skipped();
	} else if (startError) {
		fircStartStatus = () => m.firc_migration_fwd_start_failed({ error: startError });
	} else {
		fircStartStatus = res.fircStarted === false ? () => m.firc_migration_fwd_start_not_running() : () => m.firc_migration_fwd_start_running();
	}
	const switchErr = res.switchError || res.postActionError;
	const removed = res.removedEngine;
	const applied = res.postActionApplied ?? '';
	let engineStatus: UiText;
	if (switchErr) engineStatus = () => m.firc_migration_fwd_switch_failed({ error: switchErr });
	else if (applied === 'keep_current') engineStatus = () => m.firc_migration_fwd_not_switched({ engine: opts.activeEngine });
	else if (removed)
		engineStatus = () =>
			m.firc_migration_fwd_switched_removed({ tunnels: removed.tunnelsRemoved, subs: removed.subscriptionsRemoved ?? 0 });
	else engineStatus = () => m.firc_migration_fwd_switched({ action: applied });
	return {
		kind: opts.hasPostErr || res.recoveryRequired ? 'error' : 'success',
		configStatus:
			res.configMigrated === false
				? () => m.firc_migration_fwd_config_not_migrated()
				: () => m.firc_migration_fwd_config_saved({ tunnels, groups }),
		snapshotStatus: snapshotStatus(opts.effectiveSnap, res.snapshotCreated?.id, true),
		fircStartStatus,
		engineStatus,
		recoveryRequired: res.recoveryRequired,
	};
}

export function forwardDoneText(res: FircMigrationSummary): string {
	const tunnels = res.tunnels?.length ?? res.tunnelsCreated ?? 0;
	const groups = res.groups?.length ?? res.groupsCreated ?? 0;
	const id = res.snapshotCreated?.id;
	if (id) {
		return res.removedEngine
			? m.firc_migration_fwd_done_snap_removed({ tunnels, groups, id })
			: m.firc_migration_fwd_done_snap({ tunnels, groups, id });
	}
	return res.removedEngine ? m.firc_migration_fwd_done_removed({ tunnels, groups }) : m.firc_migration_fwd_done({ tunnels, groups });
}

/** Общая часть возврата на Sing-box: сохранённая копия и обратная миграция. */
function toSingboxEngineStatus(
	res: { postActionError?: string; postActionApplied?: string; removedEngine?: FircEnginePurgeResult },
	keepCurrent: () => string,
): UiText {
	const err = res.postActionError;
	const removed = res.removedEngine;
	const applied = res.postActionApplied ?? '';
	if (err) return () => m.firc_migration_to_singbox_switch_failed({ error: err });
	if (applied === 'keep_current') return keepCurrent;
	if (removed)
		return () =>
			m.firc_migration_to_singbox_switched_removed({ tunnels: removed.tunnelsRemoved, groups: removed.groupsRemoved });
	return () => m.firc_migration_to_singbox_switched({ action: applied });
}

export function savedReturnNotice(res: FircSavedSingboxRestoreResult, effectiveSnap: boolean, hasPostErr: boolean): ExecNotice {
	const files = res.restoredFiles;
	return {
		kind: hasPostErr || res.recoveryRequired ? 'error' : 'success',
		configStatus: res.removedEngine
			? () => m.firc_migration_saved_config_removed({ files })
			: () => m.firc_migration_saved_config_kept({ files }),
		snapshotStatus: snapshotStatus(effectiveSnap, res.snapshotCreated?.id, false),
		engineStatus: toSingboxEngineStatus(res, () => m.firc_migration_saved_keep_current()),
		recoveryRequired: res.recoveryRequired,
	};
}

export function reverseNotice(res: FircReverseMigrationSummary, effectiveSnap: boolean, hasPostErr: boolean): ExecNotice {
	const counts = {
		outbounds: res.outboundsCreated ?? 0,
		subs: res.subscriptionsMigrated ?? 0,
		rules: res.rulesMigrated ?? 0,
	};
	return {
		kind: hasPostErr || res.recoveryRequired ? 'error' : 'success',
		configStatus: () => m.firc_migration_rev_config_saved(counts),
		snapshotStatus: snapshotStatus(effectiveSnap, res.snapshotCreated?.id, false),
		engineStatus: toSingboxEngineStatus(res, () => m.firc_migration_rev_keep_current()),
		recoveryRequired: res.recoveryRequired,
	};
}

export function restoreNotice(restored: FircSnapshotMeta): ExecNotice {
	const err = restored.postActionError;
	const engine = restored.activeEngine;
	const applied = restored.postActionApplied;
	return {
		kind: err || restored.recoveryRequired ? 'error' : 'success',
		configStatus: () =>
			m.firc_migration_restore_config({ label: restored.label || restored.id, count: restored.filesCount }),
		snapshotStatus: () => m.firc_migration_restore_point({ time: formatTs(restored.createdAt) }),
		engineStatus: err
			? () => m.firc_migration_restore_switch_failed({ engine, error: err })
			: () => m.firc_migration_restore_engine({ engine, action: applied || m.firc_migration_confirmed() }),
		recoveryRequired: restored.recoveryRequired,
	};
}
