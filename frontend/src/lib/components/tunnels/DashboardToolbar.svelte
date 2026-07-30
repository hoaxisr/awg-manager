<script lang="ts">
	import type { Snippet } from 'svelte';
	import { Badge, SegmentedControl } from '$lib/components/ui';
	import LayoutViewToggle from '$lib/components/ui/LayoutViewToggle.svelte';
	import TunnelSearchInput from './TunnelSearchInput.svelte';
	import TunnelCreateMenu from './TunnelCreateMenu.svelte';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
	import {
		TUNNEL_DASHBOARD_LAYOUT_LABELS,
		type TunnelDashboardLayout,
	} from '$lib/stores/tunnelDashboardMode';
	import type {
		TunnelDashboardGroupMode,
		TunnelDashboardOrderMode,
	} from '$lib/stores/tunnelDashboardPrefs';

	interface Props {
		searchQuery: string;
		onSearchChange: (value: string) => void;
		layout: TunnelDashboardLayout;
		onLayoutChange: (layout: TunnelDashboardLayout) => void;
		viewMode: SingboxLayoutMode;
		onViewModeChange: (mode: SingboxLayoutMode) => void;
		orderMode?: TunnelDashboardOrderMode;
		onOrderModeChange?: (mode: TunnelDashboardOrderMode) => void;
		showOrderControl?: boolean;
		groupMode?: TunnelDashboardGroupMode;
		onGroupModeChange?: (mode: TunnelDashboardGroupMode) => void;
		showGroupControl?: boolean;
		activeTagFilter?: string | null;
		onClearTagFilter?: () => void;
		showViewToggle?: boolean;
		showListOption?: boolean;
		showSingboxCreate?: boolean;
		onCreateAwg: () => void;
		onCreateSingboxSingle?: () => void;
		onCreateSingboxGroup?: () => void;
		onCreateSingboxSubscription?: () => void;
		onCreateAwg3?: () => void;
		createIcon: Snippet;
		/** Дополнительные действия (экспорт, статус) перед меню создания. */
		actions?: Snippet;
	}

	let {
		searchQuery,
		onSearchChange,
		layout,
		onLayoutChange,
		viewMode,
		onViewModeChange,
		orderMode = 'auto',
		onOrderModeChange,
		showOrderControl = false,
		groupMode = 'type',
		onGroupModeChange,
		showGroupControl = false,
		activeTagFilter = null,
		onClearTagFilter,
		showViewToggle = true,
		showListOption = true,
		showSingboxCreate = true,
		onCreateAwg,
		onCreateSingboxSingle,
		onCreateSingboxGroup,
		onCreateSingboxSubscription,
		onCreateAwg3,
		createIcon,
		actions,
	}: Props = $props();

	const layoutOptions: Array<{ value: TunnelDashboardLayout; label: string }> = [
		{ value: 'flat', label: TUNNEL_DASHBOARD_LAYOUT_LABELS.flat },
		{ value: 'sections', label: TUNNEL_DASHBOARD_LAYOUT_LABELS.sections },
	];

	const orderOptions: Array<{ value: TunnelDashboardOrderMode; label: string }> = [
		{ value: 'auto', label: 'Авто' },
		{ value: 'manual', label: 'Вручную' },
	];

	const groupOptions: Array<{ value: TunnelDashboardGroupMode; label: string }> = [
		{ value: 'type', label: 'Тип' },
		{ value: 'tags', label: 'Теги' },
	];
</script>

