<script lang="ts">
	import { m } from '$lib/i18n';
	import { ArrowRight, Ban, ChevronDown, Globe, Shield } from 'lucide-svelte';
	import { formatProbeLatency, probeLatencyTone } from '$lib/utils/fircFallback';
	import {
		formatCompactOutboundLabel,
		getIfaceProbe,
		ifaceDotState,
		ifaceLabel,
		isSpecialIface,
		matchingProxyGroup,
		normalizeIfaceList,
		outboundToneClass,
		primaryIface,
		resolveGroupActive,
		resolverBadgeText,
		type FircGroupsRuntime,
		type FircProbeOptions,
	} from '$lib/utils/fircGroups';
	import type { FircBlockAnswer, FircGroup } from '$lib/types/firc';
	import FircIfacePopover from './FircIfacePopover.svelte';

	interface Props {
		group: FircGroup;
		rt: FircGroupsRuntime;
		probingIfaces: Record<string, boolean>;
		probingGroups: Record<string, boolean>;
		pickerOpen: boolean;
		showOthers: boolean;
		ontogglePicker: () => void;
		onclosePicker: () => void;
		ontoggleOthers: () => void;
		onprobe: (ids: string[], opts?: FircProbeOptions) => void;
		onopenProxyGroups: () => void;
	}

	let {
		group = $bindable(),
		rt,
		probingIfaces,
		probingGroups,
		pickerOpen,
		showOthers,
		ontogglePicker,
		onclosePicker,
		ontoggleOthers,
		onprobe,
		onopenProxyGroups,
	}: Props = $props();

	let ifaceList = $derived(normalizeIfaceList(group.interface));
	let ifaceVal = $derived(primaryIface(group.interface));
	let isBlock = $derived(ifaceVal === 'block');
	let isBlackhole = $derived(ifaceVal === 'blackhole');
	let activeRes = $derived(resolveGroupActive(rt, group));
	let activeIface = $derived(activeRes.active);
	let activeBackup = $derived(activeRes.isBackup ? activeIface : null);
	let isFallbackGroup = $derived(ifaceList.length > 1 && !isSpecialIface(ifaceList[0]));
	let toneCls = $derived(outboundToneClass(activeIface || ifaceVal));
	let matchedPg = $derived(matchingProxyGroup(rt.proxyGroups, group));
	let primaryProbe = $derived(
		!isBlock && !isBlackhole && (activeIface || ifaceVal) ? getIfaceProbe(rt, activeIface || ifaceVal) : null,
	);
	let primaryProbeFailed = $derived(
		Boolean(!isBlock && !isBlackhole && primaryProbe && !primaryProbe.ok && !(isFallbackGroup && activeRes.allDead)),
	);
	let chainLabel = $derived(ifaceList.map((id) => ifaceLabel(rt, id)).join(' → '));
</script>

