<script lang="ts">
	import { m } from '$lib/i18n';
	import { StatusDot, Toggle } from '$lib/components/ui';
	import {
		Plus,
		Trash2,
		RefreshCw,
		Settings,
		ChevronDown,
		ChevronUp,
		ArrowUp,
		ArrowDown,
		Download,
		Monitor,
		Link2,
		X,
		Copy,
	} from 'lucide-svelte';
	import ServiceTile from '$lib/components/sb-router/ServiceTile.svelte';
	import {
		devicesBadgeText,
		formatTimestamp,
		groupStatusDotVariant,
		groupStatusTooltip,
		intervalOptions,
		resolverBadgeText,
		type FircGroupsRuntime,
		type FircProbeOptions,
	} from '$lib/utils/fircGroups';
	import type { FircGroup } from '$lib/types/firc';
	import FircGroupOutbound from './FircGroupOutbound.svelte';

	interface Props {
		group: FircGroup;
		orderStr: string;
		isFirst: boolean;
		isLast: boolean;
		rt: FircGroupsRuntime;
		serviceKey: string;
		probingIfaces: Record<string, boolean>;
		probingGroups: Record<string, boolean>;
		selectMode: boolean;
		selected: boolean;
		isExpanded: boolean;
		syncing: boolean;
		pickerOpen: boolean;
		showOthers: boolean;
		ontoggleSelected: () => void;
		onmove: (delta: -1 | 1) => void;
		ontoggleExpand: () => void;
		ontogglePicker: () => void;
		onclosePicker: () => void;
		ontoggleOthers: () => void;
		onprobe: (ids: string[], opts?: FircProbeOptions) => void;
		onopenProxyGroups: () => void;
		onsync: () => void;
		onaddRule: () => void;
		onopenDevices: () => void;
		oncopyDomains: () => void;
		onimportRules: () => void;
		onopenSettings: () => void;
		ondelete: () => void;
	}

	let {
		group = $bindable(),
		orderStr,
		isFirst,
		isLast,
		rt,
		serviceKey,
		probingIfaces,
		probingGroups,
		selectMode,
		selected,
		isExpanded,
		syncing,
		pickerOpen,
		showOthers,
		ontoggleSelected,
		onmove,
		ontoggleExpand,
		ontogglePicker,
		onclosePicker,
		ontoggleOthers,
		onprobe,
		onopenProxyGroups,
		onsync,
		onaddRule,
		onopenDevices,
		oncopyDomains,
		onimportRules,
		onopenSettings,
		ondelete,
	}: Props = $props();

	let hasCustomDevices = $derived(
		(group.devices?.allow?.length ?? 0) > 0 || (group.devices?.deny?.length ?? 0) > 0,
	);
</script>

