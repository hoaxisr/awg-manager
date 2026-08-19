<script lang="ts">
	import type { SystemFileEntry, FileSystemScriptStatus } from '$lib/api/client';
	import { getFileTypeInfo } from './fileIcons';
	import type { CtxMenu, ScriptAction } from './types';
	import {
		ExternalLink,
		Eye,
		Edit2,
		Download,
		RotateCw,
		Square,
		Play,
		Copy,
		Move,
		Trash2,
		FolderPlus,
		FilePlus,
		Upload,
		Check,
		RefreshCw,
	} from 'lucide-svelte';

	interface Props {
		menu: CtxMenu;
		readOnly: boolean;
		scriptStatuses: Record<string, FileSystemScriptStatus>;
		el: HTMLDivElement | undefined;
		onClose: () => void;
		onOpen: (entry: SystemFileEntry) => void;
		onProps: (entry: SystemFileEntry) => void;
		onEdit: (entry: SystemFileEntry) => void;
		onDownload: (entry: SystemFileEntry) => void;
		onScriptAction: (entry: SystemFileEntry, action: ScriptAction) => void;
		onCopyPath: (path: string) => void;
		onRename: (entry: SystemFileEntry) => void;
		onCopyTo: (path: string) => void;
		onMoveTo: (path: string) => void;
		onDelete: (entry: SystemFileEntry) => void;
		onMkdir: () => void;
		onNewFile: () => void;
		onUploadClick: () => void;
		onSelectAll: () => void;
		onRefresh: () => void;
	}

	let {
		menu,
		readOnly,
		scriptStatuses,
		el = $bindable(undefined),
		onClose,
		onOpen,
		onProps,
		onEdit,
		onDownload,
		onScriptAction,
		onCopyPath,
		onRename,
		onCopyTo,
		onMoveTo,
		onDelete,
		onMkdir,
		onNewFile,
		onUploadClick,
		onSelectAll,
		onRefresh,
	}: Props = $props();
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
	bind:this={el}
	class="file-ctx-menu"
	style="left:{menu.x}px;top:{menu.y}px"
	role="menu"
	oncontextmenu={(e) => e.preventDefault()}
>
	{#if menu.entry}
		{@const entry = menu.entry}
		<button type="button" role="menuitem" onclick={() => { onOpen(entry); onClose(); }}>
			<ExternalLink size={13} /> Открыть <kbd>Enter</kbd>
		</button>
		<button type="button" role="menuitem" onclick={() => { onProps(entry); onClose(); }}>
			<Eye size={13} /> Свойства… <kbd>F3</kbd>
		</button>
		{#if !entry.isDir}
			<button type="button" role="menuitem" onclick={() => { onEdit(entry); onClose(); }}>
				<Edit2 size={13} /> Редактировать <kbd>F4</kbd>
			</button>
			<button type="button" role="menuitem" onclick={() => { onDownload(entry); onClose(); }}>
				<Download size={13} /> Скачать
			</button>
			{@const info = getFileTypeInfo(entry.name, entry.isDir)}
			{@const isRunning = scriptStatuses[entry.path]?.running}
			{#if info.kind === 'script' || entry.mode.includes('x')}
				{#if isRunning}
					<button type="button" role="menuitem" onclick={() => { onScriptAction(entry, 'restart'); onClose(); }}>
						<RotateCw size={13} /> Перезапустить скрипт
					</button>
					<button type="button" role="menuitem" class="danger" onclick={() => { onScriptAction(entry, 'stop'); onClose(); }}>
						<Square size={13} /> Остановить скрипт
					</button>
				{:else}
					<button type="button" role="menuitem" onclick={() => { onScriptAction(entry, 'start'); onClose(); }}>
						<Play size={13} /> Запустить скрипт
					</button>
				{/if}
			{/if}
		{/if}
		<hr />
		<button type="button" role="menuitem" onclick={() => { onCopyPath(entry.path); onClose(); }}>
			<Copy size={13} /> Копировать путь
		</button>
		{#if !readOnly && entry.name !== '..'}
			<button type="button" role="menuitem" onclick={() => { onRename(entry); onClose(); }}>
				<Edit2 size={13} /> Переименовать… <kbd>F2</kbd>
			</button>
			<button type="button" role="menuitem" onclick={() => { onCopyTo(entry.path); onClose(); }}>
				<Copy size={13} /> Копировать в…
			</button>
			<button type="button" role="menuitem" onclick={() => { onMoveTo(entry.path); onClose(); }}>
				<Move size={13} /> Переместить в…
			</button>
			<button type="button" role="menuitem" class="danger" onclick={() => { onDelete(entry); onClose(); }}>
				<Trash2 size={13} /> Удалить <kbd>F8</kbd>
			</button>
		{/if}
	{:else}
		{#if !readOnly}
			<button type="button" role="menuitem" onclick={() => { onMkdir(); onClose(); }}>
				<FolderPlus size={13} /> Создать папку <kbd>F7</kbd>
			</button>
			<button type="button" role="menuitem" onclick={() => { onNewFile(); onClose(); }}>
				<FilePlus size={13} /> Создать файл
			</button>
			<button type="button" role="menuitem" onclick={() => { onUploadClick(); onClose(); }}>
				<Upload size={13} /> Загрузить файл…
			</button>
		{/if}
		<button type="button" role="menuitem" onclick={() => { onSelectAll(); onClose(); }}>
			<Check size={13} /> Выделить всё <kbd>Ctrl+A</kbd>
		</button>
		<button type="button" role="menuitem" onclick={() => { onRefresh(); onClose(); }}>
			<RefreshCw size={13} /> Обновить <kbd>F5</kbd>
		</button>
	{/if}
</div>

<style>
	.file-ctx-menu {
		position: fixed;
		z-index: 1100;
		background: var(--color-bg-secondary, #1f2335);
		border: 1px solid var(--color-border, #3b4261);
		border-radius: 6px;
		padding: 0.3rem 0;
		box-shadow: 0 4px 16px rgba(0,0,0,0.4);
		min-width: 200px;
		display: flex;
		flex-direction: column;
	}
	.file-ctx-menu button {
		background: none;
		border: none;
		padding: 0.35rem 0.75rem;
		display: flex;
		align-items: center;
		gap: 0.5rem;
		color: var(--color-text-primary);
		font-size: 0.82rem;
		cursor: pointer;
		text-align: left;
		width: 100%;
	}
	.file-ctx-menu button:hover {
		background: var(--color-bg-hover, #292e42);
		color: var(--color-accent, #7aa2f7);
	}
	.file-ctx-menu button.danger {
		color: var(--color-error, #f7768e);
	}
	.file-ctx-menu hr {
		border: none;
		border-top: 1px solid var(--color-border);
		margin: 0.25rem 0;
	}
	.file-ctx-menu kbd {
		margin-left: auto;
		font-size: 0.7rem;
		opacity: 0.6;
		background: var(--color-bg-tertiary);
		padding: 0.1rem 0.3rem;
		border-radius: 3px;
	}
</style>
