<script lang="ts">
	import type { Snippet } from 'svelte';
	import { m } from '$lib/i18n';
	import type { FircTunnel } from '$lib/types/firc';
	import type { FircTunnelDiagnostic } from '$lib/utils/fircDiagnostics';
	import type { FircLinkMeta } from './fircLinkMeta';
	import type { SingboxDelayState } from '$lib/utils/singboxDelay';
	import { Badge, TunnelListActions, type StatusDotVariant } from '$lib/components/ui';
	import {
		TunnelDelaySparkBars,
		TunnelListTrafficCell,
		TunnelMetaText,
		TunnelSingboxPingButton,
		TunnelTitleRow,
	} from '$lib/components/tunnels';

	interface Props {
		kind: 'proxy' | 'subscription';
		tunnel: FircTunnel;
		title: string;
		statusVariant: StatusDotVariant;
		running: boolean;
		cardState: SingboxDelayState;
		latText: string;
		checking: boolean;
		history: number[];
		diagnostic: FircTunnelDiagnostic;
		linkMeta: FircLinkMeta;
		listenPortsText: string;
		endpointText: string;
		preferredNodeName: string;
		liveActiveNodeName: string;
		nodeCount: number;
		inlineRxRate: number;
		inlineTxRate: number;
		trafficSparkSeries: { rx: number[]; tx: number[] };
		deleting: boolean;
		extra: Snippet;
		onCheck: (silent?: boolean) => void;
		onEdit: (e?: MouseEvent | KeyboardEvent) => void;
		onTest: () => void;
		onDelete: () => void;
	}

	let {
		kind,
		tunnel,
		title,
		statusVariant,
		running,
		cardState,
		latText,
		checking,
		history,
		diagnostic,
		linkMeta,
		listenPortsText,
		endpointText,
		preferredNodeName,
		liveActiveNodeName,
		nodeCount,
		inlineRxRate,
		inlineTxRate,
		trafficSparkSeries,
		deleting,
		extra,
		onCheck,
		onEdit,
		onTest,
		onDelete,
	}: Props = $props();
</script>

<tr
	class="sbx-sub-active-row"
	class:ok={cardState === 'ok'}
	class:slow={cardState === 'slow'}
	class:fail={cardState === 'fail'}
