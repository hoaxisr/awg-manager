<script lang="ts">
	import { untrack } from 'svelte';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import {
		Button,
		Modal,
		SegmentedControl,
		type SegmentedOption,
	} from '$lib/components/ui';
	import { Monitor, Shield } from 'lucide-svelte';
	import FircGroupDevicesRow from './FircGroupDevicesRow.svelte';
	import FircGroupDevicesSearch from './FircGroupDevicesSearch.svelte';
	import FircGroupDevicesManualBar from './FircGroupDevicesManualBar.svelte';
	import type { FircDeviceSelector, FircHost, FircPolicy } from '$lib/types/firc';

	interface Props {
		open: boolean;
		groupName?: string;
		initialDevices?: FircDeviceSelector;
		hosts: FircHost[];
		policies: FircPolicy[];
		onclose: () => void;
		onapply: (selector: FircDeviceSelector) => void;
	}

	let {
		open,
		groupName = '',
		initialDevices,
		hosts,
		policies,
		onclose,
		onapply,
	}: Props = $props();

	let mode = $state<'all' | 'allow'>('all');
	let denyEntries = $state<Set<string>>(new Set());
	let allowEntries = $state<Set<string>>(new Set());
	let initialDenyOrder = $state<Set<string>>(new Set());
	let initialAllowOrder = $state<Set<string>>(new Set());
	let searchQuery = $state('');
	let manualInput = $state('');

	let wasOpen = false;
	$effect(() => {
		if (open && !wasOpen) {
			untrack(() => initFromInitial(initialDevices));
		}
		wasOpen = open;
	});

	function initFromInitial(selector: FircDeviceSelector | undefined) {
		const allow = selector?.allow ?? [];
		const deny = selector?.deny ?? [];
		if (allow.length > 0) {
			mode = 'allow';
			allowEntries = new Set(allow);
			initialAllowOrder = new Set(allow);
			denyEntries = new Set();
			initialDenyOrder = new Set();
		} else {
			mode = 'all';
			denyEntries = new Set(deny);
			initialDenyOrder = new Set(deny);
			allowEntries = new Set();
			initialAllowOrder = new Set();
		}
		searchQuery = '';
		manualInput = '';
	}

	function hostEntryKey(h: FircHost): string {
		return h.mac ? `mac:${h.mac.toLowerCase()}` : h.ip;
	}

	function formatHostMeta(h: FircHost): string {
		const validIp = h.ip && h.ip !== '0.0.0.0' ? h.ip : '';
		const macStr = h.mac ? h.mac.toUpperCase() : '';
		if (validIp && macStr) return `${validIp} · ${macStr}`;
		return validIp || macStr || h.ip || '';
	}

	let dedupedHosts = $derived.by(() => {
		const seen = new Set<string>();
		const out: FircHost[] = [];
		for (const h of hosts) {
			const key = hostEntryKey(h);
			if (!key || seen.has(key)) continue;
			seen.add(key);
			out.push(h);
		}
		return out;
	});

	function formatEntryChipLabel(entry: string): string {
		if (entry.startsWith('policy:')) {
			const pName = entry.slice('policy:'.length);
			const pol = policies.find((p) => p.name === pName);
			return pol?.description ? `${pol.description} (${pName})` : entry;
		}
		if (entry.startsWith('mac:')) {
			const mac = entry.slice('mac:'.length).toLowerCase();
			const h = dedupedHosts.find((x) => x.mac?.toLowerCase() === mac);
			if (h?.name) return `${h.name} (${mac})`;
		}
		const byIp = dedupedHosts.find((x) => x.ip === entry && x.ip !== '0.0.0.0');
		if (byIp?.name) return `${byIp.name} (${entry})`;
		return entry;
	}

	function isEntryEnabled(key: string): boolean {
		return mode === 'all' ? !denyEntries.has(key) : allowEntries.has(key);
	}

	function setEntryEnabled(key: string, enabled: boolean) {
		if (mode === 'all') {
			const next = new Set(denyEntries);
			if (enabled) next.delete(key);
			else next.add(key);
			denyEntries = next;
		} else {
			const next = new Set(allowEntries);
			if (enabled) next.add(key);
			else next.delete(key);
			allowEntries = next;
		}
	}

	function removeEntryChip(entry: string) {
		if (mode === 'all') {
			const next = new Set(denyEntries);
			next.delete(entry);
			denyEntries = next;
		} else {
			const next = new Set(allowEntries);
			next.delete(entry);
			allowEntries = next;
		}
	}

	function resetCurrentMode() {
		if (mode === 'all') {
			denyEntries = new Set();
		} else {
			allowEntries = new Set();
		}
	}

	function addManualEntry() {
		const v = manualInput.trim();
		if (!v) return;
		if (mode === 'all') {
			const next = new Set(denyEntries);
			next.add(v);
			denyEntries = next;
		} else {
			const next = new Set(allowEntries);
			next.add(v);
			allowEntries = next;
		}
		manualInput = '';
	}

	function handleSave() {
		if (mode === 'allow') {
			if (allowEntries.size === 0) {
				notifications.warning(m.firc_devices_warn_select_one());
				return;
			}
			onapply({ allow: Array.from(allowEntries), deny: [] });
			return;
		}
		onapply({ allow: [], deny: Array.from(denyEntries) });
	}

	let currentEntries = $derived(mode === 'all' ? denyEntries : allowEntries);
	let activeInitialOrder = $derived(mode === 'all' ? initialDenyOrder : initialAllowOrder);

	let sortedPolicies = $derived.by(() => {
		return [...policies].sort((a, b) => {
			const aPin = activeInitialOrder.has(`policy:${a.name}`) ? 1 : 0;
			const bPin = activeInitialOrder.has(`policy:${b.name}`) ? 1 : 0;
			if (aPin !== bPin) return bPin - aPin;
			return (a.description || a.name).localeCompare(b.description || b.name);
		});
	});

	let filteredSortedHosts = $derived.by(() => {
		const q = searchQuery.trim().toLowerCase();
		const base = q
			? dedupedHosts.filter(
					(h) =>
						(h.name && h.name.toLowerCase().includes(q)) ||
						(h.ip && h.ip.toLowerCase().includes(q)) ||
						(h.mac && h.mac.toLowerCase().includes(q)),
				)
			: [...dedupedHosts];
		return base.sort((a, b) => {
			const aPin = activeInitialOrder.has(hostEntryKey(a)) ? 1 : 0;
			const bPin = activeInitialOrder.has(hostEntryKey(b)) ? 1 : 0;
			if (aPin !== bPin) return bPin - aPin;
			const aOnline = a.online === true ? 1 : 0;
			const bOnline = b.online === true ? 1 : 0;
			if (aOnline !== bOnline) return bOnline - aOnline;
			return (a.name || a.ip || a.mac || '').localeCompare(b.name || b.ip || b.mac || '');
		});
	});

	let activePoliciesCount = $derived(
		policies.filter((p) => isEntryEnabled(`policy:${p.name}`)).length,
	);
	let activeHostsCount = $derived(
		dedupedHosts.filter((h) => isEntryEnabled(hostEntryKey(h))).length,
	);

	let modeOptions = $derived<SegmentedOption<'all' | 'allow'>[]>([
		{ value: 'all', label: m.firc_devices_mode_all() },
		{ value: 'allow', label: m.firc_devices_mode_allow() },
	]);
