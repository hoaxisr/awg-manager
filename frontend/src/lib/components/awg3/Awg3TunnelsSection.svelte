<script lang="ts">
	// Секция «AWG3 туннели» страницы туннелей: тулбар (поиск + вид + импорт),
	// импорт-модалка и рендер списка через Awg3TunnelCard. Зеркалит идиомы
	// SingboxTunnelsTabSection (view-режимы, table vs grid, EmptyState).
	import { LayoutViewToggle, Button } from '$lib/components/ui';
	import { EmptyState } from '$lib/components/layout';
	import { TunnelToolbarViewRow } from '$lib/components/tunnels';
	import Awg3TunnelCard from './Awg3TunnelCard.svelte';
	import Awg3ImportModal from './Awg3ImportModal.svelte';
	import { awg3Tunnels } from '$lib/stores/awg3';
	import { pluralForm, TUNNEL_WORDS } from '$lib/utils/pluralize';
	import type { SingboxLayoutMode, TunnelRenderMode } from '$lib/constants/singboxLayout';
	import type { Awg3Tunnel } from '$lib/types';
	import { Waypoints, Download } from 'lucide-svelte';

	interface Props {
		tunnels: Awg3Tunnel[];
		renderMode: TunnelRenderMode;
		layout: SingboxLayoutMode;
		showGridListToggle: boolean;
		searchQuery: string;
		layoutMode: SingboxLayoutMode;
		autoDelayCheckNonce: number;
		// Дашборд-режим (секции по типу) прячет тулбар и передаёт уже
		// отфильтрованный список — как SingboxTunnelsTabSection при dashboardOn.
		showToolbar?: boolean;
	}

	let {
		tunnels,
		renderMode,
		layout,
		showGridListToggle,
		searchQuery = $bindable(),
		layoutMode = $bindable(),
		autoDelayCheckNonce,
		showToolbar = true,
	}: Props = $props();

	let importOpen = $state(false);

	const filtered = $derived.by(() => {
		// Без тулбара поле поиска скрыто, а список приходит уже отфильтрованным.
		if (!showToolbar) return tunnels;
		const q = searchQuery.trim().toLowerCase();
		if (q === '') return tunnels;
		return tunnels.filter(
			(t) => t.tag.toLowerCase().includes(q) || t.host.toLowerCase().includes(q),
		);
	});

	const searchEmpty = $derived(tunnels.length > 0 && filtered.length === 0);

	const cardLayout = $derived<SingboxLayoutMode>(renderMode === 'list-card' ? 'list' : layout);
</script>

