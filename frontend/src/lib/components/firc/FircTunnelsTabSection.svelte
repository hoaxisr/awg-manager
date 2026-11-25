<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { m } from '$lib/i18n';
	import {
		stripFircEmojis,
		type FircTunnel,
		type FircTunnelState,
	} from '$lib/types/firc';
	import { StatStrip, Stat, LayoutViewToggle, Button, Badge } from '$lib/components/ui';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';
	import { TunnelToolbarViewRow } from '$lib/components/tunnels';
	import { SingboxTunnelCard } from '$lib/components/singbox';
	import FircResourceCard from '$lib/components/firc/FircResourceCard.svelte';
	import FircTunnelEditModal from '$lib/components/firc/FircTunnelEditModal.svelte';
	import FircTunnelsRuntimeStrip from './FircTunnelsRuntimeStrip.svelte';
	import FircTunnelsTable from './FircTunnelsTable.svelte';
	import { singboxTunnels, singboxStatus } from '$lib/stores/singbox';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { showSummary } from '$lib/stores/showSummary';
	import {
		SINGBOX_LAYOUT_STORAGE_KEY,
		parseSingboxLayoutMode,
		readTunnelMobileLayout,
		subscribeTunnelMobileLayout,
		type SingboxLayoutMode,
		type TunnelRenderMode,
	} from '$lib/constants/singboxLayout';
	import FircForeignNotice from './FircForeignNotice.svelte';
	import {
		fircForeign,
		fircStatus,
		fircTunnels,
		fircTunnelsState,
		fircProbeResults,
		fircInterfaceProbeResults,
		reloadFircAll,
		createAndOpenFircTunnel,
		acquireFircTunnelsStatePoll,
	} from '$lib/stores/firc';
	import {
		fircMigrationAppliedNonce,
		openFircMigrationModal,
	} from '$lib/stores/fircMigrationModal';
	import { formatBytes } from '$lib/utils/format';

	const FIRC_LAYOUT_STORAGE_KEY = 'firc_tunnels_layout_mode';

	interface Props {
		dashboardMode?: boolean;
		showTrustTunnels?: boolean;
		onOpenSingboxDetail?: (tag: string) => void;
		embedEditModal?: boolean;
		effectiveLayoutOverride?: SingboxLayoutMode;
		effectiveRenderModeOverride?: TunnelRenderMode;
		searchQueryOverride?: string;
	}

	let {
		dashboardMode = false,
		showTrustTunnels = true,
		onOpenSingboxDetail,
		embedEditModal = true,
		effectiveLayoutOverride,
		effectiveRenderModeOverride,
		searchQueryOverride,
	}: Props = $props();

	let loading = $state($fircTunnels.length === 0);
	let searchQuery = $state('');
	let layoutMode = $state<SingboxLayoutMode>('compact');
	let layoutReady = $state(false);
	let mobileLayout = $state(readTunnelMobileLayout());
	let trafficTotals = $state<Record<string, { down: number; up: number }>>({});

	let releaseStatePoll: (() => void) | undefined;
	let unsubTunnelsState: (() => void) | undefined;
	let lastTrafficAt = $state(0);
	let unsubSbTunnels: (() => void) | undefined;
	let unsubSbStatus: (() => void) | undefined;
	let unsubMobile: (() => void) | undefined;

	const tunnels = $derived($fircTunnels);
	const sbTunnelsList = $derived($singboxTunnels.data ?? []);
	const sbRouterSettings = singboxRouter.settings;

	const liveStateMap = $derived.by(() => {
		const map: Record<string, FircTunnelState> = {};
		for (const s of $fircTunnelsState) {
			map[s.id] = s;
		}
		return map;
	});

	const effectiveSearchQuery = $derived(
		searchQueryOverride !== undefined ? searchQueryOverride : searchQuery,
	);

	const effectiveLayout = $derived<SingboxLayoutMode>(
		effectiveLayoutOverride ?? layoutMode,
	);

	const effectiveRenderMode = $derived<TunnelRenderMode>(
		effectiveRenderModeOverride ??
			(effectiveLayout === 'list'
				? mobileLayout
					? 'list-card'
					: 'table'
				: effectiveLayout === 'dense'
					? 'dense'
					: 'compact'),
	);

	const cardLayout = $derived<SingboxLayoutMode>(
		effectiveRenderMode === 'list-card' ? 'list' : effectiveLayout,
	);

	function isSubscriptionTunnel(tun: FircTunnel): boolean {
		return (tun.sources ?? []).some((s) => s.kind === 'subscription');
	}

	function tunnelTitle(tun: FircTunnel): string {
		return stripFircEmojis(tun.description || tun.id) || tun.device || tun.id;
	}

	function handleLayoutChange(next: SingboxLayoutMode): void {
		layoutMode = next;
		if (layoutReady && typeof localStorage !== 'undefined') {
			localStorage.setItem(FIRC_LAYOUT_STORAGE_KEY, next);
		}
	}

	async function loadInitial(silent = false): Promise<void> {
		if (!silent && $fircTunnels.length === 0) {
			loading = true;
		}
		try {
			await reloadFircAll();
		} catch {
			// errors handled inside reloadFircAll / state polling
		} finally {
			if (!silent) loading = false;
		}
	}

	// Накопительного счётчика у fircd нет — только скорость. Интегрируем её по
	// фактическому интервалу между опросами; разрыв больше 10 с (вкладка в фоне,
	// таймер притормозил) пропускаем, а не выдумываем байты за него. Итог —
	// оценка за время просмотра, так и подписана.
	function accumulatePollTraffic(states: FircTunnelState[]): void {
		const now = Date.now();
		const dt = lastTrafficAt > 0 ? (now - lastTrafficAt) / 1000 : 0;
		lastTrafficAt = now;
		if (!states || states.length === 0 || dt <= 0 || dt > 10) return;
		const next = { ...trafficTotals };
		let changed = false;
		for (const st of states) {
			const rx = st.rxBps ?? 0;
			const tx = st.txBps ?? 0;
			if (rx > 0 || tx > 0) {
				const prev = next[st.id] ?? { down: 0, up: 0 };
				next[st.id] = {
					down: prev.down + Math.round(rx * dt),
					up: prev.up + Math.round(tx * dt),
				};
				changed = true;
			}
		}
		if (changed) {
			trafficTotals = next;
		}
	}

	onMount(() => {
		const stored =
			localStorage.getItem(FIRC_LAYOUT_STORAGE_KEY) ??
			localStorage.getItem(SINGBOX_LAYOUT_STORAGE_KEY);
		const parsed = parseSingboxLayoutMode(stored);
		if (parsed) layoutMode = parsed;
		layoutReady = true;

		unsubMobile = subscribeTunnelMobileLayout((mobile) => {
			mobileLayout = mobile;
		});
		unsubSbStatus = singboxStatus.subscribe(() => {});
		unsubSbTunnels = singboxTunnels.subscribe(() => {});

		void loadInitial();

		// Опрос состояния — общий с главной (acquireFircTunnelsStatePoll);
		// трафик копим по каждому пришедшему снимку.
		releaseStatePoll = acquireFircTunnelsStatePoll();
		unsubTunnelsState = fircTunnelsState.subscribe((stList) => accumulatePollTraffic(stList));
	});

	onDestroy(() => {
		releaseStatePoll?.();
		unsubTunnelsState?.();
		unsubSbStatus?.();
		unsubSbTunnels?.();
		unsubMobile?.();
	});

	let lastObservedMigrationNonce = 0;
	$effect(() => {
		const nonce = $fircMigrationAppliedNonce;
		if (nonce > lastObservedMigrationNonce) {
			lastObservedMigrationNonce = nonce;
			void loadInitial(true);
		}
	});

	const filteredTunnels = $derived.by(() => {
		const q = effectiveSearchQuery.trim().toLowerCase();
		if (!q) return tunnels;
		return tunnels.filter((t) => {
			const live = liveStateMap[t.id];
			const haystack = [
				t.device,
				t.description ?? '',
				t.id,
				...(t.order ?? []),
				...(t.sources ?? []).map((s) => `${s.name ?? ''} ${s.url ?? ''} ${s.link ?? ''}`),
				...(live?.nodes ?? []).map((n) => `${n.name} ${n.key}`),
			];
			return haystack.some((v) => v.toLowerCase().includes(q));
		});
	});

	const filteredTrustTunnels = $derived.by(() => {
		if (!showTrustTunnels) return [];
		const q = effectiveSearchQuery.trim().toLowerCase();
		if (!q) return sbTunnelsList;
		return sbTunnelsList.filter(
			(t) =>
				t.tag.toLowerCase().includes(q) ||
				t.protocol.toLowerCase().includes(q) ||
				(t.server ?? '').toLowerCase().includes(q),
		);
	});

	const activeRoutingEngine = $derived(
		($fircStatus !== null ? $fircStatus.selected : $sbRouterSettings?.routingEngine === 'firc')
			? 'FIRC'
			: 'Sing-box',
	);
	const daemonRunning = $derived(Boolean($fircStatus?.running));
	const daemonDisabled = $derived(Boolean($fircStatus?.recoveryRequired));

	const summaryStats = $derived.by(() => {
		const total = tunnels.length;
		const runningCount = tunnels.filter(
			(t) => t.enable && liveStateMap[t.id]?.status === 'up',
		).length;

		let totalDown = 0;
		let totalUp = 0;
		let leaderBytes = 0;
		let leaderName = '—';
		const delaySamples: number[] = [];

		for (const t of tunnels) {
			const acc = trafficTotals[t.id];
			const st = liveStateMap[t.id];
			const down = acc?.down ?? 0;
			const up = acc?.up ?? 0;
			totalDown += down;
			totalUp += up;

			const totalTunBytes = down + up;
			const instantRate = (st?.rxBps ?? 0) + (st?.txBps ?? 0);
			const rankMetric = totalTunBytes > 0 ? totalTunBytes : instantRate;
			if (rankMetric > leaderBytes && totalTunBytes > 0) {
				leaderBytes = totalTunBytes;
				leaderName = tunnelTitle(t);
			}

			if (t.enable && st?.status === 'up') {
				const ifaceProbe = t.device ? $fircInterfaceProbeResults[t.device] : undefined;
				if (ifaceProbe?.ok && ifaceProbe.latencyMs > 0) {
					delaySamples.push(Math.round(ifaceProbe.latencyMs));
					continue;
				}
				const probeMap = $fircProbeResults[t.id];
				if (probeMap) {
					let bestMs: number | null = null;
					for (const row of Object.values(probeMap)) {
						if (!row.ok) continue;
						const ms = row.firstByteMs ?? row.handshakeMs ?? 0;
						if (ms > 0 && (bestMs === null || ms < bestMs)) {
							bestMs = ms;
						}
					}
					if (bestMs !== null) {
						delaySamples.push(bestMs);
					}
				}
			}
		}

		const avgDelayMs =
			delaySamples.length > 0
				? Math.round(delaySamples.reduce((sum, v) => sum + v, 0) / delaySamples.length)
				: null;

		return [
			{
				value: `${runningCount}/${total}`,
				label: m.tunnels_unit_tunnels({ count: runningCount }),
				sub: m.singbox_tabs_running_sub({
					active: runningCount,
					stopped: Math.max(0, total - runningCount),
				}),
			},
			{
				value: formatBytes(totalDown + totalUp),
				label: m.firc_tabs_traffic_while_viewing(),
				sub: `↓ ${formatBytes(totalDown)} · ↑ ${formatBytes(totalUp)}`,
			},
			{
				value: avgDelayMs !== null ? `${avgDelayMs} ms` : '—',
				label: m.singbox_tabs_avg_delay(),
				sub: m.singbox_tabs_avg_delay_sub(),
			},
			{
				value: leaderBytes > 0 ? formatBytes(leaderBytes) : '—',
				label: m.tunnels_awg_stat_leader(),
				sub: leaderName,
			},
		];
	});
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

