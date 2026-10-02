<script lang="ts">
	import { m } from '$lib/i18n';
	import type { SystemProcSnapshot } from '$lib/api/client';
	import type { SystemCpuCore } from '$lib/api/clientSystem';
	import { Activity, Clock, Layers } from 'lucide-svelte';

	interface Props {
		snapshot: SystemProcSnapshot;
	}

	let { snapshot }: Props = $props();

	const numCores = $derived(snapshot.cores.filter((c: SystemCpuCore) => c.id !== 'total').length || 2);
	const load1Pct = $derived(Math.round((snapshot.loadAvg[0] / numCores) * 100));
	const loadStatusClass = $derived(load1Pct >= 80 ? 'high' : load1Pct >= 45 ? 'med' : 'low');
	const loadStatusText = $derived(
		load1Pct >= 100
			? m.system_processes_load_overload()
			: load1Pct >= 70
				? m.system_processes_load_high()
				: load1Pct >= 30
					? m.system_processes_load_moderate()
					: m.system_processes_load_low(),
	);

	function formatUptime(sec: number): string {
		if (!sec) return '—';
		const days = Math.floor(sec / 86400);
		const hours = Math.floor((sec % 86400) / 3600);
		const minutes = Math.floor((sec % 3600) / 60);
		if (days > 0) return m.system_processes_uptime_dhm({ days, hours, minutes });
		if (hours > 0) return m.system_processes_uptime_hm({ hours, minutes });
		return m.system_processes_uptime_ms({ minutes, seconds: sec % 60 });
	}
</script>

<div class="sys-meta-grid">
	<div class="meta-item">
		<div class="meta-k" title={m.system_processes_meta_load_title({ cores: numCores })}>
			<Activity size={13} />
			<span>{m.system_processes_meta_load_label()}</span>
		</div>
		<div class="meta-v">
			<span class="load-status-pill level-{loadStatusClass}" title={m.system_processes_meta_load_now_title({ pct: load1Pct, cores: numCores })}>
				<span class="dot"></span>
				<span>{loadStatusText} ({load1Pct}%)</span>
			</span>
			<div class="load-badges-group" title={m.system_processes_meta_load_badges_title()}>
				<span class="load-badge">{m.system_processes_meta_1m()} <strong>{snapshot.loadAvg[0].toFixed(2)}</strong></span>
				<span class="load-badge">{m.system_processes_meta_5m()} <strong>{snapshot.loadAvg[1].toFixed(2)}</strong></span>
				<span class="load-badge">{m.system_processes_meta_15m()} <strong>{snapshot.loadAvg[2].toFixed(2)}</strong></span>
			</div>
		</div>
	</div>
	<div class="meta-item">
		<div class="meta-k"><Clock size={13} /> {m.system_processes_meta_uptime()}</div>
		<div class="meta-v"><strong>{formatUptime(snapshot.uptimeSeconds)}</strong></div>
	</div>
	<div class="meta-item">
		<div class="meta-k"><Layers size={13} /> {m.system_processes_meta_tasks()}</div>
		<div class="meta-v">
			<strong>{snapshot.processSummary.total}</strong> {m.system_processes_meta_total()} (<strong>{snapshot.processSummary.running}</strong> {m.system_processes_meta_active()}, <strong>{snapshot.processSummary.threads}</strong> {m.system_processes_meta_threads()})
		</div>
	</div>
</div>

<style>
	/* System Metadata */
	.sys-meta-grid {
		display: grid;
		grid-template-columns: 1fr;
		gap: 0.35rem;
		margin-top: 0.2rem;
		padding: 0.5rem 0.65rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		font-size: 0.8rem;
	}

	.meta-item {
		display: flex;
		justify-content: space-between;
		align-items: center;
	}

	.meta-k {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		color: var(--color-text-muted);
	}

	.meta-v {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		color: var(--color-text-primary);
	}

	.load-status-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		font-size: 0.72rem;
		font-weight: 600;
		padding: 0.1rem 0.45rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
	}
	.load-status-pill .dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		flex-shrink: 0;
	}
	.load-status-pill.level-low {
		background: rgba(16, 185, 129, 0.12);
		border-color: rgba(16, 185, 129, 0.3);
		color: #059669;
	}
	:global(.dark) .load-status-pill.level-low {
		background: rgba(16, 185, 129, 0.15);
		color: #34d399;
	}
	.load-status-pill.level-low .dot { background: #10b981; }

	.load-status-pill.level-med {
		background: rgba(245, 158, 11, 0.12);
		border-color: rgba(245, 158, 11, 0.3);
		color: #d97706;
	}
	:global(.dark) .load-status-pill.level-med {
		background: rgba(245, 158, 11, 0.15);
		color: #fbbf24;
	}
	.load-status-pill.level-med .dot { background: #f59e0b; }

	.load-status-pill.level-high {
		background: rgba(239, 68, 68, 0.12);
		border-color: rgba(239, 68, 68, 0.3);
		color: #dc2626;
	}
	:global(.dark) .load-status-pill.level-high {
		background: rgba(239, 68, 68, 0.15);
		color: #f87171;
	}
	.load-status-pill.level-high .dot { background: #ef4444; }

	.load-badges-group {
		display: flex;
		align-items: center;
		gap: 0.25rem;
	}

	.load-badge {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		padding: 0.08rem 0.35rem;
		border-radius: 4px;
		font-size: 0.74rem;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-secondary);
	}
	.load-badge strong {
		color: var(--color-text-primary);
	}
</style>
