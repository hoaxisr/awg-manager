<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { Ban, ChevronDown, RefreshCw, Zap } from 'lucide-svelte';
	import { formatProbeLatency, probeLatencyTone, type FircProxyGroupRuntime } from '$lib/utils/fircFallback';
	import {
		MAX_GROUP_IFACES,
		applyProxyGroupToGroup,
		getIfaceProbe,
		getPickerGroups,
		ifaceDotState,
		ifaceDotTooltip,
		ifaceLabel,
		isSpecialIface,
		proxyGroupRuntime,
		setPrimaryIface,
		shortIfaceTag,
		toggleBackupIface,
		type FircGroupsRuntime,
		type FircProbeOptions,
		type PickerItem,
	} from '$lib/utils/fircGroups';
	import type { FircGroup, FircProxyGroup } from '$lib/types/firc';

	interface Props {
		group: FircGroup;
		rt: FircGroupsRuntime;
		ifaceList: string[];
		activeIface: string | null;
		isFallbackGroup: boolean;
		matchedPg: FircProxyGroup | undefined;
		probingIfaces: Record<string, boolean>;
		probingGroups: Record<string, boolean>;
		showOthers: boolean;
		ontoggleOthers: () => void;
		onprobe: (ids: string[], opts?: FircProbeOptions) => void;
		onopenProxyGroups: () => void;
		onclose: () => void;
	}

	let {
		group = $bindable(),
		rt,
		ifaceList,
		activeIface,
		isFallbackGroup,
		matchedPg,
		probingIfaces,
		probingGroups,
		showOthers,
		ontoggleOthers,
		onprobe,
		onopenProxyGroups,
		onclose,
	}: Props = $props();

	let pickerGroups = $derived(getPickerGroups(rt.interfaces, group));
	let canAddMoreIfaces = $derived(
		ifaceList.length > 0 && !isSpecialIface(ifaceList[0]) && ifaceList.length < MAX_GROUP_IFACES,
	);

	function pickPrimary(id: string) {
		if (setPrimaryIface(group, id)) onclose();
	}

	function toggleBackup(id: string, e: MouseEvent) {
		e.stopPropagation();
		toggleBackupIface(group, id);
	}

	function applyProxyGroup(pg: FircProxyGroup) {
		if (applyProxyGroupToGroup(group, pg)) onclose();
	}

	function proxyGroupTitle(pgRt: FircProxyGroupRuntime): string {
		if (!pgRt.activeIface) return m.firc_fallback_all_dead_title();
		const iface = `${ifaceLabel(rt, pgRt.activeIface)} (${pgRt.activeIface})`;
		return pgRt.isOnBackup
			? m.firc_fallback_active_backup_title({ iface })
			: m.firc_fallback_active_primary_title({ iface });
	}

	function anchorPopover(node: HTMLElement) {
		function updatePosition() {
			const root = node.closest('.iface-picker-root') as HTMLElement | null;
			if (!root || typeof window === 'undefined') return;
			const rect = root.getBoundingClientRect();
			const vh = window.innerHeight;
			const vw = window.innerWidth;
			const spaceBelow = Math.max(0, vh - rect.bottom - 14);
			const spaceAbove = Math.max(0, rect.top - 14);

			if (spaceBelow < 310 && spaceAbove > spaceBelow) {
				node.style.top = 'auto';
				node.style.bottom = 'calc(100% + 5px)';
				node.style.maxHeight = `${Math.min(350, Math.max(180, spaceAbove))}px`;
			} else {
				node.style.top = 'calc(100% + 5px)';
				node.style.bottom = 'auto';
				node.style.maxHeight = `${Math.min(350, Math.max(180, spaceBelow))}px`;
			}

			const popWidth = Math.min(310, Math.max(230, vw - 20));
			node.style.width = `${popWidth}px`;
			let leftOffset = 0;
			if (rect.left + popWidth > vw - 10) {
				leftOffset = vw - 10 - popWidth - rect.left;
			}
			if (rect.left + leftOffset < 10) {
				leftOffset = 10 - rect.left;
			}
			node.style.left = `${leftOffset}px`;
			node.style.right = 'auto';
		}

		updatePosition();
		const raf = requestAnimationFrame(() => {
			updatePosition();
			const primaryRow = node.querySelector('.iface-pop-row.is-primary') as HTMLElement | null;
			if (primaryRow) {
				primaryRow.scrollIntoView({ block: 'nearest' });
			}
		});
		window.addEventListener('resize', updatePosition);
		window.addEventListener('scroll', updatePosition, true);

		return {
			destroy() {
				cancelAnimationFrame(raf);
				window.removeEventListener('resize', updatePosition);
				window.removeEventListener('scroll', updatePosition, true);
			},
		};
	}
</script>

