<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, IconButton } from '$lib/components/ui';
	import { RefreshCw, Settings, Shield, Zap } from 'lucide-svelte';
	import { formatProbeLatency, probeLatencyTone } from '$lib/utils/fircFallback';
	import {
		allFallbackInterfaceIds,
		proxyGroupRuntime,
		type FircGroupsRuntime,
		type FircProbeOptions,
	} from '$lib/utils/fircGroups';

	interface Props {
		rt: FircGroupsRuntime;
		probingIfaces: Record<string, boolean>;
		probingGroups: Record<string, boolean>;
		onprobe: (ids: string[], opts?: FircProbeOptions) => void;
		onconfigure: () => void;
	}

	let { rt, probingIfaces, probingGroups, onprobe, onconfigure }: Props = $props();

	let allFallbackIds = $derived(allFallbackInterfaceIds(rt.proxyGroups, rt.groups));
	let singlePg = $derived(rt.proxyGroups.length === 1);
</script>

<div class="pg-panel">
	<div class="pg-panel-head">
		<div class="pg-panel-title">
			<Shield size={13} />
			<span>{m.firc_fallback_panel_title()}</span>
		</div>
		<div class="pg-panel-actions">
			{#if allFallbackIds.length > 0}
				<Button
					variant="secondary"
					size="sm"
					disabled={Boolean(probingGroups['__all_pg__'])}
					onclick={() => onprobe(allFallbackIds, { groupKey: '__all_pg__' })}
					title={m.firc_fallback_test_all_title()}
				>
					{#snippet iconBefore()}<Zap size={12} class={probingGroups['__all_pg__'] ? 'spin' : ''} />{/snippet}
					{probingGroups['__all_pg__']
						? m.firc_fallback_testing()
						: singlePg
							? m.firc_fallback_test()
							: m.firc_fallback_test_all()}
				</Button>
			{/if}
			<Button
				variant="ghost"
				size="sm"
				onclick={onconfigure}
				title={m.firc_fallback_configure_chain()}
			>
				{#snippet iconBefore()}<Settings size={12} />{/snippet}
				{m.firc_fallback_edit()}
			</Button>
		</div>
	</div>

	<div class="pg-rows">
		{#each rt.proxyGroups as pg (pg.id)}
			{@const pgRt = proxyGroupRuntime(rt, pg)}
			{@const isPgTesting = Boolean(probingGroups[`pg:${pg.id}`])}
			{@const activeMem = pgRt.activeIface ? pgRt.members.find((x) => x.id === pgRt.activeIface) : undefined}
			{@const activeProbeFailed = Boolean(activeMem?.probe && !activeMem.probe.ok)}
			<div class="pg-row" class:is-backup={pgRt.isOnBackup} class:all-dead={pgRt.allDead}>
				<div
					class="pg-row-ident"
					title={pgRt.hasDivergentActive && pgRt.divergentSummary ? `${pg.name} (${pgRt.divergentSummary})` : pg.name}
				>
					<span
						class="iface-dot"
						class:alive={Boolean(pgRt.activeIface) && !pgRt.isOnBackup && !activeProbeFailed}
						class:backup={pgRt.isOnBackup && !activeProbeFailed}
						class:warn={Boolean(pgRt.activeIface) && activeProbeFailed && !pgRt.allDead}
						class:dead={pgRt.allDead}
						class:unknown={!pgRt.activeIface && !pgRt.allDead}
					></span>
					<strong class="pg-row-name">{pg.name}</strong>
					{#if pgRt.isOnBackup && pgRt.activeIface}
						<span
							class="backup-badge"
							title={m.firc_fallback_active_backup_title({ iface: pgRt.activeIface })}
						>
							{m.firc_fallback_backup_prefix()}
						</span>
					{:else if pgRt.allDead}
						<span class="dead-badge" title={m.firc_fallback_all_dead_title()}>
							{m.firc_fallback_unavailable()}
						</span>
					{/if}
				</div>

				<div class="pg-chain-inline">
					{#each pgRt.members as mem, mIdx (mem.id)}
						{@const tone = probeLatencyTone(mem.probe)}
						{@const checking = Boolean(probingIfaces[mem.id])}
						{@const probeFailed = Boolean(mem.probe && !mem.probe.ok)}
						{@const isWarnActive = mem.state !== 'dead' && mem.isActive && probeFailed}
						{#if mIdx > 0}
							<span class="pg-chain-sep" aria-hidden="true">›</span>
						{/if}
						<button
							type="button"
							class="pg-node-pill"
							class:is-active={mem.isActive && !pgRt.isOnBackup && !isWarnActive}
							class:is-backup={mem.isActive && pgRt.isOnBackup && !isWarnActive}
							class:is-probe-warn={isWarnActive}
							class:is-dead={mem.state === 'dead'}
							disabled={checking}
							onclick={() => onprobe([mem.id])}
							title={[
								mem.id,
								mem.isActive ? m.firc_fallback_active_marker() : '',
								mem.error,
								probeFailed
									? m.firc_fallback_probe_error_detail({
											error: mem.probe?.error || m.firc_fallback_timeout(),
										})
									: '',
								m.firc_fallback_test_iface_title({ name: mem.label }),
							]
								.filter(Boolean)
								.join(' · ')}
						>
							<span
								class="pg-mem-dot"
								class:active={mem.isActive && !pgRt.isOnBackup && !probeFailed}
								class:backup={mem.isActive && pgRt.isOnBackup && !probeFailed}
								class:warn={mem.state !== 'dead' && probeFailed}
								class:alive={!mem.isActive && mem.state === 'alive' && !probeFailed}
								class:dead={mem.state === 'dead'}
								class:unknown={!mem.isActive && mem.state === 'unknown' && !probeFailed}
							></span>
							<span class="pg-node-name">{mem.label}</span>
							<span class="pg-node-delay {tone}">
								{#if checking}
									<RefreshCw size={10} class="spin" />
								{:else}
									{formatProbeLatency(mem.probe)}
								{/if}
							</span>
						</button>
					{/each}
				</div>

				{#if !singlePg}
					<IconButton
						disabled={isPgTesting || pg.interfaces.length === 0}
						onclick={() =>
							onprobe(pg.interfaces, {
								groupKey: `pg:${pg.id}`,
								url: pg.check?.mode === 'url' && pg.check.url ? pg.check.url : undefined,
							})}
						title={m.firc_fallback_test_group_title({ name: pg.name })}
						ariaLabel={m.firc_fallback_test_group_title({ name: pg.name })}
					>
						<Zap size={11} class={isPgTesting ? 'spin' : ''} />
					</IconButton>
				{/if}
			</div>
		{/each}
	</div>
</div>

<style>
	.backup-badge,
	.dead-badge {
		font-size: 10px;
		padding: 0 5px;
		line-height: 16px;
		border-radius: 4px;
		white-space: nowrap;
		font-variant-numeric: tabular-nums;
		flex-shrink: 0;
	}

	.backup-badge {
		background: var(--color-warning-tint);
		color: var(--color-warning);
		border: 1px solid var(--color-warning-border);
	}

	.dead-badge {
		background: var(--color-error-tint);
		color: var(--color-error);
		border: 1px solid var(--color-error-border);
	}

	.iface-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
		display: inline-block;
	}

	.iface-dot.alive {
		background: var(--color-success);
		box-shadow: 0 0 4px var(--color-success-border);
	}

	.iface-dot.backup,
	.iface-dot.warn {
		background: var(--color-warning);
		box-shadow: 0 0 4px var(--color-warning-border);
	}

	.iface-dot.dead {
		background: var(--color-error);
		box-shadow: 0 0 4px var(--color-error-border);
	}

	.iface-dot.unknown {
		background: var(--text-muted);
		opacity: 0.55;
	}

	.pg-panel {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px 10px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius);
	}

	.pg-panel-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 6px;
	}

	.pg-panel-title {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		font-weight: 700;
		color: var(--text-primary);
		min-width: 0;
	}

	.pg-panel-actions {
		display: flex;
		align-items: center;
		gap: 5px;
		flex-wrap: wrap;
	}

	.pg-rows {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.pg-row {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px 10px;
		padding: 5px 9px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		min-width: 0;
	}

	.pg-row.is-backup {
		border-color: var(--color-warning-border);
	}

	.pg-row.all-dead {
		border-color: var(--color-error-border);
	}

	.pg-row-ident {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		flex-shrink: 0;
		max-width: 100%;
	}

	.pg-row-name {
		font-size: 12px;
		font-weight: 700;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 210px;
	}

	.pg-chain-inline {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 4px;
		flex: 1 1 auto;
		min-width: 0;
	}

	.pg-chain-sep {
		font-size: 11px;
		color: var(--text-muted);
		user-select: none;
	}

	.pg-node-pill {
		appearance: none;
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 2px 7px;
		min-height: 22px;
		border-radius: 5px;
		border: 1px solid var(--border);
		background: var(--bg-secondary);
		color: var(--text-secondary);
		font-family: inherit;
		font-size: 11.5px;
		cursor: pointer;
		max-width: 100%;
		min-width: 0;
		transition: border-color var(--t-fast), background var(--t-fast), color var(--t-fast);
	}

	.pg-node-pill:hover:not(:disabled) {
		border-color: var(--color-accent);
		color: var(--text-primary);
	}

	.pg-node-pill:disabled {
		opacity: 0.65;
		cursor: wait;
	}

	.pg-node-pill.is-active {
		background: color-mix(in srgb, var(--color-success) 10%, var(--bg-secondary));
		border-color: var(--color-success-border);
		color: var(--text-primary);
		font-weight: 600;
	}

	.pg-node-pill.is-backup,
	.pg-node-pill.is-probe-warn {
		background: color-mix(in srgb, var(--color-warning) 10%, var(--bg-secondary));
		border-color: var(--color-warning-border);
		color: var(--text-primary);
		font-weight: 600;
	}

	.pg-node-pill.is-dead {
		opacity: 0.72;
		border-color: var(--color-error-border);
	}

	.pg-node-name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 150px;
	}

	.pg-node-delay {
		display: inline-flex;
		align-items: center;
		font-family: var(--font-mono);
		font-size: 10.5px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
		flex-shrink: 0;
	}

	.pg-node-delay.good {
		color: var(--latency-color-ok);
	}

	.pg-node-delay.medium {
		color: var(--latency-color-slow);
	}

	.pg-node-delay.bad {
		color: var(--latency-color-fail);
	}

	.pg-node-delay.none {
		color: var(--text-muted);
	}

	.pg-mem-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.pg-mem-dot.active {
		background: var(--color-success);
		box-shadow: 0 0 6px var(--color-success-border);
		animation: firc-active-pulse 1.5s ease-in-out infinite;
	}

	.pg-mem-dot.backup,
	.pg-mem-dot.warn {
		background: var(--color-warning);
		box-shadow: 0 0 6px var(--color-warning-border);
	}

	.pg-mem-dot.alive {
		background: var(--color-success);
		opacity: 0.55;
	}

	.pg-mem-dot.dead {
		background: var(--color-error);
	}

	.pg-mem-dot.unknown {
		background: var(--text-muted);
		opacity: 0.55;
	}
</style>