{#if showToolbar && tunnels.length > 0}
	<div class="tunnels-toolbar">
		<div class="toolbar-title">
			<span class="section-title">AWG3 туннели</span>
			<span class="tunnel-count">
				{tunnels.length}
				{pluralForm(tunnels.length, TUNNEL_WORDS)}
			</span>
		</div>
		<div class="toolbar-actions">
			<TunnelToolbarViewRow
				sourceRowCount={tunnels.length}
				showViewToggle={tunnels.length > 0}
				searchQuery={searchQuery}
				onSearchChange={(value) => (searchQuery = value)}
			>
				{#snippet viewToggle()}
					<LayoutViewToggle
						value={layoutMode}
						showListOption={showGridListToggle}
						ariaLabel="Вид туннелей"
						onchange={(v) => (layoutMode = v)}
					/>
				{/snippet}
			</TunnelToolbarViewRow>
			<Button variant="primary" size="md" onclick={() => (importOpen = true)} iconBefore={importIcon}>
				Импортировать
			</Button>
		</div>
	</div>
{/if}

{#snippet importIcon()}
	<Download size={16} aria-hidden="true" />
{/snippet}

{#if tunnels.length === 0}
	<EmptyState
		title="Нет AWG3 туннелей"
		description="Импортируй JSON-конфиг AWG3-клиента — он станет sing-box endpoint'ом."
		icon={emptyIcon}
		action={emptyAction}
	/>
	{#snippet emptyIcon()}
		<Waypoints aria-hidden="true" />
	{/snippet}
	{#snippet emptyAction()}
		<Button variant="primary" size="md" onclick={() => (importOpen = true)} iconBefore={importIcon}>
			Импортировать
		</Button>
	{/snippet}
{:else if renderMode === 'table'}
	<div class="tunnel-table-wrap">
		<table class="tunnel-data-table awg3-tunnel-table">
			<colgroup>
				<col class="c-name" />
				<col class="c-host" />
				<col class="c-hp" />
				<col class="c-timers" />
				<col class="c-delay" />
				<col class="c-actions" />
			</colgroup>
			<thead>
				<tr>
					<th>Туннель</th>
					<th>Хост</th>
					<th>Защита</th>
					<th>Таймеры</th>
					<th>Delay</th>
					<th class="col-actions">Действия</th>
				</tr>
			</thead>
			<tbody>
				{#each filtered as tunnel, i (tunnel.id)}
					<Awg3TunnelCard {tunnel} renderMode="table" layout="list" {autoDelayCheckNonce} autoDelayCheckDelayMs={i * 180} />
				{/each}
				{#if searchEmpty}
					<tr class="tunnel-empty-row">
						<td colspan="6">Ничего не найдено</td>
					</tr>
				{/if}
			</tbody>
		</table>
	</div>
{:else}
	<div
		class="tunnel-grid"
		class:tunnel-grid--list={renderMode === 'list-card'}
		class:tunnel-grid--dense={renderMode !== 'list-card' && layout === 'dense'}
		class:tunnel-grid--compact={renderMode !== 'list-card' && layout === 'compact'}
	>
		{#each filtered as tunnel, i (tunnel.id)}
			<Awg3TunnelCard {tunnel} renderMode={renderMode} layout={cardLayout} {autoDelayCheckNonce} autoDelayCheckDelayMs={i * 180} />
		{/each}
	</div>
	{#if searchEmpty}
		<p class="tunnel-list-empty">Ничего не найдено</p>
	{/if}
{/if}

<Awg3ImportModal
	open={importOpen}
	onclose={() => (importOpen = false)}
	onimported={() => void awg3Tunnels.refetch()}
/>

<style>
	.tunnels-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		margin-bottom: 1rem;
	}

	.toolbar-title {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
		min-width: 0;
	}

	.section-title {
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--color-text-primary);
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

	/* Заголовки таблицы AWG3: базовые td-стили строк лежат в Awg3TunnelCard,
	   здесь только шапка (thead) — своя, чтобы не зависеть от singbox-колонок. */
	:global(.awg3-tunnel-table) thead th {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		font-size: var(--tunnel-list-head-font-size);
		font-weight: 700;
		letter-spacing: var(--tunnel-list-head-letter-spacing);
		text-transform: uppercase;
		border-bottom: 1px solid var(--color-border);
		padding: var(--tunnel-list-head-padding-y) var(--tunnel-list-head-padding-x);
		text-align: left;
		vertical-align: middle;
		/* Как в общем правиле для singbox-таблиц: узкая колонка обязана резать
		   свой заголовок, иначе «ТАЙМЕРЫ» наезжает на «DELAY». */
		overflow: hidden;
	}

	:global(.awg3-tunnel-table) thead th.col-actions {
		text-align: right;
	}

	/* Имя и хост ограничены долями, свободное место достаётся таймерам: пять
	   nowrap-чипов не влезали в узкую колонку и вылезали в соседнюю. Доли, а
	   не пиксели: с фиксированными 220+280 сумма колонок (832px) перерастала
	   доступную ширину на 761–940px — шапка наезжала, таблица скроллилась. */
	:global(.awg3-tunnel-table) col.c-name {
		width: 18%;
	}
	:global(.awg3-tunnel-table) col.c-host {
		width: 24%;
	}
	:global(.awg3-tunnel-table) col.c-hp {
		width: 72px;
	}
	:global(.awg3-tunnel-table) col.c-delay {
		width: 168px;
	}
	:global(.awg3-tunnel-table) col.c-actions {
		width: var(--tunnel-list-col-actions, 92px);
	}

	/* Имя и хост режутся по ellipsis. .tag/.host — inline-боксы, к ним
	   text-overflow не применяется, поэтому display: block обязателен. */
	:global(.awg3-tunnel-table td.cell-host),
	:global(.awg3-tunnel-table td.cell-name) {
		overflow: hidden;
	}
	:global(.awg3-tunnel-table td.cell-host .host),
	:global(.awg3-tunnel-table td.cell-name .tag) {
		display: block;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
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
	}
</style>
