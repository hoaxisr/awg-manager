<script lang="ts">
	import { onMount } from 'svelte';
	import type { ManagedServer, ManagedPeer, ManagedPeerStats, ManagedServerStats, ASCParams } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import { formatBytes } from '$lib/utils/format';
	import { EarthLock, Plus, RefreshCw, Settings, Trash2 } from 'lucide-svelte';
	import { Toggle, Button, SegmentedControl, ChipMultiSelect, VersionBadge, Stat, StatStrip } from '$lib/components/ui';
	import type { SegmentedOption } from '$lib/components/ui/segmentedControl';
	import {
		EditManagedServerModal,
		AddManagedPeerModal,
		EditManagedPeerModal,
		PeerConfModal,
		PeerSortControls,
		ManagedPeerTable,
		ServerAccessPolicyDropdown,
		ServerSettingsPanel,
	} from '$lib/components/servers';
	import { comparePeerFieldsDirected } from '$lib/utils/peerSort';
	import { peerSort } from '$lib/stores/peerSort';
	import { classifyAwgVersionFromAsc } from '$lib/utils/classifyAwgVersion';
	import { formatSubnetPlaceholder, maskToPrefix, resolveNatMode, type NatMode } from '$lib/utils/network';
	import { countActiveManagedPeers } from '$lib/utils/serverPeerActivity';

	interface Props {
		server: ManagedServer;
		stats: ManagedServerStats | null;
		routerIP?: string;
		onDeleted?: () => void;
		onUpdated?: () => void;
		onOpenASC: () => void;
		ingressEnabled?: boolean;
		onToggleIngress?: (interfaceName: string, enabled: boolean) => Promise<void>;
		lanSegmentOptions?: { value: string; label: string }[];
	}

	let { server, stats, routerIP = '', onDeleted = () => {}, onUpdated = () => {}, onOpenASC, ingressEnabled = false, onToggleIngress = async () => {}, lanSegmentOptions = [] }: Props = $props();

	let serverId = $derived(server.interfaceName);

	let serverDisplayName = $derived(server.description || server.interfaceName);

	let editServerOpen = $state(false);
	let addPeerOpen = $state(false);
	let editPeerOpen = $state(false);
	let confModalOpen = $state(false);
	let selectedPeer = $state<ManagedPeer | null>(null);
	let confPubkey = $state('');
	let confPeerName = $state('');
	let deleting = $state(false);
	let confirmDelete = $state(false);

	let searchQuery = $state('');

	function getPeerStats(publicKey: string): ManagedPeerStats | undefined {
		return stats?.peers?.find(p => p.publicKey === publicKey);
	}

	let sortedPeers = $derived.by(() => {
		let peers = server.peers ?? [];

		if (searchQuery) {
			const q = searchQuery.toLowerCase();
			peers = peers.filter(p =>
				(p.description || '').toLowerCase().includes(q) ||
				p.tunnelIP.toLowerCase().includes(q)
			);
		}

		const sortBy = $peerSort.sortBy;
		if (sortBy === null) return peers;

		const sorted = [...peers].sort((a, b) => {
			const sa = getPeerStats(a.publicKey);
			const sb = getPeerStats(b.publicKey);
			return comparePeerFieldsDirected(
				{
					name: a.description || a.publicKey,
					ip: a.tunnelIP,
					endpoint: sa?.endpoint || '-',
					rxBytes: sa?.rxBytes ?? null,
					txBytes: sa?.txBytes ?? null,
					online: sa?.online ?? null,
					lastHandshake: sa?.lastHandshake ?? null,
				},
				{
					name: b.description || b.publicKey,
					ip: b.tunnelIP,
					endpoint: sb?.endpoint || '-',
					rxBytes: sb?.rxBytes ?? null,
					txBytes: sb?.txBytes ?? null,
					online: sb?.online ?? null,
					lastHandshake: sb?.lastHandshake ?? null,
				},
				sortBy,
				$peerSort.sortAsc,
			);
		});

		return sorted;
	});

	let onlineCount = $derived(countActiveManagedPeers(server.peers, stats?.peers));
	let statusUnknown = $derived(stats === null);
	let isUp = $derived(stats?.status === 'up');
	let totalRx = $derived(stats?.peers?.reduce((sum, p) => sum + p.rxBytes, 0) ?? 0);
	let totalTx = $derived(stats?.peers?.reduce((sum, p) => sum + p.txBytes, 0) ?? 0);
	let togglingPeerKeys = $state(new Set<string>());

	async function handleDeleteServer() {
		if (!confirmDelete) {
			confirmDelete = true;
			setTimeout(() => { confirmDelete = false; }, 3000);
			return;
		}
		deleting = true;
		try {
			const fresh = await api.deleteManagedServer(serverId);
			servers.applyMutationResponse(fresh);
			notifications.success('Сервер удалён');
			onDeleted();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка удаления');
		} finally {
			deleting = false;
			confirmDelete = false;
		}
	}

	async function handleTogglePeer(peer: ManagedPeer) {
		if (togglingPeerKeys.has(peer.publicKey)) return;
		togglingPeerKeys = new Set(togglingPeerKeys).add(peer.publicKey);
		try {
			const fresh = await api.toggleManagedPeer(serverId, peer.publicKey, !peer.enabled);
			servers.applyMutationResponse(fresh);
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		} finally {
			const next = new Set(togglingPeerKeys);
			next.delete(peer.publicKey);
			togglingPeerKeys = next;
		}
	}

	function isPeerToggling(publicKey: string): boolean {
		return togglingPeerKeys.has(publicKey);
	}

	async function doDeletePeer(peer: ManagedPeer) {
		try {
			const fresh = await api.deleteManagedPeer(serverId, peer.publicKey);
			servers.applyMutationResponse(fresh);
			notifications.success('Клиент удалён');
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка удаления');
			throw e;
		}
	}

	function openEditPeer(peer: ManagedPeer) {
		selectedPeer = peer;
		editPeerOpen = true;
	}

	const WAN_IP_MASKED = 'показать';

	let wanIP = $state('');
	let showWanIP = $state(false);
	let lanRouterLabel = $derived(routerIP ? ` (${routerIP})` : '');
	let vpnSubnetLabel = $derived(formatSubnetPlaceholder(server.address, server.mask));

	let togglingEnabled = $state(false);
	let restartingServer = $state(false);

	async function handleToggleEnabled() {
		togglingEnabled = true;
		try {
			const fresh = await api.setManagedServerEnabled(serverId, !isUp);
			servers.applyMutationResponse(fresh);
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переключения');
		} finally {
			togglingEnabled = false;
		}
	}

	async function handleRestartOrStart() {
		if (restartingServer) return;
		restartingServer = true;

		try {
			await api.restartManagedServer(serverId);
			notifications.success(isUp ? 'Команда рестарта отправлена' : 'Команда запуска отправлена');
			servers.invalidate();
		} catch {
			notifications.warning('Команда могла быть отправлена, соединение могло временно прерваться');
		} finally {
			restartingServer = false;
		}
	}

	let togglingNAT = $state(false);
	let togglingIngress = $state(false);

	let natMode = $derived<NatMode>(resolveNatMode(server.natMode, server.natEnabled));

	const natModeOptions: SegmentedOption<'full' | 'internet-only' | 'none'>[] = [
		{ value: 'full', label: 'Полный' },
		{ value: 'internet-only', label: 'Интернет' },
		{ value: 'none', label: 'Без NAT' },
	];

	async function handleToggleIngress() {
		togglingIngress = true;
		try {
			await onToggleIngress(server.interfaceName, !ingressEnabled);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переключения egress в sing-box');
		} finally {
			togglingIngress = false;
		}
	}

	async function handleSetNATMode(mode: 'full' | 'internet-only' | 'none') {
		if (mode === natMode) return;
		togglingNAT = true;
		try {
			const fresh = await api.setManagedServerNATMode(serverId, mode);
			servers.applyMutationResponse(fresh);
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка изменения режима NAT');
		} finally {
			togglingNAT = false;
		}
	}

	let settingLAN = $state(false);
	async function handleSetLANSegments(next: string[]) {
		if (settingLAN) return;
		settingLAN = true;
		try {
			const fresh = await api.setManagedServerLANSegments(serverId, next);
			servers.applyMutationResponse(fresh);
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка изменения доступа в LAN');
		} finally { settingLAN = false; }
	}

	function openConf(peer: ManagedPeer) {
		confPubkey = peer.publicKey;
		confPeerName = peer.description || 'peer';
		confModalOpen = true;
	}

	let policyChanging = $state(false);
	let ascParams = $state<ASCParams | null>(null);
	let ascLoadedFor = $state('');

	$effect(() => {
		const id = server.interfaceName;
		if (ascLoadedFor === id) return;

		if (ascLoadedFor && ascLoadedFor !== id) {
			ascParams = null;
			ascLoadedFor = '';
		}

		let cancelled = false;

		void (async () => {
			try {
				const params = await api.getManagedServerASC(id);
				if (!cancelled) {
					ascParams = params;
					ascLoadedFor = id;
				}
			} catch {
				if (!cancelled) {
					ascParams = null;
					ascLoadedFor = '';
				}
			}
		})();

		return () => {
			cancelled = true;
		};
	});

	let awgVersion = $derived(classifyAwgVersionFromAsc(ascParams));

	onMount(async () => {
		void api.getWANIP().then((ip) => { wanIP = ip; }).catch(() => {});
	});

	async function handlePolicyChange(newPolicy: string) {
		if (newPolicy === server.policy) return;
		policyChanging = true;
		try {
			const fresh = await api.setManagedServerPolicy(serverId, newPolicy);
			servers.applyMutationResponse(fresh);
			notifications.success('Политика обновлена');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка изменения политики');
		} finally {
			policyChanging = false;
		}
	}
