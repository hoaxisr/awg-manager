<script lang="ts">
	// Вкладка «Sing-box туннели» страницы туннелей — выделено из
	// routes/+page.svelte (класс 2): разметка дословно, состояние — пропсами.
	import { StatStrip, Stat, LayoutViewToggle, Button, Badge, TableSortHeader } from '$lib/components/ui';
	import { TunnelToolbarViewRow } from '$lib/components/tunnels';
	import { SingboxInstallBanner, SingboxTunnelCard } from '$lib/components/singbox';
	import { singboxTunnelTableSort, type SingboxTunnelSortKey } from '$lib/stores/tunnelTableSort';
	import { formatRunningSub, pluralForm, TUNNEL_WORDS } from '$lib/utils/pluralize';
	import { formatBytes } from '$lib/utils/format';
	import { ariaSort } from '$lib/utils/tunnelTableSort';
	import type { SingboxLayoutMode, TunnelRenderMode } from '$lib/constants/singboxLayout';
	import type { SingboxTunnel } from '$lib/types';
	import type { SingboxTunnelListStats } from '$lib/components/subscriptions/subscriptionVMs';
	import { Globe, LayoutGrid, Link } from 'lucide-svelte';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';

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
		/** Активные подписки — тоже sing-box: при них тулбар виден и с пустым списком туннелей. */
		hasActiveSubscriptions: boolean;
		/** Тулбар (и его поиск/переключатель вида) рендерится только вне дашборда. */
		singboxTunnelsSearchQuery?: string;
		singboxTunnelsLayoutMode?: SingboxLayoutMode;
		handleSingboxTunnelSortChange: (key: SingboxTunnelSortKey) => void;
		openSingboxDetail: (tag: string) => void;
		openWizard: (preselect: 'choose' | 'single' | 'inline' | 'url') => void;
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
		hasActiveSubscriptions,
		singboxTunnelsSearchQuery = $bindable(''),
		singboxTunnelsLayoutMode = $bindable('compact'),
		handleSingboxTunnelSortChange,
		openSingboxDetail,
		openWizard,
	}: Props = $props();
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

	{#if !dashboardOn}
	<SingboxInstallBanner />
	{#if singboxTunnelsList.length > 0 || hasActiveSubscriptions}
		<div class="tunnels-toolbar">
			<span class="tunnel-count">
				{singboxTunnelsList.length}
				{pluralForm(singboxTunnelsList.length, TUNNEL_WORDS)}
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
							ariaLabel="Вид туннелей"
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
					Добавить
				</Button>
			</div>
		</div>
	{/if}
	{/if}
	{#if !dashboardOn && singboxTunnelsList.length === 0}
		<div class="empty-kinds">
			<button type="button" class="empty-kind-card" onclick={() => openWizard('single')}>
				<Link class="empty-kind-icon" size={28} strokeWidth={1.6} aria-hidden="true" />
				<div class="empty-kind-title">Один сервер</div>
				<div class="empty-kind-desc">
					Вставь share-link — получишь sing-box туннель со своим Proxy NDMS.
				</div>
			</button>
			<button type="button" class="empty-kind-card" onclick={() => openWizard('inline')}>
				<LayoutGrid class="empty-kind-icon" size={28} strokeWidth={1.6} aria-hidden="true" />
				<div class="empty-kind-title">Группа серверов</div>
				<div class="empty-kind-desc">
					Несколько ссылок одной группой с общим Proxy: ручной выбор или автовыбор по скорости.
				</div>
			</button>
			<button type="button" class="empty-kind-card" onclick={() => openWizard('url')}>
				<Globe class="empty-kind-icon" size={28} strokeWidth={1.6} aria-hidden="true" />
				<div class="empty-kind-title">Подписка по URL</div>
				<div class="empty-kind-desc">
					Адрес подписки провайдера — список обновляется автоматически.
				</div>
			</button>
		</div>
		<div class="info-card">
			<h3 class="info-title">О Sing-box</h3>
			<p class="info-section-desc">
				Универсальный прокси с поддержкой современных протоколов:
			</p>
			<div class="info-versions">
				<div class="info-version">
					<Badge variant="accent" size="sm" mono>VLESS</Badge>
					<span class="info-version-desc">Лёгкий протокол без шифрования на уровне протокола. Поддерживает <strong>Reality</strong> (маскировка под настоящий TLS-сервер) и транспорт gRPC для обхода DPI.</span>
				</div>
				<div class="info-version">
					<Badge variant="error" size="sm" mono>Trojan</Badge>
					<span class="info-version-desc">TLS-туннель с парольной аутентификацией. Работает поверх TCP, поддерживает WebSocket и gRPC как транспорт.</span>
				</div>
				<div class="info-version">
					<Badge variant="success" size="sm" mono>Shadowsocks</Badge>
					<span class="info-version-desc">Классический прокси с шифрованием на уровне приложения. Современные шифры (AES-GCM, ChaCha20) и плагины obfs-local / v2ray-plugin.</span>
				</div>
				<div class="info-version">
					<Badge variant="warning" size="sm" mono>Hysteria2</Badge>
					<span class="info-version-desc">QUIC-based, устойчив к потерям пакетов и работает поверх UDP. Паролевая аутентификация, обфускация salamander.</span>
				</div>
				<div class="info-version">
					<Badge variant="info" size="sm" mono>NaiveProxy</Badge>
					<span class="info-version-desc">HTTP/2 с полноценным TLS-маскированием под обычный HTTPS-сервер. Сложно отличим от браузерного трафика.</span>
				</div>
				<div class="info-version">
					<Badge variant="purple" size="sm" mono>Mieru</Badge>
					<span class="info-version-desc">Мультиплексированный прокси с парольной аутентификацией. TCP и UDP в одном профиле, несколько портов и транспортов.</span>
				</div>
			</div>
		</div>
	{:else if singboxTunnelsList.length > 0 || (dashboardOn && dashboardSingboxTunnels.length > 0)}
		{#if !dashboardOn}
			<div class="awg-summary-row">
				<StatStrip>
					<Stat
						value={`${singboxTunnelListStats.running}/${singboxTunnelListStats.count}`}
						label={pluralForm(singboxTunnelListStats.running, TUNNEL_WORDS)}
						sub={formatRunningSub(singboxTunnelListStats.running, singboxTunnelListStats.count)}
					/>
					<Stat
						value={formatBytes(singboxTunnelListStats.down + singboxTunnelListStats.up)}
						label="Суммарный трафик"
						sub={`↓ ${formatBytes(singboxTunnelListStats.down)} · ↑ ${formatBytes(singboxTunnelListStats.up)}`}
					/>
					<Stat
						value={singboxTunnelListStats.avgDelayMs !== null
							? `${singboxTunnelListStats.avgDelayMs} ms`
							: '—'}
						label="Средний delay"
						sub="по последним проверкам"
					/>
					<Stat
						value={singboxTunnelListStats.leaderBytes > 0
							? formatBytes(singboxTunnelListStats.leaderBytes)
							: '—'}
						label="Лидер по трафику"
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
								<TableSortHeader label="Туннель" sortKey={'name'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
							</th>
							<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'protocol', $singboxTunnelTableSort.sortAsc)}>
								<TableSortHeader label="Протокол" sortKey={'protocol'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
							</th>
							<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'running', $singboxTunnelTableSort.sortAsc)}>
								<TableSortHeader label="Процесс" sortKey={'running'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
							</th>
							<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'traffic', $singboxTunnelTableSort.sortAsc)}>
								<TableSortHeader label="Трафик" sortKey={'traffic'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
							</th>
							<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'ping', $singboxTunnelTableSort.sortAsc)}>
								<TableSortHeader label="Ping" sortKey={'ping'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
							</th>
							<th class="col-actions">Действия</th>
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
						<td colspan="7">Ничего не найдено</td>
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
				<p class="tunnel-list-empty">Ничего не найдено</p>
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

	/* Empty-state kind picker — three clickable cards opening the wizard
	   on the matching step 2. Mirrors the wizard's step-1 visual so the
	   transition into the modal feels continuous. */
	.empty-kinds {
		display: grid;
		grid-template-columns: 1fr;
		gap: 0.7rem;
		margin-top: 0.5rem;
	}

	@media (min-width: 600px) {
	.empty-kinds { grid-template-columns: 1fr 1fr 1fr; }
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
		border-color: var(--color-accent);
		background: rgba(59, 130, 246, 0.04);
		transform: translateY(-1px);
	}

	.empty-kind-card:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	:global(.empty-kind-icon) { color: var(--color-accent); }

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
