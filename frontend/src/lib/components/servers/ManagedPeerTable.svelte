<script lang="ts">
	import type { ManagedPeer, ManagedPeerStats } from '$lib/types';
	import { Toggle } from '$lib/components/ui';
	import { formatBytes, formatRelativeTime } from '$lib/utils/format';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { peerSort } from '$lib/stores/peerSort';
	import type { PeerSortKey } from '$lib/utils/peerSort';

	interface Props {
		peers: ManagedPeer[];
		getPeerStats: (publicKey: string) => ManagedPeerStats | undefined;
		confirmDeletePeerKey: string | null;
		onTogglePeer: (peer: ManagedPeer) => void;
		onOpenConf: (peer: ManagedPeer) => void;
		onOpenEditPeer: (peer: ManagedPeer) => void;
		onDeletePeerClick: (peer: ManagedPeer) => void;
	}

	let {
		peers,
		getPeerStats,
		confirmDeletePeerKey,
		onTogglePeer,
		onOpenConf,
		onOpenEditPeer,
		onDeletePeerClick,
	}: Props = $props();

	function peerName(peer: ManagedPeer): string {
		return peer.description || `${peer.publicKey.slice(0, 8)}...`;
	}

	function peerStatus(peer: ManagedPeer, peerStats: ManagedPeerStats | undefined): 'disabled' | 'online' | 'offline' {
		if (!peer.enabled) return 'disabled';
		return peerStats?.online ? 'online' : 'offline';
	}

	function splitHandshakeLabel(value: string): { main: string; suffix?: string } {
		const trimmed = value.trim();
		if (trimmed.endsWith(' назад')) {
			return { main: trimmed.slice(0, -' назад'.length), suffix: 'назад' };
		}
		return { main: trimmed };
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

	function isInsideInlineToggle(event: Event): boolean {
		return event.target instanceof HTMLElement && !!event.target.closest('.peer-inline-toggle');
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

	function ariaSort(key: PeerSortKey): 'ascending' | 'descending' | 'none' {
		if ($peerSort.sortBy !== key) return 'none';
		return $peerSort.sortAsc ? 'ascending' : 'descending';
	}

	function toggleHeaderSort(key: PeerSortKey) {
		if ($peerSort.sortBy === key) {
			peerSort.toggleDir();
		} else {
			peerSort.setSortBy(key);
		}
	}

	function cycleNameHeaderSort() {
		const { sortBy, sortAsc } = $peerSort;

		if (sortBy !== 'name' && sortBy !== 'online') {
			peerSort.setSort('name', true);
			return;
		}

		if (sortBy === 'name' && sortAsc) {
			peerSort.setSort('name', false);
			return;
		}

		if (sortBy === 'name' && !sortAsc) {
			peerSort.setSort('online', false);
			return;
		}

		if (sortBy === 'online' && !sortAsc) {
			peerSort.setSort('online', true);
			return;
		}

		peerSort.setSort('name', true);
	}

	function isNameHeaderActive(): boolean {
		return $peerSort.sortBy === 'name' || $peerSort.sortBy === 'online';
	}

	function nameHeaderIndicator(): string {
		if ($peerSort.sortBy === 'name') return $peerSort.sortAsc ? 'A↑' : 'A↓';
		if ($peerSort.sortBy === 'online') return $peerSort.sortAsc ? '○↑' : '●↓';
		return '↕';
	}

	function nameAriaSort(): 'ascending' | 'descending' | 'none' {
		if ($peerSort.sortBy !== 'name' && $peerSort.sortBy !== 'online') return 'none';
		return $peerSort.sortAsc ? 'ascending' : 'descending';
	}
</script>

{#snippet SortHeader(label: string, key: PeerSortKey)}
	<button
		type="button"
		class="sort-header-btn"
		class:active={$peerSort.sortBy === key}
		onclick={() => toggleHeaderSort(key)}
		title={`Сортировать по колонке «${label}»`}
	>
		<span>{label}</span>
		<span class="sort-indicator" aria-hidden="true">
			{#if $peerSort.sortBy === key}
				{$peerSort.sortAsc ? '↑' : '↓'}
			{:else}
				↕
			{/if}
		</span>
	</button>
{/snippet}

<div class="desktop-peer-table">
	<div class="table-wrap">
		<table class="managed-peer-table">
			<thead>
				<tr>
					<th class="col-name" aria-sort={nameAriaSort()}>
						<button
							type="button"
							class="sort-header-btn name-sort-header"
							class:active={isNameHeaderActive()}
							onclick={cycleNameHeaderSort}
							title="Сортировка: имя A→Z, имя Z→A, онлайн сверху, офлайн сверху"
						>
							<span>Имя</span>
							<span class="sort-subhint" aria-hidden="true">/ онлайн</span>
							<span class="sort-indicator" aria-hidden="true">{nameHeaderIndicator()}</span>
						</button>
					</th>
					<th class="col-ip" aria-sort={ariaSort('ip')}>{@render SortHeader('IP', 'ip')}</th>
					<th class="col-endpoint" aria-sort={ariaSort('endpoint')}>{@render SortHeader('Endpoint', 'endpoint')}</th>
					<th class="col-traffic" aria-sort={ariaSort('traffic')}>{@render SortHeader('Трафик', 'traffic')}</th>
					<th class="col-handshake" aria-sort={ariaSort('handshake')}>{@render SortHeader('Handshake', 'handshake')}</th>
					<th class="col-actions">Действия</th>
				</tr>
			</thead>
			<tbody>
				{#each peers as peer (peer.publicKey)}
					{@const peerStats = getPeerStats(peer.publicKey)}
					{@const status = peerStatus(peer, peerStats)}
					{@const endpointValue = peerStats?.endpoint || '-'}
					{@const ep = splitEndpoint(endpointValue)}
					{@const hs = peerStats?.lastHandshake ? splitHandshakeLabel(formatRelativeTime(peerStats.lastHandshake)) : null}
					<tr class:peer-disabled={!peer.enabled}>
						<td
							class="peer-name-cell"
							role="button"
							tabindex="0"
							title={peer.enabled ? `Отключить «${peerName(peer)}»` : `Включить «${peerName(peer)}»`}
							onclick={(e) => {
								if (isInsideInlineToggle(e)) return;
								onTogglePeer(peer);
							}}
							onkeydown={(e) => {
								if (isInsideInlineToggle(e)) return;
								if (e.key === 'Enter' || e.key === ' ') {
									e.preventDefault();
									onTogglePeer(peer);
								}
							}}
						>
							<div class="peer-name-row">
								<span
									class="status-dot"
									class:dot-online={status === 'online'}
									class:dot-offline={status === 'offline'}
									class:dot-disabled={status === 'disabled'}
								></span>
								<span class="peer-name">{peerName(peer)}</span>
							</div>
							<div class="peer-status-sub">
								<div class="peer-inline-toggle">
									<Toggle checked={peer.enabled} onchange={() => onTogglePeer(peer)} size="sm" />
								</div>
								<span>{status}</span>
							</div>
						</td>
						<td class="col-ip">
							<button
								type="button"
								class="cell-copy mono tech-value"
								onclick={() => void copyCellValue(peer.tunnelIP, 'IP')}
								title={`Скопировать IP ${peer.tunnelIP}`}
							>
								{peer.tunnelIP}
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
								<span class="traffic-rx">RX: {formatBytes(peerStats?.rxBytes ?? 0)}</span>
								<span class="traffic-tx">TX: {formatBytes(peerStats?.txBytes ?? 0)}</span>
							</div>
						</td>
						<td class="col-handshake tech-value">
							{#if hs}
								<span class="handshake-text">{hs.main}</span>
								{#if hs.suffix}
									<span class="handshake-suffix">{" "}{hs.suffix}</span>
								{/if}
							{:else}
								-
							{/if}
						</td>
						<td class="col-actions">
							<div class="peer-actions">
								<button class="peer-action-btn" onclick={() => onOpenConf(peer)} title={`Скачать .conf для «${peerName(peer)}»`}>
									<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
										<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
										<polyline points="7 10 12 15 17 10" />
										<line x1="12" y1="15" x2="12" y2="3" />
									</svg>
								</button>
								<button class="peer-action-btn" onclick={() => onOpenEditPeer(peer)} title={`Редактировать «${peerName(peer)}»`}>
									<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
										<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7" />
										<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" />
									</svg>
								</button>
								<button
									class="peer-action-btn peer-action-btn-danger"
									class:peer-action-btn-confirm={confirmDeletePeerKey === peer.publicKey}
									onclick={() => onDeletePeerClick(peer)}
									title={confirmDeletePeerKey === peer.publicKey
										? `Нажмите ещё раз, чтобы удалить «${peerName(peer)}»`
										: `Удалить «${peerName(peer)}»`}
								>
									<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
										<line x1="18" y1="6" x2="6" y2="18" />
										<line x1="6" y1="6" x2="18" y2="18" />
									</svg>
								</button>
							</div>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
</div>

<div class="mobile-peer-list">
	{#each peers as peer (peer.publicKey)}
		{@const peerStats = getPeerStats(peer.publicKey)}
		{@const status = peerStatus(peer, peerStats)}
		{@const endpointValue = peerStats?.endpoint || '-'}
		{@const ep = splitEndpoint(endpointValue)}
		{@const hs = peerStats?.lastHandshake ? splitHandshakeLabel(formatRelativeTime(peerStats.lastHandshake)) : null}
		<article class="mobile-peer-card" class:peer-disabled={!peer.enabled} class:has-actions={true}>
			<div class="mobile-peer-main">
				<div class="mobile-peer-title-row">
					<span
						class="status-dot"
						class:dot-online={status === 'online'}
						class:dot-offline={status === 'offline'}
						class:dot-disabled={status === 'disabled'}
					></span>
					<span class="mobile-peer-name">{peerName(peer)}</span>
				</div>

				<div class="mobile-peer-status-row">
					<span class="mobile-peer-status-left">
						<span class="peer-inline-toggle">
							<Toggle checked={peer.enabled} onchange={() => onTogglePeer(peer)} size="sm" />
						</span>
						<span>{status}</span>
					</span>

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
						onclick={() => void copyCellValue(peer.tunnelIP, 'IP')}
						title={`Скопировать IP ${peer.tunnelIP}`}
					>
						<span class="mobile-label">IP</span> {peer.tunnelIP}
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

				<div class="mobile-peer-traffic-row mono tech-value">
					<span>RX: {formatBytes(peerStats?.rxBytes ?? 0)}</span>
					<span>TX: {formatBytes(peerStats?.txBytes ?? 0)}</span>
				</div>
			</div>

			<div class="mobile-peer-actions">
				<button class="peer-action-btn" onclick={() => onOpenConf(peer)} title={`Скачать .conf для «${peerName(peer)}»`}>
					<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
						<polyline points="7 10 12 15 17 10" />
						<line x1="12" y1="15" x2="12" y2="3" />
					</svg>
				</button>
				<button class="peer-action-btn" onclick={() => onOpenEditPeer(peer)} title={`Редактировать «${peerName(peer)}»`}>
					<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7" />
						<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" />
					</svg>
				</button>
				<button
					class="peer-action-btn peer-action-btn-danger"
					class:peer-action-btn-confirm={confirmDeletePeerKey === peer.publicKey}
					onclick={() => onDeletePeerClick(peer)}
					title={confirmDeletePeerKey === peer.publicKey
						? `Нажмите ещё раз, чтобы удалить «${peerName(peer)}»`
						: `Удалить «${peerName(peer)}»`}
				>
					<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<line x1="18" y1="6" x2="6" y2="18" />
						<line x1="6" y1="6" x2="18" y2="18" />
					</svg>
				</button>
			</div>
		</article>
	{/each}
</div>

<style>
	.table-wrap {
		overflow-x: auto;
	}

	.desktop-peer-table {
		display: block;
	}

	.mobile-peer-list {
		display: none;
	}

	.managed-peer-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 12px;
		table-layout: auto;
	}

	.managed-peer-table th {
		text-align: center;
		background: color-mix(in srgb, var(--accent) 16%, transparent);
		color: var(--accent);
		font-weight: 600;
		padding: 0.65rem 0.75rem;
		line-height: 1.2;
		border-bottom: 1px solid color-mix(in srgb, var(--accent) 30%, transparent);
		white-space: nowrap;
	}

	.managed-peer-table td {
		padding: 0.55rem 0.5rem;
		border-bottom: 1px solid var(--border);
		vertical-align: middle;
		transition: background-color 0.15s ease, box-shadow 0.15s ease;
	}

	.managed-peer-table tbody tr:hover td {
		background: color-mix(in srgb, var(--bg-hover) 70%, transparent);
		box-shadow: inset 2px 0 0 color-mix(in srgb, var(--accent) 35%, transparent);
	}

	.managed-peer-table tbody tr.peer-disabled:hover td {
		background: color-mix(in srgb, var(--bg-hover) 45%, transparent);
		box-shadow: inset 2px 0 0 color-mix(in srgb, var(--text-muted) 35%, transparent);
	}

	.peer-disabled {
		opacity: 0.5;
	}

	td.peer-name-cell {
		min-width: 140px;
		text-align: left;
		cursor: pointer;
	}

	.peer-name {
		font-weight: 500;
		font-size: 13px;
		line-height: 1.15;
		color: var(--text-primary);
		white-space: normal;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.peer-name-row {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		min-width: 0;
	}

	.peer-name-row .status-dot {
		flex-shrink: 0;
	}

	.peer-name-row .peer-name {
		min-width: 0;
	}

	.peer-status-sub {
		display: flex;
		align-items: center;
		justify-content: flex-start;
		width: 100%;
		gap: 0.25rem;
		font-size: 10px;
		color: var(--text-muted);
		line-height: 1;
		margin-top: 0.1rem;
	}

	.peer-inline-toggle {
		display: inline-flex;
		align-items: center;
		line-height: 1;
	}

	.peer-inline-toggle :global(.toggle-container) {
		gap: 0;
		padding: 0;
		min-height: 0;
	}

	.peer-inline-toggle :global(.toggle-container.sm .toggle-slider) {
		width: 24px;
		height: 14px;
	}

	.peer-inline-toggle :global(.toggle-container.sm .toggle-slider::before) {
		width: 10px;
		height: 10px;
		left: 2px;
		bottom: 2px;
	}

	.peer-inline-toggle :global(.toggle-container.sm input:checked ~ .toggle-slider::before) {
		transform: translateX(10px);
	}

	.peer-inline-toggle :global(.toggle-spinner-slot) {
		width: 0;
		margin: 0;
	}

	.mono {
		font-family: var(--font-mono, monospace);
	}

	.tech-value {
		font-size: 10px;
		line-height: 1.15;
	}

	.status-dot {
		display: inline-block;
		width: 6px;
		height: 6px;
		border-radius: 999px;
		margin-right: 0.35rem;
		vertical-align: middle;
	}

	.dot-online {
		background: var(--success, #22c55e);
		box-shadow: 0 0 3px var(--success, #22c55e);
	}

	.dot-offline {
		background: var(--text-muted);
	}

	.dot-disabled {
		background: var(--text-muted);
	}

	.traffic-cell {
		display: flex;
		flex-direction: column;
		gap: 0;
		line-height: 1.05;
		white-space: nowrap;
	}

	.col-name {
		width: 24%;
	}

	.col-ip {
		width: 12%;
		white-space: nowrap;
	}

	.col-endpoint {
		width: auto;
		max-width: 180px;
	}

	.endpoint-text {
		display: block;
		white-space: normal;
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.15;
	}

	.endpoint-port {
		display: block;
		white-space: nowrap;
		line-height: 1.15;
	}

	.col-traffic {
		width: 14%;
	}

	.col-handshake {
		width: 12%;
		white-space: nowrap;
	}

	.handshake-text,
	.handshake-suffix {
		display: block;
	}

	.col-actions {
		width: 11%;
		white-space: nowrap;
	}

	td.col-ip,
	td.col-endpoint,
	td.col-traffic,
	td.col-handshake {
		text-align: center;
	}

	td.col-actions {
		text-align: right;
	}

	.peer-actions {
		display: inline-flex;
		align-items: center;
		justify-content: flex-end;
		width: 100%;
		gap: 0.375rem;
	}

	.peer-action-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0.375rem;
		background: transparent;
		border: none;
		color: var(--text-secondary);
		cursor: pointer;
		border-radius: var(--radius-sm);
		transition: color 0.15s ease, background 0.15s ease;
	}

	.peer-action-btn:hover {
		background: var(--bg-hover);
		color: var(--text-primary);
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

	.cell-copy {
		padding: 0;
		border: 0;
		background: none;
		color: inherit;
		cursor: pointer;
		text-align: left;
	}

	.cell-copy:hover {
		text-decoration: underline;
		color: var(--text-primary);
	}

	.sort-header-btn {
		width: 100%;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 0.3rem;
		padding: 0;
		border: 0;
		background: transparent;
		color: inherit;
		font: inherit;
		font-weight: inherit;
		text-transform: inherit;
		letter-spacing: inherit;
		cursor: pointer;
	}

	.sort-header-btn:hover {
		color: var(--text-primary);
	}

	.sort-header-btn.active {
		color: var(--accent);
	}

	.name-sort-header {
		justify-content: center;
	}

	.sort-subhint {
		opacity: 0.65;
		font-size: 0.85em;
		font-weight: inherit;
		text-transform: none;
		letter-spacing: normal;
	}

	.sort-header-btn.active .sort-subhint {
		opacity: 0.85;
	}

	.sort-indicator {
		min-width: 1.8em;
		text-align: left;
		flex: 0 0 auto;
		opacity: 0.65;
		font-size: 0.8em;
	}

	.sort-header-btn.active .sort-indicator {
		opacity: 1;
	}

	@media (max-width: 640px) {
		.desktop-peer-table {
			display: none;
		}

		.mobile-peer-list {
			display: flex;
			flex-direction: column;
			gap: 0.4rem;
		}

		.mobile-peer-card {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			gap: 0.5rem;
			padding: 0.55rem 0.65rem;
			border: 1px solid var(--border);
			border-radius: var(--radius);
			background: var(--bg-primary);
		}

		.mobile-peer-card:not(.has-actions) {
			grid-template-columns: minmax(0, 1fr);
		}

		.mobile-peer-main {
			min-width: 0;
		}

		.mobile-peer-title-row {
			display: flex;
			align-items: center;
			gap: 0.3rem;
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
			justify-content: flex-start;
			align-items: center;
			gap: 0.35rem;
			margin-top: 0.1rem;
			font-size: 10px;
			line-height: 1;
			color: var(--text-muted);
		}

		.mobile-peer-status-left {
			display: inline-flex;
			align-items: center;
			gap: 0.25rem;
			min-width: 0;
		}

		.mobile-peer-handshake {
			text-align: left;
			white-space: nowrap;
			color: var(--text-muted);
			margin-left: 0.1rem;
		}

		.mobile-peer-net-row {
			display: grid;
			grid-template-columns: minmax(0, 0.9fr) minmax(0, 1.25fr);
			gap: 0.5rem;
			margin-top: 0.35rem;
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
			margin-top: 0.25rem;
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
			flex-direction: row;
			align-items: flex-end;
			justify-content: flex-end;
			gap: 0.25rem;
			align-self: start;
			flex-wrap: nowrap;
		}

		.mobile-peer-actions .peer-action-btn {
			width: 32px;
			height: 32px;
			padding: 0;
			border: 1px solid var(--border);
			border-radius: var(--radius-sm);
			background: var(--bg-secondary);
		}

		@media (max-width: 360px) {
			.mobile-peer-actions {
				display: grid;
				grid-template-columns: repeat(2, 32px);
				grid-auto-rows: 32px;
				gap: 0.25rem;
				justify-content: end;
			}

			.mobile-peer-actions .peer-action-btn:nth-child(1) {
				grid-column: 2;
				grid-row: 1;
			}

			.mobile-peer-actions .peer-action-btn:nth-child(2) {
				grid-column: 1;
				grid-row: 2;
			}

			.mobile-peer-actions .peer-action-btn:nth-child(3) {
				grid-column: 2;
				grid-row: 2;
			}
		}

		.peer-disabled {
			opacity: 0.6;
		}
	}
</style>
