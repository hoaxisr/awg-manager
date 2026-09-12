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

	<!-- Архив несёт секреты ОТКРЫТЫМ текстом, и пользователь обязан знать это
	     ДО выгрузки: его пересылают в поддержку и кладут в облако. Шифруется
	     только ключ подписки Amnezia, и это создаёт ложное впечатление, будто
	     защищён весь архив. Зашифровать остальное тем же секретом устройства
	     нельзя: секрет в архив не кладётся намеренно, и такой бэкап,
	     восстановленный на ДРУГОМ роутере, потерял бы приватные ключи
	     туннелей — то есть перестал бы быть бэкапом. -->
	<p class="backup-secrets">
		<strong>Архив содержит секреты в открытом виде:</strong> ключ API панели, приватные и
		preshared-ключи туннелей и пиров. Кто получил файл — получил доступ к панели и к VPN.
		Храните и пересылайте его как пароль. Отдельно зашифрован только ключ подписки
		Amnezia Premium — он привязан к этому роутеру и на другом не прочитается.
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
