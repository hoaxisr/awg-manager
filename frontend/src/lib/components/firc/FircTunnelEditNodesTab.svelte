<script lang="ts">
	import { m } from '$lib/i18n';
	import type { FircTunnelFilteredNode, FircTunnelProbeRow } from '$lib/types/firc';
	import { formatFircSkipReason } from '$lib/utils/fircDiagnostics';
	import { Button, Badge, Toggle, IconButton } from '$lib/components/ui';
	import { Activity, ArrowUp, ArrowDown, Search, LoaderCircle } from 'lucide-svelte';
	import { totalProbeLatencyMs, type DisplayNode } from './fircTunnelEdit';

	interface Props {
		exclude: string[];
		nodes: DisplayNode[];
		filteredNodes: FircTunnelFilteredNode[];
		tunProbes: Record<string, FircTunnelProbeRow>;
		previewError: string;
		busyAction: '' | 'probe' | 'refresh' | 'restart';
		probingNodeKey: string | null;
		probeProgress: { done: number; total: number } | null;
		nodeQuery: string;
		filteredOpen: boolean;
		hasSources?: boolean;
		isDirty?: boolean;
		enabled?: boolean;
		uplinkKind?: string;
		subFetching?: boolean;
		saving?: boolean;
		onsave?: () => void;
		onprobe: () => void;
		onprobenode: (key: string) => void;
		onsort: () => void;
		onincludeall: () => void;
		onmove: (index: number, delta: -1 | 1) => void;
		ontoggle: (key: string, included: boolean) => void;
	}

	let {
		exclude,
		nodes,
		filteredNodes,
		tunProbes,
		previewError,
		busyAction,
		probingNodeKey,
		probeProgress,
		nodeQuery = $bindable(),
		filteredOpen = $bindable(),
		hasSources = false,
		isDirty = false,
		enabled = true,
		uplinkKind = 'auto',
		subFetching = false,
		saving = false,
		onsave,
		onprobe,
		onprobenode,
		onsort,
		onincludeall,
		onmove,
		ontoggle,
	}: Props = $props();

	const q = $derived(nodeQuery.trim().toLowerCase());
</script>

