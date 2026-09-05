<script lang="ts">
	// Flat-режим дашборда туннелей — выделено из routes/+page.svelte
	// (класс 2): снипеты карточек всех видов + тулбар/сетка/группировка по
	// тегам. Состояние страницы приходит live-контекстом (ctx); ghost-слой
	// drag-переупорядочивания остался в странице (fixed-позиционирование).
	import { TunnelCard, ExternalTunnelCard, SystemTunnelCard } from '$lib/components/tunnels';
	import DashboardToolbar from '$lib/components/tunnels/DashboardToolbar.svelte';
	import { Button, StoreStatusBadge } from '$lib/components/ui';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';
	import { tunnelDashboardOrderMode, tunnelDashboardTags, tunnelDashboardGroupMode } from '$lib/stores/tunnelDashboardPrefs';
	import { getItemTags } from '$lib/utils/tunnelDashboardTags';
	import TunnelTagChips from '$lib/components/tunnels/TunnelTagChips.svelte';
	import { tunnels } from '$lib/stores/tunnels';
	import { goto } from '$app/navigation';
	import { tunnelDashboardLayout, tunnelDashboardView } from '$lib/stores/tunnelDashboardMode';
	import DashboardSummary from '$lib/components/tunnels/DashboardSummary.svelte';
	import { showSummary } from '$lib/stores/showSummary';
	import TunnelSectionHeader from '$lib/components/tunnels/TunnelSectionHeader.svelte';
	import { SingboxInstallBanner, SingboxTunnelCard } from '$lib/components/singbox';
	import { Awg3TunnelCard } from '$lib/components/awg3';
	import SubscriptionActiveCard from '$lib/components/subscriptions/SubscriptionActiveCard.svelte';
	import SubscriptionCard from '$lib/components/subscriptions/SubscriptionCard.svelte';
	import { EmptyState } from '$lib/components/layout';
	import { pluralForm, TUNNEL_WORDS } from '$lib/utils/pluralize';
	import { GripVertical, Download } from 'lucide-svelte';
	import type { TunnelDashboardFlatItem } from '$lib/utils/tunnelDashboardFlat';
	import type { DashboardFlatContext } from './dashboardFlatContext';

	let { ctx }: { ctx: DashboardFlatContext } = $props();
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

