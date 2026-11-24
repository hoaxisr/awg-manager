<script lang="ts">
	import { m } from '$lib/i18n';
	import type { FircMonitorSnapshot } from '$lib/types/firc';
	import { Badge, StatusDot } from '$lib/components/ui';
	import { formatBytes, formatTime, formatOutboundLabel } from './fircMonitorFormat';

	interface Props {
		view: 'sources' | 'details';
		snapshot: FircMonitorSnapshot | null;
	}

	let { view, snapshot }: Props = $props();

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
											<span class="bytes-meta">{m.firc_monitor_devices({ n: srv.activeClients })}</span>
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
											<span class="bytes-meta">{m.firc_monitor_devices({ n: grp.activeClients })}</span>
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
</style>
