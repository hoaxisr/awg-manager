<script lang="ts">
	// Вкладка «AWG» страницы туннелей — выделено из routes/+page.svelte
	// (класс 2). Разметка перенесена дословно с заменой обращений к
	// состоянию страницы на ctx.* — единый проп-контекст с live-геттерами
	// (см. awgTabContext.ts). Сторы сортировки — прямым импортом.
	import { StatStrip, Stat, LayoutViewToggle, Button, Badge, TableSortHeader, StatusDot, TrafficSparkline, StoreStatusBadge, Toggle } from '$lib/components/ui';
	import { TunnelCard, ExternalTunnelCard, SystemTunnelCard, TunnelToolbarViewRow, AdoptTunnelDialog, TunnelLockGlyph } from '$lib/components/tunnels';
	import { EmptyState } from '$lib/components/layout';
	import { awgTunnelTableSort } from '$lib/stores/tunnelTableSort';
	import { formatBitRate, formatBytes } from '$lib/utils/format';
	import { pluralForm, TUNNEL_WORDS } from '$lib/utils/pluralize';
	import { ariaSort } from '$lib/utils/tunnelTableSort';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';
	import TunnelSectionHeader from '$lib/components/tunnels/TunnelSectionHeader.svelte';
	import TunnelTitleRow from '$lib/components/tunnels/TunnelTitleRow.svelte';
	import TunnelMetaText from '$lib/components/tunnels/TunnelMetaText.svelte';
	import TunnelListTrafficCell from '$lib/components/tunnels/TunnelListTrafficCell.svelte';
	import { formatRelativeTime, formatDuration } from '$lib/utils/format';
	import { type AwgTunnelSortKey } from '$lib/stores/tunnelTableSort';
	import { goto } from '$app/navigation';
	import { tunnels } from '$lib/stores/tunnels';
	import TunnelListActions from '$lib/components/ui/TunnelListActions.svelte';
	import DefaultRouteBadge from '$lib/components/tunnels/DefaultRouteBadge.svelte';
	import TunnelPingButton from '$lib/components/tunnels/TunnelPingButton.svelte';
	import { secondsSince } from '$lib/utils/format';
	import { awgManagedStatusDot } from '$lib/utils/statusDot';
	import { awgPingStatusNote, awgShowConnectivityRow, awgRecoveringVisual, awgToggleTint } from '$lib/utils/awgPingStatus';
	import { Eye, EyeOff, Upload, Download, Server } from 'lucide-svelte';
	import { showSummary } from '$lib/stores/showSummary';
	import type { AwgTabContext } from './awgTabContext';

	let { ctx }: { ctx: AwgTabContext } = $props();
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

