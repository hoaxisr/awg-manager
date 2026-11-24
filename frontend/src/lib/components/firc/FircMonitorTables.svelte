<script lang="ts">
	import { m } from '$lib/i18n';
	import type { FircMonitorClientStat, FircMonitorSnapshot } from '$lib/types/firc';
	import { Badge, StatusDot } from '$lib/components/ui';
	import { ChevronDown, ChevronRight, Monitor } from 'lucide-svelte';
	import { formatBytes, formatTime, formatOutboundLabel } from './fircMonitorFormat';

	interface Props {
		view: 'sources' | 'details';
		snapshot: FircMonitorSnapshot | null;
		onFilterClientEvents?: (ip: string) => void;
	}

	let { view, snapshot, onFilterClientEvents }: Props = $props();

	let showGlobalClients = $state(false);
	let expandedGroupClients = $state<Record<string, boolean>>({});
	let expandedServerClients = $state<Record<string, boolean>>({});

	function toggleGroupClients(id: string) {
		expandedGroupClients = { ...expandedGroupClients, [id]: !expandedGroupClients[id] };
	}

	function toggleServerClients(id: string) {
		expandedServerClients = { ...expandedServerClients, [id]: !expandedServerClients[id] };
	}

	let totalTxBytes = $derived.by(() => {
		if (snapshot?.totalTxBytes !== undefined) return snapshot.totalTxBytes;
		return (snapshot?.groups ?? []).reduce((acc, g) => acc + (g.connTxBytes || 0), 0);
	});

	let totalRxBytes = $derived.by(() => {
		if (snapshot?.totalRxBytes !== undefined) return snapshot.totalRxBytes;
		return (snapshot?.groups ?? []).reduce((acc, g) => acc + (g.connRxBytes || 0), 0);
	});

	let totalRulePackets = $derived.by(() =>
		(snapshot?.groups ?? []).reduce((acc, g) => acc + (g.matchedPackets || 0), 0),
	);

	let totalRuleBytes = $derived.by(() =>
		(snapshot?.groups ?? []).reduce((acc, g) => acc + (g.matchedBytes || 0), 0),
	);
</script>

