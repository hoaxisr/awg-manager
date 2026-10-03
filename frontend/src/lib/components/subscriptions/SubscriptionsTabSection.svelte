<script lang="ts">
	import { m } from '$lib/i18n';
	// Вкладка «Sing-box подписки» страницы туннелей. Выделено из
	// routes/+page.svelte (класс 2 реструктуризации): разметка перенесена
	// дословно, состояние страницы приходит пропсами, сквозные сторы
	// (singboxSubscriptionTableSort) импортируются напрямую.
	import { LoadingSpinner, EmptyState } from '$lib/components/layout';
	import { StatStrip, Stat, LayoutViewToggle, Button, TableSortHeader } from '$lib/components/ui';
	import { TunnelToolbarViewRow } from '$lib/components/tunnels';
	import TunnelSectionHeader from '$lib/components/tunnels/TunnelSectionHeader.svelte';
	import { SingboxInstallBanner } from '$lib/components/singbox';
	import SubscriptionActiveCard from '$lib/components/subscriptions/SubscriptionActiveCard.svelte';
	import SubscriptionCard from '$lib/components/subscriptions/SubscriptionCard.svelte';
	import SubscriptionGroupsSection from '$lib/components/subscriptions/SubscriptionGroupsSection.svelte';
	import { singboxSubscriptionTableSort, type SubscriptionSortKey } from '$lib/stores/tunnelTableSort';
		import { formatBytes } from '$lib/utils/format';
	import { ariaSort } from '$lib/utils/tunnelTableSort';
	import type { SingboxLayoutMode, TunnelRenderMode } from '$lib/constants/singboxLayout';
	import type { Subscription } from '$lib/types';
	import type { SubscriptionActiveCardVM, SubscriptionsTrafficStats } from './subscriptionVMs';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';
	import { showSummary } from '$lib/stores/showSummary';



	interface Props {
		loading: boolean;
		dashboardOn: boolean;
		dashboardSectionsLayout: boolean;
		subscriptionsInitialLoading: boolean;
		subscriptionsFetchFailed: boolean;
		subscriptionsError: string | null;
		subscriptionsList: Subscription[];
		subscriptionsActiveCards: SubscriptionActiveCardVM[];
		sortedFilteredSubscriptionsActiveCards: SubscriptionActiveCardVM[];
		sortedFilteredSubscriptionsListRows: Subscription[];
		singboxSubscriptionsTrafficStats: SubscriptionsTrafficStats;
		singboxSubscriptionsSourceRowCount: number;
		singboxSubscriptionsSearchEmpty: boolean;
		singboxInstalled: boolean;
		singboxStatusLoading: boolean;
		singboxAutoDelayCheckNonce: number;
		showSingboxGridListToggle: boolean;
		effectiveSingboxSubscriptionsEffectiveLayout: SingboxLayoutMode;
		effectiveSingboxSubscriptionsRenderMode: TunnelRenderMode;
		liveActives: Record<string, string>;
		singboxSubscriptionsSearchQuery: string;
		singboxSubscriptionsLayoutMode: SingboxLayoutMode;
		handleSubscriptionSortChange: (key: SubscriptionSortKey) => void;
		openSingboxDetail: (tag: string) => void;
		openWizard: (mode: 'url') => void;
		requestSubscriptionDelete: (id: string) => void;
	}

	let {
		loading,
		dashboardOn,
		dashboardSectionsLayout,
		subscriptionsInitialLoading,
		subscriptionsFetchFailed,
		subscriptionsError,
		subscriptionsList,
		subscriptionsActiveCards,
		sortedFilteredSubscriptionsActiveCards,
		sortedFilteredSubscriptionsListRows,
		singboxSubscriptionsTrafficStats,
		singboxSubscriptionsSourceRowCount,
		singboxSubscriptionsSearchEmpty,
		singboxInstalled,
		singboxStatusLoading,
		singboxAutoDelayCheckNonce,
		showSingboxGridListToggle,
		effectiveSingboxSubscriptionsEffectiveLayout,
		effectiveSingboxSubscriptionsRenderMode,
		liveActives,
		singboxSubscriptionsSearchQuery = $bindable(),
		singboxSubscriptionsLayoutMode = $bindable(),
		handleSubscriptionSortChange,
		openSingboxDetail,
		openWizard,
		requestSubscriptionDelete,
	}: Props = $props();
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

	{#if subscriptionsInitialLoading}
		<div class="loading-centered">
			<LoadingSpinner size="md" message={m.subscriptions_tab_loading()} />
		</div>
	{:else if subscriptionsFetchFailed}
		<EmptyState
			title={m.subscriptions_tab_load_failed()}
			description={subscriptionsError ?? m.subscriptions_tab_load_failed_desc()}
		/>
	{:else}
		{#if !dashboardOn && !singboxStatusLoading}
			<SingboxInstallBanner />
		{/if}

		{#if singboxStatusLoading || singboxInstalled}
			{#if !dashboardOn}
			<div class="tunnels-toolbar">
				<span class="tunnel-count">
					{subscriptionsList.length}
					{m.tunnels_unit_subscriptions({ count: subscriptionsList.length })}
				</span>
				<div class="toolbar-actions">
					<TunnelToolbarViewRow
						sourceRowCount={singboxSubscriptionsSourceRowCount}
						showViewToggle={subscriptionsList.length > 0}
						searchQuery={singboxSubscriptionsSearchQuery}
						onSearchChange={(value) => (singboxSubscriptionsSearchQuery = value)}
					>
						{#snippet viewToggle()}
							<LayoutViewToggle
								value={singboxSubscriptionsLayoutMode}
								showListOption={showSingboxGridListToggle}
								ariaLabel={m.subscriptions_tab_view_aria()}
								onchange={(v) => (singboxSubscriptionsLayoutMode = v)}
							/>
						{/snippet}
					</TunnelToolbarViewRow>
					<Button
						variant="primary"
						size="md"
						onclick={() => openWizard('url')}
						iconBefore={createIcon}
					>
						{m.common_add()}
					</Button>
				</div>
			</div>
			{/if}
			{#if subscriptionsList.length === 0}
				<div class="subscription-empty">
					<div class="subscription-empty-title">{m.subscriptions_tab_empty_title()}</div>
					<p class="subscription-empty-desc">
						{m.subscriptions_tab_empty_desc()}
					</p>
					<Button
						variant="primary"
						size="md"
						onclick={() => openWizard('url')}
						iconBefore={createIcon}
					>
						{m.subscriptions_tab_add()}
					</Button>
				</div>
			{:else}
				{#if !dashboardOn && $showSummary}
					<div class="awg-summary-row">
						<StatStrip>
							<Stat
								value={`${singboxSubscriptionsTrafficStats.activeCount}/${singboxSubscriptionsTrafficStats.count}`}
								label={m.tunnels_unit_subscriptions({ count: singboxSubscriptionsTrafficStats.activeCount })}
								sub={m.singbox_tabs_running_sub({
									active: singboxSubscriptionsTrafficStats.activeCount,
									stopped: Math.max(
										0,
										singboxSubscriptionsTrafficStats.count - singboxSubscriptionsTrafficStats.activeCount,
									),
								})}
							/>
							<Stat
								value={formatBytes(
									singboxSubscriptionsTrafficStats.down + singboxSubscriptionsTrafficStats.up,
								)}
								label={m.singbox_tabs_total_traffic()}
								sub={`↓ ${formatBytes(singboxSubscriptionsTrafficStats.down)} · ↑ ${formatBytes(singboxSubscriptionsTrafficStats.up)}`}
							/>
							<Stat
								value={singboxSubscriptionsTrafficStats.avgDelayMs !== null
									? `${singboxSubscriptionsTrafficStats.avgDelayMs} ms`
									: '—'}
								label={m.singbox_tabs_avg_delay()}
								sub={singboxSubscriptionsTrafficStats.delaySamples > 0
									? m.subscriptions_tab_avg_delay_sub({ count: singboxSubscriptionsTrafficStats.delaySamples })
									: m.subscriptions_tab_no_delay_samples()}
							/>
							<Stat
								value={singboxSubscriptionsTrafficStats.leaderBytes > 0
									? formatBytes(singboxSubscriptionsTrafficStats.leaderBytes)
									: '—'}
								label={m.tunnels_awg_stat_leader()}
								sub={singboxSubscriptionsTrafficStats.leaderBytes > 0
									? m.subscriptions_tab_leader_share({ name: singboxSubscriptionsTrafficStats.leaderName, pct: singboxSubscriptionsTrafficStats.leaderSharePct })
									: '—'}
							/>
						</StatStrip>
					</div>
				{/if}
				{#if effectiveSingboxSubscriptionsRenderMode === 'table'}
				<div class="tunnel-table-wrap">
					<table class="tunnel-data-table singbox-sub-table">
						<colgroup>
							<col class="col-delay" />
							<col class="col-name" />
							<col class="col-active" />
							<col class="col-traffic" />
							<col class="col-ping" />
							<col class="col-actions" />
						</colgroup>
						<thead>
							<tr>
								<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'delay', $singboxSubscriptionTableSort.sortAsc)}>
									<TableSortHeader label="Delay" sortKey={'delay'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
								</th>
								<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'label', $singboxSubscriptionTableSort.sortAsc)}>
									<TableSortHeader label={m.subscriptions_tab_col_subscription()} sortKey={'label'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
								</th>
								<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'active', $singboxSubscriptionTableSort.sortAsc)}>
									<TableSortHeader label={m.subscriptions_active_server()} sortKey={'active'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
								</th>
								<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'traffic', $singboxSubscriptionTableSort.sortAsc)}>
									<TableSortHeader label={m.tunnels_awg_col_traffic()} sortKey={'traffic'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
								</th>
								<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'ping', $singboxSubscriptionTableSort.sortAsc)}>
									<TableSortHeader label="Ping" sortKey={'ping'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
								</th>
								<th class="col-actions">{m.tunnels_awg_col_actions()}</th>
							</tr>
						</thead>
						<tbody>
					{#if sortedFilteredSubscriptionsActiveCards.length > 0}
						{#each sortedFilteredSubscriptionsActiveCards as card, i (card.subscription.id)}
							<SubscriptionActiveCard
								subscription={card.subscription}
								activeMember={card.activeMember}
								autoDelayCheckNonce={singboxAutoDelayCheckNonce}
								autoDelayCheckDelayMs={i * 180}
								layout="list"
								renderMode="table"
								ondetail={(tag) => openSingboxDetail(tag)}
							/>
						{/each}
					{/if}
					{#if sortedFilteredSubscriptionsListRows.length > 0}
						{#if dashboardSectionsLayout}
							<TunnelSectionHeader
								variant="table-row"
								title={m.subscriptions_tab_stopped()}
								count={sortedFilteredSubscriptionsListRows.length}
								countLabel={m.tunnels_unit_subscriptions({ count: sortedFilteredSubscriptionsListRows.length })}
								colspan={6}
							/>
						{:else}
							<tr class="tunnel-section-row">
								<td colspan="6">{m.subscriptions_tab_stopped_count({ count: sortedFilteredSubscriptionsListRows.length })}</td>
							</tr>
						{/if}
						{#each sortedFilteredSubscriptionsListRows as sub (sub.id)}
							<SubscriptionCard
								subscription={sub}
								liveActiveMember={liveActives[sub.id] || null}
								layout="list"
								renderMode="table"
								ondelete={requestSubscriptionDelete}
								ondetail={(tag) => openSingboxDetail(tag)}
							/>
						{/each}
					{/if}
					{#if singboxSubscriptionsSearchEmpty}
						<tr class="tunnel-empty-row">
							<td colspan="6">{m.tunnels_dashboard_empty_title()}</td>
						</tr>
					{/if}
						</tbody>
					</table>
				</div>
				{:else if effectiveSingboxSubscriptionsRenderMode === 'list-card'}
				{#snippet subscriptionActiveListCards()}
					{#each sortedFilteredSubscriptionsActiveCards as card, i (card.subscription.id)}
						<SubscriptionActiveCard
							subscription={card.subscription}
							activeMember={card.activeMember}
							autoDelayCheckNonce={singboxAutoDelayCheckNonce}
							autoDelayCheckDelayMs={i * 180}
							layout="list"
							renderMode="list-card"
							ondetail={(tag) => openSingboxDetail(tag)}
						/>
					{/each}
				{/snippet}
				{#snippet subscriptionStoppedListCards()}
					{#each sortedFilteredSubscriptionsListRows as sub (sub.id)}
						<SubscriptionCard
							subscription={sub}
							liveActiveMember={liveActives[sub.id] || null}
							layout="list"
							renderMode="list-card"
							ondelete={requestSubscriptionDelete}
							ondetail={(tag) => openSingboxDetail(tag)}
						/>
					{/each}
				{/snippet}
				{#if dashboardOn}
					{#if sortedFilteredSubscriptionsActiveCards.length > 0}
						<div class="tunnel-grid tunnel-grid--list">
							{@render subscriptionActiveListCards()}
						</div>
					{/if}
					{#if sortedFilteredSubscriptionsListRows.length > 0}
						{#if dashboardSectionsLayout}
							<TunnelSectionHeader
								nested
								title={m.subscriptions_tab_stopped()}
								count={sortedFilteredSubscriptionsListRows.length}
								countLabel={m.tunnels_unit_subscriptions({ count: sortedFilteredSubscriptionsListRows.length })}
							/>
						{/if}
						<div class="tunnel-grid tunnel-grid--list">
							{@render subscriptionStoppedListCards()}
						</div>
					{/if}
				{:else}
				<div class="tunnel-grid tunnel-grid--list">
					{@render subscriptionActiveListCards()}
					{@render subscriptionStoppedListCards()}
				</div>
				{/if}
				{#if singboxSubscriptionsSearchEmpty}
					<p class="tunnel-list-empty">{m.tunnels_dashboard_empty_title()}</p>
				{/if}
				{:else}
				{#if subscriptionsActiveCards.length > 0}
					<div
						class="tunnel-grid"
						class:tunnel-grid--dense={effectiveSingboxSubscriptionsEffectiveLayout === 'dense'}
						class:tunnel-grid--compact={effectiveSingboxSubscriptionsEffectiveLayout === 'compact'}
					>
						{#each sortedFilteredSubscriptionsActiveCards as card, i (card.subscription.id)}
							<SubscriptionActiveCard
								subscription={card.subscription}
								activeMember={card.activeMember}
								autoDelayCheckNonce={singboxAutoDelayCheckNonce}
								autoDelayCheckDelayMs={i * 180}
								layout={effectiveSingboxSubscriptionsEffectiveLayout}
								renderMode={effectiveSingboxSubscriptionsRenderMode}
								ondetail={(tag) => openSingboxDetail(tag)}
							/>
						{/each}
					</div>
				{/if}
				{#if sortedFilteredSubscriptionsListRows.length > 0}
					{#snippet subscriptionStoppedGrid()}
						<div
							class="tunnel-grid"
							class:tunnel-grid--dense={effectiveSingboxSubscriptionsEffectiveLayout === 'dense'}
							class:tunnel-grid--compact={effectiveSingboxSubscriptionsEffectiveLayout === 'compact'}
						>
							{#each sortedFilteredSubscriptionsListRows as sub (sub.id)}
								<SubscriptionCard
									subscription={sub}
									liveActiveMember={liveActives[sub.id] || null}
									layout={effectiveSingboxSubscriptionsEffectiveLayout}
									renderMode={effectiveSingboxSubscriptionsRenderMode}
									ondelete={requestSubscriptionDelete}
									ondetail={(tag) => openSingboxDetail(tag)}
								/>
							{/each}
						</div>
					{/snippet}
					{#if dashboardSectionsLayout}
						<TunnelSectionHeader
							nested
							title={m.subscriptions_tab_stopped()}
							count={sortedFilteredSubscriptionsListRows.length}
							countLabel={m.tunnels_unit_subscriptions({ count: sortedFilteredSubscriptionsListRows.length })}
						/>
						{@render subscriptionStoppedGrid()}
					{:else}
						<div
							class="external-section"
							class:singbox-sub-inactive-section={sortedFilteredSubscriptionsActiveCards.length === 0}
						>
							<h2 class="section-title">{m.subscriptions_tab_stopped()}</h2>
							{@render subscriptionStoppedGrid()}
						</div>
					{/if}
				{/if}
				{#if singboxSubscriptionsSearchEmpty}
					<p class="tunnel-list-empty">{m.tunnels_dashboard_empty_title()}</p>
				{/if}
				{/if}
			{/if}
			{#if !dashboardOn}
				<SubscriptionGroupsSection subscriptions={subscriptionsList} />
			{/if}
		{/if}
	{/if}


<style>

	/* ── D7: drag-reorder (общее pointer-ядро sb-router/reorderDrag).
	   Движок вертикальный, поэтому на время активного drag сетка
	   схлопывается в одну колонку — индексы вставки и индикатор
	   становятся однозначными на любой плотности. ── */

	/* Toolbar (count + actions row above the tunnel grid) */
	.tunnels-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 1rem;
	}

	.tunnel-count {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.toolbar-actions {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.toolbar-actions :global(.btn.size-md) {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		box-sizing: border-box;
		height: 32px;
		min-height: 32px;
		max-height: 32px;
		padding-block: 0;
	}

	.toolbar-actions :global(.btn.variant-primary:hover:not(:disabled):not(.is-disabled)) {
		background: transparent;
		color: var(--color-accent);
		border-color: var(--color-accent);
		filter: none;
	}

	.external-section {
		margin-top: 2rem;
		padding-top: 1.5rem;
		border-top: 1px solid var(--border);
	}

	.section-title {
		font-size: 1rem;
		font-weight: 600;
		color: var(--text-secondary);
		margin-bottom: 1rem;
	}

	.subscription-empty {
		padding: 3rem 1.5rem;
		text-align: center;
		border: 1px dashed var(--color-border);
		border-radius: 6px;
		margin-top: 0.5rem;
	}

	@media (max-width: 760px) {
	.tunnels-toolbar {
			flex-direction: column;
			align-items: stretch;
			gap: 0.75rem;
		}
	.toolbar-actions {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			align-items: stretch;
			gap: 0.5rem;
			width: 100%;
		}
	.toolbar-actions :global(.toolbar-view-row) {
			grid-column: 1 / -1;
		}
	.toolbar-actions > :global(.btn) {
			width: 100%;
			min-height: 32px;
		}
	.toolbar-actions > :global(.btn:only-of-type) {
			grid-column: 1 / -1;
		}
}
</style>
