<script lang="ts">
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
	import { formatRunningSub, pluralForm, SUBSCRIPTION_WORDS } from '$lib/utils/pluralize';
	import { formatBytes } from '$lib/utils/format';
	import { ariaSort } from '$lib/utils/tunnelTableSort';
	import type { SingboxLayoutMode, TunnelRenderMode } from '$lib/constants/singboxLayout';
	import type { Subscription } from '$lib/types';
	import type { MihomoNativeSubscription, MihomoRuntimeProvider, MihomoRuntimeProxy } from '$lib/types';
	import type { SubscriptionActiveCardVM, SubscriptionsTrafficStats } from './subscriptionVMs';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';
	import { showSummary } from '$lib/stores/showSummary';
	import MihomoNativeResourceCard from '$lib/components/mihomo/MihomoNativeResourceCard.svelte';
	import { singboxTraffic } from '$lib/stores/singbox';



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
		singboxInstalled: boolean;
		singboxStatusLoading: boolean;
		singboxAutoDelayCheckNonce: number;
		showSingboxGridListToggle: boolean;
		effectiveSingboxSubscriptionsEffectiveLayout: SingboxLayoutMode;
		effectiveSingboxSubscriptionsRenderMode: TunnelRenderMode;
		liveActives: Record<string, string>;
		mihomoSubscriptions?: MihomoNativeSubscription[];
		mihomoRuntimeProxies?: Record<string, MihomoRuntimeProxy>;
		mihomoRuntimeProviders?: Record<string, MihomoRuntimeProvider>;
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
		singboxInstalled,
		singboxStatusLoading,
		singboxAutoDelayCheckNonce,
		showSingboxGridListToggle,
		effectiveSingboxSubscriptionsEffectiveLayout,
		effectiveSingboxSubscriptionsRenderMode,
		liveActives,
		mihomoSubscriptions = [],
		mihomoRuntimeProxies = {},
		mihomoRuntimeProviders = {},
		singboxSubscriptionsSearchQuery = $bindable(),
		singboxSubscriptionsLayoutMode = $bindable(),
		handleSubscriptionSortChange,
		openSingboxDetail,
		openWizard,
		requestSubscriptionDelete,
	}: Props = $props();

	let visibleMihomoSubscriptions = $derived.by(() => {
		if (dashboardOn) return [];
		const query = singboxSubscriptionsSearchQuery.trim().toLowerCase();
		return mihomoSubscriptions.filter((subscription) => {
			if (!query) return true;
			const group = subscription.groupName ? mihomoRuntimeProxies[subscription.groupName] : undefined;
			const provider = subscription.providerName ? mihomoRuntimeProviders[subscription.providerName] : undefined;
			return [subscription.name, subscription.url ?? '', subscription.groupName ?? '', group?.now ?? '', ...(provider?.proxies ?? []).map((proxy) => proxy.name)]
				.some((value) => value.toLowerCase().includes(query));
		});
	});
	let totalSubscriptionCount = $derived(subscriptionsList.length + (dashboardOn ? 0 : mihomoSubscriptions.length));
	let combinedSearchEmpty = $derived(
		singboxSubscriptionsSearchQuery.trim() !== '' && sortedFilteredSubscriptionsActiveCards.length === 0 && sortedFilteredSubscriptionsListRows.length === 0 && visibleMihomoSubscriptions.length === 0,
	);
	let mihomoActiveCount = $derived(
		mihomoSubscriptions.filter((subscription) => {
			const group = subscription.groupName ? mihomoRuntimeProxies[subscription.groupName] : undefined;
			const active = group?.now ? mihomoRuntimeProxies[group.now] : undefined;
			return subscription.enabled && Boolean(active) && active?.alive !== false;
		}).length,
	);
	let mihomoDelayValues = $derived(
		mihomoSubscriptions.map((subscription) => {
			const group = subscription.groupName ? mihomoRuntimeProxies[subscription.groupName] : undefined;
			return group?.now ? mihomoRuntimeProxies[group.now]?.history?.at(-1)?.delay ?? 0 : 0;
		}).filter((delay) => delay > 0),
	);
	let combinedAverageDelay = $derived.by(() => {
		const samples = [...mihomoDelayValues];
		if (singboxSubscriptionsTrafficStats.avgDelayMs !== null) samples.push(singboxSubscriptionsTrafficStats.avgDelayMs);
		return samples.length ? Math.round(samples.reduce((sum, value) => sum + value, 0) / samples.length) : null;
	});
	let mihomoTraffic = $derived.by(() => mihomoSubscriptions.reduce((total, subscription) => {
		const traffic = subscription.groupName ? $singboxTraffic.get(subscription.groupName) : undefined;
		return { down: total.down + (traffic?.download ?? 0), up: total.up + (traffic?.upload ?? 0) };
	}, { down: 0, up: 0 }));
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

	{#if subscriptionsInitialLoading && mihomoSubscriptions.length === 0}
		<div class="loading-centered">
			<LoadingSpinner size="md" message="Загружаем подписки..." />
		</div>
	{:else if subscriptionsFetchFailed && mihomoSubscriptions.length === 0}
		<EmptyState
			title="Не удалось загрузить подписки"
			description={subscriptionsError ?? 'Проверьте соединение с роутером и обновите страницу.'}
		/>
	{:else}
		{#if !dashboardOn && !singboxStatusLoading}
			<SingboxInstallBanner />
		{/if}

		{#if singboxStatusLoading || singboxInstalled || mihomoSubscriptions.length > 0}
			{#if !dashboardOn}
			<div class="tunnels-toolbar">
				<span class="tunnel-count">
					{totalSubscriptionCount}
					{pluralForm(totalSubscriptionCount, SUBSCRIPTION_WORDS)}
				</span>
				<div class="toolbar-actions">
					<TunnelToolbarViewRow
						sourceRowCount={singboxSubscriptionsSourceRowCount + mihomoSubscriptions.length}
						showViewToggle={totalSubscriptionCount > 0}
						searchQuery={singboxSubscriptionsSearchQuery}
						onSearchChange={(value) => (singboxSubscriptionsSearchQuery = value)}
					>
						{#snippet viewToggle()}
							<LayoutViewToggle
								value={singboxSubscriptionsLayoutMode}
								showListOption={showSingboxGridListToggle}
								ariaLabel="Вид подписок"
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
						Добавить
					</Button>
				</div>
			</div>
			{/if}
			{#if totalSubscriptionCount === 0}
				<div class="subscription-empty">
					<div class="subscription-empty-title">Нет подписок</div>
					<p class="subscription-empty-desc">
						Добавьте подписку — мастер скачает список серверов и создаст selector-туннель.
					</p>
					<Button
						variant="primary"
						size="md"
						onclick={() => openWizard('url')}
						iconBefore={createIcon}
					>
						Добавить подписку
					</Button>
				</div>
			{:else}
				{#if !dashboardOn && $showSummary}
					<div class="awg-summary-row">
						<StatStrip>
							<Stat
								value={`${singboxSubscriptionsTrafficStats.activeCount + mihomoActiveCount}/${totalSubscriptionCount}`}
								label={pluralForm(singboxSubscriptionsTrafficStats.activeCount + mihomoActiveCount, SUBSCRIPTION_WORDS)}
								sub={formatRunningSub(
									singboxSubscriptionsTrafficStats.activeCount + mihomoActiveCount,
									totalSubscriptionCount,
								)}
							/>
							<Stat
								value={formatBytes(
									singboxSubscriptionsTrafficStats.down + singboxSubscriptionsTrafficStats.up + mihomoTraffic.down + mihomoTraffic.up,
								)}
								label="Суммарный трафик"
								sub={`↓ ${formatBytes(singboxSubscriptionsTrafficStats.down + mihomoTraffic.down)} · ↑ ${formatBytes(singboxSubscriptionsTrafficStats.up + mihomoTraffic.up)}`}
							/>
							<Stat
								value={combinedAverageDelay !== null
									? `${combinedAverageDelay} ms`
									: '—'}
								label="Средний delay"
								sub={singboxSubscriptionsTrafficStats.delaySamples + mihomoDelayValues.length > 0
									? `по ${singboxSubscriptionsTrafficStats.delaySamples + mihomoDelayValues.length} активным подпискам`
									: 'нет активных замеров'}
							/>
							<Stat
								value={singboxSubscriptionsTrafficStats.leaderBytes > 0
									? formatBytes(singboxSubscriptionsTrafficStats.leaderBytes)
									: '—'}
								label="Лидер по трафику"
								sub={singboxSubscriptionsTrafficStats.leaderBytes > 0
									? `${singboxSubscriptionsTrafficStats.leaderName} · ${singboxSubscriptionsTrafficStats.leaderSharePct}% всего`
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
									<TableSortHeader label="Подписка" sortKey={'label'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
								</th>
								<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'active', $singboxSubscriptionTableSort.sortAsc)}>
									<TableSortHeader label="Активный сервер" sortKey={'active'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
								</th>
								<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'traffic', $singboxSubscriptionTableSort.sortAsc)}>
									<TableSortHeader label="Трафик" sortKey={'traffic'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
								</th>
								<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'ping', $singboxSubscriptionTableSort.sortAsc)}>
									<TableSortHeader label="Ping" sortKey={'ping'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
								</th>
								<th class="col-actions">Действия</th>
							</tr>
						</thead>
						<tbody>
					{#each visibleMihomoSubscriptions as subscription, i (subscription.id)}
						<MihomoNativeResourceCard kind="subscription" {subscription} runtimeProxies={mihomoRuntimeProxies} runtimeProviders={mihomoRuntimeProviders} layout="list" renderMode="table" autoDelayCheckNonce={singboxAutoDelayCheckNonce} autoDelayCheckDelayMs={i * 180} />
					{/each}
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
								title="Остановлено"
								count={sortedFilteredSubscriptionsListRows.length}
								countLabel={pluralForm(sortedFilteredSubscriptionsListRows.length, SUBSCRIPTION_WORDS)}
								colspan={6}
							/>
						{:else}
							<tr class="tunnel-section-row">
								<td colspan="6">Остановлено · {sortedFilteredSubscriptionsListRows.length}</td>
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
					{#if combinedSearchEmpty}
						<tr class="tunnel-empty-row">
							<td colspan="6">Ничего не найдено</td>
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
								title="Остановлено"
								count={sortedFilteredSubscriptionsListRows.length}
								countLabel={pluralForm(sortedFilteredSubscriptionsListRows.length, SUBSCRIPTION_WORDS)}
							/>
						{/if}
						<div class="tunnel-grid tunnel-grid--list">
							{@render subscriptionStoppedListCards()}
						</div>
					{/if}
				{:else}
				<div class="tunnel-grid tunnel-grid--list">
					{#each visibleMihomoSubscriptions as subscription, i (subscription.id)}
						<MihomoNativeResourceCard kind="subscription" {subscription} runtimeProxies={mihomoRuntimeProxies} runtimeProviders={mihomoRuntimeProviders} layout="list" renderMode="list-card" autoDelayCheckNonce={singboxAutoDelayCheckNonce} autoDelayCheckDelayMs={i * 180} />
					{/each}
					{@render subscriptionActiveListCards()}
					{@render subscriptionStoppedListCards()}
				</div>
				{/if}
				{#if combinedSearchEmpty}
					<p class="tunnel-list-empty">Ничего не найдено</p>
				{/if}
				{:else}
				{#if subscriptionsActiveCards.length > 0 || visibleMihomoSubscriptions.length > 0}
					<div
						class="tunnel-grid"
						class:tunnel-grid--dense={effectiveSingboxSubscriptionsEffectiveLayout === 'dense'}
						class:tunnel-grid--compact={effectiveSingboxSubscriptionsEffectiveLayout === 'compact'}
					>
						{#each visibleMihomoSubscriptions as subscription, i (subscription.id)}
							<MihomoNativeResourceCard kind="subscription" {subscription} runtimeProxies={mihomoRuntimeProxies} runtimeProviders={mihomoRuntimeProviders} layout={effectiveSingboxSubscriptionsEffectiveLayout} renderMode={effectiveSingboxSubscriptionsRenderMode} autoDelayCheckNonce={singboxAutoDelayCheckNonce} autoDelayCheckDelayMs={i * 180} />
						{/each}
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
							title="Остановлено"
							count={sortedFilteredSubscriptionsListRows.length}
							countLabel={pluralForm(sortedFilteredSubscriptionsListRows.length, SUBSCRIPTION_WORDS)}
						/>
						{@render subscriptionStoppedGrid()}
					{:else}
						<div
							class="external-section"
							class:singbox-sub-inactive-section={sortedFilteredSubscriptionsActiveCards.length === 0}
						>
							<h2 class="section-title">Остановлено</h2>
							{@render subscriptionStoppedGrid()}
						</div>
					{/if}
				{/if}
				{#if combinedSearchEmpty}
					<p class="tunnel-list-empty">Ничего не найдено</p>
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
