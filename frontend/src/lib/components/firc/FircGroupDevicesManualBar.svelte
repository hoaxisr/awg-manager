<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { X } from 'lucide-svelte';

	interface Props {
		mode: 'all' | 'allow';
		entries: Set<string>;
		value: string;
		chipLabel: (entry: string) => string;
		onadd: () => void;
		onremove: (entry: string) => void;
	}

	let { mode, entries, value = $bindable(), chipLabel, onadd, onremove }: Props = $props();
</script>

<div class="dm-manual-bar">
	<div class="dm-manual-row">
		<input
			type="text"
			class="dm-manual-inp"
			aria-label={mode === 'all'
				? m.firc_devices_manual_placeholder_exclude()
				: m.firc_devices_manual_placeholder_include()}
			placeholder={mode === 'all'
				? m.firc_devices_manual_placeholder_exclude()
				: m.firc_devices_manual_placeholder_include()}
			bind:value
			onkeydown={(e) => {
				if (e.key === 'Enter') {
					e.preventDefault();
					onadd();
				}
			}}
		/>
		<Button
			variant="secondary"
			size="sm"
			onclick={onadd}
			disabled={!value.trim()}
		>
			{mode === 'all' ? m.firc_devices_manual_btn_exclude() : m.common_add()}
		</Button>
	</div>

	{#if entries.size > 0}
		<div class="dm-chips-strip">
			{#each Array.from(entries) as entry (entry)}
				<span class="dm-chip" class:dm-chip-excl={mode === 'all'}>
					<span class="dm-chip-label">{chipLabel(entry)}</span>
					<button
						type="button"
						class="dm-chip-remove"
						title={m.firc_devices_chip_remove()}
						aria-label={m.firc_devices_chip_remove()}
						onclick={() => onremove(entry)}
					>
						<X size={11} />
					</button>
				</span>
			{/each}
		</div>
	{/if}
</div>

<style>
	.dm-manual-bar {
		flex: 0 0 auto;
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 10px 12px 12px;
		border-top: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-bg-primary) 35%, transparent);
	}

	.dm-manual-row {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.dm-manual-inp {
		flex: 1;
		min-width: 0;
		height: 32px;
		padding: 0 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-family: var(--font-mono);
		font-size: 12px;
		outline: none;
		transition: border-color var(--t-fast);
	}

	.dm-manual-inp:focus {
		border-color: var(--color-accent);
	}

	.dm-chips-strip {
		display: flex;
		flex-wrap: wrap;
		gap: 5px;
		max-height: 60px;
		overflow-y: auto;
	}

	.dm-chip {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-text-primary);
		font-size: 11.5px;
		font-weight: 500;
		max-width: 260px;
	}

	.dm-chip.dm-chip-excl {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
	}

	.dm-chip-label {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dm-chip-remove {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border: 0;
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		padding: 1px;
		border-radius: 3px;
		flex-shrink: 0;
	}

	.dm-chip-remove:hover {
		color: var(--color-error);
		background: var(--color-error-tint);
	}
</style>