<div class="subtab-panel">
	<div class="nodes-toolbar">
		<div class="search-box">
			<Search size={13} />
			<input
				type="text"
				placeholder={m.firc_tunnel_edit_search_placeholder()}
				bind:value={nodeQuery}
			/>
		</div>
		<div class="nodes-toolbar-actions">
			<Button
				variant="secondary"
				size="sm"
				loading={busyAction === 'probe'}
				disabled={Boolean(busyAction || probingNodeKey)}
				onclick={onprobe}
			>
				{m.firc_tunnel_edit_probe_all()}{#if probeProgress}{' '}({probeProgress.done}/{probeProgress.total}){/if}
			</Button>
			<Button variant="secondary" size="sm" onclick={onsort}>
				{m.firc_tunnel_edit_sort_by_ping()}
			</Button>
			<Button variant="ghost" size="sm" onclick={onincludeall}>
				{m.firc_devices_enable_all()}
			</Button>
		</div>
	</div>

	{#if previewError}
		<div class="preview-err">{previewError}</div>
	{/if}

	{#if nodes.length === 0}
		<div class="sub-empty">
			{#if filteredNodes.length > 0}
				{m.firc_filter_all_nodes_hidden()}
			{:else if hasSources && subFetching}
				<span class="sub-empty-loading">
					<LoaderCircle size={14} class="animate-spin" />
					<span>{m.firc_reason_sub_fetching()}</span>
				</span>
			{:else if hasSources && !enabled && uplinkKind !== 'auto'}
				{m.firc_tunnel_edit_nodes_enable_uplink_to_load()}
			{:else if hasSources && isDirty}
				<div class="sub-empty-stack">
					<span>{m.firc_tunnel_edit_nodes_save_to_load()}</span>
					{#if onsave}
						<Button
							variant="primary"
							size="sm"
							loading={saving}
							disabled={busyAction === 'refresh'}
							onclick={onsave}
						>
							{m.firc_tunnel_edit_save_and_load()}
						</Button>
					{/if}
				</div>
			{:else}
				{m.firc_tunnel_edit_nodes_empty()}
			{/if}
		</div>
	{:else}
		<div class="nodes-table-wrap">
			<table class="nodes-table">
				<thead>
					<tr>
						<th style="width: 68px;">{m.firc_tunnel_edit_col_priority()}</th>
						<th>{m.firc_tunnel_edit_col_node()}</th>
						<th>{m.firc_tunnel_edit_col_source()}</th>
						<th>{m.firc_tunnel_edit_col_status()}</th>
						<th>{m.firc_tunnel_edit_col_latency()}</th>
						<th style="width: 70px; text-align: right;">{m.firc_tunnel_edit_col_in_pool()}</th>
					</tr>
				</thead>
				<tbody>
					{#each nodes as node, nIdx (node.key)}
						{#if !q || node.name.toLowerCase().includes(q) || node.key.toLowerCase().includes(q)}
							{@const pr = tunProbes[node.key]}
							{@const isIncluded = !exclude.includes(node.key)}
							<tr class:excluded-row={!isIncluded}>
								<td>
									<div class="prio-cell">
										<span class="prio-num">{String(nIdx + 1).padStart(2, '0')}</span>
										<!-- Стрелки 11px стопкой: IconButton (28×28, иконка 16px) раздул бы строку таблицы втрое. -->
										<div class="prio-btns">
											<button
												type="button"
												class="mini-icon-btn"
												aria-label={m.firc_tunnel_edit_move_up()}
												disabled={nIdx === 0}
												onclick={() => onmove(nIdx, -1)}
											>
												<ArrowUp size={11} />
											</button>
											<button
												type="button"
												class="mini-icon-btn"
												aria-label={m.firc_tunnel_edit_move_down()}
												disabled={nIdx === nodes.length - 1}
												onclick={() => onmove(nIdx, 1)}
											>
												<ArrowDown size={11} />
											</button>
										</div>
									</div>
								</td>
								<td class="node-name-cell">
									{node.name}
									{#if node.v6}
										<Badge variant="info" size="xs" mono title={m.firc_tunnel_edit_ipv6_title()}>IPv6</Badge>
									{/if}
								</td>
								<td class="mono-cell">{node.source || '—'}</td>
								<td>
									{#if !isIncluded}
										<Badge variant="muted" size="xs">{m.firc_tunnel_edit_state_excluded()}</Badge>
									{:else if node.state === 'active'}
										<Badge variant="success" size="xs">{m.firc_tunnel_edit_state_active()}</Badge>
									{:else if node.state === 'reserve'}
										<Badge variant="info" size="xs">{m.firc_tunnel_edit_state_reserve()}</Badge>
									{:else if node.state === 'down'}
										<Badge variant="error" size="xs" title={node.skipReason}>
											{m.firc_tunnel_edit_state_down()}
										</Badge>
									{:else if node.state === 'skipped'}
										<div class="node-status-stack">
											<Badge variant="warning" size="xs" title={node.skipReason}>
												{m.firc_node_state_skipped()}
											</Badge>
											{#if node.skipReason}
												<span class="node-skip-reason" title={node.skipReason}>
													{formatFircSkipReason(node.skipReason)}
												</span>
											{/if}
										</div>
									{:else}
										<Badge variant="muted" size="xs">{m.firc_tunnel_edit_state_pending()}</Badge>
									{/if}
								</td>
								<td class="mono-cell">
									<div class="probe-cell">
										<div class="probe-vals">
											{#if pr}
												{@const totalMs = totalProbeLatencyMs(pr)}
												{#if !pr.ok}
													<span class="probe-err" title={pr.why}>{m.firc_tunnel_edit_probe_timeout()}</span>
												{:else if totalMs !== null}
													<span
														class="probe-ok"
														title={m.firc_probe_total_title()}
													>
														{m.firc_tunnel_edit_ms({ ms: totalMs })}
													</span>
													{#if pr.handshakeMs != null && pr.firstByteMs != null}
														<span class="probe-breakdown">
															{m.firc_probe_breakdown({
																connect: String(pr.handshakeMs),
																reply: String(pr.firstByteMs),
															})}
														</span>
													{/if}
												{/if}
											{:else}
												<span class="muted-dash">—</span>
											{/if}
										</div>
										{#if !node.skipReason}
											<IconButton
												title={m.firc_probe_single_title()}
												ariaLabel={m.firc_probe_single_aria({ name: node.name })}
												loading={probingNodeKey === node.key}
												disabled={Boolean(busyAction || probingNodeKey)}
												onclick={() => onprobenode(node.key)}
											>
												<Activity size={12} />
											</IconButton>
										{/if}
									</div>
								</td>
								<td style="text-align: right;">
									<Toggle
										size="sm"
										spinner="none"
										checked={isIncluded}
										onchange={(v) => ontoggle(node.key, v)}
									/>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>
	{/if}

	{#if filteredNodes.length > 0}
		<section class="filtered-section">
			<button
				type="button"
				class="filtered-toggle"
				aria-expanded={filteredOpen}
				onclick={() => (filteredOpen = !filteredOpen)}
			>
				<span class="lbl">{m.firc_filter_hidden_toggle({ count: filteredNodes.length })}</span>
				<span class="filtered-chevron" class:open={filteredOpen} aria-hidden="true">▸</span>
			</button>
			{#if filteredOpen}
				<div class="hint">
					{m.firc_filter_hidden_hint()}
				</div>
				<div class="filtered-grid">
					{#each filteredNodes as fn (fn.key)}
						<div class="filtered-card">
							<div class="filtered-title">{fn.name || fn.key}</div>
							{#if fn.source}
								<div class="filtered-meta mono">{fn.source}</div>
							{/if}
						</div>
					{/each}
				</div>
			{/if}
		</section>
	{/if}
</div>

<style>
	.subtab-panel {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.nodes-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		flex-wrap: wrap;
	}
	.search-box {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 0 10px;
		height: 30px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary);
		color: var(--text-secondary);
		flex: 1 1 180px;
		max-width: 260px;
	}
	.search-box input {
		border: 0;
		background: transparent;
		color: var(--text-primary);
		font-size: 12px;
		width: 100%;
		outline: none;
	}
	.nodes-toolbar-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}
	.nodes-table-wrap {
		max-height: 320px;
		overflow-x: auto;
		overflow-y: auto;
		border: 1px solid var(--border);
		border-radius: 8px;
	}
	.nodes-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 12px;
	}
	.nodes-table th {
		text-align: left;
		padding: 7px 10px;
		font-size: 10.5px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
		background: var(--bg-tertiary);
		border-bottom: 1px solid var(--border);
		position: sticky;
		top: 0;
		z-index: 1;
	}
	.nodes-table td {
		padding: 6px 10px;
		border-bottom: 1px solid var(--border);
		vertical-align: middle;
	}
	.excluded-row {
		opacity: 0.5;
	}
	.prio-cell {
		display: inline-flex;
		align-items: center;
		gap: 4px;
	}
	.prio-num {
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--text-muted);
	}
	.prio-btns {
		display: flex;
		flex-direction: column;
	}
	.mini-icon-btn {
		border: 0;
		background: transparent;
		color: var(--text-muted);
		padding: 0;
		cursor: pointer;
		line-height: 1;
	}
	.mini-icon-btn:hover:not(:disabled) {
		color: var(--text-primary);
	}
	.mini-icon-btn:disabled {
		opacity: 0.25;
		cursor: default;
	}
	.node-name-cell {
		font-weight: 500;
		color: var(--text-primary);
	}
	.mono-cell {
		font-family: var(--font-mono);
		font-size: 11.5px;
		color: var(--text-secondary);
	}
	.probe-ok {
		color: var(--color-success, #9ece6a);
		font-weight: 600;
	}
	.probe-err {
		color: var(--color-error, #f7768e);
	}
	.lbl {
		font-size: 11.5px;
		color: var(--text-secondary);
		font-weight: 500;
	}
	.hint {
		font-size: 11px;
		color: var(--text-muted);
	}
	.mono {
		font-family: var(--font-mono);
	}
	.sub-empty {
		padding: 20px;
		text-align: center;
		color: var(--text-muted);
		font-size: 12px;
		border: 1px dashed var(--border);
		border-radius: 8px;
	}
	.sub-empty-stack {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 10px;
	}
	.sub-empty-loading {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		color: var(--text-secondary);
	}
	.probe-cell {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 6px;
		min-width: 96px;
	}
	.probe-vals {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		line-height: 1.25;
	}
	.probe-breakdown {
		font-size: 10px;
		color: var(--text-muted);
		white-space: nowrap;
	}
	.node-status-stack {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 3px;
	}
	.node-skip-reason {
		font-size: 11px;
		line-height: 1.3;
		color: var(--color-warning, #e0af68);
		overflow-wrap: anywhere;
	}
	.preview-err {
		padding: 8px 10px;
		border-radius: 6px;
		border: 1px solid color-mix(in srgb, var(--color-error, #f7768e) 45%, var(--border));
		background: color-mix(in srgb, var(--color-error, #f7768e) 10%, var(--bg-tertiary));
		color: var(--color-error, #f7768e);
		font-size: 11.5px;
	}
	.filtered-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 10px 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-tertiary);
	}
	.filtered-toggle {
		display: flex;
		align-items: center;
		justify-content: space-between;
		width: 100%;
		padding: 0;
		border: 0;
		background: transparent;
		cursor: pointer;
		text-align: left;
	}
	.filtered-chevron {
		font-size: 12px;
		color: var(--text-muted);
		transition: transform 0.15s ease;
	}
	.filtered-chevron.open {
		transform: rotate(90deg);
	}
	.filtered-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
		gap: 6px;
		max-height: 180px;
		overflow-y: auto;
	}
	.filtered-card {
		padding: 6px 8px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-primary);
		opacity: 0.8;
		min-width: 0;
	}
	.filtered-title {
		font-size: 11.5px;
		font-weight: 500;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.filtered-meta {
		font-size: 10.5px;
		color: var(--text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
</style>