<!-- Строка интерфейса в пикере. rankTitle/dotTitle не заданы у «остальных» интерфейсов. -->
{#snippet ifaceRow(item: PickerItem, label: string, rankTitle: string | undefined, withDotTitle: boolean)}
	{@const rank = ifaceList.indexOf(item.id)}
	{@const dState = ifaceDotState(rt, group, item.id)}
	{@const isActMember = isFallbackGroup && activeIface === item.id}
	{@const rowProbe = getIfaceProbe(rt, item.id)}
	{@const rowChecking = Boolean(probingIfaces[item.id])}
	<div
		class="iface-pop-row"
		class:is-primary={rank === 0}
		class:is-backup={rank > 0}
	>
		<button
			type="button"
			class="iface-rank-btn"
			class:rank-primary={rank === 0}
			class:rank-backup={rank > 0}
			disabled={rank < 0 && !canAddMoreIfaces}
			onclick={(e) => toggleBackup(item.id, e)}
			title={rankTitle}
		>
			{rank >= 0 ? rank + 1 : ''}
		</button>
		<button
			type="button"
			class="iface-row-main"
			onclick={() => pickPrimary(item.id)}
		>
			{#if dState}
				<span
					class="iface-dot {dState}"
					class:active-pulse={isActMember}
					title={withDotTitle ? ifaceDotTooltip(rt, group, item.id) : undefined}
				></span>
			{/if}
			<span class="iface-row-text">{label}</span>
			{#if isActMember}
				<span class="pop-active-tag">{m.firc_fallback_active_tag()}</span>
			{/if}
		</button>
		<button
			type="button"
			class="pg-delay-btn compact {probeLatencyTone(rowProbe)}"
			disabled={rowChecking}
			onclick={(e) => {
				e.stopPropagation();
				void onprobe([item.id]);
			}}
			title={m.firc_fallback_test_iface_title({ name: item.label })}
		>
			<span class="pg-delay-val">{formatProbeLatency(rowProbe)}</span>
			{#if rowChecking}
				<RefreshCw size={9} class="spin" />
			{/if}
		</button>
	</div>
{/snippet}

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="iface-popover" use:anchorPopover onclick={(e) => e.stopPropagation()}>
	<div class="iface-pop-sec">
		<div class="iface-pop-cap pg-cap-row">
			<span>{m.firc_fallback_panel_title()}</span>
			<Button variant="ghost" size="sm" onclick={onopenProxyGroups}>
				{rt.proxyGroups.length > 0 ? m.firc_groups_pop_configure() : m.firc_groups_pop_create()}
			</Button>
		</div>
		{#if rt.proxyGroups.length === 0}
			<div class="pg-empty-hint">
				{m.firc_groups_pop_empty_hint()}
			</div>
		{:else}
			{#each rt.proxyGroups as pg (pg.id)}
				{@const isPgActive = matchedPg?.id === pg.id}
				{@const pgRt = proxyGroupRuntime(rt, pg)}
				{@const pgActProbe = pgRt.activeIface ? getIfaceProbe(rt, pgRt.activeIface) : null}
				<div class="iface-pop-row" class:is-primary={isPgActive}>
					<button
						type="button"
						class="iface-row-main"
						onclick={() => applyProxyGroup(pg)}
						title={proxyGroupTitle(pgRt)}
					>
						<span
							class="iface-dot {pgRt.activeIface ? (pgRt.isOnBackup ? 'backup' : 'alive') : pgRt.allDead ? 'dead' : 'unknown'}"
						></span>
						<span class="iface-row-text">
							<strong>{pg.name}</strong> — {pg.interfaces.map((id) => shortIfaceTag(rt.interfaces, id)).join(' → ')}
						</span>
					</button>
					{#if pgRt.activeIface}
						<span class="pop-pg-runtime">
							{ifaceLabel(rt, pgRt.activeIface)}{pgActProbe ? ` · ${formatProbeLatency(pgActProbe)}` : ''}
						</span>
					{/if}
				</div>
			{/each}
		{/if}
	</div>

	{#if pickerGroups.tunnels.length > 0}
		<div class="iface-pop-sec">
			<div class="iface-pop-cap">{m.firc_groups_opt_tunnels()}</div>
			{#each pickerGroups.tunnels as item (item.id)}
				{@const rank = ifaceList.indexOf(item.id)}
				{@render ifaceRow(
					item,
					item.label,
					rank === 0
						? m.firc_groups_rank_primary_tip()
						: rank > 0
							? m.firc_groups_rank_backup_tip({ rank: rank + 1 })
							: canAddMoreIfaces
								? m.firc_groups_rank_add_tip({ rank: ifaceList.length + 1 })
								: m.firc_groups_rank_max_chain_tip({ max: MAX_GROUP_IFACES }),
					true,
				)}
			{/each}
		</div>
	{/if}

	{#if pickerGroups.wans.length > 0}
		<div class="iface-pop-sec">
			<div class="iface-pop-cap">{m.firc_groups_pop_direct_cap()}</div>
			{#each pickerGroups.wans as item (item.id)}
				{@const rank = ifaceList.indexOf(item.id)}
				{@render ifaceRow(
					item,
					`Direct: ${item.label}`,
					rank === 0
						? m.firc_groups_rank_direct_primary_tip()
						: rank > 0
							? m.firc_groups_rank_direct_backup_tip({ rank: rank + 1 })
							: canAddMoreIfaces
								? m.firc_groups_rank_direct_add_tip({ rank: ifaceList.length + 1 })
								: m.firc_groups_rank_max_tip({ max: MAX_GROUP_IFACES }),
					true,
				)}
			{/each}
		</div>
	{/if}

	<div class="iface-pop-sec">
		<div class="iface-pop-cap">{m.firc_groups_opt_block()}</div>
		<button
			type="button"
			class="iface-special-row"
			class:is-primary={ifaceList[0] === 'blackhole'}
			onclick={() => pickPrimary('blackhole')}
		>
			<Ban size={13} class="special-ico" />
			<div class="special-txt">
				<span class="special-title">{m.firc_groups_block()}</span>
				<span class="special-sub">{m.firc_groups_pop_blackhole_sub()}</span>
			</div>
		</button>
		<button
			type="button"
			class="iface-special-row"
			class:is-primary={ifaceList[0] === 'block'}
			onclick={() => pickPrimary('block')}
		>
			<Ban size={13} class="special-ico" />
			<div class="special-txt">
				<span class="special-title">{m.firc_groups_block_dns()}</span>
				<span class="special-sub">0.0.0.0 / NXDOMAIN / REFUSED</span>
			</div>
		</button>
	</div>

	{#if pickerGroups.others.length > 0}
		<div class="iface-pop-sec">
			<button
				type="button"
				class="iface-more-toggle"
				onclick={ontoggleOthers}
			>
				<span>
					{showOthers
						? m.firc_groups_pop_hide_others()
						: m.firc_groups_pop_more_others({ count: pickerGroups.others.length })}
				</span>
				<ChevronDown
					size={12}
					class={showOthers ? 'rot-180' : ''}
				/>
			</button>
			{#if showOthers}
				{#each pickerGroups.others as item (item.id)}
					{@render ifaceRow(item, item.label, undefined, false)}
				{/each}
			{/if}
		</div>
	{/if}

	<div class="iface-pop-footer">
		{#if ifaceList.length > 1}
			{@const isPopTesting = Boolean(probingGroups[`grp:${group.id}`])}
			<div class="iface-pop-footer-row">
				<span>
					{#if activeIface}
						{m.firc_fallback_active_prefix()}: {ifaceLabel(rt, activeIface)} ·{' '}
					{/if}Fallback: <strong>{ifaceList.join(' → ')}</strong>
				</span>
				<Button
					variant="ghost"
					size="sm"
					disabled={isPopTesting}
					onclick={(e) => {
						e.stopPropagation();
						void onprobe(ifaceList, { groupKey: `grp:${group.id}` });
					}}
				>
					{#snippet iconBefore()}<Zap size={10} class={isPopTesting ? 'spin' : ''} />{/snippet}
					{m.firc_fallback_test()}
				</Button>
			</div>
		{:else}
			{m.firc_groups_pop_footer_hint()}
		{/if}
	</div>
</div>

<style>
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

	.iface-dot.backup {
		background: var(--color-warning);
		box-shadow: 0 0 4px var(--color-warning-border);
	}

	.iface-dot.active-pulse {
		background: var(--color-success);
		box-shadow: 0 0 6px var(--color-success-border);
		animation: firc-active-pulse 1.5s ease-in-out infinite;
	}

	.iface-dot.dead {
		background: var(--color-error);
		box-shadow: 0 0 4px var(--color-error-border);
	}

	.iface-dot.unknown {
		background: var(--text-muted);
		opacity: 0.55;
	}

	.pop-active-tag {
		font-size: 9.5px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		padding: 1px 5px;
		border-radius: 4px;
		background: var(--color-success-tint);
		color: var(--color-success);
		border: 1px solid var(--color-success-border);
		flex-shrink: 0;
	}

	.pop-pg-runtime {
		font-family: var(--font-mono);
		font-size: 10px;
		color: var(--color-success);
		white-space: nowrap;
		flex-shrink: 0;
	}

	.iface-pop-footer-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
	}

	@keyframes firc-active-pulse {
		0%,
		100% {
			opacity: 1;
			transform: scale(1);
		}
		50% {
			opacity: 0.45;
			transform: scale(0.85);
		}
	}

	.iface-popover {
		position: absolute;
		top: calc(100% + 5px);
		right: 0;
		z-index: 80;
		width: 330px;
		max-width: calc(100vw - 24px);
		box-sizing: border-box;
		max-height: 390px;
		overflow-y: auto;
		background: var(--bg-secondary);
		border: 1px solid var(--border-hover, var(--border));
		border-radius: var(--radius);
		box-shadow: var(--shadow);
		padding: 6px;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.iface-pop-sec {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.iface-pop-sec + .iface-pop-sec {
		border-top: 1px solid var(--border);
		padding-top: 5px;
	}

	.iface-pop-cap {
		font-size: 10px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
		padding: 2px 6px;
	}

	.iface-pop-row {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 3px 5px;
		border-radius: 5px;
		transition: background 0.12s ease;
	}

	.iface-pop-row:hover {
		background: var(--bg-tertiary);
	}

	.iface-pop-row.is-primary {
		background: var(--color-accent-tint);
	}

	.iface-pop-row.is-backup {
		background: var(--color-warning-tint);
	}

	.iface-rank-btn {
		appearance: none;
		width: 19px;
		height: 19px;
		border-radius: 50%;
		border: 1.5px solid var(--border-hover, var(--border));
		background: transparent;
		color: var(--text-secondary);
		font-family: var(--font-mono);
		font-size: 10.5px;
		font-weight: 700;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		cursor: pointer;
		flex-shrink: 0;
		padding: 0;
		transition:
			background 0.12s ease,
			border-color 0.12s ease,
			color 0.12s ease;
	}

	.iface-rank-btn:hover:not(:disabled) {
		border-color: var(--accent);
		color: var(--text-primary);
	}

	.iface-rank-btn.rank-primary {
		background: var(--accent);
		border-color: var(--accent);
		color: #fff;
	}

	.iface-rank-btn.rank-backup {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
		color: var(--color-warning);
	}

	.iface-rank-btn:disabled {
		opacity: 0.3;
		cursor: not-allowed;
	}

	.iface-row-main {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-primary);
		font-size: 12px;
		font-family: inherit;
		display: flex;
		align-items: center;
		gap: 7px;
		flex: 1;
		min-width: 0;
		text-align: left;
		cursor: pointer;
		padding: 2px 2px;
	}

	.iface-row-text {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.iface-special-row {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-primary);
		font-family: inherit;
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 5px 8px;
		border-radius: 5px;
		cursor: pointer;
		text-align: left;
		width: 100%;
	}

	.iface-special-row:hover {
		background: var(--bg-tertiary);
	}

	.iface-special-row.is-primary {
		background: var(--color-error-tint);
		color: var(--color-error);
	}

	.iface-special-row :global(.special-ico) {
		color: var(--color-error);
		flex-shrink: 0;
	}

	.special-txt {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}

	.special-title {
		font-size: 12px;
		font-weight: 600;
	}

	.special-sub {
		font-size: 10.5px;
		color: var(--text-muted);
	}

	.iface-more-toggle {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-muted);
		font-size: 11px;
		font-family: inherit;
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 4px 6px;
		cursor: pointer;
		border-radius: 4px;
	}

	.iface-more-toggle:hover {
		color: var(--text-primary);
		background: var(--bg-tertiary);
	}

	.iface-more-toggle :global(.rot-180) {
		transform: rotate(180deg);
	}

	.iface-pop-footer {
		border-top: 1px solid var(--border);
		padding: 5px 6px 2px;
		font-size: 10.5px;
		color: var(--text-muted);
		line-height: 1.35;
	}

	.pg-delay-btn {
		appearance: none;
		display: inline-flex;
		align-items: center;
		gap: 3px;
		padding: 1px 5px;
		border-radius: 4px;
		border: 1px solid transparent;
		background: transparent;
		font-family: var(--font-mono);
		font-size: 10.5px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		line-height: 1.2;
		cursor: pointer;
		flex-shrink: 0;
		transition: background var(--t-fast), border-color var(--t-fast);
	}

	.pg-delay-btn.compact {
		font-size: 10px;
		padding: 1px 4px;
	}

	.pg-delay-btn:hover:not(:disabled) {
		background: var(--bg-tertiary);
		border-color: var(--border);
	}

	.pg-delay-btn.good {
		color: var(--latency-color-ok);
	}

	.pg-delay-btn.medium {
		color: var(--latency-color-slow);
	}

	.pg-delay-btn.bad {
		color: var(--latency-color-fail);
	}

	.pg-delay-btn.none {
		color: var(--text-muted);
	}

	.pg-delay-val {
		white-space: nowrap;
	}

	.pg-cap-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.pg-empty-hint {
		padding: 4px 8px 6px;
		font-size: 11px;
		color: var(--text-muted);
		line-height: 1.35;
	}
</style>