<div class="outbound-select-wrap">
	<ArrowRight size={14} class="outbound-arrow" />
	<div class="iface-picker-root">
		<button
			type="button"
			class="tone-select-pill iface-picker-btn {toneCls}"
			class:on-backup={Boolean(activeBackup)}
			class:probe-warn={primaryProbeFailed}
			class:all-dead={isFallbackGroup && activeRes.allDead}
			onclick={(e) => {
				e.stopPropagation();
				ontogglePicker();
			}}
			title={[
				activeBackup
					? m.firc_groups_backup_active_tip({
							backup: ifaceLabel(rt, activeBackup),
							primary: ifaceLabel(rt, ifaceList[0]),
						})
					: isFallbackGroup && activeIface
						? m.firc_fallback_active_chain_title({
								iface: ifaceLabel(rt, activeIface),
								chain: matchedPg ? `${matchedPg.name}: ${chainLabel}` : chainLabel,
							})
						: ifaceList.length > 1
							? m.firc_groups_chain_tip({ chain: ifaceList.join(' → ') })
							: resolverBadgeText(rt, group),
				primaryProbeFailed
					? m.firc_fallback_probe_error_detail({
							error: primaryProbe?.error || m.firc_fallback_timeout(),
						})
					: '',
				primaryProbeFailed && isFallbackGroup && (!group.check || group.check.mode === 'none')
					? m.firc_fallback_check_disabled_hint()
					: '',
			]
				.filter(Boolean)
				.join(' · ')}
		>
			{#if isBlock || isBlackhole}
				<Ban size={13} />
			{:else}
				{@const rawDotState = activeRes.allDead && isFallbackGroup ? 'dead' : ifaceDotState(rt, group, activeIface || ifaceVal)}
				{@const dotState = primaryProbeFailed && rawDotState && rawDotState !== 'dead' ? 'warn' : rawDotState}
				{#if dotState}
					<span class="iface-dot {dotState}" class:backup={Boolean(activeBackup) && !primaryProbeFailed}></span>
				{:else if toneCls === 'tone-awg'}
					<Shield size={13} />
				{:else}
					<Globe size={13} />
				{/if}
			{/if}
			<span class="iface-btn-label">
				{formatCompactOutboundLabel(rt, group, activeIface, isFallbackGroup, activeRes.allDead)}
			</span>
			{#if !isBlock && !isBlackhole && primaryProbe && !(isFallbackGroup && activeRes.allDead)}
				<span class="pill-delay-tag {probeLatencyTone(primaryProbe)}">
					{formatProbeLatency(primaryProbe)}
				</span>
			{/if}
			{#if isFallbackGroup}
				{#if activeBackup}
					<span class="backup-badge">{m.firc_fallback_backup_prefix()}</span>
				{:else}
					<span class="fallback-count-chip">+{ifaceList.length - 1}</span>
				{/if}
			{/if}
			<ChevronDown size={12} class="iface-chev" />
		</button>

		{#if pickerOpen}
			<FircIfacePopover
				bind:group
				{rt}
				{ifaceList}
				{activeIface}
				{isFallbackGroup}
				{matchedPg}
				{probingIfaces}
				{probingGroups}
				{showOthers}
				{ontoggleOthers}
				{onprobe}
				{onopenProxyGroups}
				onclose={onclosePicker}
			/>
		{/if}
	</div>

	{#if isBlock}
		<select
			class="inp block-mode-sel"
			value={group.block ?? 'null'}
			onchange={(e) => {
				group.block = (e.currentTarget as HTMLSelectElement)
					.value as FircBlockAnswer;
			}}
			title={m.firc_groups_block_mode_tip()}
		>
			<option value="null">0.0.0.0 / ::</option>
			<option value="nxdomain">NXDOMAIN</option>
			<option value="refused">REFUSED</option>
		</select>
	{/if}
</div>

<style>
	:global(html[data-layout-compact='true']) .iface-btn-label {
		max-width: 120px;
	}

	.outbound-select-wrap {
		display: flex;
		align-items: center;
		gap: 5px;
	}

	.outbound-select-wrap :global(.outbound-arrow) {
		color: var(--text-muted);
		flex-shrink: 0;
	}

	.iface-picker-root {
		position: relative;
		display: inline-flex;
	}

	.tone-select-pill {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 2px 9px;
		border-radius: 6px;
		border: 1px solid var(--border);
		font-size: 11.5px;
		font-weight: 600;
		height: 27px;
		box-sizing: border-box;
		cursor: pointer;
		font-family: inherit;
		transition:
			border-color var(--t-fast),
			background var(--t-fast);
	}

	.tone-select-pill:hover {
		filter: brightness(1.08);
	}

	.tone-select-pill.on-backup,
	.tone-select-pill.probe-warn {
		border-color: var(--color-warning-border) !important;
	}

	.tone-select-pill.all-dead {
		border-color: var(--color-error-border) !important;
	}

	.iface-btn-label {
		max-width: 145px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
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

	.pill-delay-tag {
		font-family: var(--font-mono);
		font-size: 10.5px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
		flex-shrink: 0;
	}

	.pill-delay-tag.good {
		color: var(--latency-color-ok);
	}

	.pill-delay-tag.medium {
		color: var(--latency-color-slow);
	}

	.pill-delay-tag.bad {
		color: var(--latency-color-fail);
	}

	.pill-delay-tag.none {
		color: var(--text-muted);
	}

	.iface-picker-btn :global(.iface-chev) {
		opacity: 0.7;
		flex-shrink: 0;
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

	.tone-select-pill.tone-subscription {
		color: var(--color-info);
		background: var(--color-info-tint);
		border-color: var(--color-info-border);
	}

	.tone-select-pill.tone-awg {
		color: var(--color-accent);
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
	}

	.tone-select-pill.tone-proxy {
		color: var(--color-info);
		background: var(--color-info-tint);
		border-color: var(--color-info-border);
	}

	.tone-select-pill.tone-composite {
		color: var(--color-accent);
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
	}

	.tone-select-pill.tone-block {
		color: var(--color-error);
		background: var(--color-error-tint);
		border-color: var(--color-error-border);
	}

	.block-mode-sel {
		width: auto;
		max-width: 105px;
		padding: 2px 6px;
		height: 26px;
		font-size: 11px;
	}

	.inp {
		padding: 5px 9px;
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		color: var(--text-primary);
		font-size: 12.5px;
		font-family: inherit;
	}

	.inp:focus {
		outline: none;
		border-color: var(--accent);
	}

	@media (max-width: 768px) {
		.outbound-select-wrap,
		.iface-picker-root {
			max-width: 100%;
			min-width: 0;
		}
		.tone-select-pill {
			max-width: 100%;
			height: auto;
			min-height: 27px;
			flex-wrap: wrap;
			padding: 3px 8px;
		}
		.iface-btn-label {
			max-width: 150px;
		}
	}
</style>
