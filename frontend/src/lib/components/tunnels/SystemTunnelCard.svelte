<script lang="ts">
	import { untrack } from 'svelte';
	import { Eye, EyeOff, Server } from 'lucide-svelte';
	import type { SystemTunnel, ConnectivityResult } from '$lib/types';
	import { api } from '$lib/api/client';
	import { formatRelativeTime, formatDuration, formatBitRate } from '$lib/utils/format';
	import { TrafficChart, TrafficSparkline, Badge, PingButton, TunnelListActions } from '$lib/components/ui';
	import type { StatusDotVariant } from '$lib/components/ui/StatusDot.svelte';
	import TunnelTitleRow from '$lib/components/tunnels/TunnelTitleRow.svelte';
	import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';

	interface Props {
		tunnel: SystemTunnel;
		view?: 'cards' | 'compact' | 'list';
		onMarkServer?: (id: string) => void;
		ondetail?: (id: string) => void;
		ontest: (id: string, name: string) => void;
	}

	let { tunnel, view = 'cards', onMarkServer, ondetail, ontest }: Props = $props();

	let connectivity = $state<ConnectivityResult | null>(null);
	let checking = $state(false);
	let showEndpoint = $state(false);

	// Connectivity check toggle (persisted in localStorage)
	const CC_KEY_PREFIX = 'systunnel_cc_disabled_';
	// svelte-ignore state_referenced_locally — intentional: initial value from localStorage
	let checkDisabled = $state(localStorage.getItem(CC_KEY_PREFIX + tunnel.id) === 'true');

	function toggleCheckDisabled() {
		checkDisabled = !checkDisabled;
		localStorage.setItem(CC_KEY_PREFIX + tunnel.id, String(checkDisabled));
		if (checkDisabled) {
			connectivity = null;
		}
	}

	async function checkConnectivity() {
		if (tunnel.status !== 'up' || checking || checkDisabled) return;
		checking = true;
		try {
			connectivity = await api.checkSystemTunnelConnectivity(tunnel.id);
		} catch {
			connectivity = null;
		} finally {
			checking = false;
		}
	}

	// Auto-check connectivity every 60s when up
	$effect(() => {
		const status = tunnel.status;
		const disabled = checkDisabled;
		if (status !== 'up' || disabled) {
			connectivity = null;
			return;
		}
		untrack(() => checkConnectivity());
		const interval = setInterval(checkConnectivity, 60000);
		return () => clearInterval(interval);
	});

	let statusDot = $derived.by((): { variant: StatusDotVariant; pulse: boolean; label: string } => {
		if (tunnel.status !== 'up') {
			return { variant: 'muted', pulse: false, label: 'Выключен' };
		}
		if (!tunnel.peer?.online) {
			return { variant: 'warning', pulse: false, label: 'Без handshake' };
		}
		return { variant: 'success', pulse: false, label: 'Активен' };
	});

	// Traffic chart — live only (no server history for system tunnels)
	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);

	let initialLoadDone = false;
	$effect(() => {
		const id = tunnel.id;
		if (initialLoadDone) return;
		initialLoadDone = true;
		untrack(() => loadHistory(id));
	});

	$effect(() => {
		const id = tunnel.id;
		const update = () => {
			const t = getTrafficRates(id);
			rxRates = t.rx;
			txRates = t.tx;
		};
		update();
		return subscribeTraffic(update);
	});

	let chartHeight = $derived(view === 'compact' ? 76 : 100);

	let inlineRxRate = $derived(rxRates.length > 0 ? rxRates[rxRates.length - 1] : 0);
	let inlineTxRate = $derived(txRates.length > 0 ? txRates[txRates.length - 1] : 0);

	let isDenseCard = $derived(view === 'cards' || view === 'list');
	let isCompactCard = $derived(view === 'compact');
	let isListCard = $derived(view === 'list');

	type ConnectivityState = 'idle' | 'connected' | 'disconnected' | 'checking';
	let connState = $derived.by<ConnectivityState>(() => {
		if (tunnel.status !== 'up' || checkDisabled) return 'idle';
		if (checking || connectivity === null) return 'checking';
		return connectivity.connected ? 'connected' : 'disconnected';
	});
	let latencyMs = $derived(connectivity?.latency ?? null);
	let showConnectivityRow = $derived(tunnel.status === 'up');
	let showPingButton = $derived(showConnectivityRow && !checkDisabled);
	let compactStatusHint = $derived(
		isCompactCard && tunnel.status === 'up' && !tunnel.peer?.online ? 'Без handshake' : '',
	);

	let displayName = $derived(tunnel.description || tunnel.id);

	function openTest(): void {
		ontest(tunnel.id, displayName);
	}