</script>

<Modal
	{open}
	{onclose}
	title={groupName
		? m.firc_devices_modal_title({ name: groupName })
		: m.firc_devices_modal_title_default()}
	size="wide"
	width="1060px"
	bodyLayout="fill"
>
	<div class="dm-root">
		<div class="dm-top">
			<div class="dm-top-row">
				<SegmentedControl
					value={mode}
					options={modeOptions}
					ariaLabel={m.firc_devices_mode_aria()}
					onchange={(v) => (mode = v)}
				/>

				<div
					class="dm-status-pill"
					class:dm-status-ok={mode === 'all' && denyEntries.size === 0}
					class:dm-status-warn={(mode === 'all' && denyEntries.size > 0) ||
						(mode === 'allow' && allowEntries.size === 0)}
					class:dm-status-accent={mode === 'allow' && allowEntries.size > 0}
					data-testid="firc-dm-summary"
				>
					{#if mode === 'all'}
						{#if denyEntries.size === 0}
							<span>{m.firc_devices_summary_all_active()}</span>
						{:else}
							<span>{m.firc_devices_summary_excluded({ count: denyEntries.size })}</span>
							<button type="button" class="dm-reset-btn" onclick={resetCurrentMode}>
								{m.firc_devices_enable_all()}
							</button>
						{/if}
					{:else if allowEntries.size === 0}
						<span>{m.firc_devices_summary_need_one()}</span>
					{:else}
						<span>{m.firc_devices_summary_selected({ count: allowEntries.size })}</span>
						<button type="button" class="dm-reset-btn" onclick={resetCurrentMode}>
							{m.firc_devices_clear_all()}
						</button>
					{/if}
				</div>
			</div>

			<p class="dm-mode-hint">
				{mode === 'all' ? m.firc_devices_hint_all() : m.firc_devices_hint_allow()}
			</p>
		</div>

		<div
			class="dm-workspace"
			class:dm-single-col={policies.length === 0 || dedupedHosts.length === 0}
		>
			{#if policies.length === 0 && dedupedHosts.length === 0}
				<div class="dm-empty-state">{m.firc_devices_empty_all()}</div>
			{:else}
				{#if policies.length > 0}
					<section class="dm-pane dm-pane-policies">
						<div class="dm-pane-head">
							<div class="dm-pane-title-wrap">
								<Shield size={14} class="dm-pane-icon" />
								<span class="dm-pane-title">{m.firc_devices_policies_title()}</span>
							</div>
							<span class="dm-count-pill" data-testid="firc-dm-policies-count">
								{activePoliciesCount} / {policies.length}
							</span>
						</div>

						<div class="dm-list-scroll">
							<div class="dm-policies-list">
								{#each sortedPolicies as pol (pol.name)}
									{@const key = `policy:${pol.name}`}
									{@const isOn = isEntryEnabled(key)}
									{@const titleText = pol.description ? `${pol.description} (${pol.name})` : pol.name}
									<FircGroupDevicesRow
										entryKey={key}
										{mode}
										{isOn}
										label={pol.description || pol.name}
										meta={pol.devices > 0 ? `${pol.name} · ${m.firc_devices_policy_hosts({ count: pol.devices })}` : pol.name}
										title={titleText}
										onset={(next) => setEntryEnabled(key, next)}
									/>
								{/each}
							</div>
						</div>
					</section>
				{/if}

				{#if dedupedHosts.length > 0}
					<section class="dm-pane dm-pane-devices">
						<div class="dm-pane-head dm-devices-head">
							<div class="dm-pane-title-wrap">
								<Monitor size={14} class="dm-pane-icon" />
								<span class="dm-pane-title">{m.firc_devices_hosts_title()}</span>
								<span class="dm-count-pill" data-testid="firc-dm-hosts-count">
									{activeHostsCount} / {dedupedHosts.length}
								</span>
							</div>

							<FircGroupDevicesSearch bind:value={searchQuery} />
						</div>

						<div class="dm-list-scroll">
							{#if filteredSortedHosts.length === 0}
								<div class="dm-empty-search">
									{m.firc_devices_search_empty({ query: searchQuery })}
								</div>
							{:else}
								<div class="dm-devices-grid" class:dm-cols-3={policies.length === 0}>
									{#each filteredSortedHosts as h (hostEntryKey(h))}
										{@const key = hostEntryKey(h)}
										{@const isOn = isEntryEnabled(key)}
										{@const label = h.name || h.ip || h.mac}
										{@const meta = formatHostMeta(h)}
										{@const titleText = [label, meta].filter(Boolean).join(' · ')}
										<FircGroupDevicesRow
											entryKey={key}
											{mode}
											{isOn}
											label={label ?? ''}
											{meta}
											title={titleText}
											online={h.online}
											onset={(next) => setEntryEnabled(key, next)}
										/>
									{/each}
								</div>
							{/if}
						</div>
					</section>
				{/if}
			{/if}
		</div>

		<FircGroupDevicesManualBar
			{mode}
			entries={currentEntries}
			bind:value={manualInput}
			chipLabel={formatEntryChipLabel}
			onadd={addManualEntry}
			onremove={removeEntryChip}
		/>
	</div>

	{#snippet actions()}
		<div class="dm-footer-btns">
			<Button variant="ghost" size="sm" onclick={onclose}>{m.common_cancel()}</Button>
			<Button variant="primary" size="sm" onclick={handleSave}>{m.common_apply()}</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.dm-root {
		display: flex;
		flex-direction: column;
		flex: 1;
		min-height: 0;
		height: min(740px, calc(100dvh - 10rem));
	}

	.dm-top {
		flex: 0 0 auto;
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 12px 12px 10px;
		border-bottom: 1px solid var(--color-border);
	}

	.dm-top-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
	}

	.dm-status-pill {
		display: inline-flex;
		align-items: center;
		gap: 10px;
		padding: 5px 11px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		font-size: 12px;
		font-weight: 500;
	}

	.dm-status-pill.dm-status-ok {
		background: var(--color-success-tint);
		border-color: var(--color-success-border);
		color: var(--color-success);
	}

	.dm-status-pill.dm-status-warn {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
		color: var(--color-warning);
	}

	.dm-status-pill.dm-status-accent {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
	}

	.dm-reset-btn {
		border: 0;
		background: transparent;
		color: var(--color-text-primary);
		font-size: 11.5px;
		font-weight: 600;
		cursor: pointer;
		padding: 1px 4px;
		border-radius: 4px;
		text-decoration: underline;
		text-underline-offset: 2px;
	}

	.dm-reset-btn:hover {
		color: var(--color-accent);
	}

	.dm-mode-hint {
		margin: 0;
		font-size: 12px;
		line-height: 1.4;
		color: var(--color-text-muted);
	}

	.dm-workspace {
		flex: 1;
		min-height: 0;
		overflow: hidden;
		display: grid;
		grid-template-columns: 280px minmax(0, 1fr);
	}

	.dm-workspace.dm-single-col {
		grid-template-columns: 1fr;
	}

	.dm-pane {
		display: flex;
		flex-direction: column;
		min-width: 0;
		min-height: 0;
		padding: 10px 12px;
		gap: 8px;
	}

	.dm-pane-policies {
		border-right: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-bg-primary) 45%, transparent);
	}

	.dm-workspace.dm-single-col .dm-pane-policies {
		border-right: none;
	}

	.dm-pane-head {
		flex: 0 0 auto;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		min-height: 30px;
	}

	.dm-pane-title-wrap {
		display: flex;
		align-items: center;
		gap: 7px;
		min-width: 0;
	}

	:global(.dm-pane-icon) {
		color: var(--color-text-muted);
		flex-shrink: 0;
	}

	.dm-pane-title {
		font-size: 11.5px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-secondary);
		white-space: nowrap;
	}

	.dm-count-pill {
		font-family: var(--font-mono);
		font-size: 11px;
		font-weight: 600;
		padding: 1px 7px;
		border-radius: var(--radius-pill);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		white-space: nowrap;
		flex-shrink: 0;
	}

	.dm-list-scroll {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		padding-right: 3px;
	}

	.dm-policies-list {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.dm-devices-grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 6px;
		align-content: start;
	}

	.dm-devices-grid.dm-cols-3 {
		grid-template-columns: repeat(3, minmax(0, 1fr));
	}

	.dm-empty-state,
	.dm-empty-search {
		padding: 28px 16px;
		text-align: center;
		font-size: 12.5px;
		color: var(--color-text-muted);
	}

	.dm-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		width: 100%;
	}

	@media (max-width: 980px) {
		.dm-workspace {
			grid-template-columns: 240px minmax(0, 1fr);
		}
		.dm-devices-grid.dm-cols-3 {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}

	@media (max-width: 768px) {
		.dm-root {
			height: auto;
			max-height: calc(100dvh - 10rem);
			overflow-y: auto;
		}
		.dm-workspace {
			display: flex;
			flex-direction: column;
			overflow: visible;
			flex: 0 0 auto;
		}
		.dm-pane-policies {
			border-right: none;
			border-bottom: 1px solid var(--color-border);
		}
		.dm-list-scroll {
			overflow-y: visible;
		}
		.dm-devices-grid,
		.dm-devices-grid.dm-cols-3 {
			grid-template-columns: 1fr;
		}
		.dm-devices-head {
			flex-wrap: wrap;
		}
	}
</style>
