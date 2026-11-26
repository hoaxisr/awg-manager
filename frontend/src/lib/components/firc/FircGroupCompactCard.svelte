<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Badge, IconButton, StatusDot } from '$lib/components/ui';
	import { ArrowRight, RefreshCw, Zap, AlertTriangle } from 'lucide-svelte';
	import { formatProbeLatency, probeLatencyTone } from '$lib/utils/fircFallback';
	import {
		devicesBadgeText,
		formatCompactOutboundLabel,
		formatIfaceButtonText,
		getIfaceProbe,
		groupStatusDotVariant,
		ifaceDotState,
		ifaceLabel,
		isSpecialIface,
		matchingProxyGroup,
		normalizeIfaceList,
		primaryIface,
		resolveGroupActive,
		type FircGroupsRuntime,
		type FircProbeOptions,
	} from '$lib/utils/fircGroups';
	import type { FircGroup } from '$lib/types/firc';

	interface Props {
		group: FircGroup;
		orderStr: string;
		rt: FircGroupsRuntime;
		probingIfaces: Record<string, boolean>;
		probingGroups: Record<string, boolean>;
		selectMode: boolean;
		selected: boolean;
		dupCount?: number;
		dupKind?: 'shadowed' | 'split';
		ontoggleSelected: () => void;
		onprobe: (ids: string[], opts?: FircProbeOptions) => void;
		onopenRules: () => void;
		onopenSettings: () => void;
		onopenDuplicate?: () => void;
	}

	let {
		group,
		orderStr,
		rt,
		probingIfaces,
		probingGroups,
		selectMode,
		selected,
		dupCount = 0,
		dupKind = 'shadowed',
		ontoggleSelected,
		onprobe,
		onopenRules,
		onopenSettings,
		onopenDuplicate,
	}: Props = $props();

	let ifaceList = $derived(normalizeIfaceList(group.interface));
	let ifaceVal = $derived(primaryIface(group.interface));
	let isSpecial = $derived(isSpecialIface(ifaceVal));
	let hasCustomDevices = $derived(
		(group.devices?.allow?.length ?? 0) > 0 || (group.devices?.deny?.length ?? 0) > 0,
	);
	let activeRes = $derived(resolveGroupActive(rt, group));
	let activeIface = $derived(activeRes.active);
	let activeBackup = $derived(activeRes.isBackup ? activeIface : null);
	let isFallbackGroup = $derived(ifaceList.length > 1 && !isSpecialIface(ifaceList[0]));
	let matchedPg = $derived(matchingProxyGroup(rt.proxyGroups, group));
	let primaryProbe = $derived(
		!isSpecial && (activeIface || ifaceVal) ? getIfaceProbe(rt, activeIface || ifaceVal) : null,
	);
	let primaryProbeFailed = $derived(
		Boolean(!isSpecial && primaryProbe && !primaryProbe.ok && !(isFallbackGroup && activeRes.allDead)),
	);
</script>

