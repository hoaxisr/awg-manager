<script lang="ts">
	import type { SystemProcessItem } from '$lib/api/client';
	import { formatBytes } from '$lib/utils/format';
	import { Square } from 'lucide-svelte';
	import { getCpuClass } from './shared';

	interface Props {
		proc: SystemProcessItem;
		onkill: (proc: SystemProcessItem) => void;
	}

	let { proc, onkill }: Props = $props();

	const cpuLvl = $derived(getCpuClass(proc.cpuPercent));
</script>

<tr class="proc-row" class:is-self={proc.isSelf} class:is-high-cpu={proc.cpuPercent > 30}>
	<!-- PID -->
	<td class="col-td-pid">
		<code>{proc.pid}</code>
	</td>

	<!-- User -->
	<td class="col-td-user">
		<span class="user-pill" class:root={proc.user === 'root'}>{proc.user}</span>
	</td>

	<!-- State -->
	<td class="col-td-state">
		<span class="state-badge state-{proc.state.toLowerCase()}" title={
			proc.state === 'R' ? 'Выполняется (Running)' :
			proc.state === 'S' ? 'Ожидание (Sleeping)' :
			proc.state === 'D' ? 'Ожидание диска (Disk sleep)' :
			proc.state === 'Z' ? 'Зомби (Zombie)' : 'Остановлен'
		}>
			{proc.state}
		</span>
	</td>

	<!-- Threads -->
	<td class="col-td-threads">
		{proc.threads}
	</td>

	<!-- CPU % -->
	<td class="col-td-cpu">
		<div class="cpu-cell-wrap">
			<span class="cpu-val-text level-{cpuLvl}">
				{proc.cpuPercent.toFixed(1)}%
			</span>
			{#if proc.cpuPercent > 0.5}
				<div class="mini-bar">
					<div
						class="mini-bar-fill bar-level-{cpuLvl}"
						style="width: {Math.min(100, proc.cpuPercent)}%"
					></div>
				</div>
			{/if}
		</div>
	</td>

	<!-- Memory -->
	<td class="col-td-mem">
		<div class="mem-cell-wrap">
			<span class="mem-rss">{formatBytes(proc.memoryRss)}</span>
			{#if proc.memoryPercent > 0.1}
				<span class="mem-pct">({proc.memoryPercent.toFixed(1)}%)</span>
			{/if}
		</div>
	</td>

	<!-- Command / Process -->
	<td class="col-td-cmd">
		<div class="cmd-wrap">
			<span class="proc-name">{proc.name}</span>
			{#if proc.isSelf}
				<span class="badge-self">AWG Manager</span>
			{/if}
			{#if proc.isCritical}
				<span class="badge-critical">Системный</span>
			{/if}
			<span class="proc-cmdline" title={proc.cmdline}>{proc.cmdline}</span>
		</div>
	</td>

	<!-- Actions -->
	<td class="col-td-act">
		<button
			type="button"
			class="btn-kill"
			class:btn-kill-self={proc.isSelf}
			title={proc.isSelf ? 'Остановить сервис AWG Manager' : 'Завершить процесс'}
			onclick={() => onkill(proc)}
		>
			<Square size={11} />
		</button>
	</td>
</tr>

<style>
	.proc-row td {
		padding: 0.45rem 0.5rem;
		border-bottom: 1px solid var(--color-border);
		text-align: left;
		vertical-align: middle;
	}

	/* Fixed column widths */
	.col-td-pid { width: 56px; }
	.col-td-user { width: 68px; }
	.col-td-state { width: 48px; text-align: center; }
	.col-td-threads { width: 56px; text-align: center; }
	.col-td-cpu { width: 76px; }
	.col-td-mem { width: 110px; }
	.col-td-cmd { width: auto; overflow: hidden; }
	.col-td-act { width: 48px; text-align: center; }

	.proc-row:hover {
		background: var(--color-bg-hover, rgba(255,255,255,0.03));
	}

	.proc-row.is-self {
		background: var(--color-accent-tint, rgba(96, 165, 250, 0.08));
	}

	.col-td-pid code {
		font-weight: 700;
		color: var(--color-text-primary);
		font-size: 0.78rem;
	}

	.user-pill {
		font-size: 0.72rem;
		padding: 0.05rem 0.25rem;
		border-radius: 3px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
	}

	.user-pill.root {
		color: var(--color-accent);
		font-weight: 600;
	}

	.state-badge {
		display: inline-block;
		font-size: 0.7rem;
		font-weight: 700;
		padding: 0.05rem 0.3rem;
		border-radius: 3px;
		text-align: center;
	}
	.state-r {
		background: rgba(16, 185, 129, 0.15);
		color: #10b981;
	}
	.state-s {
		background: rgba(148, 163, 184, 0.15);
		color: #94a3b8;
	}
	.state-d {
		background: rgba(245, 158, 11, 0.15);
		color: #f59e0b;
	}
	.state-z {
		background: rgba(239, 68, 68, 0.15);
		color: #ef4444;
	}

	.cpu-cell-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}

	.cpu-val-text {
		font-weight: 700;
		font-family: var(--font-mono, monospace);
		font-size: 0.78rem;
	}

	.mini-bar {
		height: 3px;
		background: var(--color-bg-tertiary);
		border-radius: 999px;
		overflow: hidden;
		width: 40px;
	}

	.mini-bar-fill {
		height: 100%;
	}
	.bar-level-low { background: #10b981; }
	.bar-level-med { background: #f59e0b; }
	.bar-level-high { background: #ef4444; }

	.mem-cell-wrap {
		display: flex;
		align-items: baseline;
		gap: 0.2rem;
		white-space: nowrap;
	}
	.mem-rss {
		font-weight: 600;
		color: var(--color-text-primary);
		font-size: 0.78rem;
	}

	.mem-pct {
		font-size: 0.7rem;
		color: var(--color-text-muted);
	}

	.cmd-wrap {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.proc-name {
		font-weight: 700;
		color: var(--color-text-primary);
		flex-shrink: 0;
	}

	.badge-self {
		font-size: 0.65rem;
		font-weight: 700;
		padding: 0.05rem 0.3rem;
		border-radius: 3px;
		background: rgba(96, 165, 250, 0.2);
		color: #60a5fa;
		flex-shrink: 0;
	}

	.badge-critical {
		font-size: 0.65rem;
		font-weight: 700;
		padding: 0.05rem 0.3rem;
		border-radius: 3px;
		background: rgba(245, 158, 11, 0.15);
		color: #f59e0b;
		flex-shrink: 0;
	}

	.proc-cmdline {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono, monospace);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		flex: 1;
		min-width: 0;
	}

	.btn-kill {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		border-radius: 4px;
		padding: 0.25rem 0.35rem;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		justify-content: center;
	}

	.btn-kill:hover {
		background: var(--color-error-tint, rgba(239, 68, 68, 0.15));
		color: var(--color-error, #f87171);
		border-color: var(--color-error);
	}

	.btn-kill.btn-kill-self:hover {
		background: rgba(245, 158, 11, 0.2);
		color: #f59e0b;
		border-color: #f59e0b;
	}

	/* Text color levels */
	.level-low {
		color: #059669;
	}
	:global(.dark) .level-low {
		color: #34d399;
	}

	.level-med {
		color: #d97706;
	}
	:global(.dark) .level-med {
		color: #fbbf24;
	}

	.level-high {
		color: #dc2626;
	}
	:global(.dark) .level-high {
		color: #f87171;
	}
</style>
