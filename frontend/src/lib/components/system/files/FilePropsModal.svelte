<script lang="ts">
	import { api, type SystemFileEntry, type FileSystemScriptStatus } from '$lib/api/client';
	import { Modal, Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { formatBytes } from '$lib/utils/format';
	import { getFileTypeInfo } from './fileIcons';
	import {
		Copy,
		Download,
		FileText,
		Folder,
		Shield,
		Hash,
		Play,
		RotateCw,
		Square,
		Terminal,
	} from 'lucide-svelte';

	interface Props {
		open: boolean;
		entry: SystemFileEntry | null;
		readOnly?: boolean;
		onClose: () => void;
		onEdit?: (entry: SystemFileEntry) => void;
		onUpdated?: () => void;
	}

	let { open = false, entry, readOnly = false, onClose, onEdit, onUpdated }: Props = $props();

	let md5Hash = $state<string | null>(null);
	let sha256Hash = $state<string | null>(null);
	let calculatingMd5 = $state(false);
	let calculatingSha = $state(false);

	// Permissions state
	let uR = $state(true);
	let uW = $state(true);
	let uX = $state(false);
	let gR = $state(true);
	let gW = $state(false);
	let gX = $state(false);
	let oR = $state(true);
	let oW = $state(false);
	let oX = $state(false);
	let savingChmod = $state(false);

	// Script status
	let scriptStatus = $state<FileSystemScriptStatus | null>(null);
	let runningScript = $state(false);
	let scriptOutput = $state<string | null>(null);

	const typeInfo = $derived(entry ? getFileTypeInfo(entry.name, entry.isDir) : { kind: 'generic', color: '#94a3b8' });

	$effect(() => {
		if (entry && open) {
			md5Hash = null;
			sha256Hash = null;
			scriptOutput = null;
			parseMode(entry.mode);
			if (!entry.isDir) {
				void checkScript(entry.path);
			} else {
				scriptStatus = null;
			}
		}
	});

	async function checkScript(p: string) {
		try {
			scriptStatus = await api.systemFilesScriptStatus(p);
		} catch {
			scriptStatus = null;
		}
	}

	async function runAction(action: 'start' | 'stop' | 'restart' | 'run') {
		if (!entry) return;
		runningScript = true;
		try {
			const res = await api.systemFilesScriptAction({ path: entry.path, action });
			scriptOutput = res.output || (res.ok ? 'Успешно выполнено' : 'Ошибка');
			if (res.ok) {
				notifications.success(`Скрипт: ${action} выполнен`);
			} else {
				notifications.error(res.error || 'Ошибка запуска');
			}
			await checkScript(entry.path);
			onUpdated?.();
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка выполнения скрипта'));
		} finally {
			runningScript = false;
		}
	}

	function parseMode(m: string) {
		if (!m) return;
		if (m.length >= 9) {
			const str = m.slice(-9);
			uR = str[0] === 'r';
			uW = str[1] === 'w';
			uX = str[2] === 'x';
			gR = str[3] === 'r';
			gW = str[4] === 'w';
			gX = str[5] === 'x';
			oR = str[6] === 'r';
			oW = str[7] === 'w';
			oX = str[8] === 'x';
		}
	}

	const octalMode = $derived.by(() => {
		const u = (uR ? 4 : 0) + (uW ? 2 : 0) + (uX ? 1 : 0);
		const g = (gR ? 4 : 0) + (gW ? 2 : 0) + (gX ? 1 : 0);
		const o = (oR ? 4 : 0) + (oW ? 2 : 0) + (oX ? 1 : 0);
		return `0${u}${g}${o}`;
	});

	async function applyChmod() {
		if (!entry || readOnly) return;
		savingChmod = true;
		try {
			const rawOctal = octalMode.replace(/^0/, '');
			await api.systemFilesChmod(entry.path, rawOctal);
			notifications.success(`Права изменены на ${rawOctal}`);
			onUpdated?.();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось изменить права'));
		} finally {
			savingChmod = false;
		}
	}

	async function calcMd5() {
		if (!entry || entry.isDir) return;
		calculatingMd5 = true;
		try {
			const res = await api.systemFilesChecksum(entry.path, 'md5');
			md5Hash = res.checksum;
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка вычисления MD5'));
		} finally {
			calculatingMd5 = false;
		}
	}

	async function calcSha256() {
		if (!entry || entry.isDir) return;
		calculatingSha = true;
		try {
			const res = await api.systemFilesChecksum(entry.path, 'sha256');
			sha256Hash = res.checksum;
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка вычисления SHA256'));
		} finally {
			calculatingSha = false;
		}
	}

	async function copy(val: string) {
		const ok = await copyToClipboard(val);
		if (ok) notifications.success('Скопировано в буфер');
	}
</script>

<Modal
	{open}
	title="Свойства объекта"
	size="md"
	onclose={onClose}
>
	{#if entry}
		<div class="props-content">
			<!-- Header badge -->
			<div class="header-card">
				<div class="icon-wrap" style="color: {typeInfo.color};">
					{#if entry.isDir}
						<Folder size={32} />
					{:else}
						<FileText size={32} />
					{/if}
				</div>
				<div class="name-block">
					<div class="file-name">{entry.name}</div>
					<div class="path-row">
						<code>{entry.path}</code>
						<button type="button" class="btn-copy" onclick={() => copy(entry?.path ?? '')} title="Копировать путь">
							<Copy size={13} />
						</button>
					</div>
				</div>
			</div>

			<!-- Script / Service Runtime Control Box -->
			{#if scriptStatus?.isScript}
				<div class="section-box script-box">
					<div class="section-title">
						<Terminal size={15} />
						<span>Управление скриптом / процессом</span>
					</div>

					<div class="script-status-row">
						<div class="pill-badge" class:running={scriptStatus.running}>
							<span class="dot"></span>
							<strong>{scriptStatus.running ? 'Запущен в системе' : 'Остановлен'}</strong>
							{#if scriptStatus.pids?.length}
								<span>(PID: {scriptStatus.pids.join(', ')})</span>
							{/if}
						</div>

						<div class="script-btns">
							{#if !scriptStatus.running}
								<Button size="sm" variant="primary" loading={runningScript} onclick={() => runAction(scriptStatus?.isService ? 'start' : 'run')}>
									{#snippet iconBefore()}<Play size={13} />{/snippet}
									Запустить
								</Button>
							{:else}
								<Button size="sm" variant="secondary" loading={runningScript} onclick={() => runAction('restart')}>
									{#snippet iconBefore()}<RotateCw size={13} />{/snippet}
									Перезапуск
								</Button>
								<Button size="sm" variant="danger" loading={runningScript} onclick={() => runAction('stop')}>
									{#snippet iconBefore()}<Square size={13} />{/snippet}
									Остановить
								</Button>
							{/if}
						</div>
					</div>

					{#if scriptOutput}
						<pre class="script-out">{scriptOutput}</pre>
					{/if}
				</div>
			{/if}

			<!-- Basic info table -->
			<div class="info-grid">
				<div class="info-row">
					<span class="info-label">Тип:</span>
					<span class="info-val">{entry.isDir ? 'Каталог (Папка)' : 'Файл'}</span>
				</div>
				<div class="info-row">
					<span class="info-label">Размер:</span>
					<span class="info-val">{entry.isDir ? '—' : formatBytes(entry.size)}</span>
				</div>
				<div class="info-row">
					<span class="info-label">Изменён:</span>
					<span class="info-val">{new Date(entry.modTime).toLocaleString()}</span>
				</div>
			</div>

			<!-- Permissions (chmod) calculator -->
			<div class="section-box">
				<div class="section-title">
					<Shield size={15} />
					<span>Права доступа (chmod {octalMode})</span>
				</div>

				<div class="chmod-table">
					<div class="chmod-header">
						<span></span>
						<span>Чтение (r)</span>
						<span>Запись (w)</span>
						<span>Запуск (x)</span>
					</div>
					<div class="chmod-row">
						<span class="role">Владелец:</span>
						<label><input type="checkbox" bind:checked={uR} disabled={readOnly} /></label>
						<label><input type="checkbox" bind:checked={uW} disabled={readOnly} /></label>
						<label><input type="checkbox" bind:checked={uX} disabled={readOnly} /></label>
					</div>
					<div class="chmod-row">
						<span class="role">Группа:</span>
						<label><input type="checkbox" bind:checked={gR} disabled={readOnly} /></label>
						<label><input type="checkbox" bind:checked={gW} disabled={readOnly} /></label>
						<label><input type="checkbox" bind:checked={gX} disabled={readOnly} /></label>
					</div>
					<div class="chmod-row">
						<span class="role">Остальные:</span>
						<label><input type="checkbox" bind:checked={oR} disabled={readOnly} /></label>
						<label><input type="checkbox" bind:checked={oW} disabled={readOnly} /></label>
						<label><input type="checkbox" bind:checked={oX} disabled={readOnly} /></label>
					</div>
				</div>

				{#if !readOnly}
					<div class="chmod-actions">
						<Button size="sm" variant="secondary" loading={savingChmod} onclick={applyChmod}>
							Применить права ({octalMode})
						</Button>
					</div>
				{/if}
			</div>

			<!-- Checksum section (for files only) -->
			{#if !entry.isDir}
				<div class="section-box">
					<div class="section-title">
						<Hash size={15} />
						<span>Контрольные суммы (Хэш)</span>
					</div>

					<div class="hash-list">
						<div class="hash-item">
							<span class="hash-name">MD5:</span>
							{#if md5Hash}
								<code class="hash-val">{md5Hash}</code>
								<button type="button" class="btn-copy" onclick={() => copy(md5Hash ?? '')}><Copy size={12} /></button>
							{:else}
								<Button size="sm" variant="ghost" loading={calculatingMd5} onclick={calcMd5}>
									Рассчитать MD5
								</Button>
							{/if}
						</div>

						<div class="hash-item">
							<span class="hash-name">SHA256:</span>
							{#if sha256Hash}
								<code class="hash-val">{sha256Hash}</code>
								<button type="button" class="btn-copy" onclick={() => copy(sha256Hash ?? '')}><Copy size={12} /></button>
							{:else}
								<Button size="sm" variant="ghost" loading={calculatingSha} onclick={calcSha256}>
									Рассчитать SHA256
								</Button>
							{/if}
						</div>
					</div>
				</div>
			{/if}
		</div>
	{/if}

	{#snippet actions()}
		<div class="footer-btns">
			<div class="left-actions">
				{#if entry && !entry.isDir}
					<Button
						variant="secondary"
						onclick={() => {
							if (entry) window.open(api.systemFilesDownloadUrl(entry.path), '_blank');
						}}
					>
						{#snippet iconBefore()}<Download size={14} />{/snippet}
						Скачать
					</Button>
					{#if onEdit && entry}
						<Button
							variant="secondary"
							onclick={() => {
								if (entry) {
									onClose();
									onEdit(entry);
								}
							}}
						>
							{#snippet iconBefore()}<FileText size={14} />{/snippet}
							Редактировать
						</Button>
					{/if}
				{/if}
			</div>
			<Button variant="ghost" onclick={onClose}>Закрыть</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.props-content {
		display: flex;
		flex-direction: column;
		gap: 0.85rem;
	}

	.header-card {
		display: flex;
		gap: 0.85rem;
		align-items: center;
		padding: 0.75rem;
		background: var(--color-bg-tertiary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
	}
	.icon-wrap {
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
	}
	.name-block {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		min-width: 0;
		flex: 1;
	}
	.file-name {
		font-weight: 700;
		font-size: 1rem;
		word-break: break-all;
		color: var(--color-text-primary);
	}
	.path-row {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.78rem;
		color: var(--color-text-muted);
	}
	.path-row code {
		word-break: break-all;
	}

	.info-grid {
		display: grid;
		grid-template-columns: 1fr;
		gap: 0.35rem;
		padding: 0.5rem 0.75rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		font-size: 0.85rem;
	}
	.info-row {
		display: flex;
		justify-content: space-between;
		padding: 0.2rem 0;
	}
	.info-label {
		color: var(--color-text-muted);
	}
	.info-val {
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.section-box {
		padding: 0.75rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}
	.section-title {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	/* Script runtime status */
	.script-status-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
	.pill-badge {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.2rem 0.5rem;
		border-radius: 999px;
		font-size: 0.78rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}
	.pill-badge .dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: #94a3b8;
	}
	.pill-badge.running {
		background: var(--color-success-tint, rgba(16, 185, 129, 0.15));
		border-color: rgba(16, 185, 129, 0.35);
		color: var(--color-success, #34d399);
	}
	.pill-badge.running .dot {
		background: var(--color-success, #22c55e);
		box-shadow: 0 0 6px var(--color-success, #22c55e);
	}
	.script-btns {
		display: flex;
		gap: 0.3rem;
	}
	.script-out {
		margin: 0.2rem 0 0 0;
		padding: 0.4rem;
		background: #0f172a;
		border-radius: 4px;
		font-family: var(--font-mono, monospace);
		font-size: 0.74rem;
		color: #38bdf8;
		max-height: 80px;
		overflow-y: auto;
		white-space: pre-wrap;
	}

	.chmod-table {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		font-size: 0.82rem;
	}
	.chmod-header, .chmod-row {
		display: grid;
		grid-template-columns: 90px 1fr 1fr 1fr;
		align-items: center;
		text-align: center;
	}
	.chmod-header {
		font-weight: 600;
		color: var(--color-text-muted);
		font-size: 0.75rem;
	}
	.chmod-row .role {
		text-align: left;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.chmod-actions {
		display: flex;
		justify-content: flex-end;
		margin-top: 0.35rem;
	}

	.hash-list {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		font-size: 0.82rem;
	}
	.hash-item {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-height: 28px;
	}
	.hash-name {
		font-weight: 600;
		width: 65px;
		color: var(--color-text-muted);
	}
	.hash-val {
		font-size: 0.75rem;
		background: var(--color-bg-tertiary);
		padding: 0.2rem 0.4rem;
		border-radius: 4px;
		word-break: break-all;
		flex: 1;
	}

	.btn-copy {
		background: none;
		border: none;
		cursor: pointer;
		padding: 0.2rem;
		color: var(--color-text-muted);
		border-radius: 4px;
		display: inline-flex;
		align-items: center;
	}
	.btn-copy:hover {
		color: var(--color-accent);
	}

	.footer-btns {
		display: flex;
		justify-content: space-between;
		width: 100%;
		align-items: center;
	}
	.left-actions {
		display: flex;
		gap: 0.4rem;
	}
</style>
