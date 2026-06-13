<script lang="ts">
	import { STATUS_LABEL, type PeerRowProps } from '$lib/utils/peerRowVM';
	import { Toggle } from '$lib/components/ui';
	import { Download, SquarePen, Trash2 } from 'lucide-svelte';

	let { peer, vm, showToggle, showDownload, showActions, toggling, onToggle, onConf, onEdit, onDelete, onCopy }: PeerRowProps = $props();
</script>

<tr class="peer-row" class:peer-disabled={!vm.enabled}>
	<td class="col-name">
		<div class="name-cell">
			{#if showToggle}
				<span class="peer-inline-toggle">
					<Toggle checked={vm.enabled} onchange={() => onToggle(peer)} disabled={toggling} size="sm" spinner="none" />
				</span>
			{/if}
			<span class="peer-name">{vm.name}</span>
		</div>
	</td>
	<td class="col-status">
		<span class="peer-status status-{vm.status}">
			<span class="status-dot" class:dot-online={vm.status === 'online'} class:dot-offline={vm.status === 'offline'} class:dot-disabled={vm.status === 'disabled'}></span>
			{STATUS_LABEL[vm.status]}
		</span>
	</td>
	<td class="col-ip">
		<button type="button" class="cell-copy mono tech-value" onclick={() => onCopy(vm.ip, 'IP')} title={`Скопировать IP ${vm.ip}`}>
			{vm.ip}
		</button>
	</td>
	<td class="col-endpoint mono tech-value">{vm.endpointHost}</td>
	<td class="col-rx mono tech-value">{vm.rx}</td>
	<td class="col-tx mono tech-value">{vm.tx}</td>
	<td class="col-handshake mono tech-value">
		{#if vm.handshake}{vm.handshake.main}{#if vm.handshake.suffix}{" "}{vm.handshake.suffix}{/if}{:else}-{/if}
	</td>
	{#if showDownload || showActions}
		<td class="col-actions">
			<div class="peer-actions">
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
		</td>
	{/if}
</tr>

<style>
	.name-cell {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}
	.peer-name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.peer-row.peer-disabled {
		opacity: 0.55;
	}
	.peer-status {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-size: 0.6875rem;
		letter-spacing: 0.04em;
	}
	.status-online { color: var(--color-success); }
	.status-offline { color: var(--color-text-muted); }
	.status-disabled { color: var(--color-text-muted); }
	.status-dot { width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0; }
	.dot-online { background: var(--color-success); }
	.dot-offline { background: var(--color-text-muted); }
	.dot-disabled { background: var(--color-border); }
	.col-rx, .col-tx { text-align: right; }
	.peer-actions { display: flex; gap: 0.25rem; justify-content: flex-end; }
</style>
