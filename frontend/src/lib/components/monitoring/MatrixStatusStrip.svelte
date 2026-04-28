<script lang="ts">
	import type { MonitoringSnapshot } from '$lib/types';

	interface Props {
		snapshot: MonitoringSnapshot;
	}

	let { snapshot }: Props = $props();

	const tunnelHasOk = $derived.by(() => {
		const m = new Map<string, boolean>();
		for (const c of snapshot.cells) {
			if (c.ok) m.set(c.tunnelId, true);
		}
		return m;
	});

	const total = $derived(snapshot.tunnels.length);
	const up = $derived(snapshot.tunnels.filter((t) => tunnelHasOk.get(t.id)).length);
	const failed = $derived(total - up);

	const avgLatency = $derived.by(() => {
		const ok = snapshot.cells.filter((c) => c.ok && c.latencyMs !== null);
		if (ok.length === 0) return null;
		const sum = ok.reduce((acc, c) => acc + (c.latencyMs ?? 0), 0);
		return Math.round(sum / ok.length);
	});
</script>

<div class="strip">
	<div class="tile">
		<span class="value">{total}</span>
		<span class="label">Всего</span>
	</div>
	<div class="tile">
		<span class="value tone-good">{up}</span>
		<span class="label">Up</span>
	</div>
	<div class="tile">
		<span class="value" class:tone-bad={failed > 0}>{failed}</span>
		<span class="label">Failed</span>
	</div>
	<div class="tile">
		<span class="value">{avgLatency !== null ? `${avgLatency}ms` : '—'}</span>
		<span class="label">Средняя latency</span>
	</div>
</div>

<style>
	.strip {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 0.75rem;
		margin-bottom: 1rem;
	}

	.tile {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 1rem;
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.value {
		font-family: var(--font-mono);
		font-size: 28px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		line-height: 1;
	}

	.label {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.tone-good { color: var(--color-success); }
	.tone-bad { color: var(--color-error); }

	@media (max-width: 768px) {
		.strip {
			grid-template-columns: repeat(2, 1fr);
		}
	}
</style>
