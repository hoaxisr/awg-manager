<script lang="ts">
	interface Props {
		latencyMs: number | null;
		ok: boolean;
		activeForRestart: boolean;
		onClick?: () => void;
		ariaLabel?: string;
	}

	let { latencyMs, ok, activeForRestart, onClick, ariaLabel = '' }: Props = $props();

	const tone = $derived.by(() => {
		if (!ok || latencyMs === null) return 'failed';
		if (latencyMs < 100) return 'good';
		if (latencyMs <= 250) return 'warn';
		return 'bad';
	});

	const display = $derived.by(() => {
		if (!ok || latencyMs === null) return '—';
		return `${latencyMs}ms`;
	});
</script>

<button
	type="button"
	class="cell tone-{tone}"
	class:has-click={!!onClick}
	onclick={onClick}
	aria-label={ariaLabel}
>
	{#if activeForRestart}
		<span class="badge" aria-label="Активный target для рестарта">★</span>
	{/if}
	<span class="value">{display}</span>
</button>

<style>
	.cell {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-width: 84px;
		height: 32px;
		padding: 0 0.5rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font: inherit;
		font-family: var(--font-mono);
		font-size: 12px;
		font-variant-numeric: tabular-nums;
		cursor: default;
		position: relative;
		transition: filter var(--t-fast) ease;
	}

	.cell.has-click {
		cursor: pointer;
	}

	.cell.has-click:hover {
		filter: brightness(1.1);
	}

	.tone-good {
		background: var(--color-success-tint);
		color: var(--color-success);
		border-color: var(--color-success-border);
	}

	.tone-warn {
		background: var(--color-warning-tint);
		color: var(--color-warning);
		border-color: var(--color-warning-border);
	}

	.tone-bad {
		background: var(--color-error-tint);
		color: var(--color-error);
		border-color: var(--color-error-border);
	}

	.tone-failed {
		background: var(--color-muted-tint);
		color: var(--color-text-muted);
		border-color: var(--color-border);
	}

	.badge {
		position: absolute;
		top: -4px;
		right: -4px;
		font-size: 10px;
		color: var(--color-accent);
		background: var(--color-bg-primary);
		border-radius: 50%;
		width: 14px;
		height: 14px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
	}

	.value {
		display: inline-block;
	}
</style>
