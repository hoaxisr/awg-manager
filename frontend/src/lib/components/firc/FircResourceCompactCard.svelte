<script lang="ts">
	import type { Snippet } from 'svelte';
	import { m } from '$lib/i18n';
	import { Eye, EyeOff } from 'lucide-svelte';
	import type { FircTunnel } from '$lib/types/firc';
	import type { FircTunnelDiagnostic } from '$lib/utils/fircDiagnostics';
	import type { FircLinkMeta } from './fircLinkMeta';
	import type { SingboxDelayState } from '$lib/utils/singboxDelay';
	import { formatBytes } from '$lib/utils/format';
	import {
		Badge,
		IconButton,
		Toggle,
		TrafficChart,
		TunnelListActions,
		type StatusDotVariant,
	} from '$lib/components/ui';
	import {
		TunnelDelaySparkBars,
		TunnelSingboxPingButton,
		TunnelTitleRow,
	} from '$lib/components/tunnels';
	import SubscriptionMemberPicker from '$lib/components/subscriptions/SubscriptionMemberPicker.svelte';

	interface SubscriptionMember {
		tag: string;
		label: string;
		protocol: string;
		server: string;
		port: number;
	}

	interface Props {
		kind: 'proxy' | 'subscription';
		tunnel: FircTunnel;
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
		stateLabel: string;
		endpointText: string;
		liveActiveNodeName: string;
		nodeCount: number;
		subscriptionMembers: SubscriptionMember[];
		activeNodeKey: string;
		rxRates: number[];
		txRates: number[];
		rxTotal: number;
		txTotal: number;
		deleting: boolean;
		showEndpoint: boolean;
		pickerOpen: boolean;
		extra: Snippet;
		onCheck: (silent?: boolean) => void;
		onEdit: (e?: MouseEvent | KeyboardEvent) => void;
		onTest: () => void;
		onDelete: () => void;
		onToggle: (next: boolean) => void;
		onPickMember: (nodeKey: string) => Promise<void>;
	}

	let {
		kind,
		tunnel,
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
		stateLabel,
		endpointText,
		liveActiveNodeName,
		nodeCount,
		subscriptionMembers,
		activeNodeKey,
		rxRates,
		txRates,
		rxTotal,
		txTotal,
		deleting,
		showEndpoint = $bindable(),
		pickerOpen = $bindable(),
		extra,
		onCheck,
		onEdit,
		onTest,
		onDelete,
		onToggle,
		onPickMember,
	}: Props = $props();
</script>

