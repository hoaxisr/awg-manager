<script lang="ts">
	import { History, Play, Trash2, X } from 'lucide-svelte';

	interface Props {
		commands: string[];
		onselect?: (command: string) => void;
		onclear?: () => void;
		onclose?: () => void;
		compact?: boolean;
	}

	let { commands, onselect, onclear, onclose, compact = false }: Props = $props();
</script>

<aside class="history-panel" class:compact aria-label="История команд">
	<div class="history-head">
		<div class="history-title">
			<History size={14} />
			<span>История</span>
		</div>
		<div class="history-actions">
			{#if commands.length > 0}
				<button type="button" class="icon-btn" title="Очистить историю" onclick={() => onclear?.()}>
					<Trash2 size={13} />
				</button>
			{/if}
			<button type="button" class="icon-btn" title="Скрыть панель" onclick={() => onclose?.()}>
				<X size={13} />
			</button>
		</div>
	</div>

	{#if commands.length === 0}
		<p class="empty">Команды появятся здесь после ввода в терминале</p>
	{:else}
		<ul class="history-list">
			{#each commands as cmd (cmd)}
				<li>
					<button type="button" class="history-item" title={cmd} onclick={() => onselect?.(cmd)}>
						<span class="cmd-text">{cmd}</span>
						<Play size={12} class="run-icon" />
					</button>
				</li>
			{/each}
		</ul>
	{/if}
</aside>

<style>
	.history-panel {
		display: flex;
		flex-direction: column;
		width: 100%;
		height: 100%;
		background: color-mix(in srgb, var(--color-bg-secondary) 85%, var(--color-bg-primary));
		min-height: 0;
		min-width: 0;
	}
	.history-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.35rem;
		padding: 0.45rem 0.5rem;
		border-bottom: 1px solid var(--color-border);
		flex-shrink: 0;
	}
	.history-title {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		opacity: 0.85;
	}
	.history-actions {
		display: flex;
		gap: 0.15rem;
	}
	.icon-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 1.5rem;
		height: 1.5rem;
		border: none;
		border-radius: 4px;
		background: transparent;
		color: inherit;
		opacity: 0.7;
		cursor: pointer;
	}
	.icon-btn:hover {
		opacity: 1;
		background: color-mix(in srgb, var(--color-bg-primary) 40%, transparent);
	}
	.empty {
		margin: 0;
		padding: 0.65rem 0.5rem;
		font-size: 0.75rem;
		line-height: 1.35;
		opacity: 0.65;
	}
	.history-list {
		list-style: none;
		margin: 0;
		padding: 0.35rem;
		overflow: auto;
		flex: 1;
		min-height: 0;
	}
	.history-item {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		width: 100%;
		text-align: left;
		border: none;
		border-radius: 5px;
		background: transparent;
		color: inherit;
		padding: 0.35rem 0.4rem;
		cursor: pointer;
		font-family: var(--font-mono, monospace);
		font-size: 0.72rem;
		line-height: 1.25;
	}
	.history-item:hover {
		background: color-mix(in srgb, var(--color-bg-primary) 55%, transparent);
	}
	.cmd-text {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.history-item :global(.run-icon) {
		flex-shrink: 0;
		opacity: 0.45;
	}
	.history-item:hover :global(.run-icon) {
		opacity: 0.9;
	}
</style>
