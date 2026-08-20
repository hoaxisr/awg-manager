<script lang="ts">
	import type { SystemProcSnapshot } from '$lib/api/client';
	import type { SystemCpuCore } from '$lib/api/clientSystem';
	import { Card } from '$lib/components/ui';
	import { formatBytes } from '$lib/utils/format';
	import { Cpu, HardDrive } from 'lucide-svelte';
	import SystemMetaGrid from './SystemMetaGrid.svelte';
	import { getCpuClass } from './shared';

	interface Props {
		snapshot: SystemProcSnapshot | null;
	}

	let { snapshot }: Props = $props();
</script>

<Card padding="md">
	<div class="dashboard-grid">
		<!-- Left: CPU Cores Bars -->
		<div class="dash-col cpu-col">
			<div class="col-head">
				<div class="col-title">
					<Cpu size={16} class="text-accent" />
					<span>Процессор (CPU)</span>
					{#if snapshot?.cpuModel}
						<span class="chip-model">{snapshot.cpuModel}</span>
					{/if}
				</div>
				{#if snapshot?.cores && snapshot.cores.length > 0}
					{@const totalCore = snapshot.cores.find((c: SystemCpuCore) => c.id === 'total') ?? snapshot.cores[0]}
					{@const level = getCpuClass(totalCore.usage)}
					<div class="pill-cpu-total level-{level}">
						<span class="dot"></span>
						<span>Всего: <strong>{totalCore.usage.toFixed(1)}%</strong></span>
					</div>
				{/if}
			</div>

			<div class="cores-list">
				{#if snapshot?.cores}
					{#each snapshot.cores.filter((c: SystemCpuCore) => c.id !== 'total') as core, i}
						{@const level = getCpuClass(core.usage)}
						<div class="core-card">
							<div class="core-header-line">
								<div class="core-id-group">
									<span class="core-idx">CPU {i + 1}</span>
									<span class="core-details-text">
										usr {core.user.toFixed(1)}% · sys {core.system.toFixed(1)}% {#if core.iowait > 0.5}· io {core.iowait.toFixed(1)}%{/if}
									</span>
								</div>
								<span class="core-percentage level-{level}">
									{core.usage.toFixed(1)}%
								</span>
							</div>

							<!-- Multi-segment htop bar -->
							<div class="htop-bar-track">
								<!-- User (emerald) -->
								<div
									class="bar-seg seg-user"
									style="width: {Math.min(100, Math.max(0, core.user))}%"
									title="Пользователь (User): {core.user.toFixed(1)}%"
								></div>
								<!-- System (amber) -->
								<div
									class="bar-seg seg-sys"
									style="width: {Math.min(100 - core.user, Math.max(0, core.system))}%"
									title="Система (Kernel): {core.system.toFixed(1)}%"
								></div>
								<!-- IOWait (orange/red) -->
								{#if core.iowait > 0}
									<div
										class="bar-seg seg-iowait"
										style="width: {Math.min(100 - core.user - core.system, Math.max(0, core.iowait))}%"
										title="Ожидание ввода-вывода: {core.iowait.toFixed(1)}%"
									></div>
								{/if}
							</div>
						</div>
					{/each}
				{:else}
					<div class="loading-hint">Сбор данных CPU…</div>
				{/if}
			</div>
		</div>

		<!-- Right: RAM & System Info -->
		<div class="dash-col mem-col">
			<div class="col-head">
				<div class="col-title">
					<HardDrive size={16} class="text-accent" />
					<span>Оперативная память</span>
				</div>
				{#if snapshot?.memory}
					<div class="pill-mem-total">
						<span>{formatBytes(snapshot.memory.used)} / {formatBytes(snapshot.memory.total)}</span>
						<strong>({snapshot.memory.usagePercent.toFixed(1)}%)</strong>
					</div>
				{/if}
			</div>

			{#if snapshot?.memory}
				<div class="mem-bars">
					<!-- RAM Bar -->
					<div class="core-card">
						<div class="core-header-line">
							<span class="core-idx">ОЗУ</span>
							<span class="core-details-text">
								Занято: {formatBytes(snapshot.memory.used)} · Кэш: {formatBytes(snapshot.memory.cached + snapshot.memory.buffers)} · Свободно: {formatBytes(snapshot.memory.available)}
							</span>
							<span class="core-percentage mem-pct-txt">
								{snapshot.memory.usagePercent.toFixed(1)}%
							</span>
						</div>
						<div class="htop-bar-track">
							<div
								class="bar-seg mem-used-seg"
								style="width: {Math.min(100, (snapshot.memory.used / snapshot.memory.total) * 100)}%"
								title="Занято приложениями: {formatBytes(snapshot.memory.used)}"
							></div>
							<div
								class="bar-seg mem-cached-seg"
								style="width: {Math.min(100 - (snapshot.memory.used / snapshot.memory.total) * 100, (snapshot.memory.cached / snapshot.memory.total) * 100)}%"
								title="Кэш и буферы: {formatBytes(snapshot.memory.cached + snapshot.memory.buffers)}"
							></div>
						</div>
					</div>

					<!-- Swap Bar (if configured) -->
					{#if snapshot.memory.swapTotal > 0}
						<div class="core-card">
							<div class="core-header-line">
								<span class="core-idx">Swap</span>
								<span class="core-details-text">
									{formatBytes(snapshot.memory.swapUsed)} / {formatBytes(snapshot.memory.swapTotal)}
								</span>
								<span class="core-percentage mem-pct-txt">
									{((snapshot.memory.swapUsed / snapshot.memory.swapTotal) * 100).toFixed(1)}%
								</span>
							</div>
							<div class="htop-bar-track">
								<div
									class="bar-seg swap-seg"
									style="width: {Math.min(100, (snapshot.memory.swapUsed / snapshot.memory.swapTotal) * 100)}%"
								></div>
							</div>
						</div>
					{/if}
				</div>
			{/if}

			<!-- System Meta Metrics -->
			{#if snapshot}
				<SystemMetaGrid {snapshot} />
			{/if}
		</div>
	</div>
</Card>
<style>
	/* 1. Hardware Dashboard */
	.dashboard-grid {
		display: grid;
		grid-template-columns: 1.15fr 1fr;
		gap: 1.25rem;
	}

	.dash-col {
		display: flex;
		flex-direction: column;
		gap: 0.65rem;
	}

	.col-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.4rem;
	}

	.col-title {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.9rem;
		font-weight: 700;
		color: var(--color-text-primary);
	}

	.chip-model {
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--color-text-muted);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		padding: 0.05rem 0.4rem;
		border-radius: 4px;
	}

	/* Badges */
	.pill-cpu-total {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.75rem;
		font-weight: 600;
		padding: 0.15rem 0.55rem;
		border-radius: 999px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
	}
	.pill-cpu-total .dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
	}
	.pill-cpu-total.level-low .dot { background: #10b981; }
	.pill-cpu-total.level-med .dot { background: #f59e0b; }
	.pill-cpu-total.level-high .dot { background: #ef4444; }

	.pill-mem-total {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.75rem;
		padding: 0.15rem 0.55rem;
		border-radius: 999px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
	}

	.cores-list, .mem-bars {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.core-card {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		padding: 0.45rem 0.6rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
	}

	.core-header-line {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.78rem;
		gap: 0.5rem;
	}

	.core-id-group {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.core-idx {
		font-weight: 700;
		color: var(--color-text-primary);
	}

	.core-details-text {
		font-size: 0.72rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono, monospace);
	}

	.core-percentage {
		font-weight: 700;
		font-family: var(--font-mono, monospace);
		font-size: 0.82rem;
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

	.mem-pct-txt {
		color: #2563eb;
	}
	:global(.dark) .mem-pct-txt {
		color: #60a5fa;
	}

	/* Multi-segment HTOP Progress bar */
	.htop-bar-track {
		height: 8px;
		background: var(--color-bg-tertiary);
		border-radius: 999px;
		overflow: hidden;
		display: flex;
		border: 1px solid var(--color-border);
	}

	.bar-seg {
		height: 100%;
		transition: width 0.3s ease;
	}

	.seg-user {
		background: #10b981;
	}
	.seg-sys {
		background: #f59e0b;
	}
	.seg-iowait {
		background: #ef4444;
	}

	.mem-used-seg {
		background: #3b82f6;
	}
	.mem-cached-seg {
		background: #06b6d4;
	}
	.swap-seg {
		background: #a855f7;
	}

	.loading-hint {
		font-size: 0.8rem;
		color: var(--color-text-muted);
		padding: 1rem;
		text-align: center;
	}

	@media (max-width: 900px) {
		.dashboard-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