{#snippet exportIcon()}
	<Download size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#snippet dashboardFlatCard(item: TunnelDashboardFlatItem, suppressAutoCheck: boolean = false)}
	{#if item.kind === 'awg-managed'}
		<TunnelCard
			tunnel={item.tunnel}
			view={ctx.effectiveAwgRenderMode === 'list-card' ? 'list' : ctx.effectiveAwgCardViewMode}
			toggleLoading={ctx.toggleLoading[item.tunnel.id] ?? false}
			deleteLoading={ctx.deleteLoading[item.tunnel.id] ?? false}
			autoConnectivityNonce={suppressAutoCheck ? 0 : ctx.awgAutoConnectivityNonce}
			autoConnectivityDelayMs={item.index * 180}
			onToggleOnOff={() => ctx.handleToggleOnOff(item.tunnel.id)}
			ondelete={() => ctx.requestDelete(item.tunnel.id)}
			ondetail={(id) => ctx.openDetail(id)}
			onLockClick={() => ctx.handleLockClick(item.tunnel.id)}
		/>
	{:else if item.kind === 'awg-system'}
		<SystemTunnelCard
			tunnel={item.tunnel}
			view={ctx.effectiveAwgRenderMode === 'list-card' ? 'list' : ctx.effectiveAwgCardViewMode}
			onMarkServer={ctx.markAsServer}
			ondetail={(id) => ctx.openDetail(id)}
			ontest={(id, name) => ctx.openAwgDiagnostics(id, name, 'system')}
		/>
	{:else if item.kind === 'awg-external'}
		<ExternalTunnelCard
			tunnel={item.tunnel}
			view={ctx.effectiveAwgRenderMode === 'list-card' ? 'list' : ctx.effectiveAwgCardViewMode}
			onadopt={(name) => ctx.handleAdoptClick(name)}
		/>
	{:else if item.kind === 'awg3'}
		<Awg3TunnelCard
			tunnel={item.tunnel}
			renderMode={ctx.effectiveSingboxTunnelsRenderMode}
			layout={ctx.effectiveSingboxTunnelsRenderMode === 'list-card' ? 'list' : ctx.effectiveSingboxTunnelsEffectiveLayout}
			autoDelayCheckNonce={suppressAutoCheck ? 0 : ctx.singboxAutoDelayCheckNonce}
			autoDelayCheckDelayMs={item.index * 180}
		/>
	{:else if item.kind === 'singbox'}
		<SingboxTunnelCard
			tunnel={item.tunnel}
			layout={ctx.effectiveSingboxTunnelsRenderMode === 'list-card' ? 'list' : ctx.effectiveSingboxTunnelsEffectiveLayout}
			renderMode={ctx.effectiveSingboxTunnelsRenderMode}
			autoDelayCheckNonce={suppressAutoCheck ? 0 : ctx.singboxAutoDelayCheckNonce}
			autoDelayCheckDelayMs={item.index * 180}
			ondetail={(tag) => ctx.openSingboxDetail(tag)}
		/>
	{:else if item.kind === 'sub-active'}
		<SubscriptionActiveCard
			subscription={item.card.subscription}
			activeMember={item.card.activeMember}
			autoDelayCheckNonce={suppressAutoCheck ? 0 : ctx.singboxAutoDelayCheckNonce}
			autoDelayCheckDelayMs={item.index * 180}
			layout={ctx.effectiveSingboxSubscriptionsEffectiveLayout}
			renderMode={ctx.effectiveSingboxSubscriptionsRenderMode}
			ondetail={(tag) => ctx.openSingboxDetail(tag)}
		/>
	{:else if item.kind === 'sub-stopped'}
		<SubscriptionCard
			subscription={item.subscription}
			liveActiveMember={ctx.liveActives[item.subscription.id] || null}
			layout={ctx.effectiveSingboxSubscriptionsEffectiveLayout}
			renderMode={ctx.effectiveSingboxSubscriptionsRenderMode}
			ondelete={ctx.requestSubscriptionDelete}
			ondetail={(tag) => ctx.openSingboxDetail(tag)}
		/>
	{/if}
{/snippet}

{#snippet dashboardItemFooter(item: TunnelDashboardFlatItem, dragIndex: number | null, dragDisabled: boolean = false)}
	{@const itemTags = getItemTags($tunnelDashboardTags, item.key)}
	<!-- Футер рендерится только когда в нём есть содержимое: грип ручного
	     порядка и/или чипы тегов. Без тегов и вне тегового вида ряд с одиноким
	     «+» под каждой карточкой не показывается; редактирование тегов живёт в
	     группировке «Теги», в сплошном виде чипы readonly (клик = фильтр). -->
	{#if dragIndex !== null || itemTags.length > 0 || ctx.dashboardGroupByTags}
		<div class="dashboard-item-footer">
			{#if dragIndex !== null}
				<button
					type="button"
					class="dashboard-item-grip"
					class:is-busy={ctx.flatDrag.busy}
					disabled={dragDisabled}
					title={dragDisabled
						? 'Перетаскивание недоступно при поиске и фильтре'
						: 'Перетащить для изменения порядка'}
					aria-label="Перетащить «{item.name}»"
					onpointerdown={dragDisabled || ctx.flatDrag.busy
						? undefined
						: (e) => ctx.handleGripPointerDown(dragIndex, e)}
					onkeydown={dragDisabled ? undefined : (e) => ctx.handleGripKeydown(dragIndex, e)}
				>
					<GripVertical size={14} strokeWidth={2} aria-hidden="true" />
				</button>
			{/if}
			{#if itemTags.length > 0 || ctx.dashboardGroupByTags}
				<TunnelTagChips
					tags={itemTags}
					readonly={!ctx.dashboardGroupByTags}
					onAdd={(raw) => tunnelDashboardTags.addTag(item.key, raw)}
					onRemove={(tag) => tunnelDashboardTags.removeTag(item.key, tag)}
					onSelect={(tag) => (ctx.dashboardTagFilter = tag)}
					activeTag={ctx.dashboardTagFilter}
				/>
			{/if}
		</div>
	{/if}
{/snippet}

{#if ctx.showSingboxSections}
	<SingboxInstallBanner />
{/if}
<div class="dashboard-sticky">
	<div class="tunnels-toolbar">
		<div class="toolbar-actions">
			<DashboardToolbar
				searchQuery={ctx.dashboardSearchQuery}
				onSearchChange={(value) => (ctx.dashboardSearchQuery = value)}
				layout={$tunnelDashboardLayout}
				onLayoutChange={(layout) => tunnelDashboardLayout.setLayout(layout)}
				viewMode={$tunnelDashboardView}
				onViewModeChange={tunnelDashboardView.setViewMode}
				showViewToggle={ctx.showSingboxListOption}
				showListOption={ctx.showSingboxListOption}
				orderMode={$tunnelDashboardOrderMode}
				onOrderModeChange={tunnelDashboardOrderMode.setMode}
				showOrderControl={ctx.dashboardFlatLayout}
				groupMode={$tunnelDashboardGroupMode}
				onGroupModeChange={tunnelDashboardGroupMode.setMode}
				showGroupControl={!ctx.dashboardFlatLayout}
				activeTagFilter={ctx.dashboardTagFilter}
				onClearTagFilter={() => (ctx.dashboardTagFilter = null)}
				showSingboxCreate={ctx.showSingboxSections}
				onCreateAwg={() => goto('/tunnels/new')}
				onCreateSingboxSingle={() => ctx.openWizard('single')}
				onCreateSingboxGroup={() => ctx.openWizard('inline')}
				onCreateSingboxSubscription={() => ctx.openWizard('url')}
				onCreateAwg3={ctx.awg3Visible ? ctx.openAwg3Import : undefined}
				{createIcon}
			>
				{#snippet actions()}
					<StoreStatusBadge store={tunnels} />
					<Button variant="secondary" size="md" onclick={ctx.handleExportAll} disabled={ctx.exporting} iconBefore={exportIcon}>
						Экспорт
					</Button>
				{/snippet}
			</DashboardToolbar>
		</div>
	</div>
	{#if $showSummary}
		<DashboardSummary stats={ctx.dashboardSummaryStats} />
	{/if}
</div>
{#if ctx.dashboardFlatCardMode}
	<div
		bind:this={ctx.flatGridEl}
		class={ctx.dashboardGridClass}
		class:dashboard-grid--reordering={ctx.flatDrag.active}
		style={ctx.dashboardDndEnabled ? ctx.flatDrag.cardsMotionStyle() : undefined}
	>
		{#each ctx.dashboardRenderItems as item, i (item.key)}
			<div
				class="dashboard-flat-item"
				class:drag-source-exiting={ctx.flatDrag.isDragSource(i)}
				class:drag-source-collapsed={ctx.flatDrag.sourceCollapsed(i)}
				style={ctx.flatDrag.isDragSource(i) ? ctx.flatDrag.dropIndicatorStyle() : undefined}
				bind:this={ctx.flatRowEls[i]}
			>
				{#if ctx.flatDrag.showsDropBefore(i)}
					<div
						class="drop-indicator"
						class:expanded={ctx.flatDrag.dropBeforeExpanded(i)}
						class:collapsing={ctx.flatDrag.dropBeforeCollapsing(i)}
						style={ctx.flatDrag.dropIndicatorStyle()}
					></div>
				{/if}
				{#if ctx.flatDrag.active}
					<!-- Во время drag карточки (с графиками) заменяются
					     компактными рядами фиксированной высоты: без двух
					     полных релэйаутов страницы, и длинный список влезает
					     на экран. Ключи те же — ключи снапшота. -->
					<div class="dashboard-reorder-row">
						<span class="dashboard-kind-badge">{ctx.DASHBOARD_KIND_LABELS[item.kind]}</span>
						<span class="dashboard-reorder-name">{item.name}</span>
					</div>
				{:else}
					{@render dashboardFlatCard(item)}
					{@render dashboardItemFooter(
						item,
						$tunnelDashboardOrderMode === 'manual' ? i : null,
						!ctx.dashboardDndEnabled,
					)}
				{/if}
			</div>
		{/each}
		{#if ctx.flatDrag.showsDropAtEnd()}
			<div
				class="drop-indicator drop-indicator-end"
				class:expanded={ctx.flatDrag.dropEndExpanded()}
				class:collapsing={ctx.flatDrag.dropEndCollapsing()}
				style={ctx.flatDrag.dropIndicatorStyle()}
			></div>
		{/if}
	</div>
{:else if ctx.dashboardGroupByTags}
	{#each ctx.dashboardTagGroups as group (group.tag ?? ' untagged')}
		<TunnelSectionHeader
			title={group.tag ?? 'Без тегов'}
			count={group.items.length}
			countLabel={pluralForm(group.items.length, TUNNEL_WORDS)}
		/>
		<div class={ctx.dashboardGridClass}>
			{#each group.items as entry (entry.item.key)}
				<div class="dashboard-flat-item">
					{@render dashboardFlatCard(entry.item, !entry.autoCheck)}
					{@render dashboardItemFooter(entry.item, null)}
				</div>
			{/each}
		</div>
	{/each}
{/if}
{#if ctx.dashboardFilterEmpty}
	<EmptyState
		title="Ничего не найдено"
		description={ctx.dashboardTagFilter !== null
			? `Нет туннелей с тегом «${ctx.dashboardTagFilter}»${ctx.dashboardSearchQuery.trim() !== '' ? ' по этому запросу' : ''}.`
			: 'По запросу не нашлось ни одного туннеля.'}
	>
		{#snippet action()}
			{#if ctx.dashboardTagFilter !== null}
				<Button variant="secondary" size="md" onclick={() => (ctx.dashboardTagFilter = null)}>
					Сбросить фильтр
				</Button>
			{:else}
				<Button variant="secondary" size="md" onclick={() => (ctx.dashboardSearchQuery = '')}>
					Очистить поиск
				</Button>
			{/if}
		{/snippet}
	</EmptyState>
{/if}

<style>
	/* Комбинированная сводка дашборда (issue #353): липкий блок «тулбар +
	   сводка» под AppHeader — тот же паттерн, что sticky-header в
	   TunnelEditHeader.svelte. */
	.dashboard-sticky {
		position: sticky;
		top: 56px;
		z-index: var(--z-sticky-secondary);
		background: var(--color-bg-primary);
		padding-bottom: 0.75rem;
		border-bottom: 1px solid var(--color-border);
		margin-bottom: 1rem;
	}

	.dashboard-sticky .tunnels-toolbar {
		margin-bottom: 0.75rem;
	}

	@media (max-width: 760px) {
	.dashboard-sticky {
			position: static;
		}
}

	/* Обёртка карточки в сплошном/теговом дашборде: карточка + футер
	   (грип ручного порядка + чипы тегов). */
	.dashboard-flat-item {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
		min-width: 0;
	}

	.dashboard-item-footer {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		min-width: 0;
		margin-top: auto;
	}

	.dashboard-item-grip {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		flex: 0 0 auto;
		background: transparent;
		border: none;
		padding: 0.125rem;
		color: var(--color-text-muted);
		opacity: 0.55;
		cursor: grab;
		touch-action: none;
		border-radius: 4px;
	}

	.dashboard-item-grip:hover {
		color: var(--color-text-primary);
		opacity: 1;
	}

	.dashboard-item-grip:active {
		cursor: grabbing;
	}

	.dashboard-item-grip.is-busy {
		cursor: wait;
		opacity: 0.3;
		pointer-events: none;
	}

	/* Ручной порядок включён, но dnd заблокирован (поиск/фильтр/загрузка
	   данных): грип виден, но неактивен — не исчезает молча. */
	.dashboard-item-grip:disabled {
		opacity: 0.3;
		cursor: not-allowed;
	}

	/* ── D7: drag-reorder (общее pointer-ядро sb-router/reorderDrag).
	   Движок вертикальный, поэтому на время активного drag сетка
	   схлопывается в одну колонку — индексы вставки и индикатор
	   становятся однозначными на любой плотности. ── */
	.tunnel-grid.dashboard-grid--reordering {
		--reorder-row-height: 40px;
		display: flex;
		flex-direction: column;
		gap: var(--card-gap, 6px);
		user-select: none;
	}

	/* Компактный ряд на время drag: бейдж вида + имя (ghost-токен остался в
	   странице вместе со своим fixed-слоем и дублем этого правила). */
	.dashboard-reorder-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		box-sizing: border-box;
		height: var(--reorder-row-height, 40px);
		padding: 0 0.75rem;
		min-width: 0;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.dashboard-kind-badge {
		flex: 0 0 auto;
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		line-height: 1.3;
		color: var(--color-text-muted);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 1px 6px;
		white-space: nowrap;
	}

	.dashboard-reorder-name {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}

	/* Во время drag строки компактные — слот источника и раскрытый
	   drop-скелетон ужимаются до высоты ряда, а не исходной карточки
	   (--drop-height меряется на pointerdown по полной карточке). */
	.dashboard-grid--reordering .dashboard-flat-item.drag-source-exiting:not(.drag-source-collapsed) {
		height: var(--reorder-row-height, 40px);
	}

	.dashboard-grid--reordering .drop-indicator.expanded:not(.collapsing) {
		height: var(--reorder-row-height, 40px);
	}

	.dashboard-flat-item.drag-source-exiting {
		overflow: hidden;
		height: var(--drop-height);
		opacity: 1;
		transition:
			height var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			opacity var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			margin var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
	}

	.dashboard-flat-item.drag-source-exiting.drag-source-collapsed {
		height: 0;
		max-height: 0;
		opacity: 0;
		margin-bottom: calc(-1 * var(--card-gap, 6px));
	}

	.drop-indicator {
		box-sizing: border-box;
		overflow: hidden;
		border: 1px solid transparent;
		border-radius: 999px;
		background: var(--color-accent);
		box-shadow: 0 0 10px color-mix(in srgb, var(--color-accent) 45%, transparent);
		opacity: 1;
		pointer-events: none;
		transition:
			height var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			margin var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			border-radius calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			background calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			box-shadow calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			border-color calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			opacity calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
	}

	.drop-indicator:not(.expanded):not(.collapsing) {
		position: absolute;
		top: -1px;
		left: 0;
		right: 0;
		height: 2px;
		margin: 0;
		z-index: 2;
	}

	.drop-indicator.expanded:not(.collapsing) {
		position: static;
		top: auto;
		height: var(--drop-height);
		margin: 0 0 var(--card-gap, 6px);
		border-radius: var(--radius-sm, 6px);
		background: color-mix(in srgb, var(--color-accent) 6%, transparent);
		border-color: color-mix(in srgb, var(--color-accent) 55%, transparent);
		border-style: dashed;
		box-shadow: none;
	}

	.drop-indicator.collapsing {
		margin: 0 !important;
		opacity: 0;
		border-color: transparent;
		background: transparent;
		box-shadow: none;
	}

	.drop-indicator.collapsing.expanded {
		position: static;
		height: 0 !important;
	}

	.drop-indicator.collapsing:not(.expanded) {
		position: absolute;
		top: -1px;
		left: 0;
		right: 0;
		height: 2px !important;
		z-index: 2;
		transition:
			opacity var(--drop-line-collapse-ms, 240ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			box-shadow calc(var(--drop-line-collapse-ms, 240ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			background calc(var(--drop-line-collapse-ms, 240ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			border-color calc(var(--drop-line-collapse-ms, 240ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
	}

	.drop-indicator-end:not(.expanded):not(.collapsing) {
		position: relative;
		top: auto;
		height: 2px;
		margin: -1px 0 0;
	}

	.drop-indicator-end.collapsing:not(.expanded) {
		position: relative;
		top: auto;
		left: auto;
		right: auto;
		height: 2px !important;
		margin: -1px 0 0 !important;
	}

	/* Toolbar (count + actions row above the tunnel grid) */
	.tunnels-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 1rem;
	}

	.toolbar-actions {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		flex-wrap: wrap;
		gap: 0.5rem;
		/* Единственный ребёнок .tunnels-toolbar — без роста тулбар жался по
		   содержимому влево, и справа оставалась пустота (#607). */
		flex: 1 1 auto;
		min-width: 0;
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
