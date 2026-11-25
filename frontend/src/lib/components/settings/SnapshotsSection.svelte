<!-- Снимки данных, которые панель делает перед обновлением. -->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, ConfirmModal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { downloadBlob } from '$lib/utils/download';
	import { formatBytes, formatDate } from '$lib/utils/format';
	import { updateSnapshots } from '$lib/stores/updateSnapshots';
	import type { PollingState } from '$lib/stores/polling';
	import type { UpdateSnapshot, UpdateSnapshotsData } from '$lib/types';
	import { restoreOp } from './restoreOp.svelte';

	interface Props {
		/** Своя карточка подписана заголовком — строке он не нужен. */
		heading?: boolean;
	}

	let { heading = true }: Props = $props();

	const restore = restoreOp();

	// Список — через стор: снимок, снятый автообновлением, и удаление в другой
	// вкладке приходят SSE-подсказкой без перезагрузки страницы.
	let snapshotsState = $state<PollingState<UpdateSnapshotsData> | null>(null);
	$effect(() => updateSnapshots.subscribe((s) => (snapshotsState = s)));
	let snapshots = $derived(snapshotsState?.data?.snapshots ?? []);
	let snapshotKeep = $derived(snapshotsState?.data?.keep ?? 3);
	let snapshotTtlDays = $derived(snapshotsState?.data?.ttlDays ?? 7);
	// Ошибку загрузки не выдаём за «снимков нет».
	let snapshotsError = $derived(snapshotsState?.status === 'error' ? snapshotsState.error : null);
	let snapshotsLoaded = $derived(snapshotsState?.data != null);
	let snapshotBusy = $state<string | null>(null);
	let restoreTarget = $state<UpdateSnapshot | null>(null);
	let deleteSnapshotTarget = $state<UpdateSnapshot | null>(null);

	async function confirmRestore() {
		const snap = restoreTarget;
		restoreTarget = null;
		if (snap) await restore.run(() => api.restoreUpdateSnapshot(snap.id));
	}

	async function downloadSnapshot(snap: UpdateSnapshot) {
		snapshotBusy = snap.id;
		try {
			downloadBlob(await api.downloadUpdateSnapshot(snap.id), `awg-manager-${snap.id}`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_backup_snapshot_download_failed());
		} finally {
			snapshotBusy = null;
		}
	}

	async function confirmDeleteSnapshot() {
		const snap = deleteSnapshotTarget;
		if (!snap) return;
		snapshotBusy = snap.id;
		try {
			updateSnapshots.applyMutationResponse(await api.deleteUpdateSnapshot(snap.id));
			deleteSnapshotTarget = null;
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_backup_snapshot_delete_failed());
		} finally {
			snapshotBusy = null;
		}
	}

	function snapshotLabel(snap: UpdateSnapshot): string {
		return snap.appVersion
			? m.settings_backup_snapshot_name({ date: formatDate(snap.createdAt), version: snap.appVersion })
			: formatDate(snap.createdAt);
	}
</script>

<div class="snapshots">
	<div class="flex flex-col gap-1">
		{#if heading}
			<span class="font-medium">{m.settings_backup_snapshots_label()}</span>
		{/if}
		<span class="setting-description">
			{m.settings_backup_snapshots_description({ keep: snapshotKeep, days: snapshotTtlDays })}
		</span>
	</div>
	{#if snapshotsError}
		<div class="snapshots-error">
			<p class="setting-description">{m.settings_backup_snapshots_load_failed({ error: snapshotsError })}</p>
			<Button variant="ghost" size="sm" onclick={() => updateSnapshots.refetch()}>{m.common_retry()}</Button>
		</div>
	{:else if !snapshotsLoaded}
		<!-- Пока список грузится, пустым его не показываем. -->
	{:else if snapshots.length === 0}
		<p class="setting-description snapshots-empty">{m.settings_backup_snapshots_empty()}</p>
	{:else}
		<ul class="snapshot-list">
			{#each snapshots as snap (snap.id)}
				<li class="snapshot-row">
					<div class="flex flex-col">
						<span class="snapshot-name">{snapshotLabel(snap)}</span>
						<span class="setting-description">{formatBytes(snap.size, 1)}</span>
					</div>
					<div class="setting-actions">
						<Button
							variant="ghost"
							size="sm"
							disabled={restore.restoring || snapshotBusy !== null}
							onclick={() => downloadSnapshot(snap)}
						>
							{m.common_download()}
						</Button>
						<Button
							variant="outline-danger"
							size="sm"
							disabled={restore.restoring || snapshotBusy !== null}
							onclick={() => (restoreTarget = snap)}
						>
							{m.settings_backup_restore()}
						</Button>
						<Button
							variant="ghost"
							size="sm"
							disabled={restore.restoring || snapshotBusy !== null}
							onclick={() => (deleteSnapshotTarget = snap)}
						>
							{m.common_delete()}
						</Button>
					</div>
				</li>
			{/each}
		</ul>
	{/if}
</div>

<ConfirmModal
	open={restoreTarget !== null}
	title={m.settings_backup_confirm_title()}
	message={restoreTarget ? m.settings_backup_snapshot_confirm_message({ name: snapshotLabel(restoreTarget) }) : ''}
	confirmLabel={m.settings_backup_restore()}
	variant="danger"
	busy={restore.restoring}
	onClose={() => (restoreTarget = null)}
	onConfirm={confirmRestore}
/>

<ConfirmModal
	open={deleteSnapshotTarget !== null}
	title={m.settings_backup_snapshot_delete_title()}
	message={deleteSnapshotTarget
		? m.settings_backup_snapshot_delete_message({ name: snapshotLabel(deleteSnapshotTarget) })
		: ''}
	confirmLabel={m.common_delete()}
	variant="danger"
	busy={snapshotBusy !== null}
	onClose={() => (deleteSnapshotTarget = null)}
	onConfirm={confirmDeleteSnapshot}
/>

<style>
	.snapshots {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	:global(.setting-row) + .snapshots {
		padding-top: 0.875rem;
		border-top: 1px solid var(--border);
	}

	.snapshots-empty {
		margin: 0;
	}

	.snapshots-error {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
	}

	.snapshots-error p {
		margin: 0;
		color: var(--warning, var(--color-warning));
	}

	.snapshot-list {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.snapshot-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.5rem 0;
	}

	.snapshot-name {
		font-size: 0.875rem;
	}
</style>
