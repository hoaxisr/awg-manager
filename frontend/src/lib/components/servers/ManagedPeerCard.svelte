<script lang="ts">
	import { STATUS_LABEL, type PeerRowProps } from '$lib/utils/peerRowVM';
	import { Toggle } from '$lib/components/ui';
	import { Download, SquarePen, Trash2 } from 'lucide-svelte';

	let { peer, vm, showToggle, showDownload, showActions, toggling, onToggle, onConf, onEdit, onDelete, onCopy }: PeerRowProps = $props();
</script>

<article class="mobile-peer-card" class:peer-disabled={!vm.enabled}>
	<div class="mobile-peer-card-top">
		<div class="mobile-peer-title-row">
			{#if showToggle}
				<span class="peer-inline-toggle">
					<Toggle checked={vm.enabled} onchange={() => onToggle(peer)} disabled={toggling} size="sm" spinner="none" />
				</span>
			{/if}
			<div class="peer-name-block">
				<span class="mobile-peer-name">{vm.name}</span>
				<div class="peer-status-sub status-{vm.status}">
					<span class="status-dot" class:dot-online={vm.status === 'online'} class:dot-offline={vm.status === 'offline'} class:dot-disabled={vm.status === 'disabled'}></span>
					<span>{STATUS_LABEL[vm.status]}</span>
				</div>
			</div>
		</div>

		{#if showDownload || showActions}
			<div class="mobile-peer-actions">
				{#if showDownload}
					<button class="peer-action-btn" onclick={() => onConf(peer)} title={`Скачать .conf для «${vm.name}»`}>
						<Download size={18} strokeWidth={2} aria-hidden="true" />
					</button>
				{/if}
				{#if showActions}
					<button class="peer-action-btn" onclick={() => onEdit(peer)} title={`Редактировать «${vm.name}»`}>
						<SquarePen size={18} strokeWidth={2} aria-hidden="true" />
					</button>
					<button class="peer-action-btn peer-action-btn-danger" onclick={() => onDelete(peer)} title={`Удалить «${vm.name}»`}>
						<Trash2 size={18} strokeWidth={2} aria-hidden="true" />
					</button>
				{/if}
			</div>
		{/if}
	</div>

	<div class="mobile-peer-card-middle">
		<span class="mobile-peer-handshake mono tech-value">
			{#if vm.handshake}{vm.handshake.main}{#if vm.handshake.suffix}{" "}{vm.handshake.suffix}{/if}{:else}-{/if}
		</span>
		<div class="mobile-peer-net-row mono tech-value">
			<button type="button" class="cell-copy mobile-peer-ip" onclick={() => onCopy(vm.ip, 'IP')} title={`Скопировать IP ${vm.ip}`}>
				<span class="mobile-label">IP</span> {vm.ip}
			</button>
			<span class="mobile-peer-endpoint"><span class="mobile-label">EP</span> {vm.endpointHost}</span>
		</div>
	</div>

	<div class="mobile-peer-card-bottom mono tech-value">
		<span>RX: {vm.rx}</span>
		<span>TX: {vm.tx}</span>
	</div>
</article>

<style>
	.mobile-peer-card {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		min-width: 0;
	}
	.mobile-peer-card.peer-disabled { opacity: 0.55; }
	.mobile-peer-card-top { display: flex; align-items: flex-start; justify-content: space-between; gap: 0.5rem; min-width: 0; }
	.mobile-peer-title-row { display: flex; align-items: center; gap: 0.5rem; min-width: 0; }
	.peer-name-block { display: flex; flex-direction: column; gap: 0.125rem; min-width: 0; }
	.mobile-peer-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
	.peer-status-sub { display: inline-flex; align-items: center; gap: 0.375rem; font-size: 0.625rem; letter-spacing: 0.04em; }
	.status-online { color: var(--color-success); }
	.status-offline, .status-disabled { color: var(--color-text-muted); }
	.status-dot { width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0; }
	.dot-online { background: var(--color-success); }
	.dot-offline { background: var(--color-text-muted); }
	.dot-disabled { background: var(--color-border); }
	.mobile-peer-actions { display: flex; gap: 0.25rem; flex-shrink: 0; }
	.mobile-peer-card-middle { display: flex; flex-direction: column; gap: 0.25rem; min-width: 0; }
	.mobile-peer-net-row { display: flex; flex-wrap: wrap; gap: 0.75rem; min-width: 0; }
	.mobile-peer-ip, .mobile-peer-endpoint { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
	.mobile-label { color: var(--color-text-muted); margin-right: 0.25rem; }
	.mobile-peer-card-bottom { display: flex; gap: 1rem; }
</style>
