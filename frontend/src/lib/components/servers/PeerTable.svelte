<script lang="ts">
	import type { WireguardServerPeer } from '$lib/types';
	import { formatRelativeTime, formatBytes } from '$lib/utils/format';

	interface Props {
		peers: WireguardServerPeer[];
		onDownloadConf?: (publicKey: string) => void;
	}

	let { peers, onDownloadConf }: Props = $props();
</script>

{#if peers.length === 0}
	<p class="text-muted">Нет пиров</p>
{:else}
	<div class="peer-table-wrap">
		<table class="peer-table">
			<thead>
				<tr>
					<th>Имя</th>
					<th>Статус</th>
					<th class="hide-mobile">Endpoint</th>
					<th>RX</th>
					<th>TX</th>
					<th class="hide-mobile">Handshake</th>
					{#if onDownloadConf}
						<th></th>
					{/if}
				</tr>
			</thead>
			<tbody>
				{#each peers as peer (peer.publicKey)}
					<tr class:peer-offline={!peer.online} class:peer-disabled={!peer.enabled}>
						<td class="peer-name">{peer.description || peer.publicKey.slice(0, 8) + '...'}</td>
						<td>
							{#if !peer.enabled}
								<span class="led led-disabled" title="Отключён"></span>
							{:else if peer.online}
								<span class="led led-online" title="Онлайн"></span>
							{:else}
								<span class="led led-offline" title="Оффлайн"></span>
							{/if}
						</td>
						<td class="mono hide-mobile">{peer.endpoint || '-'}</td>
						<td class="mono">{formatBytes(peer.rxBytes)}</td>
						<td class="mono">{formatBytes(peer.txBytes)}</td>
						<td class="mono hide-mobile">
							{#if peer.lastHandshake}
								{formatRelativeTime(peer.lastHandshake)}
							{:else}
								-
							{/if}
						</td>
						{#if onDownloadConf}
							<td>
								<button
									class="btn btn-ghost btn-sm conf-btn"
									title="Скачать .conf"
									onclick={() => onDownloadConf?.(peer.publicKey)}
								>
									<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
										<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
										<polyline points="7,10 12,15 17,10"/>
										<line x1="12" y1="15" x2="12" y2="3"/>
									</svg>
								</button>
							</td>
						{/if}
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}

<style>
	.peer-table-wrap {
		overflow-x: auto;
	}

	.peer-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.875rem;
	}

	.peer-table th,
	.peer-table td {
		padding: 0.5rem 0.75rem;
		text-align: left;
		border-bottom: 1px solid var(--border);
	}

	.peer-table th {
		font-weight: 500;
		color: var(--text-muted);
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.peer-name {
		font-weight: 500;
		max-width: 150px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.mono {
		font-family: var(--font-mono, monospace);
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.led {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 50%;
	}

	.led-online {
		background: var(--success, #10b981);
		box-shadow: 0 0 6px var(--success, #10b981);
	}

	.led-offline {
		background: var(--text-muted, #6b7280);
	}

	.led-disabled {
		background: var(--error, #ef4444);
		opacity: 0.6;
	}

	.peer-offline td {
		opacity: 0.6;
	}

	.peer-disabled td {
		opacity: 0.4;
	}

	.conf-btn {
		padding: 0.25rem;
	}

	.btn-sm {
		padding: 0.25rem 0.5rem;
		font-size: 0.8125rem;
	}

	@media (max-width: 640px) {
		.hide-mobile {
			display: none;
		}
	}
</style>
