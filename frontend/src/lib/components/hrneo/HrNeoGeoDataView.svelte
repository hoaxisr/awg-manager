<script lang="ts">
	import { api } from '$lib/api/client';
	import type { GeoFileEntry } from '$lib/types';
	import { Modal } from '$lib/components/ui';
	import { geoDownloadProgress } from '$lib/stores/geoDownload';

	interface Props {
		files: GeoFileEntry[];
		onrefresh: () => void;
	}

	let { files, onrefresh }: Props = $props();

	let addUrl = $state('');
	let addType = $state<'geoip' | 'geosite'>('geosite');
	let busy = $state<string | null>(null);
	let err = $state('');

	// Progress for the currently in-flight add (keyed by URL). Populated by SSE.
	let progress = $derived($geoDownloadProgress[addUrl.trim()] ?? null);
	let progressByPath = $derived($geoDownloadProgress);

	function progressFor(url: string) {
		// Progress events are keyed by the source URL; we look up by the
		// entry's stored URL (not the on-disk filename, which may have a
		// '_N' suffix from resolveConflict).
		return progressByPath[url] ?? null;
	}

	function fmtPercent(p: { downloaded: number; total: number }): string {
		if (p.total <= 0) return '';
		return `${Math.min(100, Math.round((p.downloaded / p.total) * 100))}%`;
	}


	async function add() {
		if (!addUrl.trim()) return;
		busy = 'add';
		err = '';
		try {
			await api.addGeoFile(addType, addUrl.trim());
			addUrl = '';
			onrefresh();
		} catch (e: unknown) {
			err = e instanceof Error ? e.message : String(e);
		} finally {
			busy = null;
		}
	}

	async function update(path: string) {
		busy = path;
		err = '';
		try {
			await api.updateGeoFile(path);
			onrefresh();
		} catch (e: unknown) {
			err = e instanceof Error ? e.message : String(e);
		} finally {
			busy = null;
		}
	}

	let pendingDelete = $state<GeoFileEntry | null>(null);

	function requestRemove(f: GeoFileEntry) {
		pendingDelete = f;
	}

	async function confirmRemove() {
		if (!pendingDelete) return;
		const f = pendingDelete;
		busy = f.path;
		err = '';
		try {
			await api.deleteGeoFile(f.path);
			pendingDelete = null;
			onrefresh();
		} catch (e: unknown) {
			err = e instanceof Error ? e.message : String(e);
		} finally {
			busy = null;
		}
	}

	function humanSize(n: number): string {
		if (n < 1024) return `${n} B`;
		if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
		return `${(n / 1024 / 1024).toFixed(1)} MB`;
	}

	function fileName(p: string): string {
		return p.split('/').pop() ?? p;
	}
</script>

