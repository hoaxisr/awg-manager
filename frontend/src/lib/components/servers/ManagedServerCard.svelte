<script lang="ts">
	import { onMount } from 'svelte';
	import type { ManagedServer, ManagedPeer, ManagedPeerStats, ManagedServerStats } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import { formatBytes } from '$lib/utils/format';
	import { Toggle, Button, IconButton, Dropdown, ChipMultiSelect, type DropdownOption, type ChipOption } from '$lib/components/ui';
	import {
		EditManagedServerModal,
		AddManagedPeerModal,
		EditManagedPeerModal,
		PeerConfModal,
		PeerSortControls,
		ManagedPeerTable,
	} from '$lib/components/servers';
	import { comparePeerFieldsDirected } from '$lib/utils/peerSort';
	import { peerSort } from '$lib/stores/peerSort';
	import { isStandardAccessPolicyName } from '$lib/utils/accessPolicy';

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
	let confirmDeletePeerKey = $state<string | null>(null);

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
				$peerSort.sortBy,
				$peerSort.sortAsc,
			);
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
		try {
			const fresh = await api.toggleManagedPeer(serverId, peer.publicKey, !peer.enabled);
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
			const fresh = await api.deleteManagedPeer(serverId, peer.publicKey);
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

	let natMode = $derived<'full' | 'internet-only' | 'none'>(
		server.natMode ?? (server.natEnabled ? 'full' : 'none')
	);

	const natModeOptions: DropdownOption[] = [
		{ value: 'full', label: 'Полный NAT' },
		{ value: 'internet-only', label: 'NAT только для интернета' },
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

	async function handleSetNATMode(mode: string) {
		const m = mode as 'full' | 'internet-only' | 'none';
		if (m === natMode) return;
		togglingNAT = true;
		try {
			const fresh = await api.setManagedServerNATMode(serverId, m);
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

	let standardPolicies = $derived(policies.filter((p) => isStandardAccessPolicyName(p.id)));

	let policyOptions = $derived<DropdownOption[]>([
		{ value: 'none', label: 'Политика по умолчанию' },
		...(orphanedPolicy ? [{ value: orphanedPolicy, label: `${orphanedPolicy} (отсутствует)` }] : []),
		...standardPolicies.map((p) => ({
			value: p.id,
			label: p.description ? `${p.id} — ${p.description}` : p.id,
		})),
	]);

	async function handlePolicyChange(newPolicy: string) {
		if (newPolicy === server.policy) return;
		policyChanging = true;
		try {
			const fresh = await api.setManagedServerPolicy(serverId, newPolicy);
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
				<h3 class="card-title">{serverDisplayName}</h3>
				<span class="badge-managed">Управляемый</span>
			</div>
			<div class="server-meta">
				<span class="meta mono">{server.interfaceName}</span>
				<span class="meta mono">{server.address}/{maskToPrefix(server.mask)}</span>
				<span class="meta mono">:{server.listenPort}</span>
				{#if server.mtu}
					<span class="meta mono">MTU {server.mtu}</span>
				{/if}
				{#if stats && (totalRx > 0 || totalTx > 0)}
					<span class="meta mono">↓{formatBytes(totalRx)} ↑{formatBytes(totalTx)}</span>
				{/if}
			</div>
		</div>
		<div class="header-actions">
			<Toggle
				checked={isUp}
				onchange={handleToggleEnabled}
				disabled={togglingEnabled || restartingServer}
				size="sm"
			/>
			<IconButton
				ariaLabel={isUp
					? `Перезапустить сервер ${serverDisplayName}`
					: `Запустить сервер ${serverDisplayName}`}
				title={isUp
					? `Перезапустить сервер «${serverDisplayName}»`
					: `Запустить сервер «${serverDisplayName}»`}
				onclick={handleRestartOrStart}
				disabled={restartingServer || togglingEnabled || deleting}
			>
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M21 12a9 9 0 1 1-2.64-6.36" />
					<path d="M21 3v6h-6" />
				</svg>
			</IconButton>
			<IconButton
				ariaLabel={`Открыть параметры обфускации сервера ${serverDisplayName}`}
				title={`Параметры обфускации сервера «${serverDisplayName}»`}
				onclick={onOpenASC}
			>
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M12 20V10M18 20V4M6 20v-4"/>
				</svg>
			</IconButton>
			<IconButton
				ariaLabel={`Открыть настройки сервера ${serverDisplayName}`}
				title={`Настройки сервера «${serverDisplayName}»`}
				onclick={() => editServerOpen = true}
			>
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z"/>
					<path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z"/>
				</svg>
			</IconButton>
			{#if confirmDelete}
				<Button
					variant="danger"
					size="sm"
					onclick={handleDeleteServer}
					loading={deleting}
					title={`Подтвердить удаление сервера «${serverDisplayName}»`}
				>
					Подтвердить?
				</Button>
			{:else}
				<IconButton
					variant="danger"
					ariaLabel={`Удалить сервер ${serverDisplayName}`}
					title={`Удалить сервер «${serverDisplayName}»`}
					onclick={handleDeleteServer}
					disabled={deleting}
				>
					<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
					</svg>
				</IconButton>
			{/if}
		</div>
	</div>

	<!-- NAT -->
	<div class="nat-row nat-row--select">
		<div class="nat-info">
			<span class="nat-label">NAT</span>
			{#if natMode === 'internet-only'}
				<span class="nat-hint">реальный IP клиента в LAN, NAT только в интернет</span>
			{:else}
				<span class="nat-hint">Трансляция адресов для выхода клиентов в интернет</span>
			{/if}
			{#if ingressEnabled && natMode === 'full'}
				<span class="nat-hint nat-warning">NAT для интернета не действует — интернет-трафик идёт через sing-box (туннель); режим NAT влияет только на видимость в LAN</span>
			{/if}
		</div>
		<div class="nat-select">
			<Dropdown
				value={natMode}
				options={natModeOptions}
				disabled={togglingNAT}
				onchange={handleSetNATMode}
				fullWidth
			/>
		</div>
	</div>

	<!-- LAN segments -->
	<div class="nat-row nat-row--select">
		<div class="nat-info">
			<span class="nat-label">Доступ в LAN</span>
			<span class="nat-hint">Сегменты LAN, доступные клиентам этого сервера</span>
		</div>
		<ChipMultiSelect values={server.lanSegments ?? []} options={lanSegmentOptions} onchange={handleSetLANSegments} />
	</div>

	<!-- Egress в sing-box -->
	<div class="nat-row">
		<div class="nat-info">
			<span class="nat-label">Маршрутизация через sing-box</span>
			<span class="nat-hint">Заворачивать интернет-трафик клиентов данного сервера в sing-box</span>
		</div>
		<Toggle checked={ingressEnabled} onchange={handleToggleIngress} disabled={togglingIngress} size="sm" />
	</div>

	<!-- Policy -->
	<div class="policy-row">
		<div class="policy-info">
			<span class="policy-label">Политика доступа</span>
			<span class="policy-hint">Регулирует выход в интернет для клиентов сервера. Применяется ко всем клиентам этого сервера.</span>
		</div>
		<div class="policy-select">
			<Dropdown
				value={selectedPolicy}
				options={policyOptions}
				disabled={policyChanging}
				onchange={handlePolicyChange}
				fullWidth
			/>
		</div>
	</div>

	<!-- Peers -->
	<div class="peers-section">
		<div class="peers-header">
			<span class="peers-title">Клиенты {#if stats}({onlineCount}/{(server.peers ?? []).length} онлайн){:else}({(server.peers ?? []).length}){/if}</span>
			<div class="peers-controls">
				<PeerSortControls
					bind:searchQuery
					showSearch={(server.peers ?? []).length >= 5}
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
				{confirmDeletePeerKey}
				onTogglePeer={handleTogglePeer}
				onOpenConf={openConf}
				onOpenEditPeer={openEditPeer}
				onDeletePeerClick={handleDeletePeerClick}
			/>
		{/if}
	</div>
</div>

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

{#snippet addPeerIcon()}
	<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
		<line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>
	</svg>
{/snippet}


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

	.nat-warning {
		color: var(--warning, #f59e0b);
	}

	.nat-row--select {
		align-items: flex-start;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.nat-row--select .nat-info {
		flex: 1 1 200px;
		min-width: 0;
	}

	.nat-select {
		flex: 0 0 auto;
		min-width: 200px;
		max-width: 280px;
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
		flex-wrap: wrap;
	}

	.policy-info {
		flex: 1 1 200px;
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
		flex: 0 0 auto;
		min-width: 240px;
		max-width: 320px;
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


	@media (max-width: 640px) {
		.policy-row {
			flex-direction: column;
			align-items: stretch;
		}

		.policy-info {
			flex: 0 0 auto;
		}

		.policy-select {
			width: 100%;
			min-width: 0;
			max-width: none;
		}

		.policy-select :global(.dropdown) {
			width: 100%;
		}

		.peers-header {
			flex-direction: column;
			align-items: stretch;
			gap: 0.5rem;
		}

		.peers-controls {
			flex-wrap: wrap;
		}

		.peers-controls :global(.btn) {
			width: 100%;
		}

		.card-header {
			flex-direction: column;
		}

		.header-actions {
			align-self: flex-end;
		}

	}

	@media (max-width: 640px) {
		.managed-card {
			overflow: hidden;
		}

		.peers-controls {
			display: grid;
			grid-template-columns: 1fr;
			width: 100%;
			gap: 0.4rem;
		}

		.peers-controls :global(.peer-sort-controls) {
			width: 100%;
		}

		.peers-controls :global(.btn) {
			width: 100%;
		}
	}
</style>
