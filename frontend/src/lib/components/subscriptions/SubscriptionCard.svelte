<script lang="ts">
	import type { Subscription } from '$lib/types';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
	import { goto } from '$app/navigation';
	import { untrack } from 'svelte';
	import { singboxDelayHistory, singboxTraffic, triggerDelayCheck } from '$lib/stores/singbox';
	import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
	import { TrafficSparkline } from '$lib/components/ui';
	import { formatBytes } from '$lib/utils/format';

	interface Props {
		subscription: Subscription;
		layout?: SingboxLayoutMode;
		ondelete?: (id: string) => void;
	}
	let { subscription, layout = 'grid', ondelete }: Props = $props();

	const latencyTag = $derived.by(() => {
		if (subscription.activeMember && subscription.memberTags.includes(subscription.activeMember)) {
			return subscription.activeMember;
		}
		return subscription.memberTags[0] ?? '';
	});

	const history = $derived($singboxDelayHistory.get(latencyTag) ?? []);
	const latest = $derived(history.length > 0 ? history[history.length - 1] : -1);
	const hasConsecutiveTimeout = $derived(
		history.length >= 2 &&
			history[history.length - 1] <= 0 &&
			history[history.length - 2] <= 0,
	);

	const DELAY_OK = 200;
	const DELAY_SLOW = 500;

	const delayState = $derived.by((): 'ok' | 'slow' | 'fail' | 'unknown' => {
		if (!latencyTag) return 'unknown';
		if (latest < 0) return 'unknown';
		if (latest <= 0) return hasConsecutiveTimeout ? 'fail' : 'slow';
		if (latest < DELAY_OK) return 'ok';
		if (latest < DELAY_SLOW) return 'slow';
		return 'slow';
	});
	const delayText = $derived.by(() => {
		if (!latencyTag) return '—';
		if (delayState === 'unknown') return '—';
		if (delayState === 'fail') return 'timeout';
		if (latest <= 0) return '…';
		return `${latest}ms`;
	});

	const traffic = $derived(latencyTag ? $singboxTraffic.get(latencyTag) : undefined);

	const trafficSparkData = $derived.by(() => {
		const n = Math.min(rxRates.length, txRates.length);
		if (n === 0) return [];
		const take = Math.min(36, n);
		const out: number[] = [];
		for (let i = n - take; i < n; i++) {
			out.push(Math.max(0, rxRates[i] ?? 0) + Math.max(0, txRates[i] ?? 0));
		}
		return out;
	});

	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);
	let trafficTag = $derived(latencyTag);

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
		if (!latencyTag || testingDelay) return;
		testingDelay = true;
		try {
			await triggerDelayCheck(latencyTag);
		} finally {
			testingDelay = false;
		}
	}
	function onDelayKeydown(e: KeyboardEvent): void {
		if (e.key === 'Enter' || e.key === ' ') {
			e.preventDefault();
			e.stopPropagation();
			void runDelayCheck(e);
		}
	}

	function open(): void {
		goto(`/subscriptions/${subscription.id}`);
	}

	function requestDelete(e: MouseEvent): void {
		e.stopPropagation();
		ondelete?.(subscription.id);
	}

	const status = $derived(
		subscription.lastError ? 'error' : subscription.lastFetched ? 'ok' : 'pending',
	);
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
	<div class="sub-list-group" class:err={status === 'error'}>
		<div
			role="button"
			tabindex="0"
			class="sub-list-click"
			onclick={open}
			onkeydown={(e) => {
				if (e.key === 'Enter' || e.key === ' ') {
					e.preventDefault();
					open();
				}
			}}
		>
			<div class="sbx-sub-inactive-row">
				<div class="list-cell" data-label="Статус">
					<div class="badge {status}">
						{#if status === 'ok'}OK{:else if status === 'error'}Ошибка{:else}—{/if}
					</div>
				</div>
				<div class="list-cell list-cell-delay" data-label="Delay">
					{#if subscription.lastError}
						<span class="delay-inline-err mono" title={subscription.lastError}>
							{subscription.lastError}
						</span>
					{:else if latencyTag}
						<button
							type="button"
							class="lat-btn {delayState}"
							class:is-checking={testingDelay}
							disabled={testingDelay}
							onclick={runDelayCheck}
							onkeydown={onDelayKeydown}
							title="Обновить delay"
						>
							{testingDelay ? '...' : delayText}
						</button>
					{:else}
						<span class="delay-dash">—</span>
					{/if}
				</div>
				<div class="list-cell list-name" data-label="Подписка">
					<div class="label-strong">{subscription.label || subscription.url}</div>
					<div class="meta mono">{subscription.inboundTag} · :{subscription.listenPort}</div>
				</div>
				<div class="list-cell" data-label="Серверов">
					{subscription.memberTags.length}
				</div>
				<div class="list-cell mono" data-label="Активен">
					{subscription.activeMember || '—'}
				</div>
				<div class="list-cell list-cell-traffic" data-label="Трафик">
					{#if subscription.lastError}
						<span class="delay-dash">—</span>
					{:else if latencyTag}
						<div class="traffic-row-list">
							<TrafficSparkline
								data={trafficSparkData}
								width={84}
								height={22}
								color="var(--color-accent)"
							/>
							<div class="traffic-mini-col mono">
								<span>↓ {formatBytes(traffic?.download ?? 0)}</span>
								<span>↑ {formatBytes(traffic?.upload ?? 0)}</span>
							</div>
						</div>
					{:else}
						<span class="delay-dash">—</span>
					{/if}
				</div>
				<div class="list-cell list-cell-ping" data-label="Ping">
					{#if subscription.lastError}
						<span class="delay-dash">—</span>
					{:else if latencyTag}
						<div
							class="spark-mini {delayState}"
							role="button"
							tabindex="0"
							onclick={(e) => runDelayCheck(e)}
							onkeydown={onDelayKeydown}
							title="Клик — обновить delay"
						>
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
				<div class="list-cell" data-label="Обновлено">
					{lastFetchedHuman}
				</div>
				<div class="list-cell list-actions" data-label="">
					{#if ondelete}
						<button
							type="button"
							class="card-remove"
							title="Удалить подписку"
							aria-label="Удалить подписку {subscription.label || subscription.url}"
							onclick={requestDelete}
						>
							<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<line x1="18" y1="6" x2="6" y2="18" />
								<line x1="6" y1="6" x2="18" y2="18" />
							</svg>
						</button>
					{/if}
				</div>
			</div>
		</div>
	</div>
{:else}
<div
	role="button"
	tabindex="0"
	class="card"
	class:panel={layout === 'grid'}
	class:err={status === 'error'}
	onclick={open}
	onkeydown={(e) => {
		if (e.key === 'Enter' || e.key === ' ') {
			e.preventDefault();
			open();
		}
	}}
>
	<div class="head">
		<div class="label">{subscription.label || subscription.url}</div>
		<div class="head-right">
			<div class="badge {status}">
				{#if status === 'ok'}OK{:else if status === 'error'}Ошибка{:else}—{/if}
			</div>
			{#if ondelete}
				<button
					type="button"
					class="card-remove"
					title="Удалить подписку"
					aria-label="Удалить подписку {subscription.label || subscription.url}"
					onclick={requestDelete}
				>
					<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<line x1="18" y1="6" x2="6" y2="18" />
						<line x1="6" y1="6" x2="18" y2="18" />
					</svg>
				</button>
			{/if}
		</div>
	</div>
	<div class="meta mono">{subscription.inboundTag} · :{subscription.listenPort}</div>
	<div class="info">
		{subscription.memberTags.length} серверов
		{#if subscription.activeMember}· активен <span class="mono">{subscription.activeMember}</span>{/if}
		· обновлено {lastFetchedHuman}
		{#if subscription.refreshHours > 0}· auto {subscription.refreshHours}ч{/if}
	</div>
	{#if subscription.lastError}
		<div class="err-msg mono">{subscription.lastError}</div>
	{/if}
</div>
{/if}

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		padding: 0.85rem 1rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		font: inherit;
		text-align: left;
		color: var(--color-text-primary);
		cursor: pointer;
	}
	.card.panel {
		padding: 16px;
		border-radius: 10px;
	}
	.sub-list-group {
		border-bottom: 1px solid var(--color-border);
	}
	.sub-list-group:last-child {
		border-bottom: none;
	}
	.sub-list-click {
		cursor: pointer;
	}
	.sub-list-click:focus-visible {
		outline: 2px solid var(--color-primary, #3b82f6);
		outline-offset: 2px;
	}
	.sbx-sub-inactive-row {
		display: grid;
		grid-template-columns:
			minmax(64px, 0.9fr)
			minmax(84px, 1fr)
			minmax(140px, 1.25fr)
			minmax(56px, 0.85fr)
			minmax(88px, 1fr)
			minmax(150px, 1.15fr)
			minmax(56px, 0.85fr)
			minmax(88px, 1fr)
			minmax(44px, 0.75fr);
		gap: 0.75rem 1rem;
		align-items: center;
		padding: 0.75rem 1rem;
		min-width: 960px;
	}
	.sub-list-group.err .sbx-sub-inactive-row {
		background: rgba(248, 81, 73, 0.04);
	}
	.sbx-sub-inactive-row .list-cell {
		display: flex;
		align-items: center;
		min-width: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}
	.list-name {
		flex-direction: column;
		align-items: flex-start !important;
		gap: 0.15rem;
	}
	.label-strong {
		font-weight: 600;
		font-size: 0.9375rem;
		color: var(--color-text-primary);
	}
	.sbx-sub-inactive-row .list-cell-delay {
		min-width: 0;
	}
	.delay-inline-err {
		font-size: 0.68rem;
		line-height: 1.25;
		color: #f85149;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		width: 100%;
	}
	.delay-dash {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}
	.lat-btn {
		padding: 0.15rem 0.45rem;
		border-radius: 4px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		border: 1px solid var(--color-border);
		font: inherit;
		font-size: 0.72rem;
		font-family: var(--font-mono, ui-monospace, monospace);
		cursor: pointer;
	}
	.lat-btn.is-checking {
		opacity: 0.55;
		cursor: wait;
	}
	.lat-btn.ok {
		color: #3fb950;
	}
	.lat-btn.slow {
		color: #d29922;
	}
	.lat-btn.fail {
		color: #f85149;
	}
	.spark-mini {
		display: flex;
		align-items: flex-end;
		gap: 1px;
		height: 20px;
		width: 100%;
		max-width: 82px;
		cursor: pointer;
	}
	.spark-mini .bar {
		flex: 1;
		min-width: 0;
		min-height: 2px;
		border-radius: 1px;
		background: var(--color-bg-tertiary);
	}
	.spark-mini.ok .bar {
		background: #3fb950;
	}
	.spark-mini.slow .bar {
		background: #d29922;
	}
	.spark-mini.fail .bar {
		background: #f85149;
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
		font-size: 0.68rem;
		line-height: 1.15;
		color: var(--color-text-muted);
		flex-shrink: 0;
	}
	.card:focus-visible {
		outline: 2px solid var(--color-primary, #3b82f6);
		outline-offset: 2px;
	}
	.card.err { border-color: #f85149; }
	.head { display: flex; justify-content: space-between; align-items: center; gap: 0.5rem; }
	.head-right { display: flex; align-items: center; gap: 0.5rem; }
	.card-remove {
		width: 22px;
		height: 22px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		background: transparent;
		border: 1px solid var(--color-border);
		border-radius: 50%;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: color 120ms, border-color 120ms, background 120ms;
	}
	.card-remove:hover {
		color: var(--color-error, #f85149);
		border-color: var(--color-error, #f85149);
		background: rgba(248, 81, 73, 0.08);
	}
	.card-remove:focus-visible {
		outline: 2px solid var(--color-error, #f85149);
		outline-offset: 1px;
	}
	.label { font-weight: 600; font-size: 0.95rem; }
	.badge { font-size: 0.72rem; padding: 0.15rem 0.5rem; border-radius: 999px; }
	.badge.ok { background: rgba(63, 185, 80, 0.15); color: #3fb950; }
	.badge.error { background: rgba(248, 81, 73, 0.15); color: #f85149; }
	.badge.pending { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
	.meta { font-size: 0.75rem; color: var(--color-text-muted); }
	.info { font-size: 0.82rem; color: var(--color-text-muted); }
	.err-msg { font-size: 0.78rem; color: #f85149; margin-top: 0.3rem; }
	.mono { font-family: var(--font-mono, ui-monospace, monospace); }
</style>