{#snippet exportIcon()}
	<Download size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#if (!ctx.dashboardOn || ctx.dashboardNothingAtAll) && ctx.awgList.length === 0 && ctx.systemList.length === 0}
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
	class="ghost-terminal"
	class:drag-over={ctx.dragOver}
	ondrop={ctx.handleDrop}
	ondragover={ctx.handleDragOver}
	ondragleave={ctx.handleDragLeave}
>
	{#if ctx.dragOver}
		<div class="drop-overlay">
			<Upload size={40} strokeWidth={1.5} aria-hidden="true" />
			<span class="drop-text">Отпустите для импорта</span>
		</div>
	{:else if ctx.importing}
		<div class="drop-overlay">
			<div class="spinner"></div>
			<span class="drop-text">Импорт...</span>
		</div>
	{:else}
		<div class="term-status">
			<span class="term-prompt">$ awg status</span>
			{#if ctx.statusLine}
				<span class="term-info">{ctx.statusLine}</span>
			{/if}
		</div>

		<div class="term-action-group">
			<div class="term-drop-hint">
				<Upload size={28} strokeWidth={1.5} aria-hidden="true" />
				<span>Перетащите .conf сюда</span>
			</div>

			<div class="term-backend-selector">
				<button
					type="button"
					class="term-backend-btn"
					class:selected={ctx.selectedBackend === 'nativewg'}
					class:disabled={ctx.sysInfo !== null && !ctx.sysInfo.backendAvailability?.nativewg}
					disabled={ctx.sysInfo !== null && !ctx.sysInfo.backendAvailability?.nativewg}
					title={ctx.nativewgHint}
					onclick={() => ctx.selectedBackend = 'nativewg'}
				>
					NativeWG
				</button>
				<button
					type="button"
					class="term-backend-btn"
					class:selected={ctx.selectedBackend === 'kernel'}
					class:disabled={ctx.sysInfo !== null && !ctx.sysInfo.backendAvailability?.kernel}
					disabled={ctx.sysInfo !== null && !ctx.sysInfo.backendAvailability?.kernel}
					onclick={() => ctx.selectedBackend = 'kernel'}
				>
					Kernel
				</button>
			</div>
			{#if ctx.nativewgHint}
				<p class="term-backend-hint">{ctx.nativewgHint}</p>
			{/if}

			<div class="term-commands">
				{#if ctx.externalList.length > 0}
					<span class="term-found">
						найдено {ctx.externalList.length} внешних интерфейс{ctx.externalList.length === 1 ? '' : 'а'}
					</span>
					<button class="term-cmd term-cmd-primary" onclick={() => {
						ctx.adoptingInterface = ctx.externalList[0].interfaceName;
						ctx.adoptDialogOpen = true;
					}}>
						<span class="term-arrow">{'>'}</span> подхватить интерфейсы
					</button>
				{/if}
				<button class="term-cmd" onclick={() => ctx.fileInput?.click()}>
					<span class="term-arrow">{'>'}</span> импортировать файл
				</button>
				<button class="term-cmd" onclick={() => goto('/tunnels/new?tab=vpn')}>
					<span class="term-arrow">{'>'}</span> импортировать ссылку
				</button>
			</div>
		</div>

		<input
			type="file"
			accept=".conf"
			bind:this={ctx.fileInput}
			onchange={ctx.handleFileSelect}
			style="display: none"
		/>
	{/if}
</div>

<div class="info-card">
	<h3 class="info-title">Об AmneziaWG</h3>
	<p class="info-section-desc">
		Форк WireGuard с обфускацией трафика. Три поколения протокола:
	</p>
	<div class="info-versions">
		<div class="info-version">
			<Badge variant="accent" size="sm" mono>AWG 1.0</Badge>
			<span class="info-version-desc">Базовая обфускация: модификация заголовков (H1–H4), junk-пакеты (Jc/Jmin/Jmax), размеры сообщений (S1–S2).</span>
		</div>
		<div class="info-version">
			<Badge variant="info" size="sm" mono>AWG 1.5</Badge>
			<span class="info-version-desc">Мимикрия протоколов: initiation-пакеты (I1–I5) маскируют соединение под QUIC, DTLS, STUN, DNS.</span>
		</div>
		<div class="info-version">
			<Badge variant="success" size="sm" mono>AWG 2.0</Badge>
			<span class="info-version-desc">Рандомизация заголовков: H1–H4 задаются диапазонами, генерируются при каждом хэндшейке.</span>
		</div>
	</div>
	<p class="info-text info-kernel">
		Работает через <strong>модуль ядра</strong> — трафик обрабатывается напрямую в ядре Linux, что снижает нагрузку на CPU.
	</p>
</div>

{:else}
	{@const totalCount = ctx.awgSummaryTotal}
	{#if !ctx.dashboardOn}
	<div class="tunnels-toolbar">
		<div class="count-group">
			<span class="tunnel-count">{totalCount} {pluralForm(totalCount, TUNNEL_WORDS)}</span>
			<StoreStatusBadge store={tunnels} />
		</div>
		<div class="toolbar-actions">
			<TunnelToolbarViewRow
				sourceRowCount={ctx.awgSourceRowCount}
				showViewToggle={ctx.showAwgViewModeSwitch}
				searchQuery={ctx.awgListSearchQuery}
				onSearchChange={(value) => (ctx.awgListSearchQuery = value)}
			>
				{#snippet viewToggle()}
					<LayoutViewToggle
						value={ctx.awgViewMode}
						denseValue="cards"
						ariaLabel="Вид туннелей"
						onchange={(mode) => (ctx.awgViewMode = mode)}
					/>
				{/snippet}
			</TunnelToolbarViewRow>
			<Button variant="secondary" size="md" onclick={ctx.handleExportAll} disabled={ctx.exporting} iconBefore={exportIcon}>
				Экспорт
			</Button>
			<Button variant="primary" size="md" onclick={() => goto('/tunnels/new')} iconBefore={createIcon}>
				Создать
			</Button>
		</div>
	</div>
	{/if}
	{#if !ctx.dashboardOn && $showSummary}
		<div class="awg-summary-row">
			<StatStrip>
				<Stat
					value={`${ctx.awgSummaryActive}/${ctx.awgSummaryTotal}`}
					label={pluralForm(ctx.awgSummaryActive, TUNNEL_WORDS)}
					sub={`AWG ${ctx.awgList.length} · system ${ctx.visibleSystemList.length} · external ${ctx.externalList.length}`}
				/>
				<Stat
					value={formatBitRate(ctx.awgSummaryPeak.rate)}
					label="Пиковая скорость"
					sub={ctx.awgSummaryPeak.name}
				/>
				<Stat
					value={formatBytes(ctx.awgSummaryRx + ctx.awgSummaryTx)}
					label="Суммарный обмен"
					sub={`↓ ${formatBytes(ctx.awgSummaryRx)} · ↑ ${formatBytes(ctx.awgSummaryTx)}`}
				/>
				<Stat
					value={ctx.awgTrafficLeader.bytes > 0 ? formatBytes(ctx.awgTrafficLeader.bytes) : '—'}
					label="Лидер по трафику"
					sub={ctx.awgTrafficLeader.name}
				/>
			</StatStrip>
		</div>
	{/if}
	{#if ctx.effectiveAwgRenderMode === 'table'}
		<div class="awg-list-table">
			<div class="awg-list-table-track">
			<div class="awg-list-row awg-list-row--head">
				<span></span>
				<span role="columnheader" aria-sort={ariaSort($awgTunnelTableSort.sortBy, 'name', $awgTunnelTableSort.sortAsc)}>
					<TableSortHeader
						label="Туннель"
						sortKey={'name'}
						activeSortKey={$awgTunnelTableSort.sortBy}
						sortAsc={$awgTunnelTableSort.sortAsc}
						onchange={(key) => ctx.handleAwgSortChange(key as AwgTunnelSortKey)}
					/>
				</span>
				<span role="columnheader" aria-sort={ariaSort($awgTunnelTableSort.sortBy, 'status', $awgTunnelTableSort.sortAsc)}>
					<TableSortHeader
						label="Статус"
						sortKey={'status'}
						activeSortKey={$awgTunnelTableSort.sortBy}
						sortAsc={$awgTunnelTableSort.sortAsc}
						onchange={(key) => ctx.handleAwgSortChange(key as AwgTunnelSortKey)}
					/>
				</span>
				<span role="columnheader" aria-sort={ariaSort($awgTunnelTableSort.sortBy, 'endpoint', $awgTunnelTableSort.sortAsc)}>
					<TableSortHeader
						label="Endpoint"
						sortKey={'endpoint'}
						activeSortKey={$awgTunnelTableSort.sortBy}
						sortAsc={$awgTunnelTableSort.sortAsc}
						onchange={(key) => ctx.handleAwgSortChange(key as AwgTunnelSortKey)}
					/>
				</span>
				<span role="columnheader" aria-sort={ariaSort($awgTunnelTableSort.sortBy, 'traffic', $awgTunnelTableSort.sortAsc)}>
					<TableSortHeader
						label="Трафик"
						sortKey={'traffic'}
						activeSortKey={$awgTunnelTableSort.sortBy}
						sortAsc={$awgTunnelTableSort.sortAsc}
						onchange={(key) => ctx.handleAwgSortChange(key as AwgTunnelSortKey)}
					/>
				</span>
				<span class="awg-list-head-actions">Действия</span>
			</div>

		{#each ctx.sortedFilteredAwgList as tunnel (tunnel.id)}
			{@const connectivity = ctx.awgConnectivityMap.get(tunnel.id)}
			{@const isEndpointShown = ctx.endpointVisible('managed', tunnel.id)}
			{@const rate = ctx.latestRate(tunnel.id)}
			{@const spark = ctx.sparklineSeries(tunnel.id)}
			{@const isActive = ctx.isManagedTunnelOn(tunnel)}
			{@const checkDisabled = (tunnel.connectivityCheck?.method ?? 'http') === 'disabled'}
			{@const connState = !isActive ? 'idle'
				: connectivity === undefined ? 'checking'
				: connectivity.connected ? 'connected' : 'disconnected'}
			{@const statusDot = awgManagedStatusDot(tunnel, connectivity)}
			{@const pingStatusNote = awgPingStatusNote(tunnel, 'short')}
			{@const showPing = ctx.showManagedPing(tunnel, connectivity) || pingStatusNote !== null}
			{@const showConnectivityRow = awgShowConnectivityRow(tunnel.status)}
				<div class="awg-list-row">
				<div class="awg-list-cell awg-list-cell-toggle">
					<TunnelLockGlyph
						locked={!!tunnel.locked}
						onclick={() => ctx.handleLockClick(tunnel.id)}
					/>
					<span title={tunnel.locked ? 'Туннель защищён от изменений' : undefined}>
						<Toggle
							checked={ctx.isManagedTunnelOn(tunnel)}
							size="sm"
							variant="flip"
							tint={awgToggleTint(tunnel, connectivity)}
							disabled={(ctx.toggleLoading[tunnel.id] ?? false) || tunnel.hasAddressConflict === true || !!tunnel.locked}
							onchange={() => ctx.handleToggleOnOff(tunnel.id)}
						/>
					</span>
				</div>
					<div class="awg-list-cell awg-list-cell-name">
						<div class="tunnel-list-name-stack">
							<TunnelTitleRow
								title={tunnel.name}
								showDot={false}
								onTitleClick={() => ctx.openDetail(tunnel.id)}
							>
								{#snippet badges()}
									<DefaultRouteBadge defaultRoute={tunnel.defaultRoute} />
									{#if tunnel.backend}
										<span class="awg-inline-badge">{tunnel.backend}</span>
									{/if}
									{#if tunnel.awgVersion}
										<span class="awg-inline-badge awg-inline-badge--muted">{tunnel.awgVersion}</span>
									{/if}
								{/snippet}
							</TunnelTitleRow>
							<TunnelMetaText>
								{tunnel.address || '—'}
								<span class="meta-dot" aria-hidden="true">·</span>
								{tunnel.interfaceName || tunnel.id}
								<span class="meta-dot" aria-hidden="true">·</span>
								MTU {tunnel.mtu ?? '—'}
							</TunnelMetaText>
							<TunnelMetaText mono>
								Uptime {tunnel.startedAt ? formatDuration(secondsSince(tunnel.startedAt)) : '—'}
							</TunnelMetaText>
						</div>
					</div>
					<div class="awg-list-cell awg-list-cell-status">
						<div class="awg-list-status-stack">
							<div class="awg-list-status-line">
							<StatusDot
								variant={statusDot.variant}
								pulse={statusDot.pulse}
								ariaLabel={statusDot.label}
							/>
							<span class="awg-list-status-text">{statusDot.label}</span>
							</div>
							<div class="awg-list-sub awg-list-handshake">
								Handshake {tunnel.lastHandshake ? formatRelativeTime(tunnel.lastHandshake) : '—'}
							</div>
							{#if tunnel.hasAddressConflict}
						<div class="awg-list-sub awg-list-sub--error">Дублирует адрес уже запущенного туннеля</div>
					{:else if showConnectivityRow}
						<div
							class="awg-list-connectivity-row"
							class:recovering={awgRecoveringVisual(tunnel)}
						>
							{#if showPing}
								<TunnelPingButton
									layout="list"
									connectivity={connState}
									latencyMs={connectivity?.latency ?? null}
									statusNote={pingStatusNote?.text}
									statusNoteTone={pingStatusNote?.tone}
									checking={ctx.pingChecking[tunnel.id] ?? false}
									onclick={() => ctx.checkPing(tunnel.id)}
								/>
							{/if}
							<button
								type="button"
								class="awg-connectivity-gear"
								onclick={() => ctx.openConnectivitySettings(tunnel)}
								title="Настройки проверки связности"
							>
								<svg width="14" height="14" viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
									<path fill-rule="evenodd" d="M7.84 1.804A1 1 0 018.82 1h2.36a1 1 0 01.98.804l.331 1.652a6.993 6.993 0 011.929 1.115l1.598-.54a1 1 0 011.186.447l1.18 2.044a1 1 0 01-.205 1.251l-1.267 1.113a7.047 7.047 0 010 2.228l1.267 1.113a1 1 0 01.206 1.25l-1.18 2.045a1 1 0 01-1.187.447l-1.598-.54a6.993 6.993 0 01-1.929 1.115l-.33 1.652a1 1 0 01-.98.804H8.82a1 1 0 01-.98-.804l-.331-1.652a6.993 6.993 0 01-1.929-1.115l-1.598.54a1 1 0 01-1.186-.447l-1.18-2.044a1 1 0 01.205-1.251l1.267-1.114a7.05 7.05 0 010-2.227L1.821 7.773a1 1 0 01-.206-1.25l1.18-2.045a1 1 0 011.187-.447l1.598.54A6.993 6.993 0 017.51 3.456l.33-1.652zM10 13a3 3 0 100-6 3 3 0 000 6z" clip-rule="evenodd" />
								</svg>
							</button>
						</div>
					{:else if isActive && checkDisabled}
						<div class="awg-list-sub">Проверка связи выключена</div>
					{/if}
					</div>
					</div>
					<div class="awg-list-cell">
						<div class="awg-list-kv-primary awg-list-mono awg-endpoint-line">
							<span class="awg-endpoint-value" title={isEndpointShown ? ctx.endpointHost(tunnel.endpoint) : ''}>
								{#if tunnel.endpoint}
									{isEndpointShown ? ctx.endpointHost(tunnel.endpoint) : '•••••••••'}
								{:else}
									—
								{/if}
							</span>
							{#if tunnel.endpoint}
								<button
									type="button"
									class="awg-endpoint-eye"
									onclick={() => ctx.toggleEndpointVisible('managed', tunnel.id)}
									title={isEndpointShown ? 'Скрыть' : 'Показать'}
								>
									{#if isEndpointShown}
										<Eye size={14} aria-hidden="true" />
									{:else}
										<EyeOff size={14} aria-hidden="true" />
									{/if}
								</button>
							{/if}
							{#if ctx.endpointPort(tunnel.endpoint)}
								<span class="awg-endpoint-port">:{ctx.endpointPort(tunnel.endpoint)}</span>
							{/if}
						</div>
						<div class="awg-list-sub">{ctx.managedRouteMeta(tunnel)}</div>
					</div>
					<div class="awg-list-cell awg-list-cell-rate">
						<TunnelListTrafficCell
							rxRate={rate.rx}
							txRate={rate.tx}
							rxData={spark.rx}
							txData={spark.tx}
							onclick={() => ctx.openDetail(tunnel.id)}
							title="Открыть детали туннеля"
						/>
					</div>
					<div class="awg-list-cell awg-list-cell-actions tunnel-list-cell--actions">
						<TunnelListActions
							editHref="/tunnels/{tunnel.id}"
							editDisabled={!!tunnel.locked}
							editTitle={tunnel.locked ? 'Туннель защищён от изменений' : `Изменить туннель «${tunnel.name}»`}
							onTest={() => ctx.openAwgDiagnostics(tunnel.id, tunnel.name)}
							testTitle="Тест туннеля «{tunnel.name}»"
							onDelete={() => ctx.requestDelete(tunnel.id)}
							deleteDisabled={!!tunnel.locked}
							deleteTitle={tunnel.locked ? 'Туннель защищён от изменений' : `Удалить туннель «${tunnel.name}»`}
							deleting={ctx.deleteLoading[tunnel.id] ?? false}
						/>
					</div>
				</div>
			{/each}

			{#if ctx.sortedFilteredSystemList.length > 0}
				{#if ctx.dashboardSectionsLayout}
					<TunnelSectionHeader
						nested
						title="Системные"
						count={ctx.sortedFilteredSystemList.length}
						countLabel={pluralForm(ctx.sortedFilteredSystemList.length, TUNNEL_WORDS)}
					/>
				{:else}
					<div class="awg-list-row awg-list-row--section">
						<div class="awg-list-section-title">Системные · {ctx.sortedFilteredSystemList.length}</div>
					</div>
				{/if}
				{#each ctx.sortedFilteredSystemList as tunnel (tunnel.id)}
					{@const isEndpointShown = ctx.endpointVisible('system', tunnel.id)}
					{@const rate = ctx.latestRate(tunnel.id)}
					{@const spark = ctx.sparklineSeries(tunnel.id)}
					<div class="awg-list-row">
						<div class="awg-list-cell awg-list-cell-toggle">
							<span class="awg-row-placeholder">SYS</span>
						</div>
						<div class="awg-list-cell awg-list-cell-name">
							<div class="tunnel-list-name-stack">
								<TunnelTitleRow
									title={tunnel.description || tunnel.id}
									showDot={false}
									onTitleClick={() => ctx.openDetail(tunnel.id)}
								>
									{#snippet badges()}
										<span class="awg-inline-badge awg-inline-badge--muted">system</span>
									{/snippet}
								</TunnelTitleRow>
								<TunnelMetaText mono>
									{tunnel.interfaceName}
									{#if tunnel.address}
										<span class="meta-dot" aria-hidden="true">·</span>
										{tunnel.address}
									{/if}
									<span class="meta-dot" aria-hidden="true">·</span>
									MTU {tunnel.mtu}
								</TunnelMetaText>
								<TunnelMetaText mono>
									Uptime {tunnel.status === 'up' && tunnel.uptime ? formatDuration(tunnel.uptime) : '—'}
								</TunnelMetaText>
							</div>
						</div>
						<div class="awg-list-cell awg-list-cell-status">
							<div class="awg-list-status-line">
								<StatusDot
									variant={ctx.systemStatusVariant(tunnel)}
									ariaLabel={ctx.systemStatusLabel(tunnel)}
								/>
								<span class="awg-list-status-text">{ctx.systemStatusLabel(tunnel)}</span>
							</div>
							<div class="awg-list-sub awg-list-handshake">
								Handshake {tunnel.peer?.lastHandshake ? formatRelativeTime(tunnel.peer.lastHandshake) : '—'}
							</div>
							<div class="awg-list-sub">{tunnel.peer?.via || 'Маршрут не определён'}</div>
						</div>
						<div class="awg-list-cell">
						<div class="awg-list-kv-primary awg-list-mono awg-endpoint-line">
							<span class="awg-endpoint-value" title={isEndpointShown ? ctx.endpointHost(tunnel.peer?.endpoint) : ''}>
								{#if tunnel.peer?.endpoint}
									{isEndpointShown ? ctx.endpointHost(tunnel.peer.endpoint) : '•••••••••'}
								{:else}
									—
								{/if}
							</span>
							{#if tunnel.peer?.endpoint}
								<button
									type="button"
									class="awg-endpoint-eye"
									onclick={() => ctx.toggleEndpointVisible('system', tunnel.id)}
									title={isEndpointShown ? 'Скрыть' : 'Показать'}
								>
									{#if isEndpointShown}
										<Eye size={14} aria-hidden="true" />
									{:else}
										<EyeOff size={14} aria-hidden="true" />
									{/if}
								</button>
							{/if}
							{#if ctx.endpointPort(tunnel.peer?.endpoint)}
								<span class="awg-endpoint-port">:{ctx.endpointPort(tunnel.peer?.endpoint)}</span>
							{/if}
						</div>
							<div class="awg-list-sub">{tunnel.address || '—'}</div>
						</div>
						<div class="awg-list-cell awg-list-cell-rate">
							<TunnelListTrafficCell
								rxRate={rate.rx}
								txRate={rate.tx}
								rxData={spark.rx}
								txData={spark.tx}
								onclick={() => ctx.openDetail(tunnel.id)}
								title="Открыть детали туннеля"
							/>
						</div>
						<div class="awg-list-cell awg-list-cell-actions tunnel-list-cell--actions">
							<TunnelListActions
								editHref="/system-tunnels/{tunnel.id}"
								editTitle="Изменить туннель «{tunnel.description || tunnel.id}»"
								onTest={() => ctx.openAwgDiagnostics(tunnel.id, tunnel.description || tunnel.id, 'system')}
								testTitle="Тест туннеля «{tunnel.description || tunnel.id}»"
							>
								{#snippet extra()}
									<button
										type="button"
										class="tunnel-list-actions__btn tunnel-list-actions__btn--primary"
										title="Перенести туннель «{tunnel.description || tunnel.id}» в серверы"
										aria-label="Перенести туннель «{tunnel.description || tunnel.id}» в серверы"
										onclick={() => ctx.markAsServer(tunnel.id)}
									>
										<Server size={14} aria-hidden="true" />
									</button>
								{/snippet}
							</TunnelListActions>
						</div>
					</div>
				{/each}
			{/if}

			{#if ctx.sortedFilteredExternalList.length > 0}
				{#if ctx.dashboardSectionsLayout}
					<TunnelSectionHeader
						nested
						title="Внешние"
						count={ctx.sortedFilteredExternalList.length}
						countLabel={pluralForm(ctx.sortedFilteredExternalList.length, TUNNEL_WORDS)}
					/>
				{:else}
					<div class="awg-list-row awg-list-row--section">
						<div class="awg-list-section-title">Внешние · {ctx.sortedFilteredExternalList.length}</div>
					</div>
				{/if}
				{#each ctx.sortedFilteredExternalList as tunnel (tunnel.interfaceName)}
					{@const isEndpointShown = ctx.endpointVisible('external', tunnel.interfaceName)}
					<div class="awg-list-row">
						<div class="awg-list-cell awg-list-cell-toggle">
							<span class="awg-row-placeholder">ext</span>
						</div>
						<div class="awg-list-cell awg-list-cell-name">
							<div class="awg-list-name-line">
								<span class="awg-list-name-static">{tunnel.interfaceName}</span>
								<span class="awg-inline-badge awg-inline-badge--muted">external</span>
								{#if tunnel.isAWG}
									<span class="awg-inline-badge">AWG</span>
								{/if}
							</div>
							<div class="awg-list-sub">
								{#if tunnel.publicKey}
									{tunnel.publicKey.slice(0, 16)}…
									<span class="awg-list-dot">·</span>
								{/if}
								#{tunnel.tunnelNumber}
							</div>
						</div>
						<div class="awg-list-cell awg-list-cell-status">
							<div class="awg-list-status-line">
								<StatusDot
									variant={ctx.externalStatusVariant(tunnel)}
									ariaLabel={ctx.externalStatusLabel(tunnel)}
								/>
								<span class="awg-list-status-text">{ctx.externalStatusLabel(tunnel)}</span>
							</div>
							<div class="awg-list-sub awg-list-handshake">
								Handshake {tunnel.lastHandshake ? formatRelativeTime(tunnel.lastHandshake) : '—'}
							</div>
							<div class="awg-list-sub">Не управляется AWG Manager</div>
						</div>
						<div class="awg-list-cell">
							<div class="awg-list-kv-primary awg-list-mono awg-endpoint-line">
								<span class="awg-endpoint-value" title={isEndpointShown ? ctx.endpointHost(tunnel.endpoint) : ''}>
									{#if tunnel.endpoint}
										{isEndpointShown ? ctx.endpointHost(tunnel.endpoint) : '•••••••••'}
									{:else}
										—
									{/if}
								</span>
								{#if tunnel.endpoint}
									<button
										type="button"
										class="awg-endpoint-eye"
										onclick={() => ctx.toggleEndpointVisible('external', tunnel.interfaceName)}
										title={isEndpointShown ? 'Скрыть' : 'Показать'}
									>
										{#if isEndpointShown}
											<Eye size={14} aria-hidden="true" />
										{:else}
											<EyeOff size={14} aria-hidden="true" />
										{/if}
									</button>
								{/if}
								{#if ctx.endpointPort(tunnel.endpoint)}
									<span class="awg-endpoint-port">:{ctx.endpointPort(tunnel.endpoint)}</span>
								{/if}
							</div>
							<div class="awg-list-sub">WG интерфейс</div>
						</div>
						<div class="awg-list-cell awg-list-cell-rate">
							<div class="awg-list-rate-stack awg-list-mono">
								<div class="traffic-rate rx">↓ {formatBytes(tunnel.rxBytes)}</div>
								<TrafficSparkline rxData={[]} txData={[]} responsive height={18} />
								<div class="traffic-rate tx">↑ {formatBytes(tunnel.txBytes)}</div>
							</div>
						</div>
						<div class="awg-list-cell awg-list-cell-actions">
							<!-- Короткая надпись: полная не влезала в колонку действий и
							     вылезала за неё на узких экранах. Смысл — в title. -->
							<Button
								variant="primary"
								size="sm"
								title="Взять под управление: {tunnel.interfaceName}"
								onclick={() => ctx.handleAdoptClick(tunnel.interfaceName)}
							>
								Взять
							</Button>
						</div>
					</div>
				{/each}
			{/if}
			{#if ctx.awgSearchEmpty}
				<div class="awg-list-row awg-list-row--section">
					<div class="awg-list-section-title">Ничего не найдено</div>
				</div>
			{/if}
			</div>
		</div>
	{:else}
		{@const awgGridView = ctx.effectiveAwgRenderMode === 'list-card' ? 'list' : ctx.effectiveAwgCardViewMode}
		<div
			class="tunnel-grid"
			class:tunnel-grid--list={ctx.effectiveAwgRenderMode === 'list-card'}
			class:tunnel-grid--dense={ctx.effectiveAwgRenderMode !== 'list-card' && ctx.effectiveAwgEffectiveViewMode === 'cards'}
			class:tunnel-grid--compact={ctx.effectiveAwgRenderMode !== 'list-card' && ctx.effectiveAwgEffectiveViewMode === 'compact'}
		>
			{#each ctx.sortedFilteredAwgList as tunnel, i (tunnel.id)}
				<TunnelCard
					{tunnel}
					view={awgGridView}
					toggleLoading={ctx.toggleLoading[tunnel.id] ?? false}
					deleteLoading={ctx.deleteLoading[tunnel.id] ?? false}
					autoConnectivityNonce={ctx.awgAutoConnectivityNonce}
					autoConnectivityDelayMs={i * 180}
					onToggleOnOff={() => ctx.handleToggleOnOff(tunnel.id)}
					ondelete={() => ctx.requestDelete(tunnel.id)}
					ondetail={(id) => ctx.openDetail(id)}
					onLockClick={() => ctx.handleLockClick(tunnel.id)}
				/>
			{/each}
			{#each ctx.sortedFilteredSystemList as tunnel (tunnel.id)}
				<SystemTunnelCard
					{tunnel}
					view={awgGridView}
					onMarkServer={ctx.markAsServer}
					ondetail={(id) => ctx.openDetail(id)}
					ontest={(id, name) => ctx.openAwgDiagnostics(id, name, 'system')}
				/>
			{/each}
		</div>

		{#if ctx.sortedFilteredExternalList.length > 0}
			<div
				class:external-section={!ctx.dashboardSectionsLayout}
			>
				{#if ctx.dashboardSectionsLayout}
					<TunnelSectionHeader
						nested
						title="Внешние"
						count={ctx.sortedFilteredExternalList.length}
						countLabel={pluralForm(ctx.sortedFilteredExternalList.length, TUNNEL_WORDS)}
					/>
				{:else}
					<h2 class="section-title">Внешние туннели</h2>
				{/if}
				<div
					class="tunnel-grid"
					class:tunnel-grid--list={ctx.effectiveAwgRenderMode === 'list-card'}
					class:tunnel-grid--dense={ctx.effectiveAwgRenderMode !== 'list-card' && ctx.effectiveAwgEffectiveViewMode === 'cards'}
					class:tunnel-grid--compact={ctx.effectiveAwgRenderMode !== 'list-card' && ctx.effectiveAwgEffectiveViewMode === 'compact'}
				>
					{#each ctx.sortedFilteredExternalList as extTunnel (extTunnel.interfaceName)}
						<ExternalTunnelCard
							tunnel={extTunnel}
							view={awgGridView}
							onadopt={(name) => ctx.handleAdoptClick(name)}
						/>
					{/each}
				</div>
			</div>
		{/if}
		{#if ctx.awgSearchEmpty}
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

	.count-group {
		display: flex;
		align-items: center;
		gap: 0.5rem;
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

	/* Empty-state ghost terminal — page-specific */
	.ghost-terminal {
		margin: 3rem 0;
		border: 2px dashed var(--color-border);
		border-radius: var(--radius);
		padding: 2rem 2rem 1.5rem;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1.5rem;
		transition: border-color var(--t-fast) ease, background var(--t-fast) ease;
	}

	.ghost-terminal.drag-over {
		border-color: var(--color-accent);
		border-style: solid;
		background: var(--color-accent-tint);
	}

	.term-status {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.25rem;
		font-family: var(--font-mono);
	}

	.term-prompt {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.term-info {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		opacity: 0.7;
	}

	.term-action-group {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1.5rem;
	}

	.term-drop-hint {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		color: var(--color-accent);
		font-size: 1.0625rem;
		font-weight: 500;
	}

	.term-drop-hint :global(svg) {
		flex-shrink: 0;
		opacity: 0.8;
	}

	.term-commands {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.125rem;
		font-family: var(--font-mono);
	}

	.term-found {
		font-size: 0.8125rem;
		color: var(--color-accent);
		margin-bottom: 0.375rem;
	}

	.term-cmd {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		background: none;
		border: none;
		color: var(--color-text-secondary);
		font-family: inherit;
		font-size: 0.875rem;
		padding: 0.375rem 0.5rem;
		border-radius: var(--radius-sm);
		cursor: pointer;
		transition: color var(--t-fast) ease, background var(--t-fast) ease;
		text-decoration: none;
	}

	.term-cmd:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-hover);
	}

	.term-cmd-primary {
		color: var(--color-accent);
	}

	.term-cmd-primary:hover {
		color: var(--color-accent-hover);
	}

	.term-arrow {
		color: var(--color-text-muted);
	}

	/* Backend selector — chip-like toggles for nativewg/kernel */
	.term-backend-selector {
		display: flex;
		gap: 8px;
	}

	.term-backend-btn {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		padding: 0.375rem 1rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: border-color var(--t-fast) ease, color var(--t-fast) ease, background var(--t-fast) ease;
	}

	.term-backend-btn:hover:not(.disabled) {
		border-color: var(--color-accent);
		color: var(--color-text-secondary);
	}

	.term-backend-btn.selected {
		border-color: var(--color-accent);
		color: var(--color-accent);
		background: var(--color-accent-tint);
	}

	.term-backend-btn.disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}

	.term-backend-hint {
		margin: 8px 0 0;
		font-family: var(--font-mono);
		font-size: 0.75rem;
		line-height: 1.4;
		color: var(--color-text-muted);
	}

	/* Drag-over / importing overlays */
	.drop-overlay {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.75rem;
		padding: 2rem 0;
		color: var(--color-accent);
	}

	.drop-text {
		font-size: 1.0625rem;
		font-weight: 500;
	}

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

	.info-text {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.6;
		margin: 0;
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

	.info-kernel {
		margin-top: 0.75rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--color-border);
	}

	.info-kernel strong {
		color: var(--color-text-primary);
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