<div
	class="card view-compact card-clickable"
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
	<div class="tunnel-card-intro">
		<div class="title-row">
			<TunnelTitleRow {title} dotVariant={statusVariant} dotPulse={running} staticTitle>
				{#snippet badges()}
					<Badge variant="accent" size="sm">FIRC</Badge>
					{#if kind === 'subscription'}
						<Badge variant="accent" size="sm">{m.tunnels_kind_subscription()}</Badge>
					{/if}
				{/snippet}
			</TunnelTitleRow>
			<TunnelSingboxPingButton
				layout="compact"
				label={latText}
				state={cardState}
				{checking}
				onclick={() => onCheck()}
			/>
			<!-- svelte-ignore a11y_click_events_have_key_events -->
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div class="toggle-stop" onclick={(e) => e.stopPropagation()}>
				<Toggle
					size="sm"
					spinner="none"
					checked={enabled}
					onchange={onToggle}
				/>
			</div>
		</div>
		<div class="iface">
			<span>{tunnel.device}</span>
			{#if listenPortsText}
				<span class="meta-dot" aria-hidden="true">·</span><span>SOCKS5 {listenPortsText}</span>
			{/if}
			<span class="meta-dot" aria-hidden="true">·</span><span>{stateLabel}</span>
		</div>
		<div class="badges">
			<span class="badge proto">{linkMeta.proto}</span>
			{#if linkMeta.security}
				<span class="badge reality">{linkMeta.security}</span>
			{/if}
			{#if linkMeta.transport}
				<span class="badge mode">{linkMeta.transport}</span>
			{/if}
			<span class="badge mode">
				{tunnel.by === 'connection'
					? m.firc_resource_by_connection()
					: tunnel.by === 'site'
						? m.firc_resource_by_site()
						: m.firc_resource_by_site_client()} · ×{tunnel.active || 1}
			</span>
		</div>
	</div>

	{#if kind === 'subscription'}
		<div class="sub-meta">
			<div>
				<span>{m.subscriptions_card_servers_count({ count: nodeCount })}</span>
				{#if liveActiveNodeName}
					<span>· {m.firc_resource_active_node()} <span class="mono">{liveActiveNodeName}</span></span>
				{/if}
			</div>
			<div>
				<span>{m.firc_resource_sources_count({ count: tunnel.sources.length })}</span>
				<span>· {m.firc_resource_auto_failover()}</span>
			</div>
		</div>
	{/if}

	{#if diagnostic.reason}
		<div
			class="firc-diag-banner"
			class:is-error={diagnostic.tone === 'error'}
			class:is-warning={diagnostic.tone === 'warning'}
			class:is-info={diagnostic.tone === 'info'}
			role="status"
			data-testid="firc-card-diagnostic"
		>
			{diagnostic.reason}
		</div>
	{/if}

	{#if kind === 'proxy'}
		<div class="divider divider-dashed"></div>
		<div class="row">
			<span class="item-label">{m.tunnels_card_server()}</span>
			<div class="server-row value">
				{#if showEndpoint}
					<span class="server-text mono">{endpointText}</span>
				{:else}
					<span class="server-hidden">●●●●●●●●</span>
				{/if}
				<IconButton
					onclick={(e) => {
						e.stopPropagation();
						showEndpoint = !showEndpoint;
					}}
					ariaLabel={showEndpoint ? m.common_hide() : m.common_show()}
				>
					{#if showEndpoint}
						<EyeOff size={12} aria-hidden="true" />
					{:else}
						<Eye size={12} aria-hidden="true" />
					{/if}
				</IconButton>
			</div>
		</div>
		<div class="row">
			<span class="item-label">{m.firc_resource_pool()}</span>
			<span class="value mono">{m.firc_resource_pool_summary({ nodes: nodeCount, sources: tunnel.sources.length })}</span>
		</div>
	{:else}
		<div class="server-section">
			<div class="server-row">
				<span class="item-label">{m.subscriptions_active_server()}</span>
				<div class="picker-anchor">
					<div class="server-control">
						<!-- Триггер выпадающего списка (aria-haspopup/expanded), а не кнопка
						     действия: у Button нет этих атрибутов и вида поля выбора. -->
						<button
							class="server-btn"
							onclick={(e) => {
								e.stopPropagation();
								pickerOpen = !pickerOpen;
							}}
							aria-haspopup="listbox"
							aria-expanded={pickerOpen}
							title={m.firc_resource_pick_server_title()}
						>
							<span class="server-text" title={endpointText}>
								{endpointText}
							</span>
							<span class="caret" aria-hidden="true">▾</span>
						</button>
						<IconButton
							onclick={(e) => {
								e.stopPropagation();
								showEndpoint = !showEndpoint;
							}}
							title={showEndpoint ? m.common_hide() : m.common_show()}
							ariaLabel={showEndpoint ? m.common_hide() : m.common_show()}
						>
							{#if showEndpoint}
								<Eye size={14} aria-hidden="true" />
							{:else}
								<EyeOff size={14} aria-hidden="true" />
							{/if}
						</IconButton>
					</div>
					{#if pickerOpen}
						<SubscriptionMemberPicker
							members={subscriptionMembers}
							activeMemberTag={activeNodeKey}
							onPick={onPickMember}
							onClose={() => (pickerOpen = false)}
						/>
					{/if}
				</div>
			</div>
		</div>
	{/if}

	<div class="actions actions--bar">
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

	<div class="chart-section">
		<div class="chart-body">
			<div class="chart-head"><span>{m.singbox_card_delay_5m()}</span></div>
			<TunnelDelaySparkBars
				{history}
				state={cardState}
				layout="compact"
				onclick={() => onCheck()}
			/>
			<div class="chart-head traffic-head">
				<span>{m.tunnels_awg_col_traffic()}</span>
				<span class="stats">
					↓ {formatBytes(rxTotal)} · ↑ {formatBytes(txTotal)}
				</span>
			</div>
			<TrafficChart
				{rxRates}
				{txRates}
				{rxTotal}
				{txTotal}
				height={56}
				onclick={onEdit}
			/>
		</div>
	</div>
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

	.tunnel-card-intro {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}
	.title-row :global(.tunnel-title-row) {
		flex: 1;
		min-width: 0;
	}
	.title-row :global(.ping-btn) {
		flex-shrink: 0;
		margin-left: auto;
	}
	.title-row :global(.badge) {
		flex-shrink: 0;
	}
	.toggle-stop {
		display: inline-flex;
		align-items: center;
		flex-shrink: 0;
	}

	.iface {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
		font-family: var(--font-mono, ui-monospace, monospace);
	}
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

	.sub-meta {
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
		line-height: 1.35;
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
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

	.divider {
		height: 0;
		border: none;
		margin: 0;
		background: none;
	}
	.divider-dashed {
		border-top: 1px dashed var(--color-border);
	}

	.row {
		display: flex;
		align-items: center;
		margin: 0;
	}
	.row .item-label {
		color: var(--color-text-muted);
		font-size: var(--sbx-card-label);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		width: 60px;
		flex-shrink: 0;
	}
	.row .value {
		font-size: var(--sbx-card-value);
		color: var(--color-text-secondary);
		font-family: var(--font-mono, monospace);
	}
	.server-row {
		display: flex;
		align-items: center;
		gap: 6px;
		flex: 1;
	}
	.server-hidden {
		color: var(--color-text-muted);
		letter-spacing: 2px;
	}
	.server-text {
		font-family: var(--font-mono, monospace);
	}

	.server-section {
		padding-top: 8px;
		border-top: 1px dashed var(--color-border);
	}
	.server-section .server-row {
		display: grid;
		grid-template-columns: max-content minmax(0, 1fr);
		gap: 0.45rem;
		align-items: center;
		margin: 0;
	}
	.server-section .item-label {
		color: var(--color-text-muted);
		font-size: var(--sbx-card-label);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.picker-anchor {
		position: relative;
		min-width: 0;
	}
	.server-control {
		display: flex;
		align-items: center;
		gap: 0.25rem;
		min-width: 0;
	}
	.server-btn {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		width: 100%;
		min-width: 0;
		padding: 0.4rem 0.55rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font: inherit;
		font-size: var(--sbx-card-value);
		color: var(--color-text-primary);
		cursor: pointer;
	}
	.server-btn:hover {
		border-color: var(--color-accent);
	}
	.server-text {
		font-size: var(--sbx-card-value);
		overflow: hidden;
		display: block;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.caret {
		color: var(--color-text-muted);
		font-size: var(--sbx-card-note);
	}

	.actions--bar {
		margin-top: 0.15rem;
	}

	.chart-head {
		display: flex;
		justify-content: space-between;
		font-size: var(--sbx-card-label);
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.chart-head .stats {
		font-size: var(--sbx-card-value);
	}
	.chart-head.traffic-head .stats {
		font-size: 0.6875rem;
	}
	.stats {
		font-family: var(--font-mono, ui-monospace, monospace);
	}
	.chart-section {
		margin: 0 -14px -12px;
		border-radius: 0 0 var(--radius) var(--radius);
		background: var(--color-bg-secondary);
		overflow: hidden;
	}
	.chart-body {
		padding: 8px 12px 10px;
	}
	.chart-body :global(.tunnel-delay-spark--compact) {
		height: 36px;
	}
	.traffic-head {
		margin-top: 8px;
	}

	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}
</style>