</script>

<div class="card server-detail-card managed-card" class:status-up={isUp}>
	<!-- Header -->
	<div class="card-header">
		<div class="header-info">
			<div class="title-row">
				<div class="title-main">
					<Toggle
						checked={isUp}
						onchange={handleToggleEnabled}
						disabled={togglingEnabled || restartingServer || statusUnknown}
						size="sm"
						spinner="none"
					/>
					<h3 class="card-title">{serverDisplayName}</h3>
				</div>
				<div class="title-badges">
					<span class="badge-managed">Управляемый</span>
					{#if ascParams !== null}
						<VersionBadge kind="awg" value={awgVersion} />
					{/if}
				</div>
			</div>
			<div class="server-meta">
				<span class="meta mono">{server.interfaceName}</span>
				<span class="meta mono">{server.address}/{maskToPrefix(server.mask)}</span>
				<span class="meta mono">:{server.listenPort}</span>
				{#if server.mtu}
					<span class="meta mono">MTU {server.mtu}</span>
				{/if}
			</div>
		</div>
		<div class="header-right">
			<div class="header-actions">
				<Button variant="secondary" size="sm" onclick={handleRestartOrStart} disabled={restartingServer || togglingEnabled || deleting} loading={restartingServer} iconBefore={restartIcon} title={statusUnknown ? `Статус сервера «${serverDisplayName}» загружается` : isUp ? `Перезапустить сервер «${serverDisplayName}»` : `Запустить сервер «${serverDisplayName}»`}>
					{statusUnknown ? 'Рестарт' : isUp ? 'Рестарт' : 'Запуск'}
				</Button>
				<Button variant="secondary" size="sm" onclick={onOpenASC} iconBefore={ascIcon} title={`Параметры обфускации сервера «${serverDisplayName}»`}>
					Обфускация
				</Button>
				<Button variant="secondary" size="sm" onclick={() => editServerOpen = true} iconBefore={settingsIcon} title={`Настройки сервера «${serverDisplayName}»`}>
					Настройки
				</Button>
				{#if confirmDelete}
					<Button variant="danger" size="sm" onclick={handleDeleteServer} loading={deleting} title={`Подтвердить удаление сервера «${serverDisplayName}»`}>
						Подтвердить?
					</Button>
				{:else}
					<Button variant="outline-danger" size="sm" onclick={handleDeleteServer} disabled={deleting} iconBefore={deleteIcon} title={`Удалить сервер «${serverDisplayName}»`}>
						Удалить
					</Button>
				{/if}
			</div>
		</div>
	</div>

	<StatStrip>
		<Stat value={stats ? formatBytes(totalRx) : '—'} label="RX" />
		<Stat value={stats ? formatBytes(totalTx) : '—'} label="TX" />
		<Stat value={`${onlineCount} / ${(server.peers ?? []).length}`} label="Клиенты" sub={onlineCount > 0 ? `${onlineCount} онлайн` : 'нет активных'} />
		<Stat value={`UDP :${server.listenPort}`} label="Listen" />
	</StatStrip>

	<!-- Settings -->
	<ServerSettingsPanel persistKey="awgm:servers:settingsCollapsed">
		<div class="setting-row">
			<div class="setting-copy">
				<span class="setting-title">NAT</span>
				{#if natMode === 'full'}
					<span class="setting-description">
						Клиент выходит в интернет с внешним IP {@render wanIpButton()}. В LAN виден не как отдельное устройство, а как роутер{lanRouterLabel}.
					</span>
				{:else if natMode === 'internet-only'}
					<span class="setting-description">
						Клиент выходит в интернет с внешним IP {@render wanIpButton()}, в LAN виден со своим VPN-адресом ({vpnSubnetLabel}).
					</span>
				{:else}
					<span class="setting-description">Выхода в интернет для клиента нет (без дополнительной подмены адреса), в LAN виден со своим VPN-адресом ({vpnSubnetLabel}).</span>
				{/if}
				{#if ingressEnabled && natMode === 'full'}
					<span class="setting-description setting-description-warning">NAT для интернета не действует — интернет-трафик идёт через sing-box (туннель); режим NAT влияет только на видимость в LAN</span>
				{/if}
			</div>
			<div class="setting-control">
				<SegmentedControl
					value={natMode}
					options={natModeOptions}
					ariaLabel="Режим NAT"
					disabled={togglingNAT}
					onchange={handleSetNATMode}
				/>
			</div>
		</div>

		<div class="setting-row">
			<div class="setting-copy">
				<span class="setting-title">Доступ в LAN</span>
				<span class="setting-description">Сегменты LAN, доступные клиентам этого сервера.</span>
			</div>
			<div class="setting-control">
				<ChipMultiSelect values={server.lanSegments ?? []} options={lanSegmentOptions} onchange={handleSetLANSegments} disabled={settingLAN} />
			</div>
		</div>

		<div class="setting-row setting-row-toggle">
			<div class="setting-copy">
				<span class="setting-title">Маршрутизация через sing-box</span>
				<span class="setting-description">Заворачивать интернет-трафик клиентов данного сервера в sing-box.</span>
			</div>
			<div class="setting-control setting-control-toggle">
				<Toggle checked={ingressEnabled} onchange={handleToggleIngress} disabled={togglingIngress} spinner="before" />
			</div>
		</div>

		<ServerAccessPolicyDropdown
			policy={server.policy}
			disabled={policyChanging}
			onchange={handlePolicyChange}
		/>
	</ServerSettingsPanel>

	<!-- Peers -->
	<div class="peers-section">
		<div class="peers-header">
			<span class="peers-title">Клиенты {#if stats}({onlineCount}/{(server.peers ?? []).length} онлайн){:else}({(server.peers ?? []).length}){/if}</span>
			<div class="peers-controls">
				<PeerSortControls
					bind:searchQuery
					showSearch={(server.peers ?? []).length > 0}
					hideSortOnDesktop
				/>
				<Button variant="secondary" size="sm" onclick={() => addPeerOpen = true} iconBefore={addPeerIcon}>
					Добавить клиента
				</Button>
			</div>
		</div>

		{#if (server.peers ?? []).length === 0}
			<div class="empty-peers">Нет клиентов. Добавьте первого.</div>
		{:else}
			<ManagedPeerTable
				peers={sortedPeers}
				{getPeerStats}
				onTogglePeer={handleTogglePeer}
				{isPeerToggling}
				onOpenConf={openConf}
				onOpenEditPeer={openEditPeer}
				onDeletePeer={doDeletePeer}
			/>
		{/if}
	</div>
</div>

{#snippet wanIpButton()}
	<button
		type="button"
		class="wan-ip-reveal mono"
		onclick={() => (showWanIP = !showWanIP)}
		title={showWanIP && wanIP ? wanIP : 'Показать внешний IP'}
		aria-label={showWanIP ? 'Скрыть внешний IP' : 'Показать внешний IP'}
		aria-pressed={showWanIP}
	>
		({showWanIP && wanIP ? wanIP : WAN_IP_MASKED})
	</button>
{/snippet}

<!-- Modals -->
<EditManagedServerModal
	bind:open={editServerOpen}
	{serverId}
	{server}
	onclose={() => editServerOpen = false}
	onUpdated={onUpdated}
/>

<AddManagedPeerModal
	bind:open={addPeerOpen}
	{serverId}
	{server}
	{routerIP}
	onclose={() => addPeerOpen = false}
	onAdded={onUpdated}
/>

{#if selectedPeer}
	<EditManagedPeerModal
		bind:open={editPeerOpen}
		{serverId}
		peer={selectedPeer}
		{routerIP}
		onclose={() => { editPeerOpen = false; selectedPeer = null; }}
		onUpdated={onUpdated}
	/>
{/if}

<PeerConfModal
	bind:open={confModalOpen}
	{serverId}
	pubkey={confPubkey}
	peerName={confPeerName}
	onclose={() => confModalOpen = false}
/>

{#snippet restartIcon()}
	<RefreshCw size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#snippet ascIcon()}
	<EarthLock size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#snippet settingsIcon()}
	<Settings size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#snippet deleteIcon()}
	<Trash2 size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#snippet addPeerIcon()}
	<Plus size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}


<style>
	.title-badges {
		flex: 1 1 auto;
		min-width: fit-content;
	}

	.badge-managed {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 11px;
		font-weight: 500;
		border-radius: 10px;
		background: rgba(59, 130, 246, 0.15);
		color: var(--accent);
	}

	.wan-ip-reveal {
		display: inline;
		padding: 0;
		margin: 0;
		border: none;
		background: none;
		font: inherit;
		font-size: inherit;
		line-height: inherit;
		color: var(--text-secondary);
		cursor: pointer;
		text-decoration: none;
		vertical-align: baseline;
		-webkit-tap-highlight-color: transparent;
	}

	.wan-ip-reveal:hover {
		color: var(--text-primary);
	}

	.wan-ip-reveal:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
		border-radius: 2px;
	}


	:global(.settings-panel-body .picker .chips) {
		background: var(--color-settings-surface-bg);
		border-color: var(--color-border);
		border-radius: var(--radius-sm);
	}
</style>
