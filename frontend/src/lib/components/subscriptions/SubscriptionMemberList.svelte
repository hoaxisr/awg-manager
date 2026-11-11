<script lang="ts">
	import { tick } from 'svelte';
	import { m } from '$lib/i18n';
	import type { SubscriptionMember } from '$lib/types';
	import { Ban, ListFilter, Plus, Search, Trash2, X } from 'lucide-svelte';
	import SubscriptionMemberCard from './SubscriptionMemberCard.svelte';
	import { isCardNestedInteraction } from '$lib/utils/cardClick';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
	import { Button, TableSortHeader } from '$lib/components/ui';
	import TunnelTableSortControls from '$lib/components/tunnels/TunnelTableSortControls.svelte';
	import type { SubscriptionMemberSortKey } from '$lib/stores/tunnelTableSort';
	import type { ProtocolFilterOption } from './memberSelectors';

	interface Props {
		members: SubscriptionMember[];
		effectiveActiveMember: string | null;
		switching: string | null;
		layout: SingboxLayoutMode;
		isInline: boolean;
		removingTag: string | null;
		minDelayMs: number | null;
		isUrlSub: boolean;
		isAutoSelect?: boolean;
		selectMode: boolean;
		selected: Set<string>;
		excluding: boolean;
		creatingTunnelTag?: string | null;
		totalCount?: number;
		availableProtocols?: ProtocolFilterOption[];
		excludedProtocols?: Set<string>;
		searchQuery?: string;
		sortKey?: SubscriptionMemberSortKey | null;
		sortAsc?: boolean;
		onpick: (tag: string) => void;
		onremove: (member: SubscriptionMember) => void;
		ontoggle: (tag: string) => void;
		onexclude: (tag: string) => void;
		oncreatetunnel?: (member: SubscriptionMember) => void;
		onSearchChange?: (value: string) => void;
		onToggleProtocol?: (protocol: string) => void;
		onSelectAllProtocols?: () => void;
		onClearAllProtocols?: () => void;
		onResetFilters?: () => void;
		onSortToggle?: (key: SubscriptionMemberSortKey) => void;
		onSortSelect?: (key: SubscriptionMemberSortKey | null) => void;
		onSortDirToggle?: () => void;
	}
	let {
		members,
		effectiveActiveMember,
		switching,
		layout,
		isInline,
		removingTag,
		minDelayMs,
		isUrlSub,
		isAutoSelect = false,
		selectMode,
		selected,
		excluding,
		creatingTunnelTag = null,
		totalCount,
		availableProtocols = [],
		excludedProtocols = new Set<string>(),
		searchQuery = '',
		sortKey = null,
		sortAsc = true,
		onpick,
		onremove,
		ontoggle,
		onexclude,
		oncreatetunnel,
		onSearchChange,
		onToggleProtocol,
		onSelectAllProtocols,
		onClearAllProtocols,
		onResetFilters,
		onSortToggle,
		onSortSelect,
		onSortDirToggle,
	}: Props = $props();

	const sourceTotal = $derived(totalCount ?? members.length);
	const activeProtoCount = $derived(
		availableProtocols.reduce((acc, item) => (excludedProtocols.has(item.protocol) ? acc : acc + 1), 0),
	);
	const hasProtocolFilter = $derived(
		availableProtocols.length > 0 && activeProtoCount < availableProtocols.length,
	);
	const hasActiveFilter = $derived(searchQuery.trim().length > 0 || hasProtocolFilter);
	const showControls = $derived(
		Boolean(onSearchChange || (availableProtocols.length > 0 && onToggleProtocol)),
	);

	const sortOptions = $derived([
		{ value: 'delay', label: 'Delay' },
		{ value: 'server', label: m.subscriptions_list_col_server() },
		{ value: 'protocol', label: m.subscriptions_list_col_protocol() },
		{ value: 'ping', label: 'Ping' },
		{ value: 'tag', label: m.subscriptions_list_col_tag() },
		{ value: 'status', label: m.subscriptions_list_col_status() },
	]);

	let filterMenuOpen = $state(false);
	let filterWrapEl = $state<HTMLDivElement | null>(null);
	let filterBtnEl = $state<HTMLButtonElement | null>(null);
	let popoverPos = $state({ top: 0, left: 0 });

	function updatePopoverPos(): void {
		if (!filterBtnEl) return;
		const rect = filterBtnEl.getBoundingClientRect();
		const maxLeft = Math.max(8, (typeof window !== 'undefined' ? window.innerWidth : 1024) - 240);
		popoverPos = {
			top: rect.bottom + 4,
			left: Math.max(8, Math.min(rect.left, maxLeft)),
		};
	}

	async function toggleFilterMenu(e: MouseEvent): Promise<void> {
		e.stopPropagation();
		if (filterMenuOpen) {
			filterMenuOpen = false;
			return;
		}
		updatePopoverPos();
		filterMenuOpen = true;
		await tick();
		updatePopoverPos();
	}

	$effect(() => {
		if (!filterMenuOpen) return;
		const onPointerDown = (e: PointerEvent) => {
			if (filterWrapEl && !filterWrapEl.contains(e.target as Node)) {
				filterMenuOpen = false;
			}
		};
		const onKeyDown = (e: KeyboardEvent) => {
			if (e.key === 'Escape') filterMenuOpen = false;
		};
		const onViewportChange = () => updatePopoverPos();

		window.addEventListener('pointerdown', onPointerDown);
		window.addEventListener('keydown', onKeyDown);
		window.addEventListener('resize', onViewportChange);
		window.addEventListener('scroll', onViewportChange, true);
		return () => {
			window.removeEventListener('pointerdown', onPointerDown);
			window.removeEventListener('keydown', onKeyDown);
			window.removeEventListener('resize', onViewportChange);
			window.removeEventListener('scroll', onViewportChange, true);
		};
	});

	function handleRowKeydown(e: KeyboardEvent, tag: string): void {
		if (switching !== null || isCardNestedInteraction(e)) return;
		if (e.key === 'Enter' || e.key === ' ') {
			e.preventDefault();
			ontoggle(tag);
		}
	}
