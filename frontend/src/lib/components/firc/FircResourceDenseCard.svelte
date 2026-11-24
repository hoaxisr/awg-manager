<script lang="ts">
	import type { Snippet } from 'svelte';
	import { m } from '$lib/i18n';
	import { Eye, EyeOff } from 'lucide-svelte';
	import type { FircTunnel } from '$lib/types/firc';
	import type { FircTunnelDiagnostic } from '$lib/utils/fircDiagnostics';
	import type { FircLinkMeta } from './fircLinkMeta';
	import type { SingboxDelayState } from '$lib/utils/singboxDelay';
	import type { TunnelRenderMode } from '$lib/constants/singboxLayout';
	import { formatBitRate } from '$lib/utils/format';
	import {
		Badge,
		IconButton,
		Toggle,
		TrafficSparkline,
		TunnelListActions,
		type StatusDotVariant,
	} from '$lib/components/ui';
	import {
		TunnelDelaySparkBars,
		TunnelSingboxPingButton,
		TunnelTitleRow,
	} from '$lib/components/tunnels';

	interface Props {
		kind: 'proxy' | 'subscription';
		tunnel: FircTunnel;
		renderMode: TunnelRenderMode;
		title: string;
		statusVariant: StatusDotVariant;
		running: boolean;
		enabled: boolean;
		cardState: SingboxDelayState;
		latText: string;
		checking: boolean;
		history: number[];
		diagnostic: FircTunnelDiagnostic;
		linkMeta: FircLinkMeta;
		listenPortsText: string;
		endpointText: string;
		hiddenEndpointText: string;
		nodeCount: number;
		inlineRxRate: number;
		inlineTxRate: number;
		trafficSparkSeries: { rx: number[]; tx: number[] };
		deleting: boolean;
		showEndpoint: boolean;
		extra: Snippet;
		onCheck: (silent?: boolean) => void;
		onEdit: (e?: MouseEvent | KeyboardEvent) => void;
		onTest: () => void;
		onDelete: () => void;
		onToggle: (next: boolean) => void;
	}

	let {
		kind,
		tunnel,
		renderMode,
		title,
		statusVariant,
		running,
		enabled,
		cardState,
		latText,
		checking,
		history,
		diagnostic,
		linkMeta,
		listenPortsText,
		endpointText,
		hiddenEndpointText,
		nodeCount,
		inlineRxRate,
		inlineTxRate,
		trafficSparkSeries,
		deleting,
		showEndpoint = $bindable(),
		extra,
		onCheck,
		onEdit,
		onTest,
		onDelete,
		onToggle,
	}: Props = $props();
</script>

<div
	class="card view-dense card-clickable"
	class:view-list={renderMode === 'list-card'}
	class:ok={cardState === 'ok'}
	class:slow={cardState === 'slow'}
	class:fail={cardState === 'fail'}
	class:unknown={cardState === 'unknown'}
	class:stopped={!enabled}
	role="button"
	tabindex="0"
	onclick={(e) => onEdit(e)}
	onkeydown={(e) => { if ((e.key === 'Enter' || e.key === ' ') && e.target === e.currentTarget) { e.preventDefault(); onEdit(e); } }}