<div class="geo-pane">
	<header class="pane-header">
		<h2>Гео-данные</h2>
		<span class="pane-meta">{files.length} файла</span>
	</header>

	{#if err}<div class="error-banner">{err}</div>{/if}

	{#if files.length === 0}
		<div class="empty">Файлы не загружены. Добавьте URL ниже.</div>
	{:else}
		<div class="files">
			{#each files as f (f.path)}
				{@const fp = progressFor(f.url)}
				<div class="file-row">
					<div class="file-info">
						<span class="file-type type-{f.type}">{f.type}</span>
						<span class="file-name">{fileName(f.path)}</span>
						<span class="file-meta">{humanSize(f.size)} · {f.tagCount} тегов</span>
						{#if busy === f.path && fp}
							<span class="row-progress">
								{#if fp.phase === 'download'}
									{fmtPercent(fp)} {humanSize(fp.downloaded)}
								{:else if fp.phase === 'validate'}
									валидация…
								{/if}
							</span>
						{/if}
					</div>
					<div class="file-actions">
						<button
							class="btn btn-ghost btn-sm"
							disabled={busy === f.path}
							onclick={() => update(f.path)}
						>
							{busy === f.path ? 'Обновление…' : 'Обновить'}
						</button>
						<button
							class="btn btn-ghost btn-sm row-danger"
							disabled={busy === f.path}
							onclick={() => requestRemove(f)}
						>
							Удалить
						</button>
					</div>
				</div>
			{/each}
		</div>
	{/if}

	<div class="add-form">
		<div class="form-label">Добавить .dat файл</div>
		<div class="add-row">
			<select class="form-select" bind:value={addType} disabled={busy === 'add'}>
				<option value="geosite">geosite</option>
				<option value="geoip">geoip</option>
			</select>
			<input
				class="form-input"
				type="url"
				placeholder="https://.../{addType}.dat"
				bind:value={addUrl}
				disabled={busy === 'add'}
			/>
			<button
				class="btn btn-primary btn-sm"
				onclick={add}
				disabled={busy === 'add' || !addUrl.trim()}
			>
				{#if busy === 'add'}
					<span class="spinner" aria-hidden="true"></span>
					Загрузка…
				{:else}
					+ Добавить
				{/if}
			</button>
		</div>
		{#if busy === 'add'}
			<div class="busy-hint">
				{#if progress?.phase === 'download'}
					Скачивание {fmtPercent(progress)} —
					{humanSize(progress.downloaded)}{progress.total > 0
						? ` из ${humanSize(progress.total)}`
						: ''}
				{:else if progress?.phase === 'validate'}
					Валидация файла…
				{:else}
					Подключение к серверу…
				{/if}
				<div class="progress-bar">
					{#if progress && progress.total > 0}
						<div
							class="progress-fill"
							style="width: {Math.min(100, (progress.downloaded / progress.total) * 100)}%"
						></div>
					{:else}
						<div class="progress-fill indeterminate"></div>
					{/if}
				</div>
			</div>
		{/if}
		<div class="form-hint">
			Тип <code>{addType}</code> должен соответствовать содержимому. Файл с 0 записей будет отклонён —
			убедитесь что выбран правильный тип для этого URL. Лимит размера: 200 МБ.
		</div>
	</div>
</div>

{#if pendingDelete}
	{@const pd = pendingDelete}
	<Modal open={true} title="Удалить гео-файл" size="sm" onclose={() => (pendingDelete = null)}>
		<p class="confirm-text">
			Удалить <strong>{fileName(pd.path)}</strong>?
		</p>
		<p class="confirm-hint">
			Файл удалится с диска и пропадёт из
			<code>{pd.type === 'geosite' ? 'GeoSiteFile' : 'GeoIPFile'}=</code> в hrneo.conf.
			Правила, использующие теги из этого файла, перестанут резолвиться.
		</p>
		{#snippet actions()}
			<button
				class="btn btn-secondary"
				onclick={() => (pendingDelete = null)}
				disabled={busy === pd.path}>Отмена</button
			>
			<button class="btn btn-danger" onclick={confirmRemove} disabled={busy === pd.path}>
				{busy === pd.path ? 'Удаление…' : 'Удалить'}
			</button>
		{/snippet}
	</Modal>
{/if}

<style>
	.geo-pane {
		display: flex;
		flex-direction: column;
		gap: 14px;
	}

	.pane-header {
		display: flex;
		align-items: baseline;
		gap: 10px;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--border);
	}
	.pane-header h2 {
		margin: 0;
		font-size: 1.0625rem;
		color: var(--text-primary);
	}
	.pane-meta {
		color: var(--text-muted);
		font-size: 0.8125rem;
	}

	.error-banner {
		background: rgba(247, 118, 142, 0.1);
		border-left: 3px solid var(--error);
		color: var(--error);
		padding: 8px 12px;
		border-radius: 4px;
		font-size: 0.8125rem;
	}

	.empty {
		padding: 24px;
		text-align: center;
		color: var(--text-muted);
		font-style: italic;
		background: var(--bg-secondary);
		border: 1px dashed var(--border);
		border-radius: 8px;
	}

	.files {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.file-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 12px;
		padding: 10px 12px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.file-info {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 0;
		flex: 1;
	}

	.file-type {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		font-weight: 600;
		padding: 2px 8px;
		border-radius: 10px;
	}
	.type-geosite {
		background: rgba(122, 162, 247, 0.15);
		color: var(--accent);
	}
	.type-geoip {
		background: rgba(125, 207, 255, 0.15);
		color: var(--info);
	}

	.file-name {
		font-family: ui-monospace, monospace;
		color: var(--text-primary);
		font-size: 0.875rem;
	}

	.file-meta {
		color: var(--text-muted);
		font-size: 0.75rem;
	}

	.file-actions {
		display: flex;
		gap: 4px;
		flex-shrink: 0;
	}

	.row-danger:hover:not(:disabled) {
		color: var(--error);
		border-color: var(--error);
	}

	.add-form {
		padding: 12px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.form-label {
		display: block;
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-primary);
		margin-bottom: 6px;
	}

	.add-row {
		display: grid;
		grid-template-columns: auto 1fr auto;
		gap: 6px;
	}

	.busy-hint {
		margin-top: 8px;
		padding: 8px 10px;
		background: rgba(122, 162, 247, 0.1);
		border-left: 3px solid var(--accent);
		color: var(--text-primary);
		font-size: 0.8125rem;
		border-radius: 4px;
	}

	.form-hint {
		margin-top: 8px;
		color: var(--text-muted);
		font-size: 0.75rem;
	}
	.form-hint code {
		background: var(--bg-tertiary);
		padding: 0 4px;
		border-radius: 3px;
		font-family: ui-monospace, monospace;
	}

	.progress-bar {
		margin-top: 6px;
		height: 6px;
		background: var(--bg-tertiary);
		border-radius: 3px;
		overflow: hidden;
	}
	.progress-fill {
		height: 100%;
		background: var(--accent);
		border-radius: 3px;
		transition: width 0.2s ease-out;
	}
	.progress-fill.indeterminate {
		width: 30%;
		animation: indeterminate 1.4s linear infinite;
	}
	@keyframes indeterminate {
		0% {
			margin-left: -30%;
		}
		100% {
			margin-left: 100%;
		}
	}

	.row-progress {
		color: var(--accent);
		font-size: 0.75rem;
		font-family: ui-monospace, monospace;
	}

	.confirm-text {
		margin: 0 0 8px;
		color: var(--text-primary);
	}
	.confirm-hint {
		margin: 0;
		color: var(--text-muted);
		font-size: 0.8125rem;
	}
	.confirm-hint code {
		background: var(--bg-tertiary);
		padding: 0 4px;
		border-radius: 3px;
		font-family: ui-monospace, monospace;
		font-size: 0.75rem;
	}

	.spinner {
		display: inline-block;
		width: 12px;
		height: 12px;
		border: 2px solid currentColor;
		border-right-color: transparent;
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
		margin-right: 6px;
		vertical-align: -2px;
	}
	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