>
	<td class="tunnel-list-cell tunnel-list-cell--delay list-cell list-cell-delay">
		<div class="lc lc-delay">
			<TunnelSingboxPingButton
				layout="list"
				label={latText}
				state={cardState}
				{checking}
				onclick={() => onCheck()}
			/>
		</div>
	</td>
	<td class="tunnel-list-cell tunnel-list-cell--name list-cell list-cell-name">
		<div class="table-name">
			<TunnelTitleRow
				{title}
				dotVariant={statusVariant}
				dotPulse={running}
				onTitleClick={onEdit}
			>
				{#snippet badges()}
					<Badge variant="accent" size="sm">AWGM Route</Badge>
				{/snippet}
			</TunnelTitleRow>
			<TunnelMetaText mono>
				<span>{tunnel.device}</span>
				{#if listenPortsText}
					<span class="meta-dot" aria-hidden="true">·</span><span>{listenPortsText}</span>
				{/if}
			</TunnelMetaText>
			{#if diagnostic.reason}
				<div
					class="firc-diag-inline"
					class:is-error={diagnostic.tone === 'error'}
					class:is-warning={diagnostic.tone === 'warning'}
					title={diagnostic.reason}
					data-testid="firc-card-diagnostic"
				>
					{diagnostic.reason}
				</div>
			{/if}
		</div>
	</td>
	{#if kind === 'proxy'}
		<td class="tunnel-list-cell list-cell list-cell-badges">
			<div class="badges">
				<span class="badge proto">{linkMeta.proto}</span>
				{#if linkMeta.security}
					<span class="badge reality">{linkMeta.security}</span>
				{/if}
				{#if linkMeta.transport}
					<span class="badge mode">{linkMeta.transport}</span>
				{/if}
			</div>
		</td>
		<td class="tunnel-list-cell list-cell list-cell-run">
			<span class="run-pill" class:run-on={running}>{running ? 'running' : 'stopped'}</span>
		</td>
	{:else}
		<td class="tunnel-list-cell list-cell" colspan="2" title={endpointText}>
			<div class="lc lc-endpoint">
				<div class="lc-endpoint-stack">
					<span class="lc-endpoint-name" title={preferredNodeName}>
						{liveActiveNodeName || preferredNodeName || '—'}
					</span>
					<span class="mono">{m.subscriptions_card_servers_count({ count: nodeCount })}</span>
				</div>
			</div>
		</td>
	{/if}
	<td class="tunnel-list-cell tunnel-list-cell--traffic list-cell list-cell-traffic">
		<div class="lc lc-traffic">
			<TunnelListTrafficCell
				rxRate={inlineRxRate}
				txRate={inlineTxRate}
				rxData={trafficSparkSeries.rx}
				txData={trafficSparkSeries.tx}
			/>
		</div>
	</td>
	<td class="tunnel-list-cell tunnel-list-cell--ping list-cell list-cell-ping-mini">
		<div class="lc">
			<TunnelDelaySparkBars {history} state={cardState} layout="list" onclick={() => onCheck()} />
		</div>
	</td>
	<td class="tunnel-list-cell tunnel-list-cell--actions list-cell list-cell-actions col-actions">
		<div class="lc lc-actions">
			<TunnelListActions
				onEdit={() => onEdit()}
				editLabel={m.common_edit()}
				{onTest}
				testDisabled={!tunnel.device}
				testTitle={m.firc_resource_test_title({ name: title })}
				{onDelete}
				{deleting}
				{extra}
			/>
		</div>
	</td>
</tr>

<style>
	.meta-dot {
		margin: 0 0.35em;
		opacity: 0.75;
	}

	.badges {
		display: flex;
		gap: 0.4rem;
		flex-wrap: wrap;
	}
	.badge {
		font-size: var(--sbx-card-badge);
		padding: 2px 8px;
		border-radius: 10px;
		font-weight: 500;
	}
	.badge.proto {
		background: rgba(88, 166, 255, 0.15);
		color: var(--color-accent);
	}
	.badge.reality {
		background: rgba(210, 153, 34, 0.15);
		color: #d29922;
	}
	.badge.mode {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}

	.firc-diag-inline {
		font-size: var(--sbx-card-note);
		line-height: 1.3;
		color: var(--color-text-secondary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 340px;
	}
	.firc-diag-inline.is-error {
		color: var(--color-error);
	}
	.firc-diag-inline.is-warning {
		color: var(--color-warning);
	}

	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}

	/* Table Row */
	.sbx-sub-active-row {
		cursor: pointer;
	}
	.lc {
		display: flex;
		align-items: center;
		min-width: 0;
		font-size: var(--sbx-card-value);
		color: var(--color-text-secondary);
		vertical-align: middle;
	}
	.lc-delay {
		justify-content: center;
		gap: 0.35rem;
		min-width: 0;
	}
	.lc-endpoint {
		display: flex;
		align-items: center;
		gap: 0.25rem;
		min-width: 0;
		overflow: hidden;
	}
	.lc-endpoint-stack {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.12rem;
		min-width: 0;
		flex: 1;
	}
	.lc-endpoint-name {
		width: 100%;
		font-size: var(--sbx-card-value);
		font-weight: 500;
		color: var(--color-text-primary);
		line-height: 1.2;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.lc-actions {
		flex-wrap: nowrap;
		gap: 0.375rem;
		justify-content: center;
		align-items: center;
		white-space: nowrap;
	}
	.table-name {
		display: grid;
		gap: 2px;
	}
	.run-pill {
		display: inline-flex;
		border-radius: 999px;
		padding: 2px 7px;
		background: var(--color-muted-bg);
		font-size: 10px;
	}
	.run-pill.run-on {
		background: var(--color-success-bg);
		color: var(--color-success);
	}
</style>
