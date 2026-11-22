<script lang="ts">
	import { untrack } from 'svelte';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import {
		Button,
		Modal,
		SegmentedControl,
		Toggle,
		type SegmentedOption,
	} from '$lib/components/ui';
	import { Monitor, Search, Shield, X } from 'lucide-svelte';
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

	function toggleEntry(key: string) {
		setEntryEnabled(key, !isEntryEnabled(key));
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
									<div
										class="dm-row"
										class:dm-row-excluded={mode === 'all' && !isOn}
										class:dm-row-selected={mode === 'allow' && isOn}
										class:dm-row-muted={mode === 'allow' && !isOn}
										data-entry={key}
										data-enabled={isOn ? 'true' : 'false'}
									>
										<button
											type="button"
											class="dm-row-trigger"
											title={titleText}
											onclick={() => toggleEntry(key)}
										>
											<span class="dm-row-text">
												<span class="dm-row-name">{pol.description || pol.name}</span>
												<span class="dm-row-meta">
													{pol.name}{#if pol.devices > 0} · {m.firc_devices_policy_hosts({ count: pol.devices })}{/if}
												</span>
											</span>
										</button>
										<div class="dm-row-control">
											{#if mode === 'all' && !isOn}
												<span class="dm-excl-pill">{m.firc_devices_badge_excluded()}</span>
											{/if}
											<Toggle
												size="sm"
												spinner="none"
												controlled
												checked={isOn}
												ariaLabel={pol.description || pol.name}
												onchange={(next) => setEntryEnabled(key, next)}
											/>
										</div>
									</div>
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

							<div class="dm-search-wrap">
								<Search size={13} class="dm-search-icon" />
								<input
									type="search"
									class="dm-search-inp"
									aria-label={m.firc_devices_search_placeholder()}
									placeholder={m.firc_devices_search_placeholder()}
									bind:value={searchQuery}
								/>
								{#if searchQuery.trim() !== ''}
									<button
										type="button"
										class="dm-search-clear"
										aria-label={m.common_clear()}
										onclick={() => (searchQuery = '')}
									>
										<X size={12} />
									</button>
								{/if}
							</div>
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
										<div
											class="dm-row"
											class:dm-row-excluded={mode === 'all' && !isOn}
											class:dm-row-selected={mode === 'allow' && isOn}
											class:dm-row-muted={mode === 'allow' && !isOn}
											data-entry={key}
											data-enabled={isOn ? 'true' : 'false'}
										>
											<button
												type="button"
												class="dm-row-trigger"
												title={titleText}
												onclick={() => toggleEntry(key)}
											>
												{#if h.online !== undefined}
													<span
														class="dm-online-dot"
														class:dm-online={h.online}
														title={h.online ? m.firc_devices_online() : m.firc_devices_offline()}
													></span>
												{/if}
												<span class="dm-row-text">
													<span class="dm-row-name">{label}</span>
													{#if meta}
														<span class="dm-row-meta">{meta}</span>
													{/if}
												</span>
											</button>
											<div class="dm-row-control">
												{#if mode === 'all' && !isOn}
													<span class="dm-excl-pill">{m.firc_devices_badge_excluded()}</span>
												{/if}
												<Toggle
													size="sm"
													spinner="none"
													controlled
													checked={isOn}
													ariaLabel={label}
													onchange={(next) => setEntryEnabled(key, next)}
												/>
											</div>
										</div>
									{/each}
								</div>
							{/if}
						</div>
					</section>
				{/if}
			{/if}
		</div>

		<div class="dm-manual-bar">
			<div class="dm-manual-row">
				<input
					type="text"
					class="dm-manual-inp"
					aria-label={mode === 'all'
						? m.firc_devices_manual_placeholder_exclude()
						: m.firc_devices_manual_placeholder_include()}
					placeholder={mode === 'all'
						? m.firc_devices_manual_placeholder_exclude()
						: m.firc_devices_manual_placeholder_include()}
					bind:value={manualInput}
					onkeydown={(e) => {
						if (e.key === 'Enter') {
							e.preventDefault();
							addManualEntry();
						}
					}}
				/>
				<Button
					variant="secondary"
					size="sm"
					onclick={addManualEntry}
					disabled={!manualInput.trim()}
				>
					{mode === 'all' ? m.firc_devices_manual_btn_exclude() : m.common_add()}
				</Button>
			</div>

			{#if currentEntries.size > 0}
				<div class="dm-chips-strip">
					{#each Array.from(currentEntries) as entry (entry)}
						<span class="dm-chip" class:dm-chip-excl={mode === 'all'}>
							<span class="dm-chip-label">{formatEntryChipLabel(entry)}</span>
							<button
								type="button"
								class="dm-chip-remove"
								title={m.firc_devices_chip_remove()}
								aria-label={m.firc_devices_chip_remove()}
								onclick={() => removeEntryChip(entry)}
							>
								<X size={11} />
							</button>
						</span>
					{/each}
				</div>
			{/if}
		</div>
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

	.dm-search-wrap {
		position: relative;
		display: flex;
		align-items: center;
		width: min(280px, 100%);
		min-width: 170px;
	}

	:global(.dm-search-icon) {
		position: absolute;
		left: 9px;
		color: var(--color-text-muted);
		pointer-events: none;
	}

	.dm-search-inp {
		width: 100%;
		height: 30px;
		padding: 0 26px 0 29px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-size: 12px;
		font-family: inherit;
		outline: none;
		transition: border-color var(--t-fast);
	}

	.dm-search-inp:focus {
		border-color: var(--color-accent);
	}

	.dm-search-clear {
		position: absolute;
		right: 5px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 20px;
		height: 20px;
		border: 0;
		border-radius: 4px;
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
	}

	.dm-search-clear:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-hover);
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

	.dm-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		min-width: 0;
		padding: 6px 9px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		transition:
			background var(--t-fast),
			border-color var(--t-fast),
			opacity var(--t-fast);
	}

	.dm-row:hover {
		border-color: var(--color-border-hover);
		background: var(--color-bg-hover);
	}

	.dm-row.dm-row-selected {
		background: color-mix(in srgb, var(--color-accent-tint) 65%, var(--color-bg-primary));
		border-color: var(--color-accent-border);
	}

	.dm-row.dm-row-excluded {
		background: color-mix(in srgb, var(--color-warning-tint) 65%, var(--color-bg-primary));
		border-color: var(--color-warning-border);
		opacity: 0.78;
	}

	.dm-row.dm-row-muted {
		opacity: 0.72;
	}

	.dm-row.dm-row-muted:hover,
	.dm-row.dm-row-excluded:hover {
		opacity: 0.96;
	}

	.dm-row-trigger {
		flex: 1;
		min-width: 0;
		display: flex;
		align-items: center;
		gap: 7px;
		border: 0;
		padding: 0;
		background: transparent;
		color: inherit;
		font: inherit;
		text-align: left;
		cursor: pointer;
	}

	.dm-online-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--color-text-muted);
		opacity: 0.45;
	}

	.dm-online-dot.dm-online {
		background: var(--color-success);
		opacity: 1;
	}

	.dm-row-text {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}

	.dm-row-name {
		font-size: 12.5px;
		font-weight: 500;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dm-row.dm-row-selected .dm-row-name {
		font-weight: 600;
	}

	.dm-row-meta {
		font-family: var(--font-mono);
		font-size: 10.5px;
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dm-row-control {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		flex-shrink: 0;
	}

	.dm-excl-pill {
		font-size: 10px;
		font-weight: 600;
		padding: 1px 6px;
		border-radius: var(--radius-pill);
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
		white-space: nowrap;
	}

	.dm-empty-state,
	.dm-empty-search {
		padding: 28px 16px;
		text-align: center;
		font-size: 12.5px;
		color: var(--color-text-muted);
	}

	.dm-manual-bar {
		flex: 0 0 auto;
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 10px 12px 12px;
		border-top: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-bg-primary) 35%, transparent);
	}

	.dm-manual-row {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.dm-manual-inp {
		flex: 1;
		min-width: 0;
		height: 32px;
		padding: 0 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-family: var(--font-mono);
		font-size: 12px;
		outline: none;
		transition: border-color var(--t-fast);
	}

	.dm-manual-inp:focus {
		border-color: var(--color-accent);
	}

	.dm-chips-strip {
		display: flex;
		flex-wrap: wrap;
		gap: 5px;
		max-height: 60px;
		overflow-y: auto;
	}

	.dm-chip {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-text-primary);
		font-size: 11.5px;
		font-weight: 500;
		max-width: 260px;
	}

	.dm-chip.dm-chip-excl {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
	}

	.dm-chip-label {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dm-chip-remove {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border: 0;
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		padding: 1px;
		border-radius: 3px;
		flex-shrink: 0;
	}

	.dm-chip-remove:hover {
		color: var(--color-error);
		background: var(--color-error-tint);
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
		.dm-search-wrap {
			width: 100%;
		}
	}
</style>