</script>

{#snippet protocolFilterControl()}
	{#if availableProtocols.length > 0 && onToggleProtocol}
		<div class="proto-filter-wrap" bind:this={filterWrapEl}>
			<button
				bind:this={filterBtnEl}
				type="button"
				class="proto-filter-btn"
				class:active={hasProtocolFilter || filterMenuOpen}
				aria-expanded={filterMenuOpen}
				aria-haspopup="dialog"
				aria-label={m.subscriptions_preview_protocol_filter_aria()}
				onclick={toggleFilterMenu}
			>
				<ListFilter size={13} aria-hidden="true" />
				<span>{m.subscriptions_members_filter_btn()}</span>
				{#if hasProtocolFilter}
					<span class="proto-filter-badge mono">{activeProtoCount}/{availableProtocols.length}</span>
				{/if}
			</button>
			{#if filterMenuOpen}
				<div
					class="proto-filter-popover"
					role="dialog"
					aria-label={m.subscriptions_preview_protocol_filter_aria()}
					style="top: {popoverPos.top}px; left: {popoverPos.left}px;"
				>
					<div class="proto-popover-actions">
						<button
							type="button"
							class="proto-bulk-btn"
							onclick={() => onSelectAllProtocols?.()}
						>
							{m.subscriptions_members_filter_all()}
						</button>
						<button
							type="button"
							class="proto-bulk-btn"
							onclick={() => onClearAllProtocols?.()}
						>
							{m.subscriptions_members_filter_none()}
						</button>
					</div>
					<div class="proto-options">
						{#each availableProtocols as item (item.protocol)}
							<label class="proto-option">
								<input
									type="checkbox"
									class="proto-check"
									checked={!excludedProtocols.has(item.protocol)}
									onchange={() => onToggleProtocol(item.protocol)}
								/>
								<span class="proto-name">{item.label}</span>
								<span class="proto-count mono">{item.count}</span>
							</label>
						{/each}
					</div>
				</div>
			{/if}
		</div>
	{/if}
{/snippet}

{#snippet searchInputControl()}
	{#if onSearchChange}
		<div class="member-search-box">
			<Search size={13} class="member-search-icon" aria-hidden="true" />
			<input
				type="text"
				class="member-search-input"
				placeholder={m.tunnels_search_placeholder()}
				aria-label={m.tunnels_search_placeholder()}
				value={searchQuery}
				oninput={(e) => onSearchChange((e.currentTarget as HTMLInputElement).value)}
			/>
			{#if searchQuery.length > 0}
				<button
					type="button"
					class="member-search-clear"
					aria-label={m.subscriptions_members_search_clear()}
					onclick={() => onSearchChange('')}
				>
					<X size={13} aria-hidden="true" />
				</button>
			{/if}
		</div>
	{/if}
{/snippet}

{#snippet toTunnelBtn(member: SubscriptionMember)}
	{#if oncreatetunnel}
		{@const creatingThis = creatingTunnelTag === member.tag}
		<button
			type="button"
			class="to-tunnel-btn"
			title={m.subscriptions_list_to_tunnel_title()}
			aria-label={m.subscriptions_list_to_tunnel_aria({ name: member.label || member.tag })}
			disabled={creatingTunnelTag !== null}
			onclick={(e) => {
				e.stopPropagation();
				oncreatetunnel(member);
			}}
		>
			<Plus size={14} aria-hidden="true" />
			{creatingThis ? m.subscriptions_list_to_tunnel_creating() : m.subscriptions_list_to_tunnel_btn()}
		</button>
	{/if}
{/snippet}

{#snippet removeOrExcludeBtn(member: SubscriptionMember)}
	{#if isInline}
		<button
			type="button"
			class="member-remove-btn"
			title={m.subscriptions_list_delete()}
			aria-label={m.subscriptions_list_delete_aria({ name: member.label || member.tag })}
			disabled={removingTag !== null}
			onclick={(e) => {
				e.stopPropagation();
				onremove(member);
			}}
		>
			<Trash2 size={14} aria-hidden="true" />
			{m.common_delete()}
		</button>
	{:else if isUrlSub && !selectMode}
		<button
			type="button"
			class="ex-btn"
			title={m.subscriptions_list_exclude()}
			aria-label={m.subscriptions_list_exclude_aria({ name: member.label || member.tag })}
			disabled={excluding}
			onclick={(e) => {
				e.stopPropagation();
				onexclude(member.tag);
			}}
		>
			<Ban size={14} aria-hidden="true" />
			{m.subscriptions_list_exclude_btn()}
		</button>
	{/if}
{/snippet}

{#snippet tableRowCells(member: SubscriptionMember)}
	{#if selectMode}
		<span class="sel-cell">
			<input
				type="checkbox"
				class="ex-check"
				checked={selected.has(member.tag)}
				tabindex="-1"
				aria-label={m.subscriptions_list_select_aria({ name: member.label || member.tag })}
				onclick={(e) => {
					e.stopPropagation();
					ontoggle(member.tag);
				}}
			/>
		</span>
	{/if}
	<SubscriptionMemberCard
		{member}
		active={member.tag === effectiveActiveMember}
		switching={switching === member.tag}
		disabled={switching !== null}
		{isAutoSelect}
		{selectMode}
		{onpick}
		layout="list"
	/>
	{#if !selectMode}
		<div class="row-actions">
			{@render toTunnelBtn(member)}
			{@render removeOrExcludeBtn(member)}
		</div>
	{/if}
{/snippet}

{#if layout === 'list'}
	<div class="awg-list-table member-list-table" class:with-actions={!selectMode} class:with-select={selectMode}>
		<div class="awg-list-table-track">
			<div class="sbx-member-list-row sbx-member-list-row--head" class:with-actions={!selectMode} class:with-select={selectMode}>
				{#if selectMode}<span class="h-sel" aria-hidden="true"></span>{/if}
				<TableSortHeader label="Delay" sortKey={'delay'} activeSortKey={sortKey} {sortAsc} onchange={(k) => onSortToggle?.(k as SubscriptionMemberSortKey)} />
				<TableSortHeader label={m.subscriptions_list_col_server()} sortKey={'server'} activeSortKey={sortKey} {sortAsc} onchange={(k) => onSortToggle?.(k as SubscriptionMemberSortKey)} />
				<TableSortHeader label={m.subscriptions_list_col_protocol()} sortKey={'protocol'} activeSortKey={sortKey} {sortAsc} onchange={(k) => onSortToggle?.(k as SubscriptionMemberSortKey)} />
				<TableSortHeader label="Ping" sortKey={'ping'} activeSortKey={sortKey} {sortAsc} onchange={(k) => onSortToggle?.(k as SubscriptionMemberSortKey)} />
				<TableSortHeader label={m.subscriptions_list_col_tag()} sortKey={'tag'} activeSortKey={sortKey} {sortAsc} onchange={(k) => onSortToggle?.(k as SubscriptionMemberSortKey)} />
				<TableSortHeader label={m.subscriptions_list_col_status()} sortKey={'status'} activeSortKey={sortKey} {sortAsc} onchange={(k) => onSortToggle?.(k as SubscriptionMemberSortKey)} />
				{#if !selectMode}<span class="h-act" aria-hidden="true"></span>{/if}
			</div>
			<div class="sbx-member-list-row member-list-meta-row" class:with-actions={!selectMode} class:with-select={selectMode}>
				<div class="meta-delay-info mono">
					<span class="meta-lbl">{m.subscriptions_list_min_delay()}</span>
					{#if minDelayMs !== null}
						<span class="meta-val"><strong>{minDelayMs} ms</strong></span>
						<span class="meta-hint" title={m.subscriptions_list_min_delay_hint()}>{m.subscriptions_list_min_delay_hint()}</span>
					{:else}
						<span class="meta-empty">—</span>
					{/if}
				</div>
				{#if showControls}
					<div class="meta-filter-cell">
						{@render protocolFilterControl()}
					</div>
				{/if}
				{#if hasActiveFilter}
					<div class="meta-filter-summary">
						<span class="meta-shown mono">
							{m.subscriptions_members_filter_shown({ shown: members.length, total: sourceTotal })}
						</span>
						{#if onResetFilters}
							<button type="button" class="meta-reset-btn" onclick={onResetFilters}>
								{m.subscriptions_members_filter_reset()}
							</button>
						{/if}
					</div>
				{/if}
				{#if showControls}
					<div class="meta-search-cell">
						{@render searchInputControl()}
					</div>
				{/if}
			</div>
			{#if members.length === 0}
				<div class="filter-empty">
					<span>{m.subscriptions_members_filter_empty()}</span>
					{#if hasActiveFilter && onResetFilters}
						<Button variant="ghost" size="sm" onclick={onResetFilters}>
							{m.subscriptions_members_filter_reset()}
						</Button>
					{/if}
				</div>
			{:else}
				{#each members as member (member.tag)}
					{#if selectMode}
						<div
							class="member-list-line with-select"
							class:active-line={member.tag === effectiveActiveMember}
							class:switching-line={switching === member.tag}
							class:is-disabled={switching !== null}
							role="button"
							tabindex={switching !== null ? -1 : 0}
							aria-pressed={selected.has(member.tag)}
							onclick={() => {
								if (switching !== null) return;
								ontoggle(member.tag);
							}}
							onkeydown={(e) => handleRowKeydown(e, member.tag)}
						>
							{@render tableRowCells(member)}
						</div>
					{:else}
						<div
							class="member-list-line with-actions"
							class:active-line={member.tag === effectiveActiveMember}
							class:switching-line={switching === member.tag}
						>
							{@render tableRowCells(member)}
						</div>
					{/if}
				{/each}
			{/if}
		</div>
	</div>
{:else}
	{#if showControls || onSortSelect}
		<div class="card-toolbar">
			<div class="card-toolbar-left">
				{@render searchInputControl()}
				{@render protocolFilterControl()}
				{#if hasActiveFilter}
					<span class="meta-shown mono">
						{m.subscriptions_members_filter_shown({ shown: members.length, total: sourceTotal })}
					</span>
					{#if onResetFilters}
						<button type="button" class="meta-reset-btn" onclick={onResetFilters}>
							{m.subscriptions_members_filter_reset()}
						</button>
					{/if}
				{/if}
			</div>
			{#if onSortSelect && onSortDirToggle}
				<div class="card-toolbar-sort" aria-label={m.subscriptions_members_sort_label()}>
					<TunnelTableSortControls
						{searchQuery}
						{sortKey}
						{sortAsc}
						options={sortOptions}
						showSearch={false}
						showSort={true}
						onSearchChange={(q) => onSearchChange?.(q)}
						onSortChange={(k) => onSortSelect(k as SubscriptionMemberSortKey | null)}
						onToggleDir={onSortDirToggle}
					/>
				</div>
			{/if}
		</div>
	{/if}
	{#if members.length === 0}
		<div class="filter-empty filter-empty--box">
			<span>{m.subscriptions_members_filter_empty()}</span>
			{#if hasActiveFilter && onResetFilters}
				<Button variant="ghost" size="sm" onclick={onResetFilters}>
					{m.subscriptions_members_filter_reset()}
				</Button>
			{/if}
		</div>
	{:else}
		<div class="grid">
			{#each members as member (member.tag)}
				{#if selectMode}
					<div
						class="member-slot member-slot--select"
						class:member-slot--active={member.tag === effectiveActiveMember}
						role="button"
						tabindex={switching !== null ? -1 : 0}
						aria-pressed={selected.has(member.tag)}
						onclick={() => {
							if (switching !== null) return;
							ontoggle(member.tag);
						}}
						onkeydown={(e) => handleRowKeydown(e, member.tag)}
					>
						<span class="sel-cell sel-cell--grid">
							<input
								type="checkbox"
								class="ex-check"
								checked={selected.has(member.tag)}
								tabindex="-1"
								aria-label={m.subscriptions_list_select_aria({ name: member.label || member.tag })}
								onclick={(e) => {
									e.stopPropagation();
									ontoggle(member.tag);
								}}
							/>
						</span>
						<SubscriptionMemberCard
							{member}
							active={member.tag === effectiveActiveMember}
							switching={switching === member.tag}
							disabled={switching !== null}
							{isAutoSelect}
							{selectMode}
							{onpick}
						/>
					</div>
				{:else}
					<div
						class="member-slot"
						class:member-slot--active={member.tag === effectiveActiveMember}
					>
						<SubscriptionMemberCard
							{member}
							active={member.tag === effectiveActiveMember}
							switching={switching === member.tag}
							disabled={switching !== null}
							{isAutoSelect}
							{selectMode}
							{onpick}
						/>
						<div class="card-actions">
							<div class="card-slot-left">
								{#if member.tag !== effectiveActiveMember && switching !== member.tag && !isAutoSelect}
									<button
										type="button"
										class="card-activate-btn"
										disabled={switching !== null}
										aria-label={m.subscriptions_list_activate_aria({ name: member.label || member.tag })}
										onclick={(e) => {
											e.stopPropagation();
											onpick(member.tag);
										}}
									>
										{m.subscriptions_list_activate_btn()}
									</button>
								{/if}
							</div>
							<div class="card-slot-center">
								{@render toTunnelBtn(member)}
							</div>
							<div class="card-slot-right">
								{@render removeOrExcludeBtn(member)}
							</div>
						</div>
					</div>
				{/if}
			{/each}
		</div>
	{/if}
{/if}

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr));
		gap: 0.8rem;
		justify-items: stretch;
		align-items: stretch;
	}
	.mono { font-family: var(--font-mono, ui-monospace, monospace); }

	.member-slot {
		position: relative;
		display: flex;
		flex-direction: column;
		min-width: 0;
		border: 1px solid var(--color-border);
		border-radius: 10px;
		background: var(--color-bg-secondary);
		overflow: hidden;
		transition: border-color 0.15s ease, background 0.15s ease;
	}
	.member-slot--active {
		border-color: #3fb950;
		background: rgba(63, 185, 80, 0.06);
	}
	.member-slot--select {
		padding-left: 2rem;
		cursor: pointer;
		user-select: none;
	}
	.member-slot--select:hover {
		border-color: var(--color-accent);
	}

	.card-actions {
		display: grid;
		grid-template-columns: 1fr auto 1fr;
		align-items: center;
		gap: 0.3rem;
		padding: 0.45rem 0.65rem;
		border-top: 1px solid var(--color-border);
		background: rgba(0, 0, 0, 0.08);
	}
	.card-slot-left {
		justify-self: start;
		min-width: 0;
	}
	.card-slot-center {
		justify-self: center;
	}
	.card-slot-right {
		justify-self: end;
		min-width: 0;
	}
	.card-actions .to-tunnel-btn,
	.card-actions .ex-btn,
	.card-actions .member-remove-btn {
		padding: 0.3rem 0.4rem;
	}
	.row-actions {
		display: inline-flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.25rem;
	}

	.card-activate-btn,
	.to-tunnel-btn,
	.member-remove-btn,
	.ex-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 4px;
		padding: 0.35rem 0.5rem;
		border: none;
		border-radius: var(--radius-sm);
		background: transparent;
		font: inherit;
		font-size: var(--sbx-card-action);
		font-weight: 500;
		white-space: nowrap;
		cursor: pointer;
		flex-shrink: 0;
		transition: background var(--t-fast) ease, color var(--t-fast) ease, border-color var(--t-fast) ease;
	}
	.card-activate-btn {
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		padding: 0.25rem 0.55rem;
		font-size: var(--sbx-card-note);
	}
	.card-activate-btn:hover:not(:disabled) {
		border-color: var(--color-accent);
		color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	.to-tunnel-btn {
		color: var(--color-text-muted);
	}
	.to-tunnel-btn:hover:not(:disabled) {
		color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	.member-remove-btn,
	.ex-btn {
		color: var(--color-text-muted);
	}
	.member-remove-btn:hover:not(:disabled),
	.ex-btn:hover:not(:disabled) {
		color: var(--color-error);
		background: var(--color-error-tint);
	}
	.card-activate-btn:disabled,
	.to-tunnel-btn:disabled,
	.member-remove-btn:disabled,
	.ex-btn:disabled {
		cursor: not-allowed;
		opacity: 0.5;
	}
	.card-activate-btn:focus-visible,
	.to-tunnel-btn:focus-visible,
	.member-remove-btn:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}
	.ex-btn:focus-visible {
		outline: 2px solid var(--color-error);
		outline-offset: 2px;
	}

	.sel-cell {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
	}
	.sel-cell--grid {
		position: absolute;
		left: 8px;
		top: 50%;
		transform: translateY(-50%);
		z-index: 2;
	}
	.ex-check {
		width: 16px;
		height: 16px;
		accent-color: var(--color-accent);
		cursor: pointer;
		margin: 0;
	}

	/* Filter & search toolbar controls */
	.card-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem 0.75rem;
		flex-wrap: wrap;
		margin-bottom: 0.75rem;
	}
	.card-toolbar-left {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		min-width: 0;
		flex: 1 1 260px;
	}
	.card-toolbar-sort {
		flex-shrink: 0;
	}
	.member-search-box {
		position: relative;
		display: inline-flex;
		align-items: center;
		width: 220px;
		min-width: 170px;
		max-width: 240px;
		flex: 1 1 180px;
	}
	.member-search-box :global(.member-search-icon) {
		position: absolute;
		left: 0.5rem;
		color: var(--color-text-muted);
		pointer-events: none;
	}
	.member-search-input {
		width: 100%;
		height: 30px;
		box-sizing: border-box;
		padding: 0 1.65rem 0 1.65rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-family: inherit;
		font-size: 0.82rem;
	}
	.member-search-input::placeholder {
		color: var(--color-text-muted);
	}
	.member-search-input:focus {
		outline: none;
		border-color: var(--color-accent);
	}
	.member-search-clear {
		position: absolute;
		right: 0.25rem;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 22px;
		height: 22px;
		padding: 0;
		border: none;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
	}
	.member-search-clear:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-tertiary);
	}

	.proto-filter-wrap {
		position: relative;
		display: inline-flex;
	}
	.proto-filter-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		height: 30px;
		padding: 0 0.6rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font: inherit;
		font-size: 0.8rem;
		font-weight: 500;
		cursor: pointer;
		white-space: nowrap;
		transition: border-color var(--t-fast) ease, background var(--t-fast) ease, color var(--t-fast) ease;
	}
	.proto-filter-btn:hover {
		border-color: var(--color-accent);
	}
	.proto-filter-btn.active {
		border-color: var(--color-accent);
		color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	.proto-filter-badge {
		font-size: 0.72rem;
		padding: 0.05rem 0.35rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.proto-filter-popover {
		position: fixed;
		z-index: 60;
		min-width: 220px;
		max-width: 280px;
		max-height: 280px;
		overflow-y: auto;
		padding: 0.45rem;
		border: 1px solid var(--color-border);
		border-radius: 8px;
		background: var(--color-bg-secondary);
		box-shadow: 0 10px 28px rgba(0, 0, 0, 0.35);
	}
	.proto-popover-actions {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		padding-bottom: 0.4rem;
		margin-bottom: 0.35rem;
		border-bottom: 1px solid var(--color-border);
	}
	.proto-bulk-btn {
		flex: 1;
		padding: 0.25rem 0.45rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font: inherit;
		font-size: 0.75rem;
		font-weight: 500;
		cursor: pointer;
	}
	.proto-bulk-btn:hover {
		border-color: var(--color-accent);
		color: var(--color-accent);
	}
	.proto-options {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}
	.proto-option {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		padding: 0.3rem 0.35rem;
		border-radius: var(--radius-sm);
		cursor: pointer;
		font-size: 0.82rem;
		color: var(--color-text-primary);
	}
	.proto-option:hover {
		background: var(--color-bg-tertiary);
	}
	.proto-check {
		width: 15px;
		height: 15px;
		accent-color: var(--color-accent);
		cursor: pointer;
		margin: 0;
	}
	.proto-name {
		flex: 1;
		min-width: 0;
	}
	.proto-count {
		font-size: 0.74rem;
		color: var(--color-text-muted);
	}

	.meta-shown {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		padding: 0.1rem 0.4rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-tertiary);
	}
	.meta-reset-btn {
		border: none;
		background: transparent;
		color: var(--color-accent);
		font: inherit;
		font-size: 0.76rem;
		cursor: pointer;
		padding: 0.1rem 0.25rem;
		text-decoration: underline;
	}
	.meta-reset-btn:hover {
		opacity: 0.85;
	}

	.filter-empty {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 0.65rem;
		flex-wrap: wrap;
		padding: 1.5rem 1rem;
		color: var(--color-text-muted);
		font-size: 0.85rem;
	}
	.filter-empty--box {
		border: 1px dashed var(--color-border);
		border-radius: 10px;
	}

	@media (max-width: 900px) {
		.grid {
			grid-template-columns: repeat(auto-fit, minmax(min(100%, 250px), 1fr));
		}
	}

	@media (max-width: 640px) {
		.grid {
			grid-template-columns: 1fr;
		}
		.card-toolbar {
			align-items: stretch;
		}
		.card-toolbar-left,
		.card-toolbar-sort {
			width: 100%;
		}
		.member-search-box {
			max-width: none;
		}
	}

	/* Table layout */
	.member-list-table {
		border: 1px solid var(--color-border);
		border-radius: 12px;
		background: var(--color-bg-secondary);
		overflow-x: auto;
		overflow-y: hidden;
		margin-top: 0.25rem;
		--awg-list-min-width: 900px;
	}

	.sbx-member-list-row,
	.member-list-line {
		display: grid;
		gap: 0 1rem;
		align-items: center;
		padding: 0.65rem 1rem;
		border-bottom: 1px solid var(--color-border);
		min-width: max(100%, max(var(--awg-list-min-width, 0px), max-content));
	}
	.sbx-member-list-row.with-actions,
	.member-list-line.with-actions {
		grid-template-columns:
			minmax(80px, 0.9fr)
			minmax(0, 1.35fr)
			minmax(0, 1fr)
			minmax(56px, 0.85fr)
			minmax(0, 0.95fr)
			minmax(92px, 0.95fr)
			minmax(145px, max-content);
	}
	.sbx-member-list-row.with-select,
	.member-list-line.with-select {
		grid-template-columns:
			28px
			minmax(80px, 0.9fr)
			minmax(0, 1.35fr)
			minmax(0, 1fr)
			minmax(56px, 0.85fr)
			minmax(0, 0.95fr)
			minmax(92px, 0.95fr);
	}
	.sbx-member-list-row--head {
		background: var(--color-bg-tertiary);
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-muted);
		padding-top: 0.75rem;
		padding-bottom: 0.75rem;
	}
	.sbx-member-list-row--head .h-act,
	.sbx-member-list-row--head .h-sel {
		display: block;
	}
	.member-list-meta-row {
		padding: 0.45rem 1rem;
		border-bottom: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
	}
	.member-list-meta-row.with-actions .meta-delay-info {
		grid-column: 1 / 3;
	}
	.member-list-meta-row.with-actions .meta-filter-cell {
		grid-column: 3 / 4;
		justify-self: start;
	}
	.member-list-meta-row.with-actions .meta-filter-summary {
		grid-column: 4 / 6;
		justify-self: start;
	}
	.member-list-meta-row.with-actions .meta-search-cell {
		grid-column: 6 / -1;
		justify-self: end;
	}
	.member-list-meta-row.with-select .meta-delay-info {
		grid-column: 1 / 4;
	}
	.member-list-meta-row.with-select .meta-filter-cell {
		grid-column: 4 / 5;
		justify-self: start;
	}
	.member-list-meta-row.with-select .meta-filter-summary {
		grid-column: 5 / 7;
		justify-self: start;
	}
	.member-list-meta-row.with-select .meta-search-cell {
		grid-column: 7 / -1;
		justify-self: end;
	}
	.meta-delay-info {
		display: flex;
		align-items: center;
		flex-wrap: nowrap;
		gap: 0.3rem;
		min-width: 0;
		overflow: hidden;
		white-space: nowrap;
	}
	.meta-filter-summary {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		white-space: nowrap;
		min-width: 0;
	}
	.meta-search-cell {
		display: flex;
		align-items: center;
		justify-content: flex-end;
	}
	.meta-search-cell .member-search-box {
		flex: 0 0 auto;
		width: 210px;
	}
	.member-list-meta-row .meta-lbl {
		text-transform: uppercase;
		letter-spacing: 0.04em;
		font-size: 0.65rem;
		font-weight: 700;
		flex-shrink: 0;
	}
	.member-list-meta-row .meta-val {
		color: var(--color-text-primary);
		flex-shrink: 0;
	}
	.member-list-meta-row .meta-val strong {
		color: #3fb950;
		font-weight: 600;
	}
	.member-list-meta-row .meta-empty {
		color: var(--color-text-muted);
	}
	.member-list-meta-row .meta-hint {
		font-size: 0.7rem;
		opacity: 0.85;
		margin-left: 0.15rem;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	.member-list-line {
		cursor: default;
		user-select: text;
	}
	.member-list-line.with-select {
		cursor: pointer;
		user-select: none;
	}
	.member-list-line :global(.mbr-flatten) {
		display: contents;
	}
	.member-list-line:last-child {
		border-bottom: none;
	}
	.member-list-line.active-line {
		background: rgba(63, 185, 80, 0.06);
	}
	.member-list-line.switching-line {
		opacity: 0.65;
		cursor: wait;
	}
	.member-list-line.is-disabled {
		cursor: not-allowed;
	}
</style>
