<script lang="ts">
	import type { SystemFileEntry } from '$lib/api/client';
	import { Eye, Edit2, Trash2, Play, RotateCw, Square } from 'lucide-svelte';
	import type { ScriptAction } from './types';

	interface Props {
		entry: SystemFileEntry;
		readOnly: boolean;
		isScript: boolean;
		isRunning: boolean;
		isBusy: boolean;
		isService: boolean;
		onScriptAction: (entry: SystemFileEntry, action: ScriptAction) => void;
		onProps: (entry: SystemFileEntry) => void;
		onEdit: (entry: SystemFileEntry) => void;
		onDelete: (entry: SystemFileEntry) => void;
	}

	let {
		entry,
		readOnly,
		isScript,
		isRunning,
		isBusy,
		isService,
		onScriptAction,
		onProps,
		onEdit,
		onDelete,
	}: Props = $props();
</script>

<div class="row-quick-btns">
	{#if isScript}
		{#if isRunning}
			<button
				type="button"
				class="icon-act-btn restart-act-btn"
				title="Перезапустить скрипт / службу"
				disabled={isBusy}
				onclick={(e) => { e.stopPropagation(); onScriptAction(entry, 'restart'); }}
			>
				<RotateCw size={12} class={isBusy ? 'spin' : ''} />
			</button>
			<button
				type="button"
				class="icon-act-btn stop-act-btn"
				title="Остановить"
				disabled={isBusy}
				onclick={(e) => { e.stopPropagation(); onScriptAction(entry, 'stop'); }}
			>
				<Square size={12} />
			</button>
		{:else}
			<button
				type="button"
				class="icon-act-btn script-act-btn"
				title="Запустить скрипт / службу"
				disabled={isBusy}
				onclick={(e) => { e.stopPropagation(); onScriptAction(entry, isService ? 'start' : 'run'); }}
			>
				<Play size={12} class={isBusy ? 'spin' : ''} />
			</button>
		{/if}
	{/if}

	<button
		type="button"
		class="icon-act-btn"
		title="Свойства"
		onclick={(e) => { e.stopPropagation(); onProps(entry); }}
	>
		<Eye size={13} />
	</button>
	{#if !entry.isDir}
		<button
			type="button"
			class="icon-act-btn"
			title="Редактировать"
			onclick={(e) => { e.stopPropagation(); onEdit(entry); }}
		>
			<Edit2 size={13} />
		</button>
	{/if}
	{#if !readOnly}
		<button
			type="button"
			class="icon-act-btn danger"
			title="Удалить"
			onclick={(e) => { e.stopPropagation(); onDelete(entry); }}
		>
			<Trash2 size={13} />
		</button>
	{/if}
</div>

<style>
	.row-quick-btns {
		display: flex;
		gap: 0.25rem;
		justify-content: flex-end;
	}
	.icon-act-btn {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		border-radius: 4px;
		padding: 0.25rem;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
	}
	.icon-act-btn:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.icon-act-btn.script-act-btn {
		color: var(--color-success, #22c55e);
	}
	.icon-act-btn.script-act-btn:hover {
		background: rgba(34, 197, 94, 0.15);
		border-color: var(--color-success);
	}
	.icon-act-btn.restart-act-btn {
		color: var(--color-accent, #60a5fa);
	}
	.icon-act-btn.restart-act-btn:hover {
		background: var(--color-accent-tint, rgba(96, 165, 250, 0.15));
		border-color: var(--color-accent);
	}
	.icon-act-btn.stop-act-btn {
		color: var(--color-error, #f87171);
	}
	.icon-act-btn.stop-act-btn:hover {
		background: var(--color-error-tint, rgba(239, 68, 68, 0.15));
		border-color: var(--color-error);
	}
	.icon-act-btn.danger:hover {
		color: var(--color-error, #f87171);
		border-color: var(--color-error);
	}
</style>
