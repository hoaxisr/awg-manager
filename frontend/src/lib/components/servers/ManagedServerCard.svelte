<script lang="ts">
	import { m } from '$lib/i18n';
	import { onMount } from 'svelte';
	import type { ManagedServer, ManagedPeer, ManagedPeerStats, ManagedServerStats, ASCParams } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import { formatBytes } from '$lib/utils/format';
	import { EarthLock, Plus, RefreshCw, Settings, Trash2 } from 'lucide-svelte';
	import { Toggle, Button, SegmentedControl, ChipMultiSelect, VersionBadge, Stat, StatStrip } from '$lib/components/ui';
	import { showSummary } from '$lib/stores/showSummary';
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
			notifications.success(m.servers_managed_deleted());
			onDeleted();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_card_delete_failed());
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
			notifications.error(e instanceof Error ? e.message : m.common_error());
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
			notifications.success(m.servers_card_peer_deleted());
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_card_delete_failed());
			throw e;
		}
	}

	function openEditPeer(peer: ManagedPeer) {
		selectedPeer = peer;
		editPeerOpen = true;
	}

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
			notifications.error(e instanceof Error ? e.message : m.servers_card_toggle_failed());
		} finally {
			togglingEnabled = false;
		}
	}

	async function handleRestartOrStart() {
		if (restartingServer) return;
		restartingServer = true;

		try {
			await api.restartManagedServer(serverId);
			notifications.success(isUp ? m.servers_card_restart_sent() : m.servers_card_start_sent());
			servers.invalidate();
		} catch {
			notifications.warning(m.servers_card_cmd_maybe_sent());
		} finally {
			restartingServer = false;
		}
	}

	let togglingNAT = $state(false);
	let togglingIngress = $state(false);

	let natMode = $derived<NatMode>(resolveNatMode(server.natMode, server.natEnabled));

	const natModeOptions: SegmentedOption<'full' | 'internet-only' | 'none'>[] = $derived([
		{ value: 'full', label: m.servers_managed_nat_full() },
		{ value: 'internet-only', label: m.servers_managed_nat_internet() },
		{ value: 'none', label: m.servers_managed_nat_none() },
	]);

	async function handleToggleIngress() {
		togglingIngress = true;
		try {
			await onToggleIngress(server.interfaceName, !ingressEnabled);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_card_ingress_failed());
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
			notifications.error(e instanceof Error ? e.message : m.servers_managed_nat_mode_failed());
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
			notifications.error(e instanceof Error ? e.message : m.servers_managed_lan_failed());
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
			notifications.success(m.servers_card_policy_updated());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_card_policy_failed());
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
					<span class="badge-managed">{m.servers_managed_badge()}</span>
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
				<Button variant="secondary" size="sm" onclick={handleRestartOrStart} disabled={restartingServer || togglingEnabled || deleting} loading={restartingServer} iconBefore={restartIcon} title={statusUnknown ? m.servers_managed_status_loading_title({ name: serverDisplayName }) : isUp ? m.servers_managed_restart_title({ name: serverDisplayName }) : m.servers_managed_start_title({ name: serverDisplayName })}>
					{statusUnknown ? m.servers_card_restart() : isUp ? m.servers_card_restart() : m.servers_card_start()}
				</Button>
				<Button variant="secondary" size="sm" onclick={onOpenASC} iconBefore={ascIcon} title={m.servers_managed_asc_title({ name: serverDisplayName })}>
					{m.tunnel_detail_tab_obfuscation()}
				</Button>
				<Button variant="secondary" size="sm" onclick={() => editServerOpen = true} iconBefore={settingsIcon} title={m.servers_managed_settings_title({ name: serverDisplayName })}>
					{m.nav_settings()}
				</Button>
				{#if confirmDelete}
					<Button variant="danger" size="sm" onclick={handleDeleteServer} loading={deleting} title={m.servers_managed_confirm_delete_title({ name: serverDisplayName })}>
						{m.servers_managed_confirm()}
					</Button>
				{:else}
					<Button variant="outline-danger" size="sm" onclick={handleDeleteServer} disabled={deleting} iconBefore={deleteIcon} title={m.servers_managed_delete_title({ name: serverDisplayName })}>
						{m.common_delete()}
					</Button>
				{/if}
			</div>
		</div>
	</div>

	{#if $showSummary}
	<StatStrip>
		<Stat value={stats ? formatBytes(totalRx) : '—'} label="RX" />
		<Stat value={stats ? formatBytes(totalTx) : '—'} label="TX" />
		<Stat value={`${onlineCount} / ${(server.peers ?? []).length}`} label={m.servers_stat_clients()} sub={onlineCount > 0 ? m.servers_stat_online({ count: onlineCount }) : m.servers_stat_none_active()} />
		<Stat value={`UDP :${server.listenPort}`} label="Listen" />
	</StatStrip>
	{/if}

	<!-- Settings -->
	<ServerSettingsPanel persistKey="awgm:servers:settingsCollapsed">
		<div class="setting-row">
			<div class="setting-copy">
				<span class="setting-title">NAT</span>
				{#if natMode === 'full'}
					<span class="setting-description">
						{m.servers_managed_nat_full_prefix()} {@render wanIpButton()}{m.servers_managed_nat_full_suffix({ router: lanRouterLabel })}
					</span>
				{:else if natMode === 'internet-only'}
					<span class="setting-description">
						{m.servers_managed_nat_full_prefix()} {@render wanIpButton()}{m.servers_managed_nat_inet_suffix({ subnet: vpnSubnetLabel })}
					</span>
				{:else}
					<span class="setting-description">{m.servers_managed_nat_none_desc({ subnet: vpnSubnetLabel })}</span>
				{/if}
				{#if ingressEnabled && natMode === 'full'}
					<span class="setting-description setting-description-warning">{m.servers_managed_nat_ingress_warning()}</span>
				{/if}
			</div>
			<div class="setting-control">
				<SegmentedControl
					value={natMode}
					options={natModeOptions}
					ariaLabel={m.servers_managed_nat_mode_aria()}
					disabled={togglingNAT}
					fullWidth
					onchange={handleSetNATMode}
				/>
			</div>
		</div>

		<div class="setting-row">
			<div class="setting-copy">
				<span class="setting-title">{m.servers_managed_lan_title()}</span>
				<span class="setting-description">{m.servers_managed_lan_desc()}</span>
				{#if server.foreignAcls?.length}
					<span class="setting-description setting-description-warning">
						{m.servers_managed_foreign_acls({ acls: server.foreignAcls.join(', ') })}
					</span>
				{/if}
			</div>
			<div class="setting-control">
				<ChipMultiSelect values={server.lanSegments ?? []} options={lanSegmentOptions} onchange={handleSetLANSegments} disabled={settingLAN} />
			</div>
		</div>

		<div class="setting-row setting-row-toggle">
			<div class="setting-copy">
				<span class="setting-title">{m.servers_card_ingress_title()}</span>
				<span class="setting-description">
					{m.servers_card_ingress_desc()}
				</span>
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
			<span class="peers-title">{#if stats}{m.servers_card_peers_title_online({ online: onlineCount, total: (server.peers ?? []).length })}{:else}{m.servers_card_peers_title_total({ total: (server.peers ?? []).length })}{/if}</span>
			<div class="peers-controls">
				<PeerSortControls
					bind:searchQuery
					showSearch={(server.peers ?? []).length > 0}
					hideSortOnDesktop
				/>
				<Button variant="secondary" size="sm" onclick={() => addPeerOpen = true} iconBefore={addPeerIcon}>
					{m.servers_peer_add_title()}
				</Button>
			</div>
		</div>

		{#if (server.peers ?? []).length === 0}
			<div class="empty-peers">{m.servers_card_no_peers()}</div>
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
		title={showWanIP && wanIP ? wanIP : m.servers_managed_wan_show_title()}
		aria-label={showWanIP ? m.servers_managed_wan_hide_title() : m.servers_managed_wan_show_title()}
		aria-pressed={showWanIP}
	>
		({showWanIP && wanIP ? wanIP : m.servers_managed_wan_show()})
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
