<script lang="ts">
	import type { Subscription } from '$lib/types';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
	import { goto } from '$app/navigation';
	import { untrack } from 'svelte';
	import { singboxDelayHistory, triggerDelayCheck } from '$lib/stores/singbox';
	import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
	import { Badge, TunnelListActions } from '$lib/components/ui';
	import {
		TunnelDelaySparkBars,
		TunnelListEndpointLine,
		TunnelListTrafficCell,
		TunnelMetaText,
		TunnelSingboxPingButton,
		TunnelTitleRow,
	} from '$lib/components/tunnels';
	import { singboxDelayFromHistory } from '$lib/utils/singboxDelay';
	import { singboxDelayStatusDot } from '$lib/utils/statusDot';
	import { resolveSubscriptionMemberTag } from '$lib/utils/subscriptionMember';
	import { humanizeDownloadError } from '$lib/utils/downloadError';
	import TunnelDiagnosticsModal from '$lib/components/testing/TunnelDiagnosticsModal.svelte';

	interface Props {
		subscription: Subscription;
		liveActiveMember?: string | null;
		layout?: SingboxLayoutMode;
		renderMode?: import('$lib/constants/singboxLayout').TunnelRenderMode;
		ondelete?: (id: string) => void;
		ondetail?: (tag: string) => void;
	}
	let {
		subscription,
		liveActiveMember = null,
		layout = 'compact',
		renderMode = 'compact',
		ondelete,
		ondetail,
	}: Props = $props();

	const resolvedMemberTag = $derived(resolveSubscriptionMemberTag(subscription, liveActiveMember));
	const lastErrorInfo = $derived(
		subscription.lastError ? humanizeDownloadError(subscription.lastError) : null,
	);

	const history = $derived(
		resolvedMemberTag ? ($singboxDelayHistory.get(resolvedMemberTag) ?? []) : [],
	);
	const delayPresentation = $derived(
		resolvedMemberTag ? singboxDelayFromHistory(history) : { state: 'unknown' as const, label: '—', latest: undefined },
	);
	const delayState = $derived(delayPresentation.state);
	const delayText = $derived(delayPresentation.label);
	const statusDot = $derived.by(() => {
		if (subscription.lastError) {
			return { variant: 'error' as const, pulse: false, label: 'error' };
		}
		if (!subscription.enabled) {
			return { variant: 'muted' as const, pulse: false, label: 'off' };
		}
		if (resolvedMemberTag) {
			return singboxDelayStatusDot(delayState, true);
		}
		return { variant: 'muted' as const, pulse: false, label: 'unknown' };
	});
	const trafficSparkSeries = $derived.by(() => {
		const n = Math.min(rxRates.length, txRates.length);
		if (n === 0) return { rx: [] as number[], tx: [] as number[] };
		const take = Math.min(36, n);
		const start = n - take;
		return {
			rx: rxRates.slice(start, n),
			tx: txRates.slice(start, n),
		};
	});

	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);
	let trafficTag = $derived(resolvedMemberTag);
	const inlineRxRate = $derived(rxRates.length > 0 ? rxRates[rxRates.length - 1] : 0);
	const inlineTxRate = $derived(txRates.length > 0 ? txRates[txRates.length - 1] : 0);

	$effect(() => {
		const tag = trafficTag;
		const update = () => {
			if (!tag) {
				rxRates = [];
				txRates = [];
				return;
			}
			const t = getTrafficRates(tag);
			rxRates = t.rx;
			txRates = t.tx;
		};
		update();
		if (!tag) return () => {};
		return subscribeTraffic(update);
	});

	$effect(() => {
		const tag = trafficTag;
		if (!tag) return;
		untrack(() => loadHistory(tag));
	});

	let testingDelay = $state(false);

	async function runDelayCheck(e?: MouseEvent | KeyboardEvent): Promise<void> {
		e?.stopPropagation();
		if (!resolvedMemberTag || testingDelay) return;
		testingDelay = true;
		try {
			await triggerDelayCheck(resolvedMemberTag);
		} finally {
			testingDelay = false;
		}
	}
	function isNestedActionEvent(e: Event): boolean {
		const target = e.target;
		if (!(target instanceof HTMLElement)) return false;
		return target.closest('button,a,input,select,textarea') !== null;
	}

	function open(e?: MouseEvent | KeyboardEvent): void {
		if (e && isNestedActionEvent(e)) return;
		goto(`/subscriptions/${subscription.id}`);
	}

	let diagnosticsOpen = $state(false);

	let selectorTag = $derived(subscription.selectorTag ?? '');
	const proxyIface = $derived(subscription.proxyIndex >= 0 ? `Proxy${subscription.proxyIndex}` : '');
	let kernelIface = $derived(subscription.proxyIndex >= 0 ? `t2s${subscription.proxyIndex}` : '');
	const isURLTest = $derived(subscription.mode === 'urltest');
	const resolvedMember = $derived(
		subscription.members?.find((m) => m.tag === resolvedMemberTag) ?? null,
	);
	const listActiveServerName = $derived(
		resolvedMember?.label?.trim() || resolvedMember?.tag?.trim() || '',
	);
	let showEndpoint = $state(false);
	let diagnosticsUnavailableReason = $derived(
		!selectorTag || !kernelIface
			? 'Для подписки не удалось определить интерфейс тестирования.'
			: undefined,
	);

	const status = $derived(
		subscription.lastError ? 'error' : subscription.lastFetched ? 'ok' : 'pending',
	);
	const feedStatusLabel = $derived(
		!subscription.enabled ? 'Выключена' : subscription.lastError ? 'Ошибка' : 'OK',
	);
	const modeLabel = $derived(subscription.mode === 'urltest' ? 'URLTest' : 'Selector');
	const isInlineGroup = $derived(subscription.isInline || !subscription.url?.trim());
	const sourceKindLabel = $derived(isInlineGroup ? 'группа' : 'подписка');
	const lastFetchedHuman = $derived(
		subscription.lastFetched ? formatRelative(subscription.lastFetched) : '—',
	);

	function formatRelative(iso: string): string {
		const d = new Date(iso);
		const diff = Date.now() - d.getTime();
		const hours = Math.floor(diff / 3_600_000);
		if (hours < 1) return 'только что';
		if (hours < 24) return `${hours}ч назад`;
		return `${Math.floor(hours / 24)}д назад`;
	}
