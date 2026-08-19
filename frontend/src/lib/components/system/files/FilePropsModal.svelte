<script lang="ts">
	import { api, type SystemFileEntry } from '$lib/api/client';
	import { Modal, Button } from '$lib/components/ui';
	import { formatBytes } from '$lib/utils/format';
	import { getFileTypeInfo } from './fileIcons';
	import FileCopyButton from './FileCopyButton.svelte';
	import FilePropsChecksums from './FilePropsChecksums.svelte';
	import FilePropsPermissions from './FilePropsPermissions.svelte';
	import FilePropsScript from './FilePropsScript.svelte';
	import { Download, FileText, Folder } from 'lucide-svelte';

	interface Props {
		open: boolean;
		entry: SystemFileEntry | null;
		readOnly?: boolean;
		onClose: () => void;
		onEdit?: (entry: SystemFileEntry) => void;
		onUpdated?: () => void;
	}

	let { open = false, entry, readOnly = false, onClose, onEdit, onUpdated }: Props = $props();

	const typeInfo = $derived(entry ? getFileTypeInfo(entry.name, entry.isDir) : { kind: 'generic', color: '#94a3b8' });
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
						<FileCopyButton value={entry.path} size={13} title="Копировать путь" />
					</div>
				</div>
			</div>

			<!-- Script / Service Runtime Control Box -->
			{#if !entry.isDir}
				<FilePropsScript {entry} {onUpdated} />
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
			<FilePropsPermissions {entry} {readOnly} {onUpdated} />

			<!-- Checksum section (for files only) -->
			{#if !entry.isDir}
				<FilePropsChecksums {entry} />
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