<div class="compact-group-card">
	<div class="compact-group-heading">
		{#if selectMode}
			<input
				type="checkbox"
				class="bulk-check"
				checked={selected}
				aria-label={m.firc_bulk_groups_select_aria({ name: group.name })}
				onchange={ontoggleSelected}
			/>
		{/if}
		<span class="compact-priority" title={m.firc_groups_rule_priority()}>{orderStr}</span>
		<StatusDot variant={groupStatusDotVariant(rt.liveMap, group)} size="sm" />
		<strong>{group.name}</strong>
		{#if dupCount > 0}
			{@const dupTip =
				dupKind === 'shadowed'
					? m.firc_dup_compact_shadowed_tip({ count: dupCount })
					: m.firc_dup_compact_split_tip({ count: dupCount })}
			<button
				type="button"
				class="compact-dup-badge"
				class:is-split={dupKind === 'split'}
				data-overlap={dupKind}
				title={dupTip}
				aria-label={dupTip}
				onclick={() => (onopenDuplicate ? onopenDuplicate() : onopenRules())}
			>
				<AlertTriangle size={12} />
				<span>{dupCount}</span>
			</button>
		{/if}
		<Badge variant={group.enable ? 'muted' : 'warning'} size="xs">{group.enable ? m.firc_groups_enabled() : m.firc_groups_disabled()}</Badge>
	</div>
	{#if isFallbackGroup}
		{@const isGridTesting = Boolean(probingGroups[`grp:${group.id}`])}
		{@const targetId = activeIface || ifaceVal}
		{@const isTargetChecking = Boolean(targetId && probingIfaces[targetId])}
		<div class="compact-out-block">
			<div class="compact-group-output">
				<ArrowRight size={13} class="compact-out-arrow" />
				<span
					class="iface-dot"
					class:alive={Boolean(activeIface) && !activeBackup && !primaryProbeFailed}
					class:backup={Boolean(activeBackup) && !primaryProbeFailed}
					class:warn={Boolean(activeIface) && primaryProbeFailed && !activeRes.allDead}
					class:dead={activeRes.allDead}
					class:unknown={!activeIface && !activeRes.allDead}
				></span>
				<span class="compact-out-name">
					{formatCompactOutboundLabel(rt, group, activeIface, true, activeRes.allDead)}
				</span>
				{#if !activeRes.allDead && primaryProbe && targetId}
					<button
						type="button"
						class="compact-delay-btn {probeLatencyTone(primaryProbe)}"
						disabled={isTargetChecking}
						onclick={() => onprobe([targetId])}
						title={m.firc_fallback_test_iface_title({ name: ifaceLabel(rt, targetId) })}
					>
						{#if isTargetChecking}
							<RefreshCw size={10} class="spin" />
						{:else}
							{formatProbeLatency(primaryProbe)}
						{/if}
					</button>
				{/if}
				{#if activeBackup}
					<span class="backup-badge">{m.firc_fallback_backup_prefix()}</span>
				{:else}
					<span class="fallback-count-chip">+{ifaceList.length - 1}</span>
				{/if}
				<span class="compact-chain-probe">
					<IconButton
						disabled={isGridTesting}
						onclick={() =>
							onprobe(ifaceList, {
								groupKey: `grp:${group.id}`,
								url: group.check?.mode === 'url' && group.check.url ? group.check.url : undefined,
							})}
						title={m.firc_fallback_test_chain_title()}
						ariaLabel={m.firc_fallback_test_chain_title()}
					>
						<Zap size={10} class={isGridTesting ? 'spin' : ''} />
					</IconButton>
				</span>
			</div>
			<div
				class="compact-chain-sub"
				title={`${matchedPg ? `${matchedPg.name}: ` : ''}${ifaceList.map((id) => ifaceLabel(rt, id)).join(' → ')}`}
			>
				{matchedPg ? `${matchedPg.name}: ` : ''}{ifaceList
					.map((id) => {
						const p = getIfaceProbe(rt, id);
						return `${ifaceLabel(rt, id)}${p ? ` ${formatProbeLatency(p)}` : ''}`;
					})
					.join(' › ')}
			</div>
		</div>
	{:else}
		{@const singleTarget = !isSpecial ? (activeIface || ifaceVal) : ''}
		{@const isSingleChecking = Boolean(singleTarget && probingIfaces[singleTarget])}
		<div class="compact-group-output">
			<ArrowRight size={13} class="compact-out-arrow" />
			{#if !isSpecial && singleTarget}
				<span class="iface-dot {ifaceDotState(rt, group, singleTarget) ?? 'unknown'}"></span>
			{/if}
			<span class="compact-out-name">{formatIfaceButtonText(rt, group)}</span>
			{#if !isSpecial && primaryProbe && singleTarget}
				<button
					type="button"
					class="compact-delay-btn {probeLatencyTone(primaryProbe)}"
					disabled={isSingleChecking}
					onclick={() => onprobe([singleTarget])}
					title={m.firc_fallback_test_iface_title({ name: ifaceLabel(rt, singleTarget) })}
				>
					{#if isSingleChecking}
						<RefreshCw size={10} class="spin" />
					{:else}
						{formatProbeLatency(primaryProbe)}
					{/if}
				</button>
			{/if}
		</div>
	{/if}
	<span class="compact-group-meta">{group.list?.url
			? m.firc_groups_compact_meta_list({ own: group.rules.length, list: group.list.rulesTotal || group.list.rules || 0 })
			: m.firc_groups_compact_meta({ own: group.rules.length })}</span>
	{#if hasCustomDevices}<span class="compact-group-meta">{devicesBadgeText(group)}</span>{/if}
	<div class="compact-group-actions">
		<Button variant="secondary" size="sm" onclick={onopenRules}>{m.firc_groups_rules_and_output()}</Button>
		<Button variant="ghost" size="sm" onclick={onopenSettings}>{m.firc_groups_settings()}</Button>
	</div>
</div>

<style>
	.compact-group-card { display: flex; flex-direction: column; gap: .45rem; padding: .75rem .875rem; min-height: 138px; height: 100%; box-sizing: border-box; }

	.compact-group-heading { display: flex; flex-wrap: wrap; align-items: center; gap: .45rem; }

	.compact-group-heading strong { overflow-wrap: anywhere; flex: 1; min-width: 100px; }

	.compact-dup-badge {
		appearance: none;
		display: inline-flex;
		align-items: center;
		gap: 3px;
		padding: 1px 5px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-warning-border);
		background: var(--color-warning-tint);
		color: var(--color-warning);
		font-family: var(--font-mono);
		font-size: 10.5px;
		font-weight: 700;
		cursor: pointer;
		flex-shrink: 0;
	}

	.compact-dup-badge:hover {
		filter: brightness(1.12);
	}

	.compact-dup-badge.is-split {
		color: var(--text-secondary);
		background: var(--bg-tertiary);
		border-color: var(--border-hover, var(--border));
	}

	.compact-priority, .compact-group-meta { font-size: .75rem; color: var(--text-muted); }

	.compact-out-block { display: flex; flex-direction: column; gap: 2px; min-width: 0; }

	.compact-group-output { display: flex; align-items: center; gap: 5px; min-width: 0; font-size: .82rem; }

	.compact-group-output :global(.compact-out-arrow) { color: var(--text-muted); flex-shrink: 0; }

	.compact-out-name { font-weight: 600; color: var(--text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; flex: 0 1 auto; }

	.compact-delay-btn {
		appearance: none;
		display: inline-flex;
		align-items: center;
		font-family: var(--font-mono);
		font-size: 11px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		padding: 0 4px;
		border-radius: 4px;
		border: 1px solid transparent;
		background: transparent;
		cursor: pointer;
		flex-shrink: 0;
		transition: border-color var(--t-fast), background var(--t-fast);
	}

	.compact-delay-btn:hover:not(:disabled) { background: var(--bg-tertiary); border-color: var(--border); }

	.compact-delay-btn.good { color: var(--latency-color-ok); }

	.compact-delay-btn.medium { color: var(--latency-color-slow); }

	.compact-delay-btn.bad { color: var(--latency-color-fail); }

	.compact-delay-btn.none { color: var(--text-muted); }

	:global(.compact-chain-probe) { margin-left: auto; flex-shrink: 0; }

	.compact-chain-sub {
		font-size: 11px;
		color: var(--text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		padding-left: 18px;
		min-width: 0;
	}

	.compact-group-actions { display: flex; flex-wrap: wrap; gap: .4rem; margin-top: auto; }

	.bulk-check {
		width: 15px;
		height: 15px;
		accent-color: var(--color-accent);
		cursor: pointer;
		flex-shrink: 0;
	}

	.backup-badge,
	.fallback-count-chip {
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

	.fallback-count-chip {
		font-family: var(--font-mono);
		font-weight: 600;
		color: var(--text-muted);
		border: 1px dotted color-mix(in srgb, var(--text-muted) 45%, transparent);
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
</style>
