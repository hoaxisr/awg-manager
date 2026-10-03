<script lang="ts">
	import { m } from '$lib/i18n';
	// Вкладка «Sing-box туннели» страницы туннелей — выделено из
	// routes/+page.svelte (класс 2): разметка дословно, состояние — пропсами.
	import { StatStrip, Stat, LayoutViewToggle, Button, Badge, TableSortHeader } from '$lib/components/ui';
	import { TunnelToolbarViewRow } from '$lib/components/tunnels';
	import { SingboxInstallBanner, SingboxTunnelCard } from '$lib/components/singbox';
	import { singboxTunnelTableSort, type SingboxTunnelSortKey } from '$lib/stores/tunnelTableSort';
	import { formatBytes } from '$lib/utils/format';
	import { ariaSort } from '$lib/utils/tunnelTableSort';
	import type { SingboxLayoutMode, TunnelRenderMode } from '$lib/constants/singboxLayout';
	import type { SingboxTunnel } from '$lib/types';
	import type { SubscriptionActiveCardVM, SingboxTunnelListStats } from '$lib/components/subscriptions/subscriptionVMs';
	import { Globe, LayoutGrid, Link, Waypoints } from 'lucide-svelte';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';
	import { showSummary } from '$lib/stores/showSummary';

	interface Props {
		dashboardOn: boolean;
		dashboardSingboxTunnels: SingboxTunnel[];
		singboxTunnelsList: SingboxTunnel[];
		sortedFilteredSingboxTunnels: SingboxTunnel[];
		singboxTunnelListStats: SingboxTunnelListStats;
		singboxTunnelsSourceRowCount: number;
		singboxTunnelsSearchEmpty: boolean;
		singboxAutoDelayCheckNonce: number;
		showSingboxGridListToggle: boolean;
		effectiveSingboxTunnelsEffectiveLayout: SingboxLayoutMode;
		effectiveSingboxTunnelsRenderMode: TunnelRenderMode;
		subscriptionsActiveCards: SubscriptionActiveCardVM[];
		singboxTunnelsSearchQuery: string;
		singboxTunnelsLayoutMode: SingboxLayoutMode;
		handleSingboxTunnelSortChange: (key: SingboxTunnelSortKey) => void;
		openSingboxDetail: (tag: string) => void;
		openWizard: (preselect: 'choose' | 'single' | 'inline' | 'url') => void;
		openAwg3Import?: () => void;
	}

	let {
		dashboardOn,
		dashboardSingboxTunnels,
		singboxTunnelsList,
		sortedFilteredSingboxTunnels,
		singboxTunnelListStats,
		singboxTunnelsSourceRowCount,
		singboxTunnelsSearchEmpty,
		singboxAutoDelayCheckNonce,
		showSingboxGridListToggle,
		effectiveSingboxTunnelsEffectiveLayout,
		effectiveSingboxTunnelsRenderMode,
		subscriptionsActiveCards,
		singboxTunnelsSearchQuery = $bindable(),
		singboxTunnelsLayoutMode = $bindable(),
		handleSingboxTunnelSortChange,
		openSingboxDetail,
		openWizard,
		openAwg3Import,
	}: Props = $props();
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

	{#if !dashboardOn}
	<SingboxInstallBanner />
	{#if singboxTunnelsList.length > 0 || subscriptionsActiveCards.length > 0}
		<div class="tunnels-toolbar">
			<span class="tunnel-count">
				{singboxTunnelsList.length}
				{m.tunnels_unit_tunnels({ count: singboxTunnelsList.length })}
			</span>
			<div class="toolbar-actions">
				<TunnelToolbarViewRow
					sourceRowCount={singboxTunnelsSourceRowCount}
					showViewToggle={singboxTunnelsList.length > 0}
					searchQuery={singboxTunnelsSearchQuery}
					onSearchChange={(value) => (singboxTunnelsSearchQuery = value)}
				>
					{#snippet viewToggle()}
						<LayoutViewToggle
							value={singboxTunnelsLayoutMode}
							showListOption={showSingboxGridListToggle}
							ariaLabel={m.tunnels_dashboard_view_aria()}
							onchange={(v) => (singboxTunnelsLayoutMode = v)}
						/>
					{/snippet}
				</TunnelToolbarViewRow>
				<Button
					variant="primary"
					size="md"
					onclick={() => openWizard('choose')}
					iconBefore={createIcon}
				>
					{m.common_add()}
				</Button>
			</div>
		</div>
	{/if}
	{/if}
	{#if !dashboardOn && singboxTunnelsList.length === 0}
		<div class="empty-kinds">
			<button type="button" class="empty-kind-card" onclick={() => openWizard('single')}>
				<Link class="empty-kind-icon" size={28} strokeWidth={1.6} aria-hidden="true" />
				<div class="empty-kind-title">{m.tunnels_create_single_title()}</div>
				<div class="empty-kind-desc">
					{m.singbox_tabs_one_server_desc()}
				</div>
			</button>
			<button type="button" class="empty-kind-card" onclick={() => openWizard('inline')}>
				<LayoutGrid class="empty-kind-icon" size={28} strokeWidth={1.6} aria-hidden="true" />
				<div class="empty-kind-title">{m.tunnels_create_group_title()}</div>
				<div class="empty-kind-desc">
					{m.singbox_tabs_group_desc()}
				</div>
			</button>
			<button type="button" class="empty-kind-card" onclick={() => openWizard('url')}>
				<Globe class="empty-kind-icon" size={28} strokeWidth={1.6} aria-hidden="true" />
				<div class="empty-kind-title">{m.tunnels_create_sub_title()}</div>
				<div class="empty-kind-desc">
					{m.singbox_tabs_subscription_desc()}
				</div>
			</button>
			{#if openAwg3Import}
				<button type="button" class="empty-kind-card" onclick={openAwg3Import}>
					<Waypoints class="empty-kind-icon" size={28} strokeWidth={1.6} aria-hidden="true" />
					<div class="empty-kind-title">AWG3 Endpoint</div>
					<div class="empty-kind-desc">
						{m.singbox_tabs_awg3_desc()}
					</div>
				</button>
			{/if}
		</div>
		<div class="info-card">
			<h3 class="info-title">{m.singbox_tabs_about()}</h3>
			<p class="info-section-desc">
				{m.singbox_tabs_about_desc()}
			</p>
			<div class="info-versions">
				<div class="info-version">
					<Badge variant="accent" size="sm" mono>VLESS</Badge>
					<span class="info-version-desc">{m.singbox_tabs_vless_prefix()} <strong>Reality</strong> {m.singbox_tabs_vless_suffix()}</span>
				</div>
				<div class="info-version">
					<Badge variant="error" size="sm" mono>Trojan</Badge>
					<span class="info-version-desc">{m.singbox_tabs_trojan_desc()}</span>
				</div>
				<div class="info-version">
					<Badge variant="success" size="sm" mono>Shadowsocks</Badge>
					<span class="info-version-desc">{m.singbox_tabs_ss_desc()}</span>
				</div>
				<div class="info-version">
					<Badge variant="warning" size="sm" mono>Hysteria2</Badge>
					<span class="info-version-desc">{m.singbox_tabs_hy2_desc()}</span>
				</div>
				<div class="info-version">
					<Badge variant="info" size="sm" mono>NaiveProxy</Badge>
					<span class="info-version-desc">{m.singbox_tabs_naive_desc()}</span>
				</div>
				<div class="info-version">
					<Badge variant="purple" size="sm" mono>Mieru</Badge>
					<span class="info-version-desc">{m.singbox_tabs_mieru_desc()}</span>
				</div>
			</div>
		</div>
	{:else if singboxTunnelsList.length > 0 || (dashboardOn && dashboardSingboxTunnels.length > 0)}
		{#if !dashboardOn && $showSummary}
			<div class="awg-summary-row">
				<StatStrip>
					<Stat
						value={`${singboxTunnelListStats.running}/${singboxTunnelListStats.count}`}
						label={m.tunnels_unit_tunnels({ count: singboxTunnelListStats.running })}
						sub={m.singbox_tabs_running_sub({ active: singboxTunnelListStats.running, stopped: Math.max(0, singboxTunnelListStats.count - singboxTunnelListStats.running) })}
					/>
					<Stat
						value={formatBytes(singboxTunnelListStats.down + singboxTunnelListStats.up)}
						label={m.singbox_tabs_total_traffic()}
						sub={`↓ ${formatBytes(singboxTunnelListStats.down)} · ↑ ${formatBytes(singboxTunnelListStats.up)}`}
					/>
					<Stat
						value={singboxTunnelListStats.avgDelayMs !== null
							? `${singboxTunnelListStats.avgDelayMs} ms`
							: '—'}
						label={m.singbox_tabs_avg_delay()}
						sub={m.singbox_tabs_avg_delay_sub()}
					/>
					<Stat
						value={singboxTunnelListStats.leaderBytes > 0
							? formatBytes(singboxTunnelListStats.leaderBytes)
							: '—'}
						label={m.tunnels_awg_stat_leader()}
						sub={singboxTunnelListStats.leaderName}
					/>
					</StatStrip>
				</div>
		{/if}
		{#if effectiveSingboxTunnelsRenderMode === 'table'}
			<div class="tunnel-table-wrap">
				<table class="tunnel-data-table singbox-tunnel-table">
					<colgroup>
						<col class="col-delay" />
						<col class="col-name" />
						<col class="col-protocol" />
						<col class="col-run" />
						<col class="col-traffic" />
						<col class="col-ping" />
						<col class="col-actions" />
					</colgroup>
					<thead>
						<tr>
							<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'delay', $singboxTunnelTableSort.sortAsc)}>
								<TableSortHeader label="Delay" sortKey={'delay'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
							</th>
							<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'name', $singboxTunnelTableSort.sortAsc)}>
								<TableSortHeader label={m.tunnels_awg_col_tunnel()} sortKey={'name'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
							</th>
							<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'protocol', $singboxTunnelTableSort.sortAsc)}>
								<TableSortHeader label={m.singbox_tabs_col_protocol()} sortKey={'protocol'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
							</th>
							<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'running', $singboxTunnelTableSort.sortAsc)}>
								<TableSortHeader label={m.singbox_tabs_col_process()} sortKey={'running'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
							</th>
							<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'traffic', $singboxTunnelTableSort.sortAsc)}>
								<TableSortHeader label={m.tunnels_awg_col_traffic()} sortKey={'traffic'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
							</th>
							<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'ping', $singboxTunnelTableSort.sortAsc)}>
								<TableSortHeader label="Ping" sortKey={'ping'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
							</th>
							<th class="col-actions">{m.tunnels_awg_col_actions()}</th>
						</tr>
					</thead>
					<tbody>
				{#each sortedFilteredSingboxTunnels as tunnel, i (tunnel.tag)}
					<SingboxTunnelCard
						{tunnel}
						layout="list"
						renderMode="table"
						autoDelayCheckNonce={singboxAutoDelayCheckNonce}
						autoDelayCheckDelayMs={i * 180}
						ondetail={(tag) => openSingboxDetail(tag)}
					/>
				{/each}
				{#if singboxTunnelsSearchEmpty}
					<tr class="tunnel-empty-row">
						<td colspan="7">{m.tunnels_dashboard_empty_title()}</td>
					</tr>
				{/if}
					</tbody>
				</table>
			</div>
		{:else}
			{@const sbTunnelCardLayout = effectiveSingboxTunnelsRenderMode === 'list-card' ? 'list' : effectiveSingboxTunnelsEffectiveLayout}
			<div
				class="tunnel-grid"
				class:tunnel-grid--list={effectiveSingboxTunnelsRenderMode === 'list-card'}
				class:tunnel-grid--dense={effectiveSingboxTunnelsRenderMode !== 'list-card' && effectiveSingboxTunnelsEffectiveLayout === 'dense'}
				class:tunnel-grid--compact={effectiveSingboxTunnelsRenderMode !== 'list-card' && effectiveSingboxTunnelsEffectiveLayout === 'compact'}
			>
				{#each sortedFilteredSingboxTunnels as tunnel, i (tunnel.tag)}
					<SingboxTunnelCard
						{tunnel}
						layout={sbTunnelCardLayout}
						renderMode={effectiveSingboxTunnelsRenderMode}
						autoDelayCheckNonce={singboxAutoDelayCheckNonce}
						autoDelayCheckDelayMs={i * 180}
						ondetail={(tag) => openSingboxDetail(tag)}
					/>
				{/each}
			</div>
			{#if singboxTunnelsSearchEmpty}
				<p class="tunnel-list-empty">{m.tunnels_dashboard_empty_title()}</p>
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

	/* Empty-state kind picker — mirrors wizard step-1 cards. */
	.empty-kinds {
		display: grid;
		grid-template-columns: 1fr;
		gap: 0.7rem;
		margin-top: 0.5rem;
	}

	@media (min-width: 560px) {
		.empty-kinds {
			grid-template-columns: repeat(2, minmax(12rem, 1fr));
		}
	}

	@media (min-width: 820px) {
		.empty-kinds {
			grid-template-columns: repeat(4, minmax(12rem, 1fr));
		}
	}

	.empty-kind-card {
		display: flex;
		flex-direction: column;
		gap: 0.45rem;
		padding: 1.1rem 1.2rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 8px;
		text-align: left;
		cursor: pointer;
		font: inherit;
		color: var(--color-text-primary);
		transition: border-color 120ms, transform 120ms, background 120ms;
	}

	.empty-kind-card:hover {
		border-color: var(--color-primary, #3b82f6);
		background: rgba(59, 130, 246, 0.04);
		transform: translateY(-1px);
	}

	.empty-kind-card:focus-visible {
		outline: 2px solid var(--color-primary, #3b82f6);
		outline-offset: 2px;
	}

	:global(.empty-kind-icon) { color: var(--color-primary, #3b82f6); }

	.empty-kind-title { font-weight: 600; font-size: 0.95rem; }

	.empty-kind-desc { color: var(--color-text-muted); font-size: 0.8rem; line-height: 1.4; }

	/* "About AmneziaWG / Sing-box" info card — page-specific */
	.info-card {
		border-left: 3px solid var(--color-accent);
		background: var(--color-bg-secondary);
		border-radius: 0 var(--radius) var(--radius) 0;
		padding: 1.25rem 1.5rem;
		margin-top: 1.5rem;
	}

	.info-title {
		font-size: 1rem;
		font-weight: 600;
		margin-bottom: 0.75rem;
	}

	.info-section-desc {
		font-size: 0.85rem;
		color: var(--color-text-muted);
		margin: 0 0 0.75rem 0;
	}

	.info-versions {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
		margin: 0.75rem 0;
	}

	.info-version {
		display: flex;
		gap: 0.75rem;
		align-items: baseline;
	}

	.info-version-desc {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.5;
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