<!-- Main Card Row -->
<div class="card" class:select-mode={selectMode}>
	{#if selectMode}
		<input
			type="checkbox"
			class="bulk-check"
			checked={selected}
			aria-label={m.firc_bulk_groups_select_aria({ name: group.name })}
			onchange={ontoggleSelected}
		/>
	{/if}
	<div class="order" title={m.firc_groups_priority_tip({ order: orderStr })}>
		{orderStr}
	</div>

	<div class="reorder-slot">
		<button
			type="button"
			class="reorder-btn"
			disabled={isFirst}
			title={m.firc_groups_move_up()}
			onclick={() => onmove(-1)}
		>
			<ArrowUp size={12} />
		</button>
		<button
			type="button"
			class="reorder-btn"
			disabled={isLast}
			title={m.firc_groups_move_down()}
			onclick={() => onmove(1)}
		>
			<ArrowDown size={12} />
		</button>
	</div>

	<div class="main">
		<div class="group-head-line">
			<div class="tile-with-status" title={groupStatusTooltip(rt.liveMap, group)}>
				<StatusDot variant={groupStatusDotVariant(rt.liveMap, group)} size="sm" />
				<ServiceTile
					{serviceKey}
					name={group.name}
					sub=""
				/>
			</div>

			<div class="group-badges-row">
				{#if group.list?.url}
					<span class="meta-chip chip-list">{m.firc_groups_chip_list()}</span>
				{/if}
				<span class="meta-chip chip-dns" title={m.firc_groups_chip_dns_tip()}>
					{resolverBadgeText(rt, group)}
				</span>
				{#if hasCustomDevices}
					<span class="meta-chip chip-devices-active" title={devicesBadgeText(group)}>
						{devicesBadgeText(group)}
					</span>
				{/if}
			</div>
		</div>

		{#if group.list?.url}
			<div class="firc-list-header">
				<div class="list-hdr-url-row">
					<Link2 size={12} class="list-hdr-ico" />
					<input
						type="url"
						class="list-url-inline font-mono"
						placeholder="https://..."
						bind:value={group.list.url}
						title={m.firc_groups_list_url_tip()}
					/>
					<button
						type="button"
						class="list-clear-btn"
						title={m.firc_groups_list_clear_tip()}
						onclick={() => (group.list = null)}
					>
						<X size={12} />
					</button>
				</div>
				<div class="list-hdr-meta-row">
					<RefreshCw size={11} class="list-hdr-ico {syncing ? 'spin' : ''}" />
					<span class="list-meta-time">{formatTimestamp(group.list.lastUpdate)}</span>
					<span class="list-meta-sep">·</span>
					<span class="list-meta-lbl">{m.firc_groups_list_update_label()}</span>
					<select
						class="list-interval-inline"
						value={group.list.interval ?? 86400}
						onchange={(e) => {
							if (group.list) {
								group.list.interval = Number(
									(e.currentTarget as HTMLSelectElement).value,
								);
							}
						}}
					>
						{#each intervalOptions() as opt}
							<option value={opt.value}>{opt.label.toLowerCase()}</option>
						{/each}
					</select>
					<span class="list-meta-sep">·</span>
					<button
						type="button"
						class="list-counts-btn"
						onclick={ontoggleExpand}
						title={m.firc_groups_list_expand_tip()}
					>
						<strong>{group.list.rulesTotal || group.list.rules || 0}</strong> {m.firc_groups_from_list()}{#if group.rules.length > 0}{' '}+ <strong>{group.rules.length}</strong> {m.firc_groups_own_suffix()}{/if}
					</button>
					{#if group.list.sync?.state === 'error' && group.list.sync.error}
						<span class="list-sync-err">· {group.list.sync.error}</span>
					{/if}
				</div>
			</div>
		{:else}
			<div class="firc-own-summary-row">
				<button
					type="button"
					class="list-counts-btn"
					onclick={ontoggleExpand}
					title={m.firc_groups_own_expand_tip()}
				>
					{#if group.rules.length > 0}
						<strong>{group.rules.length}</strong> {m.firc_groups_own_rules_suffix({ count: group.rules.length })}
					{:else}
						{m.firc_groups_no_own_rules()}
					{/if}
				</button>
			</div>
		{/if}
	</div>

	<div class="trail">
		<FircGroupOutbound
			bind:group
			{rt}
			{probingIfaces}
			{probingGroups}
			{pickerOpen}
			{showOthers}
			{ontogglePicker}
			{onclosePicker}
			{ontoggleOthers}
			{onprobe}
			{onopenProxyGroups}
		/>

		<Toggle
			size="sm"
			spinner="none"
			checked={group.enable}
			onchange={(v) => (group.enable = v)}
			ariaLabel={m.firc_groups_toggle_group_aria({ name: group.name })}
		/>

		<div class="right-slot">
			{#if group.list?.url}
				<button
					type="button"
					class="route-action-btn"
					disabled={syncing}
					title={m.firc_groups_sync_list_tip()}
					onclick={onsync}
				>
					<RefreshCw size={14} class={syncing ? 'spin' : ''} />
				</button>
			{/if}
			<button
				type="button"
				class="route-action-btn"
				title={m.firc_groups_add_rule_tip()}
				onclick={onaddRule}
			>
				<Plus size={15} />
			</button>
			<button
				type="button"
				class="route-action-btn"
				title={m.firc_groups_devices_tip()}
				onclick={onopenDevices}
			>
				<Monitor size={14} />
			</button>
			<button
				type="button"
				class="route-action-btn"
				title={m.firc_groups_copy_group_domains_tip()}
				onclick={oncopyDomains}
			>
				<Copy size={14} />
			</button>
			<button
				type="button"
				class="route-action-btn"
				title={m.firc_groups_import_rules_tip()}
				onclick={onimportRules}
			>
				<Download size={14} />
			</button>
			<button
				type="button"
				class="route-action-btn"
				title={m.firc_groups_settings_tip()}
				onclick={onopenSettings}
			>
				<Settings size={14} />
			</button>
			<button
				type="button"
				class="route-action-btn danger"
				title={m.firc_groups_delete_group_tip()}
				onclick={ondelete}
			>
				<Trash2 size={14} />
			</button>
			<button
				type="button"
				class="route-action-btn"
				title={isExpanded ? m.firc_groups_collapse_rules() : m.firc_groups_expand_rules()}
				onclick={ontoggleExpand}
			>
				{#if isExpanded}
					<ChevronUp size={15} />
				{:else}
					<ChevronDown size={15} />
				{/if}
			</button>
		</div>
	</div>
</div>

<style>
	.bulk-check {
		width: 15px;
		height: 15px;
		accent-color: var(--color-accent);
		cursor: pointer;
		flex-shrink: 0;
	}

	.card {
		display: grid;
		grid-template-columns: 24px 18px minmax(200px, 1fr) auto;
		gap: 6px 8px;
		align-items: center;
		padding: 7px 10px;
		min-width: 0;
	}

	:global(html[data-layout-compact='true']) .trail {
		gap: 4px;
	}

	:global(html[data-layout-compact='true']) .right-slot {
		gap: 2px;
	}

	.card.select-mode {
		grid-template-columns: 22px 24px 18px minmax(200px, 1fr) auto;
	}

	.order {
		font-family: var(--font-mono);
		font-size: 12px;
		font-weight: 600;
		color: var(--text-secondary);
		text-align: center;
	}

	.reorder-slot {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1px;
	}

	.reorder-btn {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-muted);
		padding: 1px;
		cursor: pointer;
		border-radius: 3px;
		display: inline-flex;
	}

	.reorder-btn:hover:not(:disabled) {
		color: var(--text-primary);
		background: var(--bg-tertiary);
	}

	.reorder-btn:disabled {
		opacity: 0.25;
		cursor: default;
	}

	.main {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 4px;
		min-width: 0;
		overflow: hidden;
	}

	.group-head-line {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 6px 10px;
		width: 100%;
		min-width: 0;
	}

	.tile-with-status {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		flex-shrink: 1;
		min-width: 0;
		max-width: 100%;
	}

	.group-badges-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 5px;
		min-width: 0;
	}

	.meta-chip {
		display: inline-flex;
		align-items: center;
		padding: 1.5px 7px;
		border-radius: var(--radius-sm);
		font-size: 11px;
		font-weight: 500;
		line-height: 1.35;
		white-space: nowrap;
		border: 1px solid transparent;
	}

	.meta-chip.chip-list {
		color: var(--color-info);
		background: var(--color-info-tint);
		border-color: var(--color-info-border);
	}

	.meta-chip.chip-dns {
		color: var(--text-secondary);
		background: var(--bg-tertiary);
		border-color: var(--border);
		font-weight: 500;
	}

	.meta-chip.chip-devices-active {
		color: var(--color-warning);
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
	}

	.firc-list-header {
		display: flex;
		flex-direction: column;
		gap: 2px;
		width: 100%;
		min-width: 0;
		padding-left: 2px;
	}

	.list-hdr-url-row {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		max-width: 100%;
	}

	.firc-list-header :global(.list-hdr-ico) {
		color: var(--text-muted);
		flex-shrink: 0;
	}

	.list-url-inline {
		background: transparent;
		border: 0;
		border-bottom: 1px dashed transparent;
		color: var(--text-secondary);
		font-size: 11.5px;
		min-width: 120px;
		width: 100%;
		max-width: 440px;
		padding: 0 2px;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.list-url-inline:hover {
		color: var(--text-primary);
		border-bottom-color: var(--border-hover);
	}

	.list-url-inline:focus {
		outline: none;
		border-bottom-color: var(--accent);
		color: var(--text-primary);
	}

	.list-clear-btn {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-muted);
		cursor: pointer;
		padding: 2px;
		border-radius: 4px;
		display: inline-flex;
		align-items: center;
		opacity: 0.6;
	}

	.list-clear-btn:hover {
		opacity: 1;
		color: var(--color-danger);
		background: var(--bg-tertiary);
	}

	.list-hdr-meta-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 5px;
		font-size: 11px;
		color: var(--text-muted);
	}

	.list-interval-inline {
		appearance: none;
		background: transparent;
		border: 0;
		border-bottom: 1px dashed var(--border-hover);
		color: var(--text-secondary);
		font-size: 11px;
		font-family: inherit;
		padding: 0 2px;
		cursor: pointer;
	}

	.list-interval-inline:focus {
		outline: none;
		border-bottom-color: var(--accent);
	}

	.list-interval-inline option {
		background: var(--bg-secondary);
		color: var(--text-primary);
	}

	.list-counts-btn {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-secondary);
		font-size: 11px;
		font-family: inherit;
		padding: 0;
		cursor: pointer;
	}

	.list-counts-btn:hover {
		color: var(--accent);
		text-decoration: underline;
	}

	.firc-own-summary-row {
		display: flex;
		align-items: center;
		padding-left: 2px;
	}

	.list-sync-err {
		color: var(--color-danger);
		font-weight: 500;
	}

	.trail {
		display: flex;
		align-items: center;
		gap: 6px;
		justify-content: flex-end;
		flex-shrink: 0;
	}

	.right-slot {
		display: flex;
		align-items: center;
		gap: 2px;
	}

	@media (max-width: 768px) {
		.card {
			grid-template-columns: 28px minmax(0, 1fr);
		}
		.card.select-mode {
			grid-template-columns: 22px 28px minmax(0, 1fr);
		}
		.reorder-slot {
			display: none;
		}
		.trail {
			grid-column: 1 / -1;
			flex-wrap: wrap;
			min-width: 0;
			justify-content: space-between;
			border-top: 1px dashed var(--border);
			padding-top: 8px;
		}
		.right-slot {
			flex-wrap: wrap;
		}
	}
</style>
