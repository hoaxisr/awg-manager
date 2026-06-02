<script lang="ts">
	import type { WireguardServerPeer } from '$lib/types';
	import { formatRelativeTime, formatBytes } from '$lib/utils/format';
	import { IconButton } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { peerSort } from '$lib/stores/peerSort';
	import { peerAriaSort } from '$lib/utils/peerSort';
	import PeerTableSortHeader from './PeerTableSortHeader.svelte';

	interface Props {
		peers: WireguardServerPeer[];
		onDownloadConf?: (publicKey: string) => void;
	}

	let { peers, onDownloadConf }: Props = $props();

	function peerName(peer: WireguardServerPeer): string {
		return peer.description || `${peer.publicKey.slice(0, 8)}...`;
	}

	function peerStatus(peer: WireguardServerPeer): 'disabled' | 'online' | 'offline' {
		if (!peer.enabled) return 'disabled';
		return peer.online ? 'online' : 'offline';
	}

	function peerIP(peer: WireguardServerPeer): string {
		const raw = peer.allowedIPs?.find((ip) => ip.includes('/32')) || peer.allowedIPs?.[0] || '-';
		// Убираем маску одиночного хоста (/32, /128) — показываем и копируем чистый IP.
		return raw.replace(/\/(32|128)$/, '');
	}

	function splitEndpoint(endpoint: string): { host: string; port?: string } {
		const trimmed = endpoint.trim();
		if (!trimmed || trimmed === '-') return { host: '-' };
		const bracketMatch = /^(\[[^\]]+\]):(\d+)$/.exec(trimmed);
		if (bracketMatch) return { host: bracketMatch[1], port: `:${bracketMatch[2]}` };
		const lastColon = trimmed.lastIndexOf(':');
		if (lastColon <= 0) return { host: trimmed };
		const host = trimmed.slice(0, lastColon);
		const port = trimmed.slice(lastColon + 1);
		if (!/^\d+$/.test(port)) return { host: trimmed };
		if (host.includes(':')) return { host: trimmed };
		return { host, port: `:${port}` };
	}

	function splitHandshakeLabel(value: string): { main: string; suffix?: string } {
		const trimmed = value.trim();
		if (trimmed.endsWith(' назад')) return { main: trimmed.slice(0, -' назад'.length), suffix: 'назад' };
		return { main: trimmed };
	}

	async function copyCellValue(value: string, label: string): Promise<void> {
		if (!value || value === '-') {
			notifications.warning(`${label} отсутствует`, { duration: 2000 });
			return;
		}
		if (await copyToClipboard(value)) {
			notifications.success(`${label} скопирован: ${value}`, { duration: 2000 });
		} else {
			notifications.error(`Не удалось скопировать ${label.toLowerCase()}`);
		}
	}

</script>

