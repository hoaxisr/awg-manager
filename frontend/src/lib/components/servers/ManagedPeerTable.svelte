<script lang="ts">
	import type { ManagedPeer, ManagedPeerStats } from '$lib/types';
	import { ConfirmModal } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { peerSort } from '$lib/stores/peerSort';
	import { peerAriaSort } from '$lib/utils/peerSort';
	import { buildPeerRowVM } from '$lib/utils/peerRowVM';
	import PeerTableSortHeader from './PeerTableSortHeader.svelte';
	import ManagedPeerRow from './ManagedPeerRow.svelte';
	import ManagedPeerCard from './ManagedPeerCard.svelte';

	interface Props {
		peers: ManagedPeer[];
		getPeerStats: (publicKey: string) => ManagedPeerStats | undefined;
		showPeerActions?: boolean;
		showPeerDownload?: boolean;
		showPeerToggle?: boolean;
		onTogglePeer: (peer: ManagedPeer) => void;
		onOpenConf: (peer: ManagedPeer) => void;
		onOpenEditPeer: (peer: ManagedPeer) => void;
		onDeletePeer: (peer: ManagedPeer) => void | Promise<void>;
		isPeerToggling?: (publicKey: string) => boolean;
	}

	let {
		peers,
		getPeerStats,
		showPeerActions = true,
		showPeerDownload = true,
		showPeerToggle = true,
		onTogglePeer,
		onOpenConf,
		onOpenEditPeer,
		onDeletePeer,
		isPeerToggling = () => false,
	}: Props = $props();

	let deletePeerTarget = $state<ManagedPeer | null>(null);
	let deletingPeer = $state(false);

	let rows = $derived(peers.map((peer) => ({ peer, vm: buildPeerRowVM(peer, getPeerStats(peer.publicKey)) })));

	function requestDeletePeer(peer: ManagedPeer) {
		deletePeerTarget = peer;
	}

	async function confirmDeletePeer() {
		if (!deletePeerTarget || deletingPeer) return;
		deletingPeer = true;
		try {
			await onDeletePeer(deletePeerTarget);
			deletePeerTarget = null;
		} catch {
			// оставить модалку открытой при ошибке
		} finally {
			deletingPeer = false;
		}
	}

	async function copyCellValue(value: string, label: string): Promise<void> {
		if (!value || value === '—' || value === '-') {
			notifications.warning(`${label} отсутствует`, { duration: 2000 });
			return;
		}
		if (await copyToClipboard(value)) {
			notifications.success(`${label} скопирован: ${value}`, { duration: 2000 });
		} else {
			notifications.error(`Не удалось скопировать ${label.toLowerCase()}`);
		}
	}

	let showActionsCol = $derived(showPeerDownload || showPeerActions);
</script>

<div class="peer-views">
<div class="desktop-peer-table">
	<div class="table-wrap">
		<table class="managed-peer-table">
			<thead>
				<tr>
					<th class="col-name" aria-sort={peerAriaSort($peerSort, 'name')}>
						<PeerTableSortHeader label="Имя" sortKey="name" />
					</th>
					<th class="col-status">Статус</th>
					<th class="col-ip" aria-sort={peerAriaSort($peerSort, 'ip')}>
						<PeerTableSortHeader label="IP" sortKey="ip" />
					</th>
					<th class="col-endpoint" aria-sort={peerAriaSort($peerSort, 'endpoint')}>
						<PeerTableSortHeader label="Endpoint" sortKey="endpoint" />
					</th>
					<th class="col-rx" aria-sort={peerAriaSort($peerSort, 'traffic')}>
						<PeerTableSortHeader label="RX" sortKey="traffic" />
					</th>
					<th class="col-tx">TX</th>
					<th class="col-handshake" aria-sort={peerAriaSort($peerSort, 'handshake')}>
						<PeerTableSortHeader label="Handshake" sortKey="handshake" />
					</th>
					{#if showActionsCol}
						<th class="col-actions">Действия</th>
					{/if}
				</tr>
			</thead>
			<tbody>
				{#each rows as { peer, vm } (peer.publicKey)}
					<ManagedPeerRow
						{peer}
						{vm}
						showToggle={showPeerToggle}
						showDownload={showPeerDownload}
						showActions={showPeerActions}
						toggling={isPeerToggling(peer.publicKey)}
						onToggle={onTogglePeer}
						onConf={onOpenConf}
						onEdit={onOpenEditPeer}
						onDelete={requestDeletePeer}
						onCopy={copyCellValue}
					/>
				{/each}
			</tbody>
		</table>
	</div>
</div>

<div class="mobile-peer-list">
	{#each rows as { peer, vm } (peer.publicKey)}
		<ManagedPeerCard
			{peer}
			{vm}
			showToggle={showPeerToggle}
			showDownload={showPeerDownload}
			showActions={showPeerActions}
			toggling={isPeerToggling(peer.publicKey)}
			onToggle={onTogglePeer}
			onConf={onOpenConf}
			onEdit={onOpenEditPeer}
			onDelete={requestDeletePeer}
			onCopy={copyCellValue}
		/>
	{/each}
</div>
</div>

{#if deletePeerTarget}
	<ConfirmModal
		open={true}
		title="Удаление клиента"
		message={`Удалить клиента «${deletePeerTarget.description || deletePeerTarget.publicKey.slice(0, 8) + '...'}»?`}
		secondary={`Туннельный IP: ${deletePeerTarget.tunnelIP}. Конфигурация и ключи будут удалены без возможности восстановления.`}
		confirmLabel="Удалить"
		busy={deletingPeer}
		onConfirm={confirmDeletePeer}
		onClose={() => { if (!deletingPeer) deletePeerTarget = null; }}
	/>
{/if}

<style>
	.table-wrap { overflow-x: auto; }
	.managed-peer-table { width: 100%; border-collapse: collapse; }
	.managed-peer-table th {
		text-align: left;
		font: 600 0.6875rem/1.2 var(--font-sans);
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: var(--color-text-muted);
		padding: 0.5rem 0.625rem;
		border-bottom: 1px solid var(--color-border);
		white-space: nowrap;
	}
	.col-rx, .col-tx { text-align: right; }
	.managed-peer-table :global(td) {
		padding: 0.625rem;
		border-bottom: 1px solid var(--color-border-subtle, var(--color-border));
		vertical-align: middle;
	}
	/* Переключение таблица/карточки по ШИРИНЕ КОНТЕЙНЕРА (а не вьюпорта):
	   при наличии rail доступная ширина < вьюпорта, поэтому viewport-медиазапрос
	   ошибается. Десктоп-таблицу показываем только когда она реально влезает. */
	.peer-views { container-type: inline-size; }
	.desktop-peer-table { display: none; }
	.mobile-peer-list { display: flex; flex-direction: column; gap: 0.5rem; }

	@container (min-width: 820px) {
		.desktop-peer-table { display: block; }
		.mobile-peer-list { display: none; }
	}
</style>