{#if $fircForeign}
<FircForeignNotice foreign={$fircForeign} selected={$fircStatus?.selected} />
{:else}
<div class="firc-tunnels-wrap">
	{#if !dashboardMode}
		<FircTunnelsRuntimeStrip
			{tunnels}
			{liveStateMap}
			{activeRoutingEngine}
			{daemonRunning}
			{daemonDisabled}
		/>

		<div class="tunnels-toolbar">
			<span class="tunnel-count">
				{tunnels.length}
				{m.tunnels_unit_tunnels({ count: tunnels.length })}
				{#if showTrustTunnels && sbTunnelsList.length > 0}
					· {sbTunnelsList.length} TrustTunnel
				{/if}
			</span>
			<div class="toolbar-actions">
				<TunnelToolbarViewRow
					sourceRowCount={tunnels.length}
					showViewToggle={tunnels.length > 0}
					{searchQuery}
					onSearchChange={(value) => (searchQuery = value)}
				>
					{#snippet viewToggle()}
						<LayoutViewToggle
							value={layoutMode}
							showListOption={!mobileLayout}
							ariaLabel={m.firc_view_mode_aria()}
							onchange={handleLayoutChange}
						/>
					{/snippet}
				</TunnelToolbarViewRow>
				<Button
					variant="secondary"
					size="md"
					onclick={() => openFircMigrationModal('forward')}
				>
					{m.firc_migration_btn()}
				</Button>
				<Button
					variant="primary"
					size="md"
					onclick={() => void createAndOpenFircTunnel('link')}
					iconBefore={createIcon}
				>
					{m.firc_add_tunnel_btn()}
				</Button>
			</div>
		</div>

		{#if $showSummary && tunnels.length > 0}
			<div class="awg-summary-row">
				<StatStrip>
					{#each summaryStats as item (item.label)}
						<Stat value={item.value} label={item.label} sub={item.sub} />
					{/each}
				</StatStrip>
			</div>
		{/if}
	{/if}

	{#if loading}
		<div class="empty-card">{m.firc_loading()}</div>
	{:else if tunnels.length === 0}
		<div class="empty-card">
			<div class="empty-title">{m.firc_empty_title()}</div>
			<div class="empty-sub">{m.firc_empty_desc()}</div>
			<div class="toolbar-actions">
				<Button
					variant="secondary"
					size="md"
					onclick={() => openFircMigrationModal('forward')}
				>
					{m.firc_migration_btn()}
				</Button>
				<Button
					variant="primary"
					size="md"
					onclick={() => void createAndOpenFircTunnel('link')}
					iconBefore={createIcon}
				>
					{m.firc_create_first_btn()}
				</Button>
			</div>
		</div>
	{:else if effectiveRenderMode === 'table'}
		<FircTunnelsTable
			tunnels={filteredTunnels}
			searchActive={effectiveSearchQuery.trim() !== ''}
			{isSubscriptionTunnel}
		/>
	{:else if filteredTunnels.length === 0 && effectiveSearchQuery.trim() !== ''}
		<div class="empty-card">
			<div class="empty-sub">{m.firc_search_empty()}</div>
			<Button variant="secondary" size="sm" onclick={() => (searchQuery = '')}>
				{m.firc_clear_search()}
			</Button>
		</div>
	{:else}
		<div
			class="tunnel-grid"
			class:tunnel-grid--list={effectiveRenderMode === 'list-card'}
			class:tunnel-grid--dense={effectiveRenderMode !== 'list-card' && effectiveLayout === 'dense'}
			class:tunnel-grid--compact={effectiveRenderMode !== 'list-card' && effectiveLayout === 'compact'}
		>
			{#each filteredTunnels as tun, i (tun.id)}
				<FircResourceCard
					kind={isSubscriptionTunnel(tun) ? 'subscription' : 'proxy'}
					tunnel={tun}
					layout={cardLayout}
					renderMode={effectiveRenderMode}
					autoDelayCheckNonce={1}
					autoDelayCheckDelayMs={i * 180}
				/>
			{/each}
		</div>
	{/if}

	{#if showTrustTunnels && filteredTrustTunnels.length > 0}
		<div class="trust-tunnels-section">
			<div class="section-divider-head">
				<span class="section-divider-title">{m.firc_trusttunnel_section_title()}</span>
				<Badge variant="default" size="xs" mono>{filteredTrustTunnels.length}</Badge>
			</div>
			<div class="tunnel-grid tunnel-grid--compact">
				{#each filteredTrustTunnels as stTunnel, i (stTunnel.tag)}
					<SingboxTunnelCard
						tunnel={stTunnel}
						layout="compact"
						renderMode="compact"
						autoDelayCheckNonce={1}
						autoDelayCheckDelayMs={i * 180}
						ondetail={(tag) => onOpenSingboxDetail?.(tag)}
					/>
				{/each}
			</div>
		</div>
	{/if}
</div>
{/if}

{#if embedEditModal}
	<FircTunnelEditModal />
{/if}

<style>
	.firc-tunnels-wrap {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.tunnels-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		flex-wrap: wrap;
	}

	.tunnel-count {
		font-size: 0.875rem;
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

	.empty-card {
		padding: 28px;
		text-align: center;
		border: 1px solid var(--color-border);
		border-radius: 10px;
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-size: 13px;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 10px;
	}

	.empty-title {
		font-size: 14px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.empty-sub {
		max-width: 520px;
		line-height: 1.45;
	}

	.trust-tunnels-section {
		display: flex;
		flex-direction: column;
		gap: 10px;
		margin-top: 8px;
	}

	.section-divider-head {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.section-divider-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-secondary);
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
			min-width: 0;
			max-width: 100%;
		}
	}

	@media (max-width: 560px) {
		.toolbar-actions {
			grid-template-columns: 1fr;
		}
	}
</style>