</script>

{#if renderMode === 'table'}
	<tr
		role="button"
		tabindex="0"
		class="sbx-sub-active-row"
		class:err={status === 'error'}
		class:off={!subscription.enabled}
		onclick={(e) => open(e)}
		onkeydown={(e) => {
			if (e.key === 'Enter' || e.key === ' ') {
				e.preventDefault();
				open(e);
			}
		}}
	>
			<td class="tunnel-list-cell tunnel-list-cell--delay lc lc-delay" data-label="Delay">
				{#if subscription.lastError}
					<span class="delay-inline-err mono" title={lastErrorInfo?.raw}>
						{lastErrorInfo?.title ?? subscription.lastError}
					</span>
				{:else if !subscription.enabled}
					<span class="delay-dash">—</span>
				{:else if resolvedMemberTag}
					<TunnelSingboxPingButton
						layout="list"
						label={delayText}
						state={delayState}
						checking={testingDelay}
						onclick={runDelayCheck}
					/>
				{:else}
					<span class="delay-dash">—</span>
				{/if}
			</td>
			<td class="tunnel-list-cell tunnel-list-cell--name lc lc-name" data-label="Подписка">
				<div class="tunnel-list-name-stack">
					<TunnelTitleRow
						title={subscription.label || subscription.url}
						dotVariant={statusDot.variant}
						dotPulse={statusDot.pulse}
						staticTitle
					/>
					<TunnelMetaText>
						<span>{subscription.memberTags.length} серверов</span>
						<span class="meta-dot" aria-hidden="true">·</span>
						<span>{lastFetchedHuman}</span>
					</TunnelMetaText>
					<TunnelMetaText mono>
						{#if proxyIface}
							<span>{proxyIface}</span>
							{#if kernelIface}<span class="meta-dot" aria-hidden="true">·</span><span>{kernelIface}</span>{/if}
							<span class="meta-dot" aria-hidden="true">·</span>
						{:else if subscription.inboundTag}
							<span>{subscription.inboundTag}</span>
							<span class="meta-dot" aria-hidden="true">·</span>
						{/if}
						<span>{modeLabel}</span>
					</TunnelMetaText>
				</div>
			</td>
			<td class="tunnel-list-cell tunnel-list-cell--endpoint lc lc-endpoint" data-label="Активный сервер">
				{#if !subscription.enabled}
					<span class="off-label">выкл</span>
				{:else if resolvedMember}
					<div class="lc-endpoint-stack">
						{#if listActiveServerName}
							<span class="lc-endpoint-name" title={listActiveServerName}>{listActiveServerName}</span>
						{/if}
						<TunnelListEndpointLine
							host={resolvedMember.server}
							port={resolvedMember.port}
							bind:show={showEndpoint}
						/>
					</div>
				{:else}
					<span class="delay-dash">—</span>
				{/if}
			</td>
			<td
				class="tunnel-list-cell tunnel-list-cell--traffic lc lc-traffic"
				data-label="Трафик"
				onclick={(e) => e.stopPropagation()}
			>
				{#if subscription.lastError || !subscription.enabled}
					<span class="delay-dash">—</span>
				{:else if resolvedMemberTag}
					<TunnelListTrafficCell
						rxRate={inlineRxRate}
						txRate={inlineTxRate}
						rxData={trafficSparkSeries.rx}
						txData={trafficSparkSeries.tx}
						onclick={() => ondetail?.(resolvedMemberTag)}
						title="Открыть детальный график"
					/>
				{:else}
					<span class="delay-dash">—</span>
				{/if}
			</td>
			<td
				class="tunnel-list-cell tunnel-list-cell--ping lc"
				data-label="Ping"
				onclick={(e) => e.stopPropagation()}
			>
				{#if subscription.lastError || !subscription.enabled}
					<span class="delay-dash">—</span>
				{:else if resolvedMemberTag}
					<TunnelDelaySparkBars
						{history}
						state={delayState}
						layout="list"
						title="Delay за последние проверки"
					/>
				{:else}
					<span class="delay-dash">—</span>
				{/if}
			</td>
			<td
				class="tunnel-list-cell tunnel-list-cell--actions lc lc-actions col-actions"
				data-label=""
				onclick={(e) => e.stopPropagation()}
			>
				<TunnelListActions
					onEdit={() => open()}
					editLabel="Открыть"
					editTitle="Открыть подписку «{subscription.label || subscription.url}»"
					onTest={() => (diagnosticsOpen = true)}
					testTitle="Открыть диагностику подписки «{subscription.label || subscription.url}»"
					onDelete={ondelete ? () => ondelete(subscription.id) : undefined}
					deleteTitle="Удалить подписку «{subscription.label || subscription.url}»"
				/>
			</td>
	</tr>
{:else if layout === 'dense' || renderMode === 'list-card'}
{@const denseCardClickProps = renderMode === 'list-card'
	? {}
	: {
			role: 'button' as const,
			tabindex: 0,
			onclick: (e: MouseEvent) => open(e),
			onkeydown: (e: KeyboardEvent) => {
				if (e.key === 'Enter' || e.key === ' ') {
					e.preventDefault();
					open(e);
				}
			},
		}}
<div
	class="card inactive-panel"
	class:view-dense={renderMode !== 'list-card'}
	class:view-list={renderMode === 'list-card'}
	class:err={status === 'error'}
	class:off={!subscription.enabled}
	{...denseCardClickProps}
>
	<div class="inactive-header-dense">
		<div class="inactive-header-main">
			<div class="inactive-title-row">
				<h3 class="inactive-title">{subscription.label || subscription.url}</h3>
				<Badge variant="accent" size="sm">{sourceKindLabel}</Badge>
			</div>
			<div class="inactive-meta-dense mono">
				{#if proxyIface}
					<span>{proxyIface}</span>
					{#if kernelIface}<span class="meta-dot" aria-hidden="true">·</span><span>{kernelIface}</span>{/if}
				{:else}
					<span>{subscription.inboundTag}</span>
				{/if}
				<span class="meta-dot" aria-hidden="true">·</span><span>:{subscription.listenPort}</span>
				<span class="meta-dot" aria-hidden="true">·</span><span>{subscription.memberTags.length} серв.</span>
			</div>
		</div>
		{#if subscription.enabled && !subscription.lastError && resolvedMemberTag}
			<TunnelSingboxPingButton
				layout="dense"
				label={delayText}
				state={delayState}
				checking={testingDelay}
				onclick={runDelayCheck}
			/>
		{:else}
			<span
				class="status-badge status-badge-dense"
				class:status-off={!subscription.enabled}
				class:status-error={subscription.enabled && status === 'error'}
				class:status-ok={subscription.enabled && status === 'ok'}
				class:status-pending={subscription.enabled && status === 'pending'}
			>
				{feedStatusLabel}
			</span>
		{/if}
	</div>
	{#if renderMode !== 'list-card'}
	<hr class="divider" />
	<div class="inactive-meta-dense secondary mono">
		<span>{modeLabel}</span>
		<span class="meta-dot" aria-hidden="true">·</span>
		<span>обновлено {lastFetchedHuman}</span>
		{#if subscription.activeMember}
			<span class="meta-dot" aria-hidden="true">·</span>
			<span>{subscription.activeMember}</span>
		{/if}
	</div>
	{#if subscription.lastError}
		<div class="inactive-err mono" title={lastErrorInfo?.raw}>{lastErrorInfo?.title ?? subscription.lastError}</div>
	{/if}
	{/if}
	{#if renderMode === 'list-card'}
	<div class="actions">
		<TunnelListActions
			variant="labeled"
			onEdit={() => open()}
			editLabel="Открыть"
			editTitle="Открыть подписку «{subscription.label || subscription.url}»"
			onTest={() => (diagnosticsOpen = true)}
			testTitle="Открыть диагностику подписки «{subscription.label || subscription.url}»"
			onDelete={ondelete ? () => ondelete(subscription.id) : undefined}
			deleteTitle="Удалить подписку «{subscription.label || subscription.url}»"
		/>
	</div>
	{/if}
</div>
{:else}
<div
	class="card panel inactive-panel view-compact"
	class:err={status === 'error'}
	class:off={!subscription.enabled}
>
	<div class="inactive-header">
		<div class="inactive-header-main">
			<h3 class="inactive-title">{subscription.label || subscription.url}</h3>
			<div class="inactive-meta-line">
				{#if proxyIface}
					<span class="inactive-iface mono">{proxyIface}{#if kernelIface} · {kernelIface}{/if}</span>
				{:else}
					<span class="inactive-iface mono">{subscription.inboundTag}</span>
				{/if}
				<span class="inactive-kind">{modeLabel}</span>
			</div>
			<div class="inactive-note">
				<Badge variant="accent" size="sm">{sourceKindLabel}</Badge>
				<span class="meta-dot" aria-hidden="true">·</span>
				:{subscription.listenPort}
			</div>
		</div>
		<div class="inactive-status-wrap">
			<span
				class="status-badge"
				class:status-off={!subscription.enabled}
				class:status-error={subscription.enabled && status === 'error'}
				class:status-ok={subscription.enabled && status === 'ok'}
				class:status-pending={subscription.enabled && status === 'pending'}
			>
				<span class="led-dot" aria-hidden="true"></span>
				{feedStatusLabel}
			</span>
		</div>
	</div>

	<div class="inactive-details">
		<div class="detail-row">
			<span class="detail-label">Серверов</span>
			<span class="detail-value">{subscription.memberTags.length}</span>
		</div>
		<div class="detail-row">
			<span class="detail-label">Активный</span>
			<span class="detail-value mono">{subscription.activeMember || '—'}</span>
		</div>
		<div class="detail-row">
			<span class="detail-label">Обновлено</span>
			<span class="detail-value">{lastFetchedHuman}</span>
		</div>
		{#if subscription.refreshHours > 0}
			<div class="detail-row">
				<span class="detail-label">Авто-обновление</span>
				<span class="detail-value">каждые {subscription.refreshHours} ч</span>
			</div>
		{/if}
		{#if subscription.lastError}
			<div class="detail-row detail-row-err">
				<span class="detail-label">Ошибка</span>
				<span class="detail-value mono" title={lastErrorInfo?.raw}>{lastErrorInfo?.title ?? subscription.lastError}</span>
			</div>
		{/if}
	</div>

	<div class="actions actions--bar-top">
		<TunnelListActions
			variant="labeled"
			onEdit={() => open()}
			editLabel="Открыть"
			editTitle="Открыть подписку «{subscription.label || subscription.url}»"
			onTest={() => (diagnosticsOpen = true)}
			testTitle="Открыть диагностику подписки «{subscription.label || subscription.url}»"
			onDelete={ondelete ? () => ondelete(subscription.id) : undefined}
			deleteTitle="Удалить подписку «{subscription.label || subscription.url}»"
		/>
	</div>
</div>
{/if}

<TunnelDiagnosticsModal
	open={diagnosticsOpen}
	kind="subscription"
	targetId={selectorTag}
	displayName={subscription.label || selectorTag || subscription.id}
	subjectLabel="подписку"
	iface={kernelIface}
	loading={false}
	unavailableReason={diagnosticsUnavailableReason}
	onclose={() => (diagnosticsOpen = false)}
/>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 12px;
		padding: 12px 14px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		font: inherit;
		text-align: left;
		color: var(--color-text-primary);
		cursor: pointer;
		transition: border-color var(--t-fast) ease;
	}
	.card.off { border-color: var(--color-muted-border); opacity: 0.72; }
	.card.panel {
		gap: 0;
		padding: 12px 14px;
		cursor: default;
	}
	.card.panel.inactive-panel {
		border: 1px dashed color-mix(in srgb, var(--color-text-muted) 38%, transparent);
	}
	.card.inactive-panel:hover {
		border-color: var(--color-accent);
	}
	.card.panel.inactive-panel.off {
		opacity: 1;
	}
	.card.panel.inactive-panel.err {
		border-color: color-mix(in srgb, var(--color-error) 45%, transparent);
	}
	.card.view-dense.inactive-panel {
		gap: 6px;
		padding: 10px 12px;
		cursor: pointer;
		text-align: left;
		font: inherit;
		color: inherit;
	}
	.card.view-list.inactive-panel {
		cursor: default;
	}
	.inactive-header-dense {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 8px;
	}
	.card.view-dense .inactive-title {
		font-size: 13px;
	}
	.inactive-meta-dense {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		font-size: 9px;
		color: var(--color-text-muted);
		line-height: 1.3;
	}

	.meta-dot {
		margin: 0 0.35em;
		opacity: 0.75;
	}

	.inactive-title-row {
		display: flex;
		align-items: center;
		gap: 5px;
		min-width: 0;
		overflow: hidden;
	}

	.inactive-title-row .inactive-title {
		flex: 0 1 auto;
		min-width: 0;
	}

	.inactive-title-row :global(.badge) {
		flex-shrink: 0;
		font-size: 9px;
		padding: 1px 5px;
	}

	.card.view-dense .divider {
		border: none;
		border-top: 1px dashed var(--color-border);
		margin: 4px 0;
		height: 0;
		background: none;
	}
	.inactive-meta-dense.secondary {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		margin-top: 2px;
	}
	.status-badge-dense {
		font-size: 9px;
		padding: 1px 6px;
		flex-shrink: 0;
	}
	.inactive-err {
		font-size: 9px;
		color: var(--color-error);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.inactive-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 12px;
	}
	.inactive-header-main {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.inactive-title {
		margin: 0;
		font-size: var(--sbx-card-title);
		line-height: var(--sbx-card-title-line-height);
		font-weight: 600;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.inactive-meta-line {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px;
	}
	.inactive-iface {
		font-size: var(--sbx-card-meta);
		font-family: var(--font-mono, ui-monospace, monospace);
		color: var(--color-text-muted);
	}
	.inactive-kind {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: var(--sbx-card-badge);
		font-weight: 500;
		border-radius: 10px;
		background: rgba(88, 166, 255, 0.15);
		color: var(--color-accent);
	}
	.inactive-note {
		font-size: var(--sbx-card-note);
		color: var(--color-text-muted);
	}
	.inactive-status-wrap {
		flex-shrink: 0;
	}
	.status-badge {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 2px 10px;
		font-size: var(--sbx-card-status);
		font-weight: 500;
		border-radius: 10px;
	}
	.status-badge.status-ok {
		background: rgba(16, 185, 129, 0.15);
		color: var(--color-success, #10b981);
	}
	.status-badge.status-error {
		background: rgba(248, 81, 73, 0.15);
		color: var(--color-error, #f85149);
	}
	.status-badge.status-off,
	.status-badge.status-pending {
		background: rgba(148, 163, 184, 0.15);
		color: var(--color-text-muted);
	}
	.led-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: currentColor;
		flex-shrink: 0;
	}
	.inactive-details {
		display: flex;
		flex-direction: column;
		gap: 10px;
		margin-top: 12px;
		padding-top: 12px;
		border-top: 1px solid var(--color-border);
	}
	.detail-row {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}
	.detail-label {
		font-size: var(--sbx-card-label);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
	}
	.detail-value {
		font-size: var(--sbx-card-value);
		font-family: var(--font-mono, ui-monospace, monospace);
		color: var(--color-text-secondary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.detail-row-err .detail-value {
		color: var(--color-error, #f85149);
		white-space: normal;
		overflow-wrap: anywhere;
	}
	.sbx-sub-active-row {
		cursor: pointer;
	}
	.sbx-sub-active-row:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: -2px;
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
		gap: 0.35rem;
		min-width: 0;
	}
	.lc-name {
		flex-direction: column;
		align-items: flex-start;
		gap: 0.15rem;
	}
	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}
	.lc-endpoint {
		display: flex;
		align-items: center;
		gap: 0.35rem;
	}
	.lc-endpoint-stack {
		display: flex;
		flex-direction: column;
		min-width: 0;
		gap: 0.1rem;
	}
	.lc-endpoint-name {
		font-size: var(--sbx-card-value);
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.off-label {
		font-size: var(--sbx-card-badge);
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
	}
	.lc-actions {
		flex-wrap: nowrap;
		gap: 0.375rem;
		justify-content: center;
		align-items: center;
		white-space: nowrap;
	}
	.delay-inline-err {
		font-size: var(--sbx-card-badge);
		line-height: 1.25;
		color: #f85149;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		width: 100%;
	}
	.delay-dash {
		font-size: var(--sbx-card-value);
		color: var(--color-text-muted);
	}
</style>