{#if peers.length === 0}
	<p class="text-muted">Нет пиров</p>
{:else}
	<div class="desktop-peer-table">
		<div class="peer-table-wrap">
			<table class="peer-table" class:has-actions={!!onDownloadConf}>
				<thead>
					<tr>
						<th class="col-name" aria-sort={peerAriaSort($peerSort, 'name')}>
							<PeerTableSortHeader label="Имя" sortKey="name" />
						</th>
						<th class="col-ip" aria-sort={peerAriaSort($peerSort, 'ip')}>
							<PeerTableSortHeader label="IP" sortKey="ip" />
						</th>
						<th class="col-endpoint" aria-sort={peerAriaSort($peerSort, 'endpoint')}>
							<PeerTableSortHeader label="Endpoint" sortKey="endpoint" />
						</th>
						<th class="col-traffic" aria-sort={peerAriaSort($peerSort, 'traffic')}>
							<PeerTableSortHeader label="Трафик" sortKey="traffic" />
						</th>
						<th class="col-handshake" aria-sort={peerAriaSort($peerSort, 'handshake')}>
							<PeerTableSortHeader label="Handshake" sortKey="handshake" />
						</th>
						{#if onDownloadConf}
							<th class="col-action"></th>
						{/if}
					</tr>
				</thead>
				<tbody>
					{#each peers as peer (peer.publicKey)}
						{@const status = peerStatus(peer)}
						{@const ipValue = peerIP(peer)}
						{@const endpointValue = peer.endpoint || '-'}
						{@const ep = splitEndpoint(endpointValue)}
						{@const hs = peer.lastHandshake ? splitHandshakeLabel(formatRelativeTime(peer.lastHandshake)) : null}
						<tr class:peer-offline={!peer.online} class:peer-disabled={!peer.enabled}>
							<td class="col-name peer-name-cell">
								<div class="peer-name-row">
									<span
										class="led"
										class:led-online={status === 'online'}
										class:led-offline={status === 'offline'}
										class:led-disabled={status === 'disabled'}
									></span>
									<span class="peer-name">{peerName(peer)}</span>
								</div>
								<div class="peer-status-sub">{status}</div>
							</td>
							<td class="mono tech-value col-ip">
								<button
									type="button"
									class="cell-copy mono tech-value"
									onclick={() => void copyCellValue(ipValue, 'IP')}
									title={`Скопировать IP ${ipValue}`}
								>
									{ipValue}
								</button>
							</td>
							<td class="col-endpoint">
								<button
									type="button"
									class="cell-copy endpoint-copy mono tech-value"
									onclick={() => void copyCellValue(endpointValue, 'Endpoint')}
									title={endpointValue !== '-' ? `Скопировать Endpoint ${endpointValue}` : 'Endpoint отсутствует'}
								>
									<span class="endpoint-text">{ep.host}</span>
									{#if ep.port}
										<span class="endpoint-port">{ep.port}</span>
									{/if}
								</button>
							</td>
							<td class="col-traffic">
								<div class="traffic-cell mono tech-value">
									<span class="traffic-rx">RX: {formatBytes(peer.rxBytes)}</span>
									<span class="traffic-tx">TX: {formatBytes(peer.txBytes)}</span>
								</div>
							</td>
							<td class="mono tech-value col-handshake">
								{#if hs}
									<span class="handshake-text">{hs.main}</span>
									{#if hs.suffix}
										<span class="handshake-suffix">{" "}{hs.suffix}</span>
									{/if}
								{:else}
									-
								{/if}
							</td>
							{#if onDownloadConf}
								<td class="col-action">
									<IconButton
										ariaLabel={`Скачать .conf для «${peerName(peer)}»`}
										title={`Скачать .conf для «${peerName(peer)}»`}
										onclick={() => onDownloadConf?.(peer.publicKey)}
									>
										<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
											<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
											<polyline points="7,10 12,15 17,10" />
											<line x1="12" y1="15" x2="12" y2="3" />
										</svg>
									</IconButton>
								</td>
							{/if}
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	</div>

	<div class="mobile-peer-list">
		{#each peers as peer (peer.publicKey)}
			{@const status = peerStatus(peer)}
			{@const ipValue = peerIP(peer)}
			{@const endpointValue = peer.endpoint || '-'}
			{@const hs = peer.lastHandshake ? splitHandshakeLabel(formatRelativeTime(peer.lastHandshake)) : null}
			<article class="mobile-peer-card" class:peer-offline={!peer.online} class:peer-disabled={!peer.enabled} class:has-actions={!!onDownloadConf}>
				<div class="mobile-peer-card-top">
					<div class="mobile-peer-title-row">
						<span
							class="led"
							class:led-online={status === 'online'}
							class:led-offline={status === 'offline'}
							class:led-disabled={status === 'disabled'}
						></span>
						<span class="mobile-peer-name">{peerName(peer)}</span>
					</div>

					{#if onDownloadConf}
						<div class="mobile-peer-actions">
							<IconButton
								ariaLabel={`Скачать .conf для «${peerName(peer)}»`}
								title={`Скачать .conf для «${peerName(peer)}»`}
								onclick={() => onDownloadConf?.(peer.publicKey)}
							>
								<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
									<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
									<polyline points="7,10 12,15 17,10" />
									<line x1="12" y1="15" x2="12" y2="3" />
								</svg>
							</IconButton>
						</div>
					{/if}
				</div>

				<div class="mobile-peer-card-middle">
					<div class="mobile-peer-status-row">
						<span>{status}</span>
						<span class="mobile-peer-handshake">
							{#if hs}
								{hs.main}{#if hs.suffix}{" "}{hs.suffix}{/if}
							{:else}
								-
							{/if}
						</span>
					</div>

					<div class="mobile-peer-net-row mono tech-value">
						<button
							type="button"
							class="cell-copy mobile-peer-ip"
							onclick={() => void copyCellValue(ipValue, 'IP')}
							title={`Скопировать IP ${ipValue}`}
						>
							{ipValue}
						</button>

						<button
							type="button"
							class="cell-copy mobile-peer-endpoint"
							onclick={() => void copyCellValue(endpointValue, 'Endpoint')}
							title={endpointValue !== '-' ? `Скопировать Endpoint ${endpointValue}` : 'Endpoint отсутствует'}
						>
							<span class="mobile-label">EP</span>
							<span class="mobile-endpoint-value">{endpointValue}</span>
						</button>
					</div>
				</div>

				<div class="mobile-peer-card-bottom">
					<div class="mobile-peer-traffic-row mono tech-value">
						<span>RX: {formatBytes(peer.rxBytes)}</span>
						<span>TX: {formatBytes(peer.txBytes)}</span>
					</div>
				</div>
			</article>
		{/each}
	</div>
{/if}

<style>
	.peer-table-wrap {
		overflow-x: auto;
		margin-top: 0.75rem;
	}

	.desktop-peer-table {
		display: block;
	}

	.mobile-peer-list {
		display: none;
	}

	.peer-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.875rem;
		table-layout: auto;
	}

	.peer-table th {
		text-align: center;
		background: color-mix(in srgb, var(--accent) 16%, transparent);
		color: var(--accent);
		border-bottom: 1px solid color-mix(in srgb, var(--accent) 30%, transparent);
		font-weight: 500;
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		padding: 0.65rem 0.75rem;
		line-height: 1.2;
	}

	.peer-table td {
		padding: 0.5rem 0.75rem;
		border-bottom: 1px solid var(--border);
		transition: background-color 0.15s ease;
	}

	.peer-table tbody tr:hover td {
		background: color-mix(in srgb, var(--bg-hover) 70%, transparent);
	}

	.peer-table tbody tr.peer-offline:hover td,
	.peer-table tbody tr.peer-disabled:hover td {
		background: color-mix(in srgb, var(--bg-hover) 45%, transparent);
	}

	.peer-name-cell {
		text-align: left;
	}

	.peer-name-row {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		min-width: 0;
	}

	.peer-name {
		font-weight: 500;
		font-size: 13px;
		line-height: 1.15;
		max-width: 150px;
		white-space: normal;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.peer-status-sub {
		font-size: 10px;
		color: var(--text-muted);
		line-height: 1;
		margin-top: 0.1rem;
	}

	.mono {
		font-family: var(--font-mono, monospace);
		color: var(--text-secondary);
	}

	.tech-value {
		font-size: 10px;
		line-height: 1.15;
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

	.col-ip,
	.col-traffic,
	.col-handshake {
		text-align: center;
	}

	.peer-table th.col-endpoint {
		text-align: center;
	}

	.peer-table td.col-endpoint {
		text-align: left;
		vertical-align: middle;
	}

	.col-action {
		text-align: right;
		white-space: nowrap;
	}

	.traffic-cell {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0;
		line-height: 1.05;
		white-space: nowrap;
	}

	.endpoint-copy {
		display: inline-flex;
		flex-direction: column;
		align-items: flex-start;
		justify-content: center;
		gap: 0;
		max-width: 100%;
		text-align: left;
	}

	.endpoint-text {
		display: inline;
		white-space: normal;
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.15;
	}

	.endpoint-port {
		display: inline;
		white-space: nowrap;
		line-height: 1.15;
	}

	.handshake-text,
	.handshake-suffix {
		display: inline;
	}

	.cell-copy {
		padding: 0;
		border: 0;
		background: none;
		color: inherit;
		cursor: pointer;
		text-align: center;
	}

	.cell-copy:hover {
		text-decoration: underline;
		color: var(--text-primary);
	}

	@media (max-width: 640px) {
		.desktop-peer-table {
			display: none;
		}

		.mobile-peer-list {
			display: flex;
			flex-direction: column;
			gap: 0.6rem;
		}

		.mobile-peer-card {
			display: flex;
			flex-direction: column;
			gap: 0.65rem;
			padding: 0.7rem 0.75rem;
			border: 1px solid var(--border);
			border-radius: var(--radius);
			background: var(--bg-primary);
		}

		.mobile-peer-card-top {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: start;
			gap: 0.5rem;
		}

		.mobile-peer-card-middle,
		.mobile-peer-card-bottom,
		.mobile-peer-title-row {
			min-width: 0;
		}

		.mobile-peer-title-row {
			display: flex;
			align-items: center;
			gap: 0.45rem;
			min-width: 0;
		}

		.mobile-peer-name {
			min-width: 0;
			max-width: 100%;
			font-size: 13px;
			font-weight: 600;
			line-height: 1.15;
			color: var(--text-primary);
			overflow-wrap: anywhere;
		}

		.mobile-peer-status-row {
			display: flex;
			justify-content: space-between;
			align-items: center;
			gap: 0.5rem;
			margin-top: 0.2rem;
			font-size: 10px;
			line-height: 1;
			color: var(--text-muted);
		}

		.mobile-peer-handshake {
			text-align: right;
			white-space: nowrap;
			color: var(--text-muted);
		}

		.mobile-peer-net-row {
			display: grid;
			grid-template-columns: minmax(0, 0.9fr) minmax(0, 1.25fr);
			gap: 0.5rem;
			margin-top: 0.45rem;
			line-height: 1.1;
		}

		.mobile-peer-ip {
			min-width: 0;
			text-align: left;
			white-space: nowrap;
		}

		.mobile-peer-endpoint {
			display: inline-flex;
			flex-direction: row;
			align-items: flex-end;
			justify-content: flex-end;
			gap: 0.25rem;
			min-width: 0;
			justify-self: end;
			max-width: 100%;
			text-align: right;
			white-space: nowrap;
		}

		.mobile-endpoint-value {
			display: block;
			min-width: 0;
			max-width: 100%;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}

		.mobile-label {
			flex: 0 0 auto;
			color: var(--text-muted);
			font-size: 9px;
			line-height: 1;
			letter-spacing: 0.04em;
		}

		.mobile-peer-traffic-row {
			display: grid;
			grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
			gap: 0.5rem;
			margin-top: 0.35rem;
			line-height: 1.1;
		}

		.mobile-peer-traffic-row > span:first-child {
			justify-self: start;
		}

		.mobile-peer-traffic-row > span:last-child {
			justify-self: end;
			text-align: right;
		}

		.mobile-peer-actions {
			display: flex;
			align-items: flex-start;
			justify-content: flex-end;
			gap: 0.25rem;
			align-self: start;
		}

		.mobile-peer-actions :global(button) {
			width: 32px;
			height: 32px;
			flex: 0 0 32px;
			padding: 0;
		}

		@media (max-width: 360px) {
			.mobile-peer-card-top {
				grid-template-columns: minmax(0, 1fr) auto;
			}

			.mobile-peer-actions {
				flex-wrap: nowrap;
				gap: 0.2rem;
			}

			.mobile-peer-actions :global(button) {
				width: 30px;
				height: 30px;
				flex-basis: 30px;
			}
		}
	}
</style>
