<script lang="ts">
	import type { Subscription } from '$lib/types';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
	import { goto } from '$app/navigation';
	import { untrack } from 'svelte';
	import { singboxDelayHistory, singboxTraffic, triggerDelayCheck } from '$lib/stores/singbox';
	import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
	import { Badge, TrafficSparkline, PingButton } from '$lib/components/ui';
	import { singboxDelayFromHistory } from '$lib/utils/singboxDelay';
	import { formatBytes } from '$lib/utils/format';
	import { resolveSubscriptionMemberTag } from '$lib/utils/subscriptionMember';
	import TunnelDiagnosticsModal from '$lib/components/testing/TunnelDiagnosticsModal.svelte';

	interface Props {
		subscription: Subscription;
		liveActiveMember?: string | null;
		layout?: SingboxLayoutMode;
		ondelete?: (id: string) => void;
		ondetail?: (tag: string) => void;
	}
	let { subscription, liveActiveMember = null, layout = 'compact', ondelete, ondetail }: Props = $props();

	const resolvedMemberTag = $derived(resolveSubscriptionMemberTag(subscription, liveActiveMember));

	const history = $derived(
		resolvedMemberTag ? ($singboxDelayHistory.get(resolvedMemberTag) ?? []) : [],
	);
	const delayPresentation = $derived(
		resolvedMemberTag ? singboxDelayFromHistory(history) : { state: 'unknown' as const, label: '—', latest: undefined },
	);
	const delayState = $derived(delayPresentation.state);
	const delayText = $derived(delayPresentation.label);
	const latest = $derived(delayPresentation.latest ?? -1);

	const traffic = $derived(resolvedMemberTag ? $singboxTraffic.get(resolvedMemberTag) : undefined);

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

	function requestDelete(e: MouseEvent): void {
		e.stopPropagation();
		ondelete?.(subscription.id);
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
	const endpointText = $derived(
		resolvedMember ? `${resolvedMember.server}:${resolvedMember.port}` : '',
	);
	let showEndpoint = $state(false);
	let diagnosticsUnavailableReason = $derived(
		!selectorTag || !kernelIface
			? 'Для подписки не удалось определить интерфейс тестирования.'
			: undefined,
	);

	function openDiagnostics(e: MouseEvent): void {
		e.preventDefault();
		e.stopPropagation();
		diagnosticsOpen = true;
	}

	function stopNestedAction(e: Event): void {
		e.stopPropagation();
	}

	function stopNestedActionKeydown(e: KeyboardEvent): void {
		e.stopPropagation();
	}

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

{#if layout === 'list'}
	<div class="sub-list-group" class:err={status === 'error'} class:off={!subscription.enabled}>
		<div
			role="button"
			tabindex="0"
			class="sbx-sub-active-row"
			onclick={(e) => open(e)}
			onkeydown={(e) => {
				if (e.key === 'Enter' || e.key === ' ') {
					e.preventDefault();
					open(e);
				}
			}}
		>
			<div class="lc lc-delay" data-label="Delay">
				{#if subscription.lastError}
					<span class="dot fail" aria-hidden="true"></span>
					<span class="delay-inline-err mono" title={subscription.lastError}>
						{subscription.lastError}
					</span>
				{:else if !subscription.enabled}
					<span class="dot unknown" aria-hidden="true"></span>
					<span class="delay-dash">—</span>
				{:else if resolvedMemberTag}
					<span class="dot {delayState}" aria-hidden="true"></span>
					<PingButton
						label={delayText}
						state={delayState}
						checking={testingDelay}
						size="sm"
						onclick={runDelayCheck}
					/>
				{:else}
					<span class="delay-dash">—</span>
				{/if}
			</div>
			<div class="lc lc-name" data-label="Подписка">
				<div class="name-title-row">
					<div class="t1">{subscription.label || subscription.url}</div>
					<Badge variant="accent" size="sm">{sourceKindLabel}</Badge>
				</div>
				<div class="t2 mono">{proxyIface}{#if kernelIface} · {kernelIface}{/if}</div>
			</div>
			<div class="lc lc-mode" data-label="Режим">
				{isURLTest ? 'URLTest' : 'Selector'}
			</div>
			<div class="lc lc-endpoint" data-label="Активный сервер">
				{#if !subscription.enabled}
					<span class="off-label">выкл</span>
				{:else if resolvedMember && endpointText}
					<div class="lc-endpoint-stack">
						{#if listActiveServerName}
							<span class="lc-endpoint-name" title={listActiveServerName}>{listActiveServerName}</span>
						{/if}
						<span class="lc-endpoint-host mono">
							{#if showEndpoint}{endpointText}{:else}••••••••{/if}
						</span>
					</div>
					<button
						type="button"
						class="eye-mini"
						onclick={(e) => {
							e.stopPropagation();
							showEndpoint = !showEndpoint;
						}}
						aria-label={showEndpoint ? 'Скрыть' : 'Показать'}
					>
						{#if showEndpoint}
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
						{:else}
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
						{/if}
					</button>
				{:else}
					<span class="delay-dash">—</span>
				{/if}
			</div>
			<div class="lc lc-members" data-label="Серверов">
				{subscription.memberTags.length}
			</div>
			<div class="lc lc-updated mono" data-label="Обновлено">
				{lastFetchedHuman}
			</div>
			<div class="lc lc-traffic" data-label="Трафик">
				{#if subscription.lastError || !subscription.enabled}
					<span class="delay-dash">—</span>
				{:else if resolvedMemberTag}
					<div class="traffic-row-list">
						<div
							role="button"
							tabindex="0"
							class="traffic-mini-click"
							onclick={(e) => {
								e.stopPropagation();
								ondetail?.(resolvedMemberTag);
							}}
							onkeydown={(e) => {
								if (e.key === 'Enter' || e.key === ' ') {
									e.preventDefault();
									e.stopPropagation();
									ondetail?.(resolvedMemberTag);
								}
							}}
							title="Открыть детальный график"
						>
							<TrafficSparkline
								rxData={trafficSparkSeries.rx}
								txData={trafficSparkSeries.tx}
								width={84}
								height={22}
							/>
						</div>
						<div class="traffic-mini-col mono">
							<span class="traffic-rate rx">↓ {formatBytes(traffic?.download ?? 0)}</span>
							<span class="traffic-rate tx">↑ {formatBytes(traffic?.upload ?? 0)}</span>
						</div>
					</div>
				{:else}
					<span class="delay-dash">—</span>
				{/if}
			</div>
			<div class="lc lc-ping-mini" data-label="Ping">
				{#if subscription.lastError || !subscription.enabled}
					<span class="delay-dash">—</span>
				{:else if resolvedMemberTag}
					<div class="spark-mini {delayState}" title="Delay за последние проверки">
						{#if history.length === 0}
							{#each Array(10) as _, i (i)}
								<div class="bar empty"></div>
							{/each}
						{:else}
							{@const max = Math.max(...history.map((v) => (v <= 0 ? 100 : v)), 100)}
							{#each history.slice(-14) as d, i (i)}
								<div class="bar" style="height: {Math.max((d <= 0 ? max : d) / max, 0.08) * 100}%;"></div>
							{/each}
						{/if}
					</div>
				{:else}
					<span class="delay-dash">—</span>
				{/if}
			</div>
			<div class="lc lc-actions" data-label="">
				<button
					type="button"
					class="action-btn"
					title="Открыть подписку «{subscription.label || subscription.url}»"
					aria-label="Открыть подписку «{subscription.label || subscription.url}»"
					onclick={(e) => {
						e.stopPropagation();
						open();
					}}
				>
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
						<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
					</svg>
				</button>
				<button
					type="button"
					class="action-btn action-test"
					title="Открыть диагностику подписки «{subscription.label || subscription.url}»"
					aria-label="Открыть диагностику подписки «{subscription.label || subscription.url}»"
					data-diagnostics-action="true"
					onpointerdown={stopNestedAction}
					onmousedown={stopNestedAction}
					onclick={openDiagnostics}
					onkeydown={(e) => {
						if (e.key === 'Enter' || e.key === ' ') openDiagnostics(e);
						else e.stopPropagation();
					}}
				>
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
						<polyline points="22,4 12,14.01 9,11.01"/>
					</svg>
				</button>
				{#if ondelete}
					<button
						type="button"
						class="action-btn action-danger"
						title="Удалить подписку «{subscription.label || subscription.url}»"
						aria-label="Удалить подписку «{subscription.label || subscription.url}»"
						onclick={requestDelete}
					>
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<polyline points="3,6 5,6 21,6"/>
							<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
						</svg>
					</button>
				{/if}
			</div>
		</div>
	</div>
{:else if layout === 'dense'}
<div
	role="button"
	tabindex="0"
	class="card view-dense inactive-panel"
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
		<span
			class="status-badge status-badge-dense"
			class:status-off={!subscription.enabled}
			class:status-error={subscription.enabled && status === 'error'}
			class:status-ok={subscription.enabled && status === 'ok'}
			class:status-pending={subscription.enabled && status === 'pending'}
		>
			{feedStatusLabel}
		</span>
	</div>
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
		<div class="inactive-err mono" title={subscription.lastError}>{subscription.lastError}</div>
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
				<span class="detail-value mono" title={subscription.lastError}>{subscription.lastError}</span>
			</div>
		{/if}
	</div>

	<div class="inactive-actions">
		<div class="actions">
			<button
				type="button"
				class="action-btn"
				title="Открыть подписку «{subscription.label || subscription.url}»"
				onclick={(e) => {
					e.stopPropagation();
					open();
				}}
			>
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
					<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
				</svg>
				Открыть подписку
			</button>
			<button
				type="button"
				class="action-btn action-test"
				title="Открыть диагностику подписки «{subscription.label || subscription.url}»"
				onpointerdown={stopNestedAction}
				onmousedown={stopNestedAction}
				onclick={openDiagnostics}
				onkeydown={stopNestedActionKeydown}
			>
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
					<polyline points="22,4 12,14.01 9,11.01"/>
				</svg>
				Тест
			</button>
			{#if ondelete}
				<button
					type="button"
					class="action-btn action-danger"
					title="Удалить подписку «{subscription.label || subscription.url}»"
					onclick={requestDelete}
				>
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<polyline points="3,6 5,6 21,6"/>
						<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
					</svg>
					Удалить
				</button>
			{/if}
		</div>
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
	.card.ok { border-color: var(--color-success-border); }
	.card.slow { border-color: var(--color-warning-border); }
	.card.fail { border-color: var(--color-error-border); }
	.card.unknown { border-color: var(--color-border); }
	.card.off { border-color: var(--color-muted-border); opacity: 0.72; }
	.card.panel {
		gap: 0;
		padding: 12px 14px;
		cursor: default;
	}
	.card.panel.inactive-panel {
		border: 1px dashed color-mix(in srgb, var(--color-text-muted) 38%, transparent);
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

	.name-title-row {
		display: flex;
		align-items: center;
		gap: 5px;
		min-width: 0;
		max-width: 100%;
	}

	.name-title-row .t1 {
		flex: 0 1 auto;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.name-title-row :global(.badge) {
		flex-shrink: 0;
		font-size: 10px;
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
	.inactive-actions {
		margin-top: 12px;
		padding-top: 12px;
		border-top: 1px solid var(--color-border);
	}
	.inactive-panel .actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		justify-content: flex-end;
		align-items: center;
	}
	.inactive-panel .action-btn {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 5px 9px;
		font-size: var(--sbx-card-action);
		font-weight: 500;
		border: none;
		background: transparent;
		color: var(--color-text-secondary);
		cursor: pointer;
		border-radius: var(--radius-sm);
		font-family: inherit;
		transition: background var(--t-fast) ease, color var(--t-fast) ease;
	}
	.inactive-panel .action-btn:hover:not(:disabled) {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}
	.inactive-panel .action-btn.action-test:hover:not(:disabled) {
		color: var(--color-success);
		background: var(--color-success-tint);
	}
	.inactive-panel .action-btn.action-danger:hover:not(:disabled) {
		color: var(--color-error);
		background: var(--color-error-tint);
	}
	.sub-list-group {
		border-bottom: 1px solid var(--color-border);
	}
	.sub-list-group:last-child {
		border-bottom: none;
	}
	.sub-list-group.off .sbx-sub-active-row {
		opacity: 0.72;
	}
	.sub-list-group.err .sbx-sub-active-row {
		background: rgba(248, 81, 73, 0.04);
	}
	.sbx-sub-active-row {
		display: grid;
		grid-template-columns:
			minmax(92px, 1fr)
			minmax(132px, 1.1fr)
			minmax(52px, 0.9fr)
			minmax(162px, 1fr)
			minmax(60px, 0.85fr)
			minmax(100px, 1.05fr)
			minmax(148px, 1.1fr)
			minmax(120px, 0.95fr)
			minmax(76px, 0.7fr);
		gap: 0.75rem 1rem;
		align-items: center;
		padding: 0.75rem 1rem;
		cursor: pointer;
		min-width: 940px;
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
	.t1 {
		font-weight: 600;
		font-size: var(--sbx-card-title);
		color: var(--color-text-primary);
	}
	.t2 {
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
	}
	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}
	.lc-endpoint {
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
	.lc-endpoint-host {
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
	}
	.off-label {
		font-size: var(--sbx-card-badge);
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
	}
	.eye-mini {
		flex-shrink: 0;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 22px;
		height: 22px;
		padding: 0;
		border: none;
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		border-radius: 4px;
	}
	.eye-mini:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-tertiary);
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}
	.dot.ok { background: #3fb950; }
	.dot.slow { background: #d29922; }
	.dot.fail { background: #f85149; }
	.dot.unknown { background: var(--color-text-muted); }
	.lc-actions {
		flex-wrap: nowrap;
		gap: 0.375rem;
		justify-content: flex-end;
		align-items: center;
		white-space: nowrap;
	}
	.action-btn {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 5px 9px;
		font-size: var(--sbx-card-action);
		font-weight: 500;
		border: none;
		background: transparent;
		color: var(--color-text-secondary);
		cursor: pointer;
		border-radius: var(--radius-sm);
		text-decoration: none;
		font-family: inherit;
		transition: background var(--t-fast) ease, color var(--t-fast) ease;
	}
	.lc-actions .action-btn {
		justify-content: center;
		padding: 0.375rem;
	}
	.action-btn:hover:not(:disabled) {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}
	.action-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.action-btn.action-danger:hover:not(:disabled) {
		color: var(--color-error);
		background: var(--color-error-tint);
	}
	.action-btn.action-test:hover:not(:disabled) {
		color: var(--color-success);
		background: var(--color-success-tint);
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
	.spark-mini {
		display: flex;
		align-items: flex-end;
		gap: 1px;
		height: 20px;
		width: 100%;
		max-width: 82px;
	}
	.spark-mini .bar {
		flex: 1;
		min-width: 0;
		min-height: 2px;
		border-radius: 1px;
		background: var(--color-bg-tertiary);
	}
	.spark-mini.ok .bar {
		background: var(--latency-bar-ok);
	}
	.spark-mini.slow .bar {
		background: var(--latency-bar-slow);
	}
	.spark-mini.fail .bar {
		background: var(--latency-bar-fail);
	}
	.spark-mini.unknown .bar,
	.spark-mini .bar.empty {
		opacity: 0.35;
		height: 30% !important;
	}
	.traffic-row-list {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		min-width: 0;
	}
	.traffic-mini-col {
		display: flex;
		flex-direction: column;
		gap: 0.08rem;
		font-size: var(--sbx-card-note);
		line-height: 1.15;
		flex-shrink: 0;
	}
	.traffic-mini-click {
		display: inline-flex;
		border-radius: 4px;
		cursor: pointer;
		transition: background var(--t-fast) ease;
	}
	.traffic-mini-click:hover {
		background: rgba(96, 165, 250, 0.06);
	}
	.traffic-mini-click:focus-visible {
		outline: 1px solid var(--color-accent, #58a6ff);
		outline-offset: 1px;
	}
	.mono { font-family: var(--font-mono, ui-monospace, monospace); }
</style>
