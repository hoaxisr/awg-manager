<script lang="ts">
	import { onMount } from 'svelte';
	import type { ManagedServer, ManagedPeer, ManagedPeerStats, ManagedServerStats } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import { formatBytes, formatRelativeTime } from '$lib/utils/format';
	import { Toggle } from '$lib/components/ui';
	import {
		EditManagedServerModal,
		AddManagedPeerModal,
		EditManagedPeerModal,
		PeerConfModal,
		PeerSortControls
	} from '$lib/components/servers';
	import { comparePeerFields, type PeerSortKey } from '$lib/utils/peerSort';

	interface Props {
		server: ManagedServer;
		stats: ManagedServerStats | null;
		routerIP?: string;
		onDeleted?: () => void;
		onUpdated?: () => void;
		onOpenASC: () => void;
	}

	let { server, stats, routerIP = '', onDeleted = () => {}, onUpdated = () => {}, onOpenASC }: Props = $props();

	let editServerOpen = $state(false);
	let addPeerOpen = $state(false);
	let editPeerOpen = $state(false);
	let confModalOpen = $state(false);
	let selectedPeer = $state<ManagedPeer | null>(null);
	let confPubkey = $state('');
	let confPeerName = $state('');
	let deleting = $state(false);
	let confirmDelete = $state(false);
	let confirmDeletePeerKey = $state<string | null>(null);

	let sortBy = $state<PeerSortKey>('name');
	let sortAsc = $state(true);
	let searchQuery = $state('');

	function getPeerStats(publicKey: string): ManagedPeerStats | undefined {
		return stats?.peers?.find(p => p.publicKey === publicKey);
	}

	let sortedPeers = $derived.by(() => {
		let peers = server.peers ?? [];

		// Filter (only when search is rendered: 5+ peers)
		if (searchQuery && peers.length >= 5) {
			const q = searchQuery.toLowerCase();
			peers = peers.filter(p =>
				(p.description || '').toLowerCase().includes(q) ||
				p.tunnelIP.toLowerCase().includes(q)
			);
		}

		const sorted = [...peers].sort((a, b) => {
			const sa = getPeerStats(a.publicKey);
			const sb = getPeerStats(b.publicKey);
			const cmp = comparePeerFields(
				{
					name: a.description || a.publicKey,
					ip: a.tunnelIP,
					rxBytes: sa?.rxBytes ?? null,
					txBytes: sa?.txBytes ?? null,
					online: sa?.online ?? null,
					lastHandshake: sa?.lastHandshake ?? null,
				},
				{
					name: b.description || b.publicKey,
					ip: b.tunnelIP,
					rxBytes: sb?.rxBytes ?? null,
					txBytes: sb?.txBytes ?? null,
					online: sb?.online ?? null,
					lastHandshake: sb?.lastHandshake ?? null,
				},
				sortBy,
			);
			return sortAsc ? cmp : -cmp;
		});

		return sorted;
	});

	let onlineCount = $derived(stats?.peers?.filter(p => p.online).length ?? 0);
	let isUp = $derived(stats?.status === 'up');
	let totalRx = $derived(stats?.peers?.reduce((sum, p) => sum + p.rxBytes, 0) ?? 0);
	let totalTx = $derived(stats?.peers?.reduce((sum, p) => sum + p.txBytes, 0) ?? 0);

	async function handleDeleteServer() {
		if (!confirmDelete) {
			confirmDelete = true;
			setTimeout(() => { confirmDelete = false; }, 3000);
			return;
		}
		deleting = true;
		try {
			const fresh = await api.deleteManagedServer();
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
		try {
			const fresh = await api.toggleManagedPeer(peer.publicKey, !peer.enabled);
			servers.applyMutationResponse(fresh);
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		}
	}

	function handleDeletePeerClick(peer: ManagedPeer) {
		if (confirmDeletePeerKey === peer.publicKey) {
			doDeletePeer(peer);
		} else {
			confirmDeletePeerKey = peer.publicKey;
			setTimeout(() => {
				if (confirmDeletePeerKey === peer.publicKey) {
					confirmDeletePeerKey = null;
				}
			}, 3000);
		}
	}

	async function doDeletePeer(peer: ManagedPeer) {
		try {
			confirmDeletePeerKey = null;
			const fresh = await api.deleteManagedPeer(peer.publicKey);
			servers.applyMutationResponse(fresh);
			notifications.success('Клиент удалён');
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка удаления');
		}
	}

	function openEditPeer(peer: ManagedPeer) {
		selectedPeer = peer;
		editPeerOpen = true;
	}

	function maskToPrefix(mask: string): string {
		if (/^\d+$/.test(mask)) return mask;
		const parts = mask.split('.').map(Number);
		let bits = 0;
		for (const p of parts) {
			bits += (p >>> 0).toString(2).split('1').length - 1;
		}
		return String(bits);
	}

	let togglingEnabled = $state(false);

	async function handleToggleEnabled() {
		togglingEnabled = true;
		try {
			const fresh = await api.setManagedServerEnabled(!isUp);
			servers.applyMutationResponse(fresh);
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переключения');
		} finally {
			togglingEnabled = false;
		}
	}

	let togglingNAT = $state(false);

	async function handleToggleNAT() {
		togglingNAT = true;
		try {
			const fresh = await api.setManagedServerNAT(!server.natEnabled);
			servers.applyMutationResponse(fresh);
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переключения NAT');
		} finally {
			togglingNAT = false;
		}
	}

	function openConf(peer: ManagedPeer) {
		confPubkey = peer.publicKey;
		confPeerName = peer.description || 'peer';
		confModalOpen = true;
	}

	let policies = $state<{ id: string; description: string }[]>([]);
	let policyChanging = $state(false);
	// Local mirror of server.policy for the <select>. On error we reset
	// it back to server.policy so the DOM reverts — without this the
	// browser keeps the failed value because no fresh snapshot arrives.
	// Empty initial value is overwritten by the $effect on mount before
	// the select is interactive.
	let selectedPolicy = $state('');

	$effect(() => {
		selectedPolicy = server.policy;
	});

	onMount(async () => {
		try {
			policies = await api.getManagedServerPolicies();
		} catch {
			policies = [];
		}
	});

	let orphanedPolicy = $derived.by(() => {
		const p = server.policy;
		if (!p || p === 'none' || p === 'permit' || p === 'deny') return null;
		if (policies.some(o => o.id === p)) return null;
		return p;
	});

	async function handlePolicyChange(newPolicy: string) {
		if (newPolicy === server.policy) return;
		policyChanging = true;
		try {
			const fresh = await api.setManagedServerPolicy(newPolicy);
			servers.applyMutationResponse(fresh);
			notifications.success('Политика обновлена');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка изменения политики');
			selectedPolicy = server.policy;
		} finally {
			policyChanging = false;
		}
	}
</script>

<div class="card managed-card" class:status-up={isUp}>
	<!-- Header -->
	<div class="card-header">
		<div class="header-info">
			<div class="flex items-center gap-2">
				<span class="led" class:led-up={isUp} class:led-down={!isUp}></span>
				<h3 class="card-title">Мой WireGuard сервер</h3>
				<span class="badge-managed">Управляемый</span>
			</div>
			<div class="server-meta">
				<span class="meta mono">{server.interfaceName}</span>
				<span class="meta mono">{server.address}/{maskToPrefix(server.mask)}</span>
				<span class="meta mono">:{server.listenPort}</span>
				{#if stats && (totalRx > 0 || totalTx > 0)}
					<span class="meta mono">↓{formatBytes(totalRx)} ↑{formatBytes(totalTx)}</span>
				{/if}
			</div>
		</div>
		<div class="header-actions">
			<Toggle
				checked={isUp}
				onchange={handleToggleEnabled}
				disabled={togglingEnabled}
				size="sm"
			/>
			<button class="btn btn-ghost btn-sm" onclick={onOpenASC} title="Параметры обфускации">
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M12 20V10M18 20V4M6 20v-4"/>
				</svg>
			</button>
			<button class="btn btn-ghost btn-sm" onclick={() => editServerOpen = true} title="Настройки">
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z"/>
					<path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z"/>
				</svg>
			</button>
			<button
				class="btn btn-sm"
				class:btn-ghost={!confirmDelete}
				class:btn-danger={confirmDelete}
				onclick={handleDeleteServer}
				disabled={deleting}
				title="Удалить сервер"
			>
				{#if confirmDelete}
					Подтвердить?
				{:else}
					<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
					</svg>
				{/if}
			</button>
		</div>
	</div>

	<!-- NAT -->
	<div class="nat-row">
		<div class="nat-info">
			<span class="nat-label">NAT</span>
			<span class="nat-hint">Трансляция адресов для выхода клиентов в интернет</span>
		</div>
		<Toggle
			checked={server.natEnabled ?? false}
			onchange={handleToggleNAT}
			disabled={togglingNAT}
			size="sm"
		/>
	</div>

	<!-- Policy -->
	<div class="policy-row">
		<div class="policy-info">
			<span class="policy-label">Политика доступа</span>
			<span class="policy-hint">Регулирует выход в интернет для клиентов сервера. Применяется ко всем клиентам этого сервера.</span>
		</div>
		<select
			class="policy-select"
			bind:value={selectedPolicy}
			disabled={policyChanging}
			onchange={(e) => handlePolicyChange(e.currentTarget.value)}
		>
			<option value="none">Сбросить (по умолчанию)</option>
			<option value="permit">Разрешить</option>
			<option value="deny">Запретить</option>
			{#if orphanedPolicy}
				<option value={orphanedPolicy}>{orphanedPolicy} (отсутствует)</option>
			{/if}
			{#each policies as p (p.id)}
				<option value={p.id}>
					{p.description ? `${p.id} — ${p.description}` : p.id}
				</option>
			{/each}
		</select>
	</div>

	<!-- Peers -->
	<div class="peers-section">
		<div class="peers-header">
			<span class="peers-title">Клиенты {#if stats}({onlineCount}/{(server.peers ?? []).length} онлайн){:else}({(server.peers ?? []).length}){/if}</span>
			<div class="peers-controls">
				<PeerSortControls
					bind:sortBy
					bind:sortAsc
					bind:searchQuery
					showSearch={(server.peers ?? []).length >= 5}
				/>
				<button class="btn btn-secondary btn-sm" onclick={() => addPeerOpen = true}>
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>
					</svg>
					Добавить
				</button>
			</div>
		</div>

		{#if (server.peers ?? []).length === 0}
			<div class="empty-peers">Нет клиентов. Добавьте первого.</div>
		{:else}
			<div class="peers-list">
				{#each sortedPeers as peer (peer.publicKey)}
					{@const peerStats = getPeerStats(peer.publicKey)}
					<div class="peer-row" class:peer-disabled={!peer.enabled}>
						<div class="peer-info">
							<div class="peer-name-row">
								{#if peerStats}
									<span class="peer-led" class:peer-led-online={peerStats.online} class:peer-led-offline={!peerStats.online}></span>
								{/if}
								<span class="peer-name">{peer.description || peer.publicKey.substring(0, 12) + '...'}</span>
							</div>
							<div class="peer-meta">
								<span class="peer-ip mono">{peer.tunnelIP}</span>
								{#if peerStats?.endpoint}
									<span class="peer-endpoint mono">{peerStats.endpoint}</span>
								{/if}
								{#if peerStats}
									<span class="peer-traffic mono">↓{formatBytes(peerStats.txBytes)} ↑{formatBytes(peerStats.rxBytes)}</span>
								{/if}
								{#if peerStats?.lastHandshake}
									<span class="peer-handshake">{formatRelativeTime(peerStats.lastHandshake)}</span>
								{/if}
							</div>
						</div>
						<div class="peer-actions">
							<Toggle
								checked={peer.enabled}
								onchange={() => handleTogglePeer(peer)}
								size="sm"
							/>
							<button class="peer-action-btn" onclick={() => openConf(peer)} title="Скачать .conf">
								<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
									<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/>
								</svg>
							</button>
							<button class="peer-action-btn" onclick={() => openEditPeer(peer)} title="Редактировать">
								<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
									<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
								</svg>
							</button>
							<button
								class="peer-action-btn peer-action-btn-danger"
								class:peer-action-btn-confirm={confirmDeletePeerKey === peer.publicKey}
								onclick={() => handleDeletePeerClick(peer)}
								title={confirmDeletePeerKey === peer.publicKey ? 'Нажмите ещё раз для удаления' : 'Удалить'}
							>
								<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
									<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
								</svg>
							</button>
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</div>
</div>

<!-- Modals -->
<EditManagedServerModal
	bind:open={editServerOpen}
	{server}
	onclose={() => editServerOpen = false}
	onUpdated={onUpdated}
/>

<AddManagedPeerModal
	bind:open={addPeerOpen}
	{server}
	{routerIP}
	onclose={() => addPeerOpen = false}
	onAdded={onUpdated}
/>

{#if selectedPeer}
	<EditManagedPeerModal
		bind:open={editPeerOpen}
		peer={selectedPeer}
		{routerIP}
		onclose={() => { editPeerOpen = false; selectedPeer = null; }}
		onUpdated={onUpdated}
	/>
{/if}

<PeerConfModal
	bind:open={confModalOpen}
	pubkey={confPubkey}
	peerName={confPeerName}
	onclose={() => confModalOpen = false}
/>


<style>
	.managed-card {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		border-color: var(--accent);
	}

	.card-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 1rem;
	}

	.header-info {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
		min-width: 0;
	}

	.card-title {
		font-size: 1.125rem;
		font-weight: 600;
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

	.server-meta {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.meta {
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.mono {
		font-family: var(--font-mono, monospace);
	}

	.header-actions {
		display: flex;
		gap: 0.25rem;
		flex-shrink: 0;
	}

	.nat-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 0.625rem 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
	}

	.nat-info {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.nat-label {
		font-size: 0.8125rem;
		font-weight: 500;
	}

	.nat-hint {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.policy-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
		padding: 0.625rem 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
	}

	.policy-info {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.policy-label {
		font-size: 0.8125rem;
		font-weight: 500;
	}

	.policy-hint {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.policy-select {
		flex-shrink: 0;
		padding: 0.375rem 0.625rem;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 0.8125rem;
		cursor: pointer;
	}

	.policy-select:disabled {
		opacity: 0.5;
		cursor: wait;
	}

	.peers-section {
		border-top: 1px solid var(--border);
		padding-top: 1rem;
	}

	.peers-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.75rem;
	}

	.peers-controls {
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

	.peers-title {
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--text-secondary);
	}

	.empty-peers {
		padding: 1.5rem;
		text-align: center;
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.peers-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.peer-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 0.625rem 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		gap: 0.75rem;
	}

	.peer-disabled {
		opacity: 0.5;
	}

	.peer-info {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.peer-name {
		font-size: 0.8125rem;
		font-weight: 500;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.peer-ip {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.peer-actions {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		flex-shrink: 0;
	}


	.peer-action-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: auto;
		height: auto;
		padding: 0.375rem;
		background: transparent;
		border: none;
		color: var(--text-secondary);
		cursor: pointer;
		border-radius: var(--radius-sm);
		transition: color 0.15s ease, background 0.15s ease;
		box-sizing: border-box;
		filter: none;
	}

	.peer-action-btn:hover {
		background: var(--bg-hover);
		color: var(--text-primary);
		filter: none;
	}

	.peer-action-btn-danger:hover {
		color: var(--error, #ef4444);
	}

	.peer-action-btn-confirm {
		background: var(--error, #ef4444);
		color: white;
	}

	.peer-action-btn-confirm:hover {
		background: var(--error, #ef4444);
		color: white;
		filter: brightness(1.1);
	}

	/* LED indicators */
	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.led-up {
		background: var(--success, #22c55e);
		box-shadow: 0 0 4px var(--success, #22c55e);
	}

	.led-down {
		background: var(--text-muted);
	}

	.peer-led {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.peer-led-online {
		background: var(--success, #22c55e);
		box-shadow: 0 0 3px var(--success, #22c55e);
	}

	.peer-led-offline {
		background: var(--text-muted);
	}

	.peer-name-row {
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

	.peer-meta {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.peer-endpoint {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.peer-traffic {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.peer-handshake {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	@media (max-width: 640px) {
		.peers-header {
			flex-direction: column;
			align-items: stretch;
			gap: 0.5rem;
		}

		.peers-controls {
			flex-wrap: wrap;
		}

		.card-header {
			flex-direction: column;
		}

		.header-actions {
			align-self: flex-end;
		}

		.peer-row {
			flex-direction: column;
			align-items: stretch;
			gap: 0.5rem;
		}

		.peer-actions {
			justify-content: flex-end;
		}
	}
</style>