>
	<div class="header header-dense">
		<div class="header-dense-body">
			<div class="title-row-dense">
				<TunnelTitleRow {title} dotVariant={statusVariant} dotPulse={running} dense staticTitle>
					{#snippet badges()}
						<Badge variant="accent" size="sm">FIRC</Badge>
						{#if kind === 'subscription'}
							<Badge variant="accent" size="sm">{m.tunnels_kind_subscription()}</Badge>
						{/if}
					{/snippet}
				</TunnelTitleRow>
			</div>
			<div class="meta-tags-dense">
				<span class="iface-dense mono">
					<span>{tunnel.device}</span>
					{#if listenPortsText}
						<span class="meta-dot" aria-hidden="true">·</span><span>{listenPortsText}</span>
					{/if}
				</span>
				<span class="badge proto">{linkMeta.proto}</span>
				{#if linkMeta.security}
					<span class="badge reality">{linkMeta.security}</span>
				{/if}
				{#if linkMeta.transport}
					<span class="badge mode">{linkMeta.transport}</span>
				{/if}
				<span class="badge mode">×{tunnel.active || 1}</span>
			</div>
		</div>
		<!-- svelte-ignore a11y_click_events_have_key_events -->
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div class="dense-toolbar" onclick={(e) => e.stopPropagation()}>
			<div class="dense-toolbar-bottom">
				<TunnelSingboxPingButton
					layout="dense"
					label={latText}
					state={cardState}
					{checking}
					onclick={() => onCheck()}
				/>
				<Toggle
					size="sm"
					spinner="none"
					checked={enabled}
					onchange={onToggle}
				/>
			</div>
		</div>
	</div>

	{#if diagnostic.reason}
		<div
			class="firc-diag-banner firc-diag-banner--dense"
			class:is-error={diagnostic.tone === 'error'}
			class:is-warning={diagnostic.tone === 'warning'}
			class:is-info={diagnostic.tone === 'info'}
			role="status"
			data-testid="firc-card-diagnostic"
		>
			{diagnostic.reason}
		</div>
	{/if}

	{#if renderMode !== 'list-card'}
		<div class="details">
			<div class="details-dense-cols">
				<div class="details-dense-col">
					<div class="kv-stacked-stat">
						<span class="kv-stacked-label">
							{kind === 'proxy' ? m.tunnels_card_server() : m.subscriptions_active_server()}
						</span>
						<span class="kv-endpoint">
							<span class="kv-stacked-value" title={endpointText}>
								{showEndpoint || kind === 'subscription' ? endpointText : hiddenEndpointText}
							</span>
							<IconButton
								onclick={(e) => {
									e.stopPropagation();
									showEndpoint = !showEndpoint;
								}}
								ariaLabel={showEndpoint ? m.common_hide() : m.common_show()}
							>
								{#if showEndpoint}
									<Eye size={12} aria-hidden="true" />
								{:else}
									<EyeOff size={12} aria-hidden="true" />
								{/if}
							</IconButton>
						</span>
					</div>
				</div>
			</div>
			<div class="dense-meta-line mono">
				<span>{m.subscriptions_card_servers_count({ count: nodeCount })}</span>
				<span>{m.firc_resource_sources_short({ count: tunnel.sources.length })}</span>
			</div>
		</div>
	{/if}

	<div class="actions">
		<TunnelListActions
			variant="labeled"
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

	{#if renderMode !== 'list-card'}
		<div class="charts-dense">
			<div class="traffic-inline">
				<TrafficSparkline
					rxData={trafficSparkSeries.rx}
					txData={trafficSparkSeries.tx}
					responsive
					height={20}
				/>
				<div class="traffic-inline-rates">
					<span class="traffic-inline-rate rx">↓ {formatBitRate(inlineRxRate)}</span>
					<span class="traffic-inline-rate tx">↑ {formatBitRate(inlineTxRate)}</span>
				</div>
			</div>
			<div class="chart-inline delay-inline">
				<div class="chart-inline-head">
					<span class="chart-inline-label">{m.singbox_card_delay_5m()}</span>
				</div>
				<TunnelDelaySparkBars
					{history}
					state={cardState}
					layout="dense"
					onclick={() => onCheck()}
				/>
			</div>
		</div>
	{/if}
</div>

<style>
	.card {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 12px 14px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		transition: border-color var(--t-fast) ease;
	}
	.card.ok {
		border-color: var(--color-success-border);
	}
	.card.slow {
		border-color: var(--color-warning-border);
	}
	.card.fail {
		border-color: var(--color-error-border);
	}
	.card.unknown {
		border-color: var(--color-border);
	}
	.card.stopped {
		opacity: 0.72;
	}

	.card.card-clickable {
		cursor: pointer;
	}
	.card.card-clickable:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.meta-dot {
		margin: 0 0.35em;
		opacity: 0.75;
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

	.firc-diag-banner {
		padding: 6px 8px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
		font-size: var(--sbx-card-meta);
		line-height: 1.35;
		overflow-wrap: anywhere;
	}
	.firc-diag-banner--dense {
		padding: 4px 6px;
		font-size: 10px;
	}
	.firc-diag-banner.is-error {
		background: var(--color-error-tint);
		border-color: var(--color-error-border);
		color: var(--color-error);
	}
	.firc-diag-banner.is-warning {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
		color: var(--color-warning);
	}
	.firc-diag-banner.is-info {
		background: var(--color-info-tint);
		border-color: var(--color-info-border);
		color: var(--color-info);
	}

	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}

	/* Dense Mode */
	.card.view-dense {
		gap: 8px;
		padding: 10px 12px;
	}
	.header-dense {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: flex-start;
		gap: 6px;
	}
	.header-dense-body {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}
	.title-row-dense {
		display: flex;
		align-items: center;
		gap: 5px;
		min-width: 0;
	}
	.meta-tags-dense {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		margin-top: 3px;
		gap: 3px;
		min-width: 0;
	}
	.iface-dense {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0;
		font-size: 9px;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-muted);
		min-width: 0;
	}
	.dense-toolbar {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		flex-shrink: 0;
	}
	.dense-toolbar-bottom {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.card.view-dense .details {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 4px 0 8px;
		border-top: 1px solid var(--color-border);
		border-bottom: 1px solid var(--color-border);
	}
	.details-dense-cols {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: 10px 10px;
		align-items: start;
	}
	.details-dense-col {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}
	.kv-stacked-stat {
		display: flex;
		flex-direction: row;
		align-items: center;
		gap: 0.35rem;
		min-width: 0;
	}
	.kv-stacked-label {
		font-size: 9px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		flex: 0 0 auto;
	}
	.kv-endpoint {
		display: flex;
		align-items: center;
		gap: 2px;
		flex: 1 1 auto;
		min-width: 0;
	}
	.kv-stacked-value {
		font-size: 10px;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	.dense-meta-line {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.35rem 0.75rem;
		min-width: 0;
		font-size: 9px;
		line-height: 1.25;
		color: var(--color-text-muted);
	}
	.charts-dense {
		display: flex;
		flex-direction: row;
		align-items: stretch;
		gap: 4px;
		width: 100%;
		min-width: 0;
	}
	.charts-dense > .delay-inline,
	.charts-dense > .traffic-inline {
		flex: 1 1 0;
		min-width: 0;
	}
	.chart-inline {
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
		padding: 5px 6px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}
	.chart-inline.delay-inline {
		gap: 0;
		padding: 0;
		overflow: hidden;
	}
	.chart-inline.delay-inline .chart-inline-head {
		padding: 5px 6px 3px;
	}
	.charts-dense .traffic-inline {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		padding: 5px 4px 5px 5px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}
	.charts-dense .traffic-inline-rates {
		display: flex;
		flex-direction: column;
		gap: 0.06rem;
		flex: 0 0 auto;
		font-size: 9px;
		line-height: 1.1;
		font-family: var(--font-mono, monospace);
	}
	.charts-dense .traffic-inline-rate.rx {
		color: var(--color-accent);
	}
	.charts-dense .traffic-inline-rate.tx {
		color: var(--color-success);
	}
	.chart-inline-head {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		gap: 6px;
		font-size: 9px;
		line-height: 1.2;
	}
	.chart-inline-label {
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		font-weight: 500;
	}
</style>