<!-- Grid: 1) Inbound Servers & LAN  2) FIRC Groups -->
<div class="mon-grid-2">
	<!-- Section 1: Inbound VPN Servers & LAN -->
	{#if view === 'sources'}
	<div class="mon-card">
		<div class="mon-card-head">
			<span>{m.firc_monitor_sources_title()}</span>
			<span class="mon-card-sub">{m.firc_monitor_sources_sub()}</span>
		</div>
		<div class="mon-table-wrap">
			<table class="mon-table">
				<thead>
					<tr>
						<th>{m.firc_monitor_th_source()}</th>
						<th>{m.firc_monitor_th_firc_status()}</th>
						<th>{m.firc_monitor_th_dns_queries()}</th>
						<th>{m.firc_monitor_th_sessions_traffic()}</th>
						<th>{m.firc_monitor_th_last_match()}</th>
					</tr>
				</thead>
				<tbody>
					{#each snapshot?.servers ?? [] as srv (srv.id)}
						{@const hasTraffic = srv.activeConns > 0 || srv.matchedQueries > 0}
						<tr class:row-active={hasTraffic}>
							<td>
								<div class="srv-cell">
									<StatusDot
										variant={hasTraffic ? 'success' : srv.routingEnabled && srv.running ? 'info' : 'muted'}
										size="sm"
										pulse={srv.activeConns > 0}
									/>
									<div>
										<div class="srv-name">{srv.name}</div>
										<div class="srv-detail mono">{srv.detail}</div>
									</div>
								</div>
							</td>
							<td>
								{#if !srv.routingEnabled}
									<Badge variant="muted" size="xs">{m.firc_monitor_src_off()}</Badge>
								{:else if hasTraffic}
									<Badge variant="success" size="xs">{srv.activeConns > 0 ? m.firc_monitor_src_has_conns() : m.firc_monitor_src_has_dns()}</Badge>
								{:else if srv.running}
									<Badge variant="info" size="xs">{m.firc_monitor_src_waiting()}</Badge>
								{:else}
									<Badge variant="muted" size="xs">{m.firc_monitor_src_stopped()}</Badge>
								{/if}
							</td>
							<td class="mono">
								<div>{m.firc_monitor_packets({ n: srv.dnsPackets })}</div>
								{#if srv.matchedQueries > 0}
									<div class="bytes-meta">{m.firc_monitor_by_rules({ n: srv.matchedQueries })}</div>
								{/if}
							</td>
							<td class="mono">
								{#if srv.activeConns > 0 || srv.txBytes > 0 || srv.rxBytes > 0}
									<div>
										<span class="conn-pill">{m.firc_monitor_sessions({ n: srv.activeConns })}</span>
										{#if srv.activeClients}
											{#if srv.clients && srv.clients.length > 0}
												<button
													type="button"
													class="mon-clients-trigger"
													aria-expanded={Boolean(expandedServerClients[srv.id])}
													onclick={() => toggleServerClients(srv.id)}
												>
													<span>{m.firc_monitor_devices({ n: srv.activeClients })}</span>
													{#if expandedServerClients[srv.id]}<ChevronDown size={11} />{:else}<ChevronRight size={11} />{/if}
												</button>
											{:else}
												<span class="bytes-meta">{m.firc_monitor_devices({ n: srv.activeClients })}</span>
											{/if}
										{/if}
									</div>
									<div class="bytes-meta">
										↓{formatBytes(srv.rxBytes)} · ↑{formatBytes(srv.txBytes)}
									</div>
								{:else}
									<span class="muted-meta">{m.firc_monitor_sessions({ n: 0 })}</span>
								{/if}
							</td>
							<td>
								{#if srv.lastDomain}
									<div class="last-match">
										<span class="mono domain-text">{srv.lastDomain}</span>
										{#if srv.lastGroup}
											<Badge variant="accent" size="xs">{srv.lastGroup}</Badge>
										{/if}
										{#if srv.lastOutbound}
											<Badge variant="info" size="xs" mono>{formatOutboundLabel(srv.lastOutbound, srv.lastOutboundLabel)}</Badge>
										{/if}
										<span class="muted-meta">{formatTime(srv.lastAt)}</span>
									</div>
								{:else}
									<span class="muted-meta">—</span>
								{/if}
							</td>
						</tr>
						{#if expandedServerClients[srv.id] && srv.clients && srv.clients.length > 0}
							<tr class="mon-clients-subrow" data-testid="firc-mon-server-clients-{srv.id}">
								<td colspan="5">
									<div class="mon-clients-box">
										<div class="mon-clients-box-head">{m.firc_monitor_clients_source_title({ source: srv.name })}</div>
										{@render clientsList(srv.clients, true)}
									</div>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>
	</div>
	{/if}

	<!-- Section 2: FIRC Groups Live Counters -->
	{#if view === 'details'}
	<div class="counters-kpi-grid">
		<div class="kpi-card">
			<div class="kpi-label">{m.firc_monitor_kpi_conns()}</div>
			<div class="kpi-value mono">{snapshot?.totalConns ?? 0} <span class="kpi-unit">{m.firc_monitor_unit_sessions()}</span></div>
			<div class="kpi-hint">
				{snapshot?.totalClients ? m.firc_monitor_kpi_conns_clients({ n: snapshot.totalClients }) : m.firc_monitor_kpi_conns_hint()}
			</div>
			{#if snapshot?.clients && snapshot.clients.length > 0}
				<button
					type="button"
					class="mon-clients-toggle"
					aria-expanded={showGlobalClients}
					aria-label={m.firc_monitor_clients_toggle_aria()}
					onclick={() => (showGlobalClients = !showGlobalClients)}
				>
					{#if showGlobalClients}
						<ChevronDown size={12} />
						<span>{m.firc_monitor_clients_hide()}</span>
					{:else}
						<ChevronRight size={12} />
						<span>{m.firc_monitor_clients_show({ count: snapshot.clients.length })}</span>
					{/if}
				</button>
			{/if}
		</div>
		<div class="kpi-card">
			<div class="kpi-label">{m.firc_monitor_kpi_volume()}</div>
			<div class="kpi-value mono">{formatBytes(totalRxBytes + totalTxBytes)}</div>
			<div class="kpi-hint mono">
				{m.firc_monitor_kpi_volume_hint({ rx: formatBytes(totalRxBytes), tx: formatBytes(totalTxBytes) })}
			</div>
		</div>
		<div class="kpi-card">
			<div class="kpi-label">{m.firc_monitor_kpi_dns()}</div>
			<div class="kpi-value mono">{snapshot?.totalMatched ?? 0} <span class="kpi-unit">{m.firc_monitor_unit_queries()}</span></div>
			<div class="kpi-hint">
				{m.firc_monitor_kpi_dns_hint({ n: snapshot?.totalDns ?? 0 })}
			</div>
		</div>
		<div class="kpi-card">
			<div class="kpi-label">{m.firc_monitor_kpi_marks()}</div>
			<div class="kpi-value mono">{totalRulePackets} <span class="kpi-unit">{m.firc_monitor_unit_packets()}</span></div>
			<div class="kpi-hint">
				{m.firc_monitor_kpi_marks_hint({ bytes: formatBytes(totalRuleBytes) })}
			</div>
		</div>
	</div>

	{#if showGlobalClients && snapshot?.clients && snapshot.clients.length > 0}
		<div class="mon-card" data-testid="firc-mon-global-clients">
			<div class="mon-card-head">
				<span>{m.firc_monitor_active_clients_title()}</span>
				<span class="mon-card-sub">{snapshot.clients.length}</span>
			</div>
			<div class="mon-clients-panel-body">
				{@render clientsList(snapshot.clients, true)}
			</div>
		</div>
	{/if}

	<div class="mon-card">
		<div class="mon-card-head">
			<span>{m.firc_monitor_groups_title()}</span>
			<span class="mon-card-sub">{m.firc_monitor_groups_sub()}</span>
		</div>
		<div class="mon-table-wrap">
			<table class="mon-table">
				<thead>
					<tr>
						<th>{m.firc_monitor_th_group_rules()}</th>
						<th>{m.firc_monitor_th_outbound()}</th>
						<th>{m.firc_monitor_th_open_now()}</th>
						<th>{m.firc_monitor_th_dns_domains()}</th>
						<th>{m.firc_monitor_th_rule_marks()}</th>
					</tr>
				</thead>
				<tbody>
					{#each snapshot?.groups ?? [] as grp (grp.id)}
						{@const hasActivity = grp.matchedPackets > 0 || grp.activeConns > 0 || grp.matchedQueries > 0}
						<tr class:row-active={hasActivity}>
							<td>
								<div class="srv-cell">
									<StatusDot
										variant={grp.live ? 'success' : 'warning'}
										size="sm"
										pulse={grp.activeConns > 0}
									/>
									<div>
										<div class="srv-name">{grp.name}</div>
										{#if grp.fakeSubnet}
											<div class="srv-detail mono">FakeIP: {grp.fakeSubnet}</div>
										{/if}
										{#if grp.rulesTotal || grp.subnetRules}
											<div class="srv-detail">
												{grp.rulesTotal ? m.firc_monitor_rules_count({ n: grp.rulesTotal }) : ''}{grp.subnetRules ? `${grp.rulesTotal ? ' · ' : ''}${m.firc_monitor_subnets_count({ n: grp.subnetRules })}` : ''}
											</div>
										{/if}
									</div>
								</div>
							</td>
							<td>
								<div class="outbound-cell">
									{#if grp.activeOutboundLabel && grp.activeOutboundLabel !== grp.activeOutbound}
										<div class="srv-name">{grp.activeOutboundLabel}</div>
									{/if}
									<Badge variant={grp.live ? 'info' : 'warning'} size="xs" mono>
										{grp.activeOutbound || '—'}
									</Badge>
								</div>
							</td>
							<td class="mono">
								{#if grp.activeConns > 0}
									<div>
										<span class="conn-pill">{m.firc_monitor_sessions({ n: grp.activeConns })}</span>
										{#if grp.activeClients}
											{#if grp.clients && grp.clients.length > 0}
												<button
													type="button"
													class="mon-clients-trigger"
													aria-expanded={Boolean(expandedGroupClients[grp.id])}
													onclick={() => toggleGroupClients(grp.id)}
												>
													<span>{m.firc_monitor_devices({ n: grp.activeClients })}</span>
													{#if expandedGroupClients[grp.id]}<ChevronDown size={11} />{:else}<ChevronRight size={11} />{/if}
												</button>
											{:else}
												<span class="bytes-meta">{m.firc_monitor_devices({ n: grp.activeClients })}</span>
											{/if}
										{/if}
									</div>
									<div class="bytes-meta">{m.firc_monitor_received()} <strong>{formatBytes(grp.connRxBytes)}</strong></div>
									<div class="bytes-meta">{m.firc_monitor_sent()} <strong>{formatBytes(grp.connTxBytes)}</strong></div>
								{:else}
									<span class="muted-meta">{m.firc_monitor_no_sessions()}</span>
								{/if}
							</td>
							<td>
								{#if grp.matchedQueries > 0}
									<div class="mono">
										<strong>{grp.matchedQueries}</strong> {m.firc_monitor_unit_queries()}{grp.uniqueDomains ? m.firc_monitor_unique_domains({ n: grp.uniqueDomains }) : ''}
									</div>
									{#if grp.lastDomain}
										<div class="srv-detail mono">{m.firc_monitor_last_domain({ domain: grp.lastDomain, time: formatTime(grp.lastAt) })}</div>
									{/if}
									{#if grp.topDomains && grp.topDomains.length > 0}
										<div class="top-domains-list">
											{#each grp.topDomains as td (td.domain)}
												<span class="top-domain-chip mono">{td.domain} ×{td.queries}</span>
											{/each}
										</div>
									{/if}
								{:else if (grp.subnetRules || grp.activeConns > 0) && (grp.activeConns > 0 || grp.matchedPackets > 0)}
									<div class="srv-detail">{m.firc_monitor_by_subnets()}</div>
									<div class="muted-meta">{m.firc_monitor_direct_ip()}</div>
								{:else}
									<div class="mono muted-meta">{m.firc_monitor_zero_queries()}</div>
								{/if}
							</td>
							<td class="mono">
								<div>{m.firc_monitor_packets({ n: grp.matchedPackets })}</div>
								<div class="bytes-meta">↑ {formatBytes(grp.matchedBytes)}</div>
								<div class="muted-meta">{m.firc_monitor_out_packets()}</div>
							</td>
						</tr>
						{#if expandedGroupClients[grp.id] && grp.clients && grp.clients.length > 0}
							<tr class="mon-clients-subrow" data-testid="firc-mon-group-clients-{grp.id}">
								<td colspan="5">
									<div class="mon-clients-box">
										<div class="mon-clients-box-head">{m.firc_monitor_clients_group_title({ group: grp.name })}</div>
										{@render clientsList(grp.clients, false)}
									</div>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>
	</div>

	<div class="counters-legend">
		<div><strong>{m.firc_monitor_th_open_now()}</strong> {m.firc_monitor_legend_open_a()}<code>{m.firc_monitor_code_received()}</code> {m.firc_monitor_legend_open_b()} <code>{m.firc_monitor_code_sent()}</code>{m.firc_monitor_legend_open_c()} <code>conntrack</code>.</div>
		<div><strong>{m.firc_monitor_th_dns_domains()}</strong> {m.firc_monitor_legend_dns()}</div>
		<div><strong>{m.firc_monitor_th_rule_marks()}</strong> {m.firc_monitor_legend_marks_a()} <code>iptables mangle</code>{m.firc_monitor_legend_marks_b()}</div>
	</div>
	{/if}
</div>

{#snippet clientsList(clients: FircMonitorClientStat[], showGroups: boolean)}
	<div class="mon-clients-list">
		{#each clients as c (c.ip)}
			<div class="mon-client-row">
				<div class="mon-client-main">
					<Monitor size={13} class="mon-client-icon" />
					<div class="mon-client-info">
						<div class="mon-client-title">
							<span class="mon-client-name">{c.name || c.ip}</span>
							{#if c.name}
								<span class="mon-client-ip mono">{c.ip}</span>
							{/if}
							{#if c.sourceName && c.sourceId !== 'lan_br0'}
								<Badge variant="accent" size="xs">{c.sourceName}</Badge>
							{/if}
							{#if c.policy}
								<Badge variant="default" size="xs">{c.policy}</Badge>
							{/if}
						</div>
						{#if c.mac || (showGroups && c.groups && c.groups.length > 0)}
							<div class="mon-client-meta">
								{#if c.mac}
									<span class="mono">{c.mac.toUpperCase()}</span>
								{/if}
								{#if showGroups && c.groups && c.groups.length > 0}
									{#each c.groups as gName (gName)}
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
					{#if onFilterClientEvents}
						<button
							type="button"
							class="mon-client-events-btn"
							title={m.firc_monitor_clients_filter_events_title({ ip: c.ip })}
							onclick={() => onFilterClientEvents(c.ip)}
						>
							{m.firc_monitor_clients_filter_events()}
						</button>
					{/if}
				</div>
			</div>
		{/each}
	</div>
{/snippet}

<style>
	.counters-kpi-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: 0.6rem;
		min-width: 0;
	}
	.kpi-card {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		padding: 0.65rem 0.8rem;
		border-radius: 8px;
		background: var(--bg-primary, rgba(0, 0, 0, 0.14));
		border: 1px solid var(--border);
		min-width: 0;
	}
	.kpi-label {
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--text-secondary);
	}
	.kpi-value {
		font-size: 1.05rem;
		font-weight: 700;
		color: var(--text-primary);
	}
	.kpi-unit {
		font-size: 0.78rem;
		font-weight: 500;
		color: var(--text-secondary);
	}
	.kpi-hint {
		font-size: 0.7rem;
		color: var(--text-muted);
		line-height: 1.35;
	}
	.outbound-cell {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.2rem;
	}
	.top-domains-list {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
		margin-top: 0.25rem;
	}
	.top-domain-chip {
		display: inline-block;
		padding: 1px 6px;
		border-radius: 4px;
		font-size: 0.68rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-secondary);
		max-width: 220px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.counters-legend {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		padding: 0.65rem 0.8rem;
		border-radius: 8px;
		background: var(--bg-primary, rgba(0, 0, 0, 0.1));
		border: 1px solid var(--border);
		font-size: 0.74rem;
		color: var(--text-muted);
		line-height: 1.4;
	}
	.counters-legend strong {
		color: var(--text-secondary);
	}
	.counters-legend code {
		font-family: var(--font-mono);
		font-size: 0.7rem;
		color: var(--text-secondary);
	}

	.mon-grid-2 {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: 0.75rem;
		min-width: 0;
		max-width: 100%;
	}

	.mon-card {
		display: flex;
		flex-direction: column;
		border-radius: 8px;
		background: var(--bg-primary, rgba(0, 0, 0, 0.14));
		border: 1px solid var(--border);
		overflow: hidden;
		min-width: 0;
		max-width: 100%;
	}

	.mon-card-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.55rem 0.75rem;
		border-bottom: 1px solid var(--border);
		font-size: 0.8rem;
		font-weight: 600;
		color: var(--text-primary);
		flex-wrap: wrap;
	}

	.mon-card-sub {
		font-size: 0.72rem;
		font-weight: 400;
		color: var(--text-muted);
	}

	.mon-table-wrap {
		overflow-x: auto;
	}

	.mon-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.78rem;
	}

	.mon-table th {
		text-align: left;
		padding: 0.45rem 0.65rem;
		font-size: 0.7rem;
		font-weight: 600;
		text-transform: uppercase;
		color: var(--text-muted);
		border-bottom: 1px solid var(--border);
		white-space: nowrap;
	}

	.mon-table td {
		padding: 0.48rem 0.65rem;
		border-bottom: 1px solid color-mix(in srgb, var(--border) 60%, transparent);
		vertical-align: middle;
	}

	.mon-table tr:last-child td {
		border-bottom: none;
	}

	.mon-table tr.row-active {
		background: color-mix(in srgb, var(--color-success, #9ece6a) 5%, transparent);
	}

	.srv-cell {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.srv-name {
		font-weight: 500;
		color: var(--text-primary);
	}

	.srv-detail {
		font-size: 0.7rem;
		color: var(--text-muted);
	}

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

	.bytes-meta,
	.muted-meta {
		font-size: 0.71rem;
		color: var(--text-muted);
	}

	.last-match {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		flex-wrap: wrap;
	}

	.domain-text {
		color: var(--text-primary);
		font-weight: 500;
	}

	.mon-clients-toggle,
	.mon-clients-trigger {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		border: 1px solid color-mix(in srgb, var(--accent) 40%, var(--border));
		border-radius: 5px;
		background: color-mix(in srgb, var(--accent) 10%, transparent);
		color: var(--text-primary);
		font: inherit;
		font-size: 0.71rem;
		font-weight: 500;
		cursor: pointer;
		padding: 1px 6px;
		transition: background 0.15s ease, border-color 0.15s ease;
	}

	.mon-clients-toggle {
		align-self: flex-start;
		margin-top: 0.2rem;
		padding: 2px 8px;
	}

	.mon-clients-trigger {
		margin-left: 0.25rem;
	}

	.mon-clients-toggle:hover,
	.mon-clients-trigger:hover {
		background: color-mix(in srgb, var(--accent) 18%, transparent);
		border-color: var(--accent);
	}

	.mon-clients-panel-body {
		padding: 0.55rem 0.75rem;
	}

	.mon-table tr.mon-clients-subrow td {
		padding: 0.35rem 0.65rem 0.6rem;
		background: color-mix(in srgb, var(--bg-secondary) 70%, transparent);
	}

	.mon-clients-box {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		padding: 0.5rem 0.65rem;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary, rgba(0, 0, 0, 0.12));
	}

	.mon-clients-box-head {
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--text-secondary);
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
