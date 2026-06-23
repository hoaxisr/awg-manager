<!-- frontend/src/lib/components/pingcheck/WatchdogCard.svelte -->
<script lang="ts">
	import { StatusDot, VersionBadge, Badge, Button, type StatusDotVariant, type BadgeVariant } from '$lib/components/ui';
	import { TunnelDelaySparkBars } from '$lib/components/tunnels';
	import { Settings, Power, PowerOff, Info, Check, X, RotateCcw } from 'lucide-svelte';
	import { formatTime } from '$lib/utils/format';
	import type { CardStats } from '$lib/utils/pingStats';

	interface Props {
		name: string;
		backend: 'kernel' | 'nativewg';
		awgVersion?: string;
		statusKind: 'alive' | 'recovering' | 'disabled' | 'stopped';
		hasPingcheck: boolean;
		isWatchdog: boolean;
		configLine: string;
		stats: CardStats | null;
		onConfigure: () => void;
		onCheckNow: () => void;
		onDisable: () => void;
	}

	let { name, backend, awgVersion, statusKind, hasPingcheck, isWatchdog, configLine, stats, onConfigure, onCheckNow, onDisable }: Props =
		$props();

	const STATUS: Record<Props['statusKind'], { dot: StatusDotVariant; pulse: boolean; label: string; badge: BadgeVariant }> = {
		alive: { dot: 'success', pulse: false, label: 'активен', badge: 'success' },
		recovering: { dot: 'warning', pulse: true, label: 'восстановление', badge: 'warning' },
		disabled: { dot: 'muted', pulse: false, label: 'выключен', badge: 'muted' },
		stopped: { dot: 'muted', pulse: false, label: 'остановлен', badge: 'muted' },
	};
	const st = $derived(STATUS[statusKind]);
	const fmt = (v: number | null) => (v === null ? '—' : `${v}ms`);
</script>

