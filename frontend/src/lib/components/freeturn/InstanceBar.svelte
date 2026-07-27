<script lang="ts">
	import { Button, Toggle } from '$lib/components/ui';
	import { Pencil, X } from 'lucide-svelte';
	import { formatUptime } from './uptime';

	interface InstanceItem {
		id: string;
		name: string;
		running?: boolean;
		startedAt?: string;
		pid?: number;
		dtlsConnections?: number;
		binaryPresent?: boolean;
	}

	interface Props {
		items: InstanceItem[];
		selectedId: string;
		canDelete?: boolean;
		showDtls?: boolean;
		onSelect: (id: string) => void;
		onToggle: (id: string, on: boolean) => void;
		onAdd: () => void;
		onDelete: (id: string) => void;
		onRename?: (id: string, name: string) => void;
	}

	let {
		items,
		selectedId,
		canDelete = true,
		showDtls = true,
		onSelect,
		onToggle,
		onAdd,
		onDelete,
		onRename
	}: Props = $props();

	let renamingId = $state<string | null>(null);
	let renameDraft = $state('');

	function startRename(item: InstanceItem) {
		if (!onRename) return;
		renamingId = item.id;
		renameDraft = item.name;
	}

	function commitRename(id: string) {
		const name = renameDraft.trim();
		if (name && onRename) onRename(id, name);
		renamingId = null;
	}

	function meta(item: InstanceItem): string {
		if (!item.running) return 'остановлен';
		return ['запущен', formatUptime(item.startedAt), item.pid ? `PID ${item.pid}` : '']
			.filter(Boolean)
			.join(' · ');
	}
</script>

<div class="ft-instance-bar">
	<div class="ft-instance-list">
		{#each items as item (item.id)}
			<div class="ft-instance-row" class:active={item.id === selectedId}>
				<button type="button" class="ft-instance-main" onclick={() => onSelect(item.id)}>
					<span class="ft-chip-dot" class:running={item.running}></span>
					{#if renamingId === item.id}
						<!-- rename handled below -->
					{:else}
						<span class="ft-instance-name">{item.name}</span>
						<span class="ft-instance-meta">{meta(item)}</span>
					{/if}
				</button>

				{#if renamingId === item.id}
					<input
						class="ft-rename-input"
						bind:value={renameDraft}
						onkeydown={(e) => {
							if (e.key === 'Enter') commitRename(item.id);
							if (e.key === 'Escape') renamingId = null;
						}}
						onblur={() => commitRename(item.id)}
					/>
				{:else if item.running && showDtls}
					<span
						class="ft-instance-dtls"
						class:live={(item.dtlsConnections ?? 0) > 0}
						title="Активные DTLS-соединения (обновляется каждые 3 с)"
					>
						DTLS {(item.dtlsConnections ?? 0).toLocaleString('ru-RU')}
					</span>
				{/if}

				<div class="ft-instance-actions">
					<Toggle
						checked={!!item.running}
						onchange={(on) => onToggle(item.id, on)}
						disabled={item.binaryPresent === false}
						controlled
						size="sm"
						label=""
						ariaLabel="{item.name}: запустить или остановить"
					/>
					{#if onRename}
						<button
							type="button"
							class="ft-chip-action"
							title="Переименовать"
							onclick={() => startRename(item)}
						>
							<Pencil size={14} />
						</button>
					{/if}
					{#if canDelete}
						<button
							type="button"
							class="ft-chip-action danger"
							title="Удалить"
							onclick={() => onDelete(item.id)}
						>
							<X size={14} />
						</button>
					{/if}
				</div>
			</div>
		{/each}
	</div>
	<Button variant="secondary" size="sm" onclick={onAdd}>+ Добавить</Button>
</div>

<style>
	.ft-instance-bar {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
		margin-bottom: 1rem;
		flex-wrap: wrap;
	}

	.ft-instance-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		flex: 1;
		min-width: 0;
	}

	.ft-instance-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.375rem 0.5rem 0.375rem 0.625rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
	}

	.ft-instance-dtls {
		margin-left: auto;
		font-size: 0.6875rem;
		font-family: var(--font-mono);
		font-weight: 600;
		color: var(--color-text-muted);
		padding: 0.125rem 0.5rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		white-space: nowrap;
	}

	.ft-instance-dtls.live {
		color: var(--color-success);
		border-color: color-mix(in srgb, var(--color-success) 35%, var(--color-border));
	}

	.ft-instance-row.active {
		border-color: var(--color-accent);
		background: var(--color-bg-primary);
	}

	.ft-instance-main {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex: 1;
		min-width: 0;
		padding: 0.125rem 0;
		background: none;
		border: none;
		color: inherit;
		cursor: pointer;
		text-align: left;
	}

	.ft-instance-name {
		font-size: 0.8125rem;
		font-weight: 600;
		white-space: nowrap;
	}

	.ft-instance-meta {
		font-size: 0.6875rem;
		color: var(--color-text-secondary);
		font-family: var(--font-mono);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.ft-instance-actions {
		display: inline-flex;
		align-items: center;
		gap: 0.125rem;
		flex-shrink: 0;
	}

	.ft-chip-dot {
		width: 0.5rem;
		height: 0.5rem;
		border-radius: 50%;
		background: var(--color-text-muted);
		flex-shrink: 0;
	}

	.ft-chip-dot.running {
		background: var(--color-success);
	}

	.ft-chip-action {
		background: none;
		border: none;
		color: var(--color-text-secondary);
		cursor: pointer;
		padding: 0.125rem 0.375rem;
		font-size: 0.875rem;
		line-height: 1;
	}

	.ft-chip-action.danger:hover {
		color: var(--color-danger);
	}

	.ft-rename-input {
		flex: 1;
		font-size: 0.8125rem;
		padding: 0.25rem 0.5rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		min-width: 0;
	}

	@media (max-width: 640px) {
		.ft-instance-main {
			flex-wrap: wrap;
		}

		.ft-instance-meta {
			flex-basis: 100%;
			padding-left: 1rem;
		}
	}
</style>