</script>

	<div
		class="card flex flex-col transition-[border-color] duration-200"
		class:status-up={tunnel.status === 'up'}
		class:status-down={tunnel.status !== 'up'}
		class:view-compact={view === 'compact'}
		class:view-dense={view === 'cards' || view === 'list'}
		class:view-list={view === 'list'}
	>
		<!-- Header: name + status + connectivity -->
		{#if isDenseCard}
			<div class="header header-dense">
				<div class="header-dense-body">
					<div class="tunnel-name-row">
						<TunnelTitleRow
							title={displayName}
							dotVariant={statusDot.variant}
							dotPulse={statusDot.pulse}
							dotLabel={statusDot.label}
							dense
							onTitleClick={() => ondetail?.(tunnel.id)}
						/>
					</div>
					<div class="meta-tags-dense">
						<Badge variant="info" size="sm">Системный</Badge>
						<span class="iface-chip-dense" title={tunnel.interfaceName}>{tunnel.interfaceName}</span>
					</div>
				</div>
				{#if showConnectivityRow}
					<div class="dense-toolbar">
						<div class="dense-toolbar-bottom">
							{#if showPingButton}
								<PingButton
									connectivity={connState}
									{latencyMs}
									checking={checking}
									size="sm"
									onclick={checkConnectivity}
								/>
							{/if}
							<button
								class="connectivity-gear"
								class:gear-disabled={checkDisabled}
								onclick={toggleCheckDisabled}
								title={checkDisabled ? 'Проверка связности выключена. Нажмите для включения' : 'Выключить проверку связности'}
							>
								<svg width="11" height="11" viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
									<path fill-rule="evenodd" d="M7.84 1.804A1 1 0 018.82 1h2.36a1 1 0 01.98.804l.331 1.652a6.993 6.993 0 011.929 1.115l1.598-.54a1 1 0 011.186.447l1.18 2.044a1 1 0 01-.205 1.251l-1.267 1.113a7.047 7.047 0 010 2.228l1.267 1.113a1 1 0 01.206 1.25l-1.18 2.045a1 1 0 01-1.187.447l-1.598-.54a6.993 6.993 0 01-1.929 1.115l-.33 1.652a1 1 0 01-.98.804H8.82a1 1 0 01-.98-.804l-.331-1.652a6.993 6.993 0 01-1.929-1.115l-1.598.54a1 1 0 01-1.186-.447l-1.18-2.044a1 1 0 01.205-1.251l1.267-1.114a7.05 7.05 0 010-2.227L1.821 7.773a1 1 0 01-.206-1.25l1.18-2.045a1 1 0 011.187-.447l1.598.54A6.993 6.993 0 017.51 3.456l.33-1.652zM10 13a3 3 0 100-6 3 3 0 000 6z" clip-rule="evenodd" />
								</svg>
							</button>
						</div>
					</div>
				{/if}
			</div>
		{:else}
			<div class="header">
				<div class="head-left">
					<TunnelTitleRow
						title={displayName}
						dotVariant={statusDot.variant}
						dotPulse={statusDot.pulse}
						dotLabel={statusDot.label}
						onTitleClick={() => ondetail?.(tunnel.id)}
					/>
					<div class="meta-line">
						<span class="iface-name">{tunnel.interfaceName}</span>
						<span class="version-badge badge-system">Системный</span>
					</div>
					{#if compactStatusHint}
						<span class="status-hint-left">{compactStatusHint}</span>
					{/if}
				</div>
				{#if showConnectivityRow}
					<div class="head-right">
						<div class="connectivity-row">
							{#if showPingButton}
								<PingButton
									connectivity={connState}
									{latencyMs}
									checking={checking}
									onclick={checkConnectivity}
								/>
							{/if}
							<button
								class="connectivity-gear"
								class:gear-disabled={checkDisabled}
								onclick={toggleCheckDisabled}
								title={checkDisabled ? 'Проверка связности выключена. Нажмите для включения' : 'Выключить проверку связности'}
							>
								<svg width="14" height="14" viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
									<path fill-rule="evenodd" d="M7.84 1.804A1 1 0 018.82 1h2.36a1 1 0 01.98.804l.331 1.652a6.993 6.993 0 011.929 1.115l1.598-.54a1 1 0 011.186.447l1.18 2.044a1 1 0 01-.205 1.251l-1.267 1.113a7.047 7.047 0 010 2.228l1.267 1.113a1 1 0 01.206 1.25l-1.18 2.045a1 1 0 01-1.187.447l-1.598-.54a6.993 6.993 0 01-1.929 1.115l-.33 1.652a1 1 0 01-.98.804H8.82a1 1 0 01-.98-.804l-.331-1.652a6.993 6.993 0 01-1.929-1.115l-1.598.54a1 1 0 01-1.186-.447l-1.18-2.044a1 1 0 01.205-1.251l1.267-1.114a7.05 7.05 0 010-2.227L1.821 7.773a1 1 0 01-.206-1.25l1.18-2.045a1 1 0 011.187-.447l1.598.54A6.993 6.993 0 017.51 3.456l.33-1.652zM10 13a3 3 0 100-6 3 3 0 000 6z" clip-rule="evenodd" />
								</svg>
							</button>
						</div>
					</div>
				{/if}
			</div>
		{/if}

		{#if !isListCard}
		<!-- Details: endpoint + via + IPv4 + uptime + handshake -->
		<div class="details">
			{#if view === 'cards'}
				<div class="details-dense-cols">
					<div class="details-dense-col details-dense-col-lead">
						{#if tunnel.peer?.endpoint}
							<div class="kv-stacked-stat">
								<span class="kv-stacked-label">Сервер</span>
								<span class="kv-endpoint">
									<span
										class="kv-stacked-value truncate"
										title={showEndpoint ? tunnel.peer.endpoint : ''}
									>
										{showEndpoint ? tunnel.peer.endpoint : '•••••••••'}
									</span>
									<button
										class="eye-btn"
										onclick={() => showEndpoint = !showEndpoint}
										title={showEndpoint ? 'Скрыть' : 'Показать'}
									>
										{#if showEndpoint}
											<Eye size={12} aria-hidden="true" />
										{:else}
											<EyeOff size={12} aria-hidden="true" />
										{/if}
									</button>
								</span>
							</div>
						{/if}
						{#if tunnel.peer?.via}
							<div class="kv-stacked-stat">
								<span class="kv-stacked-label">Подключение</span>
								<span class="kv-stacked-value" title={tunnel.peer.via}>{tunnel.peer.via}</span>
							</div>
						{/if}
						{#if tunnel.address}
							<div class="kv-stacked-stat">
								<span class="kv-stacked-label">IPv4</span>
								<span class="kv-stacked-value">{tunnel.address}</span>
							</div>
						{/if}
					</div>
					<div class="details-dense-col details-dense-col-right">
						<div class="kv-stacked-stat">
							<span class="kv-stacked-label">MTU</span>
							<span class="kv-stacked-value">{tunnel.mtu}</span>
						</div>
						{#if tunnel.status === 'up'}
							<div class="kv-stacked-stat">
								<span class="kv-stacked-label">Uptime</span>
								<span class="kv-stacked-value">
									{tunnel.uptime ? formatDuration(tunnel.uptime) : '—'}
								</span>
							</div>
							<div class="kv-stacked-stat">
								<span class="kv-stacked-label">Handshake</span>
								<span class="kv-stacked-value">
									{tunnel.peer?.lastHandshake
										? formatRelativeTime(tunnel.peer.lastHandshake)
										: '—'}
								</span>
							</div>
						{/if}
					</div>
				</div>
			{:else}
			{#if tunnel.peer?.endpoint}
				<div class="flex gap-4 items-start">
					<div class="flex flex-col gap-0.5 min-w-0 flex-1">
						<span class="detail-label">Endpoint</span>
						<span class="flex items-center gap-1 min-w-0">
							<span class="detail-value truncate" title={showEndpoint ? tunnel.peer.endpoint : ''}>{showEndpoint ? tunnel.peer.endpoint : '•••••••••'}</span>
							<button
								class="eye-btn"
								onclick={() => showEndpoint = !showEndpoint}
								title={showEndpoint ? 'Скрыть' : 'Показать'}
							>
								{#if showEndpoint}
									<Eye size={14} aria-hidden="true" />
								{:else}
									<EyeOff size={14} aria-hidden="true" />
								{/if}
							</button>
						</span>
					</div>
				</div>
			{/if}
			{#if tunnel.peer?.via}
				<div class="flex gap-4 items-start">
					<div class="flex flex-col gap-0.5 min-w-0 flex-1">
						<span class="detail-label">Подключение</span>
						<span class="detail-value">{tunnel.peer.via}</span>
					</div>
				</div>
			{/if}
			{#if tunnel.address}
				<div class="flex gap-4 items-start">
					<div class="flex flex-col gap-0.5 min-w-0 flex-1">
						<span class="detail-label">IPv4</span>
						<span class="detail-value">{tunnel.address}</span>
					</div>
				</div>
			{/if}
			{#if tunnel.status === 'up' && (tunnel.uptime || tunnel.peer?.lastHandshake)}
				<hr class="divider" />
				<div class="flex items-start stats-row">
					<div class="flex flex-col gap-0.5 min-w-0 flex-1">
						<span class="detail-label">Uptime</span>
						<span class="detail-value text-[11px] whitespace-nowrap">
							{tunnel.uptime ? formatDuration(tunnel.uptime) : '—'}
						</span>
					</div>
					<div class="flex flex-col gap-0.5 min-w-0 flex-1 items-end">
						<span class="detail-label">Handshake</span>
						<span class="detail-value text-[11px] whitespace-nowrap">
							{tunnel.peer?.lastHandshake ? formatRelativeTime(tunnel.peer.lastHandshake) : '—'}
						</span>
					</div>
				</div>
			{/if}
			{/if}
		</div>
		{/if}

		<!-- Actions -->
		<div class="actions">
			<TunnelListActions
				variant="labeled"
				editHref="/system-tunnels/{tunnel.id}"
				editTitle="Изменить туннель «{displayName}»"
				onTest={openTest}
				testTitle="Тест туннеля «{displayName}»"
			>
				{#snippet extra()}
					{#if onMarkServer}
						<button
							type="button"
							class="tunnel-list-actions__btn tunnel-list-actions__btn--primary"
							title="Перенести туннель «{displayName}» в серверы"
							aria-label="Перенести туннель «{displayName}» в серверы"
							onclick={() => onMarkServer(tunnel.id)}
						>
							<Server size={14} aria-hidden="true" />
							В серверы
						</button>
					{/if}
				{/snippet}
			</TunnelListActions>
		</div>

		<!-- Traffic -->
		{#if !isListCard && tunnel.status === 'up'}
			{#if view === 'cards'}
				<button
					type="button"
					class="traffic-inline"
					onclick={() => ondetail?.(tunnel.id)}
					title="Открыть график трафика"
				>
					<TrafficSparkline
						rxData={rxRates}
						txData={txRates}
						responsive
						height={22}
					/>
					<div class="traffic-inline-rates">
						<span class="traffic-inline-rate rx">↓ {formatBitRate(inlineRxRate)}</span>
						<span class="traffic-inline-rate tx">↑ {formatBitRate(inlineTxRate)}</span>
					</div>
				</button>
			{:else}
				<TrafficChart
					{rxRates}
					{txRates}
					rxTotal={tunnel.peer?.rxBytes ?? 0}
					txTotal={tunnel.peer?.txBytes ?? 0}
					height={chartHeight}
					onclick={() => ondetail?.(tunnel.id)}
				/>
			{/if}
		{/if}
	</div>

<style>
	/* Match TunnelCard border states */
	.status-up {
		border-color: var(--success);
	}

	.status-down {
		border-color: var(--text-muted, #6b7280);
	}













	.card.flex {
		gap: 1rem;
	}

	.card.view-compact {
		gap: 12px;
		padding: 12px 14px;
	}

	.card.view-dense {
		gap: 8px;
		padding: 10px 12px;
	}

	.card.view-dense .details {
		gap: 6px;
		padding: 6px 0;
	}

	.card.view-compact .details {
		gap: 8px;
		padding: 6px 0;
	}

	.tunnel-name-row {
		display: flex;
		align-items: center;
		gap: 5px;
		min-width: 0;
		overflow: hidden;
	}

	.card.view-dense .tunnel-name-row :global(.tunnel-title-row__name) {
		font-size: 13px;
		line-height: var(--sbx-card-title-line-height);
	}

	.meta-tags-dense {
		display: flex;
		flex-wrap: wrap;
		margin-top: 4px;
		align-items: center;
		gap: 3px;
		min-width: 0;
		overflow: hidden;
	}

	.card.view-dense .meta-tags-dense :global(.badge) {
		font-size: 9px;
		padding: 1px 5px;
		line-height: 1.3;
		flex-shrink: 0;
	}

	.iface-chip-dense {
		display: inline-block;
		min-width: 0;
		flex-shrink: 1;
		font-size: 9px;
		font-weight: 500;
		font-family: var(--font-mono, monospace);
		line-height: 1.3;
		padding: 1px 5px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 10px;
	}

	.header.header-dense {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: flex-start;
		gap: 6px;
	}

	.header-dense-body {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}

	.head-left {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}

	.head-right {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 4px;
		flex-shrink: 0;
	}

	.dense-toolbar {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		flex-shrink: 0;
	}

	.dense-toolbar-bottom {
		display: flex;
		align-items: center;
		/* gap: 2px; */
	}

	.meta-line {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.connectivity-row {
		display: flex;
		align-items: center;
		gap: 5px;
	}

	.card.view-dense .dense-toolbar-bottom .connectivity-gear {
		width: 16px;
		height: 16px;
		padding: 0;
	}

	.status-hint-left {
		align-self: flex-start;
		font-size: 11px;
		color: var(--color-warning, var(--warning, #f59e0b));
	}

	.details-dense-cols {
		display: grid;
		grid-template-columns: minmax(0, 1fr) 6.5rem;
		gap: 10px 12px;
		align-items: start;
	}

	.details-dense-col {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}

	.details-dense-col-right {
		width: 100%;
		overflow: hidden;
	}

	.kv-stacked-stat {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}

	.card.view-dense .kv-endpoint {
		display: flex;
		align-items: center;
		gap: 2px;
		min-width: 0;
	}

	.kv-stacked-label {
		font-size: 9px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
		line-height: 1.2;
	}

	.kv-stacked-value {
		font-size: 10px;
		font-family: var(--font-mono, monospace);
		color: var(--text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		line-height: 1.25;
	}

	.traffic-inline {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		width: 100%;
		min-width: 0;
		padding: 5px 6px;
		margin: 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		cursor: pointer;
		font: inherit;
		color: inherit;
		text-align: left;
		transition: background 0.15s ease, border-color 0.15s ease;
	}

	.traffic-inline :global(svg.responsive) {
		flex: 1 1 auto;
		width: 100%;
		min-width: 0;
	}

	.traffic-inline:hover {
		background: var(--color-bg-hover);
		border-color: var(--color-border-hover);
	}

	.traffic-inline:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.traffic-inline-rates {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.08rem;
		padding-block: 3px;
		min-width: 0;
		flex-shrink: 0;
		font-size: 10px;
		line-height: 1.15;
		font-family: var(--font-mono, monospace);
		font-variant-numeric: tabular-nums;
	}

	.traffic-inline-rate.rx {
		color: var(--color-accent);
	}

	.traffic-inline-rate.tx {
		color: var(--color-success);
	}

	.iface-name {
		font-size: 12px;
		font-family: var(--font-mono, monospace);
		color: var(--text-muted);
	}

	/* Badge */
	.version-badge {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 11px;
		font-weight: 500;
		border-radius: 10px;
		background: var(--bg-tertiary);
		color: var(--text-muted);
	}

	.badge-system {
		background: rgba(148, 163, 184, 0.15);
	}

	/* Eye toggle */
	.eye-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 2px;
		border: none;
		background: none;
		color: var(--text-muted);
		cursor: pointer;
		border-radius: 4px;
		flex-shrink: 0;
		transition: color 0.15s;
	}

	.eye-btn:hover {
		color: var(--text-secondary);
	}

	/* Details */
	.details {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 8px 0;
		border-top: 1px solid var(--color-border);
		border-bottom: 1px solid var(--color-border);
	}

	.detail-label {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
	}

	.detail-value {
		font-size: 13px;
		font-family: var(--font-mono, monospace);
		color: var(--text-secondary);
	}

	.divider {
		border: none;
		border-top: 1px dashed var(--color-border);
		margin: 4px 0;
	}

	.stats-row {
		white-space: nowrap;
	}

	/* Connectivity gear */
	.connectivity-gear {
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 2px;
		background: none;
		border: none;
		color: var(--text-muted);
		cursor: pointer;
		border-radius: 4px;
		transition: color 0.15s;
	}

	.connectivity-gear:hover {
		color: var(--accent);
	}

	.connectivity-gear.gear-disabled {
		opacity: 0.4;
	}

</style>
