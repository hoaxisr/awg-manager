<script lang="ts">
	import type { SystemFileEntry, FileSystemScriptStatus } from '$lib/api/client';
	import { Folder, FileText } from 'lucide-svelte';
	import { getFileTypeInfo } from './fileIcons';
	import FileRowActions from './FileRowActions.svelte';
	import type { ScriptAction } from './types';

	interface Props {
		entries: SystemFileEntry[];
		loading: boolean;
		readOnly: boolean;
		selectedPath: string | null;
		checked: Set<string>;
		scriptStatuses: Record<string, FileSystemScriptStatus>;
		runningActionPath: string | null;
		onSelect: (entry: SystemFileEntry) => void;
		onOpen: (entry: SystemFileEntry) => void;
		onToggleCheck: (path: string, ev: Event) => void;
		onContextMenu: (ev: MouseEvent, entry: SystemFileEntry | null) => void;
		onScriptAction: (entry: SystemFileEntry, action: ScriptAction) => void;
		onProps: (entry: SystemFileEntry) => void;
		onEdit: (entry: SystemFileEntry) => void;
		onDelete: (entry: SystemFileEntry) => void;
	}

	let {
		entries,
		loading,
		readOnly,
		selectedPath,
		checked,
		scriptStatuses,
		runningActionPath,
		onSelect,
		onOpen,
		onToggleCheck,
		onContextMenu,
		onScriptAction,
		onProps,
		onEdit,
		onDelete,
	}: Props = $props();

	function formatSize(n: number): string {
		if (n < 1024) return `${n} B`;
		if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
		return `${(n / (1024 * 1024)).toFixed(1)} MB`;
	}

	function formatTime(iso: string): string {
		if (!iso) return '—';
		const d = new Date(iso);
		return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString() + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
	}
</script>

<div class="files-table-container">
	{#if loading}
		<div class="loading-state">Загрузка каталога…</div>
	{:else if entries.length === 0}
		<div class="empty-state">Каталог пуст или файлы не найдены</div>
	{:else}
		<div class="table-wrap">
			<table class="fm-table" oncontextmenu={(e) => onContextMenu(e, null)}>
				<thead>
					<tr>
						<th style="width: 32px;"><span class="sr-only">Выбор</span></th>
						<th>Имя</th>
						<th style="width: 100px;">Размер</th>
						<th style="width: 150px;">Изменён</th>
						<th style="width: 90px;">Права</th>
						<th style="width: 145px; text-align: right;">Действия</th>
					</tr>
				</thead>
				<tbody>
					{#each entries as entry (entry.path)}
						{@const info = getFileTypeInfo(entry.name, entry.isDir)}
						{@const scriptSt = scriptStatuses[entry.path]}
						{@const isScript = !entry.isDir && (info.kind === 'script' || entry.mode.includes('x') || scriptSt?.isScript)}
						{@const isRunning = scriptSt?.running ?? false}
						{@const isBusy = runningActionPath === entry.path}

						<tr
							class:selected={selectedPath === entry.path}
							class:checked={checked.has(entry.path)}
							onclick={() => onSelect(entry)}
							ondblclick={() => onOpen(entry)}
							oncontextmenu={(e) => onContextMenu(e, entry)}
						>
							<td class="col-check">
								{#if entry.name !== '..'}
									<input
										type="checkbox"
										checked={checked.has(entry.path)}
										onclick={(e) => onToggleCheck(entry.path, e)}
									/>
								{/if}
							</td>
							<td class="col-name">
								<div class="name-wrap">
									{#if entry.isDir}
										<Folder size={16} class="icon-folder" />
									{:else}
										<FileText size={16} style="color: {info.color};" />
									{/if}
									<span class="entry-name" class:is-dir={entry.isDir}>{entry.name}</span>
									{#if info.badge}
										<span class="file-badge" class:badge-script={info.kind === 'script'}>{info.badge}</span>
									{/if}
									{#if isScript && isRunning}
										<span class="running-pill" title={scriptSt?.pids?.length ? `PID: ${scriptSt.pids.join(', ')}` : 'Запущен'}>
											<span class="dot"></span>
											<span>Запущен</span>
										</span>
									{/if}
								</div>
							</td>
							<td class="col-size">
								{entry.isDir ? '—' : formatSize(entry.size)}
							</td>
							<td class="col-time">
								{formatTime(entry.modTime)}
							</td>
							<td class="col-mode">
								<code>{entry.mode || '—'}</code>
							</td>
							<td class="col-actions">
								{#if entry.name !== '..'}
									<FileRowActions
										{entry}
										{readOnly}
										isScript={!!isScript}
										{isRunning}
										{isBusy}
										isService={scriptSt?.isService ?? false}
										{onScriptAction}
										{onProps}
										{onEdit}
										{onDelete}
									/>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>

<style>
	.files-table-container {
		min-width: 0;
	}
	.table-wrap {
		max-height: 520px;
		overflow-y: auto;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		background: var(--color-bg-secondary);
	}
	.fm-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.84rem;
	}
	.fm-table th, .fm-table td {
		padding: 0.45rem 0.6rem;
		border-bottom: 1px solid var(--color-border);
		text-align: left;
		vertical-align: middle;
	}
	.fm-table th {
		position: sticky;
		top: 0;
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		z-index: 1;
	}
	.fm-table tr {
		cursor: pointer;
	}
	.fm-table tr:hover {
		background: var(--color-bg-hover, rgba(255,255,255,0.03));
	}
	.fm-table tr.selected {
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.15));
	}

	.name-wrap {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		white-space: nowrap;
	}
	:global(.icon-folder) {
		color: #60a5fa;
		flex-shrink: 0;
	}
	.entry-name {
		font-weight: 500;
		color: var(--color-text-primary);
		white-space: nowrap;
	}
	.entry-name.is-dir {
		font-weight: 600;
	}
	.file-badge {
		font-size: 0.65rem;
		font-weight: 700;
		padding: 0.05rem 0.25rem;
		border-radius: 3px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		white-space: nowrap;
	}
	.file-badge.badge-script {
		background: rgba(16, 185, 129, 0.1);
		color: #047857;
		border: 1px solid rgba(16, 185, 129, 0.25);
	}
	:global(.dark) .file-badge.badge-script {
		background: rgba(16, 185, 129, 0.15);
		color: #34d399;
		border-color: rgba(16, 185, 129, 0.35);
	}
	.running-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		font-size: 0.72rem;
		font-weight: 600;
		color: #047857;
		background: rgba(16, 185, 129, 0.1);
		border: 1px solid rgba(16, 185, 129, 0.3);
		padding: 0.1rem 0.45rem;
		border-radius: 999px;
		white-space: nowrap;
	}
	:global(.dark) .running-pill {
		color: #34d399;
		background: rgba(16, 185, 129, 0.15);
		border-color: rgba(16, 185, 129, 0.35);
	}
	.running-pill .dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: #10b981;
		flex-shrink: 0;
	}

	.loading-state, .empty-state {
		padding: 2rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 0.85rem;
	}
</style>
