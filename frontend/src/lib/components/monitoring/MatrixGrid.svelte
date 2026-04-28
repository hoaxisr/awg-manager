<script lang="ts">
	import type { MonitoringSnapshot, MonitoringTarget, MonitoringTunnel, MonitoringCell } from '$lib/types';
	import MatrixCell from './MatrixCell.svelte';

	interface Props {
		snapshot: MonitoringSnapshot;
		onCellClick: (target: MonitoringTarget, tunnel: MonitoringTunnel) => void;
	}

	let { snapshot, onCellClick }: Props = $props();

	const sortedTunnels = $derived(
		[...snapshot.tunnels].sort((a, b) => a.name.localeCompare(b.name)),
	);

	function isSystem(t: MonitoringTunnel): boolean {
		return t.id.startsWith('sys-');
	}

	// Managed AWG tunnels open the pingcheck drawer on the monitoring page.
	// System tunnels are read-only — Keenetic owns their pingcheck.
	function tunnelHref(t: MonitoringTunnel): string {
		return `/monitoring?pingcheck=${encodeURIComponent(t.id)}`;
	}

	const cellByKey = $derived.by(() => {
		const m = new Map<string, MonitoringCell>();
		for (const c of snapshot.cells) {
			m.set(`${c.targetId}|${c.tunnelId}`, c);
		}
		return m;
	});

	function findCell(targetId: string, tunnelId: string): MonitoringCell | null {
		return cellByKey.get(`${targetId}|${tunnelId}`) ?? null;
	}
</script>

{#if sortedTunnels.length === 0}
	<div class="empty">Нет работающих туннелей. Запустите хотя бы один туннель для отображения матрицы.</div>
{:else}
	<div class="wrap">
		<table class="matrix">
			<thead>
				<tr>
					<th class="th-target">Target</th>
					{#each sortedTunnels as t (t.id)}
						<th class="th-tunnel">
							{#if isSystem(t)}
								<span class="tunnel-system" title="Системный туннель Keenetic — pingcheck управляется в системе">
									{t.name}
								</span>
							{:else}
								<a href={tunnelHref(t)} class="tunnel-link" title="Открыть настройки pingcheck">
									{t.name}
									<span class="settings-icon" aria-hidden="true">›</span>
								</a>
							{/if}
						</th>
					{/each}
				</tr>
			</thead>
			<tbody>
				{#each snapshot.targets as target (target.id)}
					<tr>
						<th class="td-target" scope="row">
							<span class="target-name">{target.name}</span>
							<span class="target-host">{target.host}</span>
						</th>
						{#each sortedTunnels as tunnel (tunnel.id)}
							{@const cell = findCell(target.id, tunnel.id)}
							<td class="td-cell">
								{#if cell}
									<MatrixCell
										latencyMs={cell.latencyMs}
										ok={cell.ok}
										activeForRestart={cell.activeForRestart}
										onClick={() => onCellClick(target, tunnel)}
										ariaLabel="{target.name} × {tunnel.name}"
									/>
								{:else}
									<MatrixCell latencyMs={null} ok={false} activeForRestart={false} ariaLabel="no data" />
								{/if}
							</td>
						{/each}
					</tr>
				{/each}
			</tbody>
		</table>

		<div class="legend">
			<span class="legend-item"><span class="swatch tone-good"></span>&lt;100ms</span>
			<span class="legend-item"><span class="swatch tone-warn"></span>100-250ms</span>
			<span class="legend-item"><span class="swatch tone-bad"></span>&gt;250ms</span>
			<span class="legend-item"><span class="swatch tone-failed"></span>failed</span>
			<span class="legend-item">★ — активный pingcheck target</span>
			<span class="legend-item">Клик на имя туннеля — настройки pingcheck</span>
		</div>
	</div>
{/if}

<style>
	.wrap {
		overflow-x: auto;
	}

	.matrix {
		border-collapse: separate;
		border-spacing: 0.375rem;
		width: 100%;
	}

	.th-target,
	.th-tunnel {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		padding: 0.4375rem 0.5rem;
		text-align: left;
		background: var(--color-bg-tertiary);
		border-bottom: 1px solid var(--color-border);
		position: sticky;
		top: 0;
	}

	.th-tunnel {
		min-width: 100px;
		text-align: center;
	}

	.tunnel-link {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		color: inherit;
		text-decoration: none;
		padding: 0.125rem 0.375rem;
		border-radius: var(--radius-sm);
		transition: color var(--t-fast) ease, background var(--t-fast) ease;
	}
	.tunnel-link:hover {
		color: var(--color-accent);
		background: var(--color-bg-hover);
	}
	.settings-icon {
		font-size: 14px;
		opacity: 0.7;
	}

	.tunnel-system {
		display: inline-block;
		padding: 0.125rem 0.375rem;
		color: var(--color-text-muted);
		cursor: help;
	}

	.th-target {
		left: 0;
		z-index: 2;
	}

	.td-target {
		padding: 0.375rem 0.5rem;
		text-align: left;
		font-size: 12px;
		background: var(--color-bg-secondary);
		position: sticky;
		left: 0;
		min-width: 160px;
	}

	.target-name {
		display: block;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.target-host {
		display: block;
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.td-cell {
		padding: 0.125rem;
		text-align: center;
	}

	.empty {
		padding: 3rem 1rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 14px;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius);
	}

	.legend {
		display: flex;
		gap: 1rem;
		flex-wrap: wrap;
		margin-top: 0.75rem;
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.legend-item {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
	}

	.swatch {
		display: inline-block;
		width: 12px;
		height: 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
	}

	.swatch.tone-good { background: color-mix(in srgb, var(--color-success) 50%, transparent); }
	.swatch.tone-warn { background: color-mix(in srgb, var(--color-warning) 50%, transparent); }
	.swatch.tone-bad { background: color-mix(in srgb, var(--color-error) 50%, transparent); }
	.swatch.tone-failed { background: var(--color-muted-tint); }
</style>