<div class="dashboard-toolbar">
	<div class="tunnel-toolbar-search">
		<TunnelSearchInput value={searchQuery} onInput={onSearchChange} />
	</div>

	<div class="dashboard-toolbar-layout">
		<SegmentedControl
			value={layout}
			options={layoutOptions}
			ariaLabel="Расположение туннелей на дашборде"
			onchange={(next) => onLayoutChange(next)}
		/>
	</div>

	{#if showOrderControl}
		<div class="dashboard-toolbar-order">
			<SegmentedControl
				value={orderMode}
				options={orderOptions}
				ariaLabel="Порядок туннелей на дашборде"
				onchange={(next) => onOrderModeChange?.(next)}
			/>
		</div>
	{/if}

	{#if showGroupControl}
		<div class="dashboard-toolbar-group">
			<SegmentedControl
				value={groupMode}
				options={groupOptions}
				ariaLabel="Группировка туннелей на дашборде"
				onchange={(next) => onGroupModeChange?.(next)}
			/>
		</div>
	{/if}

	{#if activeTagFilter}
		<div class="dashboard-toolbar-tag-filter">
			<Badge variant="accent">
				<span class="tag-filter-label">Тег: {activeTagFilter}</span>
				<button
					type="button"
					class="tag-filter-clear"
					aria-label="Сбросить фильтр по тегу"
					onclick={() => onClearTagFilter?.()}
				>
					&times;
				</button>
			</Badge>
		</div>
	{/if}

	{#if showViewToggle}
		<div class="dashboard-toolbar-view">
			<LayoutViewToggle
				value={viewMode}
				denseValue="dense"
				{showListOption}
				ariaLabel="Вид туннелей"
				onchange={(mode) => onViewModeChange(mode)}
			/>
		</div>
	{/if}

	{#if actions}
		<div class="dashboard-toolbar-actions">
			{@render actions()}
		</div>
	{/if}

	<div class="dashboard-toolbar-create">
		<TunnelCreateMenu
			onAwg={onCreateAwg}
			onSingboxSingle={onCreateSingboxSingle}
			onSingboxGroup={onCreateSingboxGroup}
			onSingboxSubscription={onCreateSingboxSubscription}
			onAwg3={onCreateAwg3}
			showSingbox={showSingboxCreate}
			triggerIcon={createIcon}
		/>
	</div>
</div>

<style>
	.dashboard-toolbar {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
		flex: 1 1 auto;
	}

	.tunnel-toolbar-search {
		flex: 1 1 160px;
		min-width: 120px;
		max-width: 220px;
	}

	.dashboard-toolbar-layout {
		flex: 0 1 auto;
		min-width: 0;
	}

	.dashboard-toolbar-order,
	.dashboard-toolbar-group {
		flex: 0 1 auto;
		min-width: 0;
	}

	.dashboard-toolbar-layout :global(.segmented-control),
	.dashboard-toolbar-order :global(.segmented-control),
	.dashboard-toolbar-group :global(.segmented-control) {
		height: 32px;
	}

	.dashboard-toolbar-tag-filter {
		flex: 0 1 auto;
		min-width: 0;
		display: flex;
		align-items: center;
	}

	.dashboard-toolbar-tag-filter :global(.badge) {
		max-width: 100%;
		min-width: 0;
	}

	.tag-filter-label {
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}

	.tag-filter-clear {
		border: none;
		background: transparent;
		padding: 0 2px;
		font-size: 0.75rem;
		line-height: 1;
		font-family: inherit;
		color: inherit;
		opacity: 0.7;
		cursor: pointer;
		flex-shrink: 0;
	}

	.tag-filter-clear:hover {
		opacity: 1;
	}

	.dashboard-toolbar-view {
		flex: 0 0 auto;
	}

	.dashboard-toolbar-actions {
		flex: 0 0 auto;
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}

	.dashboard-toolbar-create {
		flex: 0 0 auto;
	}

	/* Mobile: авторазмещение по order, компактно в ≤4 строки —
	   row 1: layout + view 50/50; row 2: order + group 50/50;
	   row 3: тег-фильтр + actions 50/50 (одиночный растягивается на всю
	   строку); row 4: search + create 50/50 */
	@media (max-width: 760px) {
		.dashboard-toolbar {
			display: grid;
			grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
			gap: 0.5rem;
			width: 100%;
			/* Хост (.toolbar-actions на странице туннелей) на мобильном сам
			   становится 2-колоночным гридом — без span тулбар зажимался в
			   ПОЛОВИНУ ширины экрана, и сегмент-контролы наезжали подписями
			   друг на друга. Занимаем всю строку хост-грида; в flex/блочном
			   родителе grid-column игнорируется. */
			grid-column: 1 / -1;
		}

		.dashboard-toolbar-layout {
			order: 1;
			width: 100%;
		}

		.dashboard-toolbar-view {
			order: 2;
			width: 100%;
		}

		.dashboard-toolbar-order {
			order: 3;
			width: 100%;
		}

		.dashboard-toolbar-group {
			order: 4;
			width: 100%;
		}

		.dashboard-toolbar-tag-filter {
			order: 5;
			min-width: 0;
		}

		.dashboard-toolbar-actions {
			order: 6;
			width: 100%;
			min-width: 0;
		}

		.tunnel-toolbar-search {
			order: 7;
			min-width: 0;
			max-width: none;
			width: 100%;
		}

		.dashboard-toolbar-create {
			order: 8;
			justify-self: stretch;
			align-self: center;
			min-width: 0;
		}

		/* Контрол без пары растягивается на всю строку */
		.dashboard-toolbar:not(:has(.dashboard-toolbar-view)) .dashboard-toolbar-layout,
		.dashboard-toolbar:not(:has(.dashboard-toolbar-group)) .dashboard-toolbar-order,
		.dashboard-toolbar:not(:has(.dashboard-toolbar-order)) .dashboard-toolbar-group,
		.dashboard-toolbar:not(:has(.dashboard-toolbar-actions)) .dashboard-toolbar-tag-filter,
		.dashboard-toolbar:not(:has(.dashboard-toolbar-tag-filter)) .dashboard-toolbar-actions {
			grid-column: 1 / -1;
		}

		.dashboard-toolbar-layout :global(.segmented-control),
		.dashboard-toolbar-order :global(.segmented-control),
		.dashboard-toolbar-group :global(.segmented-control),
		.dashboard-toolbar-view :global(.segmented-control) {
			width: 100%;
			min-width: 0;
			justify-content: stretch;
		}

		/* Текстовые кнопки сегмент-контролов должны сжиматься внутри узкой
		   grid-ячейки (в диапазоне 641–760px собственный мобильный брейкпоинт
		   SegmentedControl (640px) ещё не активен — без flex:1 1 0 / min-width:0
		   кнопки не влезают, и подписи наезжают друг на друга:
		   «СплошнРазделы»). display:block + line-height — чтобы работал
		   text-overflow (не применяется к тексту внутри flex-контейнера). */
		.dashboard-toolbar-layout :global(.segmented-control .segmented-control-btn),
		.dashboard-toolbar-order :global(.segmented-control .segmented-control-btn),
		.dashboard-toolbar-group :global(.segmented-control .segmented-control-btn) {
			flex: 1 1 0;
			min-width: 0;
			padding: 0 6px;
			display: block;
			line-height: 26px;
			text-align: center;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}

		.dashboard-toolbar-view :global(.segmented-control--icon .segmented-control-btn) {
			flex: 1 1 28px;
			min-width: 28px;
		}

		.dashboard-toolbar-create :global(.dropdown-wrapper) {
			display: block;
			width: 100%;
		}

		.dashboard-toolbar-create :global(.dropdown-wrapper .btn) {
			width: 100%;
			justify-content: center;
			white-space: nowrap;
		}
	}
</style>
