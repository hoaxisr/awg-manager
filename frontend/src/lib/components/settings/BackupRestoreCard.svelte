<script lang="ts">
	import { Database } from 'lucide-svelte';
	import { Button, ConfirmModal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { downloadBlob } from '$lib/utils/download';
	import { waitForBackendRestart } from '$lib/restartRecovery';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';

	let exporting = $state(false);
	let restoring = $state(false);
	let restoreConfirmOpen = $state(false);
	let pendingFile = $state<File | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);

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
			notifications.success('Резервная копия создана и скачана');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось создать резервную копию');
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

	async function confirmRestore() {
		if (!pendingFile) return;
		restoreConfirmOpen = false;
		restoring = true;
		const before = await readBackendInstanceId().catch(() => null);
		try {
			await api.importFullBackup(pendingFile);
			notifications.success('Резервная копия восстановлена. AWG Manager перезапускается…');
			const waitResult = await waitForBackendRestart({
				previousInstanceId: before,
				readInstanceId: readBackendInstanceId,
				sleep,
				now: () => Date.now(),
				timeoutMs: 120_000
			});
			if (waitResult === 'timeout') {
				notifications.warning('Не удалось дождаться перезапуска. Обновите страницу вручную.');
				restoring = false;
				return;
			}
			location.reload();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось восстановить');
			restoring = false;
		} finally {
			pendingFile = null;
		}
	}
</script>

<div class="card backup-card">
	<SettingsSectionLabel label="Резервное копирование" icon={Database} tone="blue" header />

	<p class="backup-lead">
		Полная копия каталога данных awg-manager: туннели, WDTT/FreeTurn, sing-box, маршруты и настройки.
		Кэш sing-box, pid-файлы и служебный каталог run/ не включаются.
		Перед созданием или восстановлением кратковременно останавливаются связанные процессы;
		после восстановления выполняется холодный перезапуск с синхронизацией портов linked-туннелей.
	</p>

	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Создать копию</span>
			<span class="setting-description">Архив .tar.gz сразу скачается в браузере</span>
		</div>
		<Button variant="secondary" size="sm" loading={exporting} onclick={createBackup}>
			{exporting ? 'Создание…' : 'Резервное копирование'}
		</Button>
	</div>

	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Восстановление</span>
			<span class="setting-description">
				Заменит текущие данные; перед применением старый каталог сохранится как
				<code>awg-manager.pre-restore-*</code>
			</span>
		</div>
		<Button variant="danger" size="sm" loading={restoring} onclick={openRestorePicker}>
			{restoring ? 'Восстановление…' : 'Восстановить'}
		</Button>
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
	title="Восстановить awg-manager?"
	message={pendingFile
		? `Будет загружен архив «${pendingFile.name}». Текущие данные будут заменены, затем AWG Manager перезапустится. Продолжить?`
		: ''}
	confirmLabel="Восстановить"
	variant="danger"
	busy={restoring}
	onClose={() => {
		restoreConfirmOpen = false;
		pendingFile = null;
	}}
	onConfirm={confirmRestore}
/>

<style>
	.backup-card {
		position: relative;
	}

	.backup-lead {
		margin: 0;
		padding-bottom: 0.875rem;
		border-bottom: 1px solid var(--border);
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.45;
	}

	/* File input is the last child — keep row padding like other cards. */
	.backup-card > .setting-row:last-of-type {
		padding-bottom: 0;
	}

	.setting-description code {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
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
