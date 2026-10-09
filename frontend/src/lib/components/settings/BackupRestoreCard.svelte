<script lang="ts">
	import { m } from '$lib/i18n';
	import { Database } from 'lucide-svelte';
	import { Button, ConfirmModal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { downloadBlob } from '$lib/utils/download';
	import { formatBytes, formatDate } from '$lib/utils/format';
	import { updateSnapshots } from '$lib/stores/updateSnapshots';
	import type { PollingState } from '$lib/stores/polling';
	import type { UpdateSnapshot, UpdateSnapshotsData } from '$lib/types';
	import { waitForBackendRestart } from '$lib/restartRecovery';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';

	let exporting = $state(false);
	let restoring = $state(false);
	let restoreConfirmOpen = $state(false);
	let pendingFile = $state<File | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);

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
	let pendingSnapshot = $state<UpdateSnapshot | null>(null);
	let deleteSnapshotTarget = $state<UpdateSnapshot | null>(null);


	async function readBackendInstanceId(): Promise<string | null> {
		const res = await fetch('/api/health', {
			method: 'GET',
			cache: 'no-store',
			credentials: 'same-origin'
		});
		if (!res.ok) return null;
		const body = await res.json().catch(() => null);
		const id = body?.data?.instanceId;
		return typeof id === 'string' && id.length > 0 ? id : null;
	}

	function sleep(ms: number) {
		return new Promise<void>((resolve) => setTimeout(resolve, ms));
	}

	async function createBackup() {
		exporting = true;
		try {
			const blob = await api.exportFullBackup();
			const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-');
			downloadBlob(blob, `awg-manager-backup-${stamp}.tar.gz`);
			notifications.success(m.settings_backup_created());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_backup_create_failed());
		} finally {
			exporting = false;
		}
	}

	function openRestorePicker() {
		fileInput?.click();
	}

	function onFileSelected(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0] ?? null;
		input.value = '';
		if (!file) return;
		pendingFile = file;
		restoreConfirmOpen = true;
	}

	// Общий путь восстановления из файла и из снимка: запрос, затем ожидание
	// перезапуска демона и перезагрузка страницы.
	async function runRestore(action: () => Promise<unknown>) {
		restoring = true;
		const before = await readBackendInstanceId().catch(() => null);
		try {
			await action();
			notifications.success(m.settings_backup_restored());
			const waitResult = await waitForBackendRestart({
				previousInstanceId: before,
				readInstanceId: readBackendInstanceId,
				sleep,
				now: () => Date.now(),
				timeoutMs: 120_000
			});
			if (waitResult === 'timeout') {
				notifications.warning(m.settings_backup_restart_timeout());
				restoring = false;
				return;
			}
			location.reload();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_backup_restore_failed());
			restoring = false;
		}
	}

	async function confirmRestore() {
		const file = pendingFile;
		const snap = pendingSnapshot;
		restoreConfirmOpen = false;
		pendingFile = null;
		pendingSnapshot = null;
		if (file) {
			await runRestore(() => api.importFullBackup(file));
		} else if (snap) {
			await runRestore(() => api.restoreUpdateSnapshot(snap.id));
		}
	}

	function askRestoreSnapshot(snap: UpdateSnapshot) {
		pendingSnapshot = snap;
		restoreConfirmOpen = true;
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

<div class="card backup-card">
	<SettingsSectionLabel label={m.settings_backup_title()} icon={Database} tone="blue" header />

	<p class="backup-lead">
		{m.settings_backup_lead()}
	</p>

	<!-- Архив несёт секреты ОТКРЫТЫМ текстом, и пользователь обязан знать это
	     ДО выгрузки: его пересылают в поддержку и кладут в облако. Шифруется
	     только ключ подписки Amnezia, и это создаёт ложное впечатление, будто
	     защищён весь архив. Зашифровать остальное тем же секретом устройства
	     нельзя: секрет в архив не кладётся намеренно, и такой бэкап,
	     восстановленный на ДРУГОМ роутере, потерял бы приватные ключи
	     туннелей — то есть перестал бы быть бэкапом. -->
	<p class="backup-secrets">
		<strong>{m.settings_backup_secrets_title()}</strong> {m.settings_backup_secrets_text()}
	</p>

	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_backup_create_label()}</span>
			<span class="setting-description">{m.settings_backup_create_description()}</span>
		</div>
		<Button variant="secondary" size="sm" loading={exporting} onclick={createBackup}>
			{exporting ? m.settings_backup_creating() : m.settings_backup_button()}
		</Button>
	</div>

	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_backup_restore_label()}</span>
			<span class="setting-description">
				{m.settings_backup_restore_description()}
			</span>
		</div>
		<Button variant="danger" size="sm" loading={restoring} onclick={openRestorePicker}>
			{restoring ? m.settings_backup_restoring() : m.settings_backup_restore()}
		</Button>
	</div>

	<div class="snapshots">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_backup_snapshots_label()}</span>
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
						<div class="snapshot-actions">
							<Button
								variant="ghost"
								size="sm"
								disabled={restoring || snapshotBusy !== null}
								onclick={() => downloadSnapshot(snap)}
							>
								{m.common_download()}
							</Button>
							<Button
								variant="outline-danger"
								size="sm"
								disabled={restoring || snapshotBusy !== null}
								onclick={() => askRestoreSnapshot(snap)}
							>
								{m.settings_backup_restore()}
							</Button>
							<Button
								variant="ghost"
								size="sm"
								disabled={restoring || snapshotBusy !== null}
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

	<input
		bind:this={fileInput}
		type="file"
		accept=".tar.gz,.tgz,.gz,application/gzip"
		class="sr-only"
		onchange={onFileSelected}
	/>
</div>

<ConfirmModal
	open={restoreConfirmOpen}
	title={m.settings_backup_confirm_title()}
	message={pendingFile
		? m.settings_backup_confirm_message({ name: pendingFile.name })
		: pendingSnapshot
			? m.settings_backup_snapshot_confirm_message({ name: snapshotLabel(pendingSnapshot) })
			: ''}
	confirmLabel={m.settings_backup_restore()}
	variant="danger"
	busy={restoring}
	onClose={() => {
		restoreConfirmOpen = false;
		pendingFile = null;
		pendingSnapshot = null;
	}}
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
	.backup-card {
		position: relative;
	}

	.backup-secrets {
		margin: 0.875rem 0 0;
		padding: 8px 12px;
		font-size: 0.8125rem;
		line-height: 1.45;
		color: var(--warning, var(--color-warning));
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		border-radius: 8px;
	}

	.backup-lead {
		margin: 0;
		padding-bottom: 0.875rem;
		border-bottom: 1px solid var(--border);
		font-size: 0.8125rem;
		color: var(--text-muted);
		line-height: 1.45;
	}

	.snapshots {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
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

	.snapshot-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.sr-only {
		position: absolute;
		width: 1px;
		height: 1px;
		padding: 0;
		margin: -1px;
		overflow: hidden;
		clip: rect(0, 0, 0, 0);
		white-space: nowrap;
		border: 0;
	}
</style>
