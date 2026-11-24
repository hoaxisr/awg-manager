<script lang="ts">
	import { m } from '$lib/i18n';
	import type { FircHost, FircMonitorClientStat, FircPolicy } from '$lib/types/firc';
	import { Badge } from '$lib/components/ui';
	import { hostsByIp, policyLabel } from '$lib/utils/fircPolicyDevices';
	import { Monitor } from 'lucide-svelte';
	import { formatBytes } from './fircMonitorFormat';

	interface Props {
		/** undefined — списков в последнем снимке ещё нет. */
		clients?: FircMonitorClientStat[];
		/** Точное число клиентов: бэкенд обрезает список. */
		total: number;
		hosts: FircHost[];
		policies: FircPolicy[];
		showGroups: boolean;
		onFilterClientEvents: (ip: string) => void;
	}

	let { clients, total, hosts, policies, showGroups, onFilterClientEvents }: Props = $props();

	let byIp = $derived(hostsByIp(hosts));
</script>

{#if !clients}
	<div class="mon-clients-note">{m.firc_monitor_loading_data()}</div>
{:else}
	<div class="mon-clients-list">
		{#each clients as c (c.ip)}
			{@const host = byIp.get(c.ip)}
			<div class="mon-client-row">
				<div class="mon-client-main">
					<Monitor size={13} class="mon-client-icon" />
					<div class="mon-client-info">
						<div class="mon-client-title">
							<span class="mon-client-name">{host?.name || c.ip}</span>
							{#if host?.name}
								<span class="mon-client-ip mono">{c.ip}</span>
							{/if}
							{#if c.sourceName && c.sourceId !== 'lan_br0'}
								<Badge variant="accent" size="xs">{c.sourceName}</Badge>
							{/if}
							{#if host?.policy}
								<Badge variant="default" size="xs">{policyLabel(host.policy, policies)}</Badge>
							{/if}
						</div>
						{#if host?.mac || (showGroups && c.groups)}
							<div class="mon-client-meta">
								{#if host?.mac}
									<span class="mono">{host.mac.toUpperCase()}</span>
								{/if}
								{#if showGroups}
									{#each c.groups ?? [] as gName (gName)}
										<Badge variant="info" size="xs">{gName}</Badge>
									{/each}
								{/if}
							</div>
						{/if}
					</div>
				</div>
				<div class="mon-client-stats">
					<span class="conn-pill mono">{m.firc_monitor_clients_col_conns({ count: c.conns })}</span>
					<span class="mon-client-traffic mono">↓{formatBytes(c.rxBytes)} · ↑{formatBytes(c.txBytes)}</span>
					<button
						type="button"
						class="mon-client-events-btn"
						title={m.firc_monitor_clients_filter_events_title({ ip: c.ip })}
						onclick={() => onFilterClientEvents(c.ip)}
					>
						{m.firc_monitor_clients_filter_events()}
					</button>
				</div>
			</div>
		{/each}
	</div>
	{#if clients.length < total}
		<div class="mon-clients-note">{m.firc_monitor_clients_truncated({ shown: clients.length, total })}</div>
	{/if}
{/if}

<style>
	.mono {
		font-family: var(--font-mono);
	}

	.conn-pill {
		display: inline-block;
		padding: 1px 6px;
		border-radius: 4px;
		font-size: 0.72rem;
		background: color-mix(in srgb, var(--color-success, #9ece6a) 15%, transparent);
		color: var(--color-success, #9ece6a);
	}

	.mon-clients-note {
		font-size: 0.71rem;
		color: var(--text-muted);
	}

	.mon-clients-list {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.mon-client-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.6rem;
		flex-wrap: wrap;
		padding: 0.35rem 0.5rem;
		border-radius: 5px;
		border: 1px solid color-mix(in srgb, var(--border) 65%, transparent);
		background: color-mix(in srgb, var(--bg-secondary) 65%, transparent);
	}

	.mon-client-main {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		min-width: 0;
		flex: 1 1 220px;
	}

	:global(.mon-client-icon) {
		color: var(--text-muted);
		flex-shrink: 0;
	}

	.mon-client-info {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		min-width: 0;
	}

	.mon-client-title {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		flex-wrap: wrap;
	}

	.mon-client-name {
		font-size: 0.76rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.mon-client-ip {
		font-size: 0.71rem;
		color: var(--text-muted);
	}

	.mon-client-meta {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		flex-wrap: wrap;
		font-size: 0.69rem;
		color: var(--text-muted);
	}

	.mon-client-stats {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		flex-wrap: wrap;
	}

	.mon-client-traffic {
		font-size: 0.71rem;
		color: var(--text-secondary);
	}

	.mon-client-events-btn {
		border: 1px solid var(--border);
		border-radius: 4px;
		background: transparent;
		color: var(--text-secondary);
		font: inherit;
		font-size: 0.69rem;
		padding: 1px 6px;
		cursor: pointer;
	}

	.mon-client-events-btn:hover {
		border-color: var(--accent);
		color: var(--text-primary);
		background: color-mix(in srgb, var(--accent) 12%, transparent);
	}
</style>