<div class="wd-card" class:recovering={statusKind === 'recovering'}>
	<!-- Header -->
	<div class="wd-head">
		<div class="wd-head-title">
			<StatusDot variant={st.dot} pulse={st.pulse} size="sm" />
			<span class="wd-name">{name}</span>
		</div>
		<div class="wd-head-meta">
			<VersionBadge kind="backend" value={backend} />
			{#if awgVersion}<VersionBadge kind="awg" value={awgVersion} />{/if}
			{#if isWatchdog}<Badge variant="accent" mono>watchdog</Badge>{/if}
			<span class="wd-status"><Badge variant={st.badge}>{st.label}</Badge></span>
		</div>
	</div>

	{#if hasPingcheck && stats}
		<!-- Config line -->
		<div class="wd-config">{configLine}</div>

		<!-- Stats grid -->
		<div class="wd-stats">
			<div class="wd-stat"><span class="v">{fmt(stats.avgMs)}</span><span class="k">avg</span></div>
			<div class="wd-stat"><span class="v">{stats.failsLabel}</span><span class="k">сбои</span></div>
			<div class="wd-stat"><span class="v"><RotateCcw size={13} />{stats.restarts}</span><span class="k">рестарты</span></div>
			<div class="wd-stat"><span class="v" class:loss={stats.lossPct > 0}>{stats.lossPct}%</span><span class="k">loss</span></div>
		</div>

		<!-- Last checks -->
		<div class="wd-checks">
			<div class="wd-checks-head">
				<span class="wd-checks-title">Последние проверки</span>
				<span class="wd-minmax" title="последние {stats.history.length} проверок">
					min {fmt(stats.minMs)} · max {fmt(stats.maxMs)}
				</span>
			</div>
			<div class="wd-bars">
				<TunnelDelaySparkBars history={stats.history} state="ok" maxBars={12} colorPerBar title="Проверить сейчас" onclick={onCheckNow} />
			</div>
			<div class="wd-log">
				{#each stats.recent as e (e.timestamp + e.tunnelId)}
					<div class="wd-log-row">
						<span class="ts">{formatTime(e.timestamp)}</span>
						<span class="ico" class:ok={e.success} class:bad={!e.success}>
							{#if e.success}<Check size={13} />{:else}<X size={13} />{/if}
						</span>
						<span class="lat">{e.success ? `${e.latency}ms` : '—'}</span>
						<span class="note">{e.error}</span>
					</div>
				{/each}
			</div>
		</div>

		<!-- Footer -->
		<div class="wd-foot">
			<Button variant="outline-danger" size="sm" onclick={onDisable}>
				{#snippet iconBefore()}<PowerOff size={14} />{/snippet}
				Выключить
			</Button>
			<Button variant="outline-primary" size="sm" onclick={onConfigure}>
				{#snippet iconBefore()}<Settings size={14} />{/snippet}
				Настроить
			</Button>
		</div>
	{:else}
		<!-- No pingcheck -->
		<div class="wd-note">
			<span class="wd-note-text"><Info size={14} /> Pingcheck/watchdog не настроен для этого туннеля.</span>
		</div>
		<div class="wd-foot">
			<Button variant="outline-primary" size="sm" onclick={onConfigure}>
				{#snippet iconBefore()}<Power size={14} />{/snippet}
				Включить
			</Button>
		</div>
	{/if}
</div>

<style>
	.wd-card {
		display: flex;
		flex-direction: column;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 12px;
		overflow: hidden;
		/* Make the card a size container so the header can react to the CARD's
		   width, not the viewport — cards sit in a 2-col grid, so a viewport
		   media-query is the wrong signal (a 1000px viewport gives ~470px cards). */
		container-type: inline-size;
	}
	.wd-card.recovering {
		border-color: var(--color-warning-border);
	}

	/* Header */
	.wd-head {
		display: flex;
		align-items: center;
		gap: 9px;
		padding: 12px 14px;
		border-bottom: 1px solid var(--color-border);
	}
	.wd-head-title {
		display: flex;
		align-items: center;
		gap: 9px;
		min-width: 0;
	}
	.wd-name {
		font-weight: 600;
		font-size: 14px;
		color: var(--color-text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.wd-head-meta {
		display: flex;
		align-items: center;
		gap: 6px;
		flex: 1;
		min-width: 0;
		flex-wrap: wrap;
	}
	.wd-status { margin-left: auto; }

	/* Stack name (row 1) above badges+status (row 2) once the card is too
	   narrow for a comfortable single row. Threshold 460px = measured worst-case
	   header width (~453px: longest name + NativeWG + AWG version + watchdog +
	   «восстановление») + small headroom. */
	@container (max-width: 460px) {
		.wd-head {
			flex-direction: column;
			align-items: stretch;
			gap: 8px;
		}
	}

	/* Config line */
	.wd-config {
		padding: 8px 14px;
		font-family: var(--font-mono);
		font-size: 11px;
		line-height: 1.6;
		color: var(--color-text-muted);
		border-bottom: 1px solid var(--color-border);
	}

	/* Stats grid */
	.wd-stats {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		border-bottom: 1px solid var(--color-border);
	}
	.wd-stat {
		padding: 9px 6px;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 2px;
		border-right: 1px solid color-mix(in srgb, var(--color-border) 55%, transparent);
	}
	.wd-stat:last-child { border-right: none; }
	.wd-stat .v {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 3px;
		font-family: var(--font-mono);
		font-size: 15px;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.wd-stat .v.loss { color: var(--color-error); }
	.wd-stat .k {
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
	}

	/* Last checks */
	.wd-checks { padding: 12px 14px; flex: 1; }
	.wd-checks-head {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		margin-bottom: 6px;
	}
	.wd-checks-title {
		font-size: 10px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}
	.wd-minmax {
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--color-text-muted);
	}
	.wd-bars {
		display: flex;
		align-items: flex-end;
		height: 38px;
		margin-bottom: 12px;
		padding: 2px;
		background: color-mix(in srgb, var(--color-border) 22%, transparent);
		border-radius: 4px;
	}
	.wd-bars :global(.tunnel-delay-spark) {
		flex: 1;
		height: 100%;
		gap: 2px;
	}
	.wd-log { display: flex; flex-direction: column; }
	.wd-log-row {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 4px 0;
		font-family: var(--font-mono);
		font-size: 11px;
		border-top: 1px solid color-mix(in srgb, var(--color-border) 45%, transparent);
	}
	.wd-log-row:first-child { border-top: none; }
	.wd-log-row .ts { color: var(--color-text-muted); min-width: 56px; }
	.wd-log-row .ico { display: inline-flex; align-items: center; }
	.wd-log-row .ico.ok { color: var(--color-success); }
	.wd-log-row .ico.bad { color: var(--color-error); }
	.wd-log-row .lat { color: var(--color-text-primary); min-width: 52px; }
	.wd-log-row .note { color: var(--color-text-muted); font-style: italic; }

	/* Footer */
	.wd-foot {
		padding: 10px 14px;
		border-top: 1px solid var(--color-border);
		display: flex;
		justify-content: flex-end;
		gap: 8px;
	}

	/* No-pingcheck note */
	.wd-note {
		padding: 16px 14px;
		flex: 1;
		font-size: 12px;
		color: var(--color-text-muted);
	}
	.wd-note-text { display: inline-flex; align-items: center; gap: 6px; }
</style>
