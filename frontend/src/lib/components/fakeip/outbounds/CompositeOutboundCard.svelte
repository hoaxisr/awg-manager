<!--
  Композитный outbound (selector / urltest / loadbalance) — группа с
  участниками, активным участником и групповым тестом. Карточка по мокапу
  page-outbounds-v3 (секция COMPOSITE):
    - top-left: тип-бейдж «urltest» / «selector»;
    - top-right: pencil (edit) + статус-точка;
    - body: имя (bold), сабтайтл («auto by latency · interval Ns» / «ручной
      выбор»), список участников (точка + имя, активный → бейдж «active»);
    - footer: «members · last hh:mm» + кнопка «тест группы» (gauge).

  ПЕРЕИСПОЛЬЗОВАНИЕ:
  - resolveCompositeOutboundView (sb-router) — активный участник (clash `now`
    → подписка → первый) + метки участников. Тот же helper, что и обзор.

  ЖИВЫЕ ДАННЫЕ vs КОНФИГ (FE-spec §12.1): список участников всегда виден
  (конфиг). Активный участник, health-точки, задержки и управление (select /
  тест группы) — runtime (Clash): когда движок не live, они деградируют до
  безопасного состояния, а конфиг-список остаётся.

  ЧЕСТНОСТЬ (§4): per-участник показываем ТОЛЬКО задержку, и только по запросу
  («тест группы» → proxies/test). Никакого throughput / скорости.

  Метки участников могут содержать emoji-флаги из подписки — это данные.
-->
<script lang="ts">
	import type {
		SingboxRouterOutbound,
		SingboxProxyGroup,
		Subscription,
	} from '$lib/types';
	import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
	import { Edit3, Gauge } from 'lucide-svelte';
	import { resolveCompositeOutboundView } from '$lib/components/sb-router/compositeOutboundDisplay';
	import { resolveMemberLabel } from '$lib/utils/memberLabel';
	import { delayHealth, formatDelay } from './formatDelay';

	interface Props {
		outbound: SingboxRouterOutbound;
		outbounds: SingboxRouterOutbound[];
		outboundOptions: OutboundGroup[];
		subscriptions: Subscription[];
		proxyGroups: SingboxProxyGroup[];
		/** Runtime live? Gates active-member / health / select / test. */
		live: boolean;
		/** memberTag → delay (ms) from the last group test; 0 = timeout. */
		testDelays: Record<string, number> | undefined;
		/** Group test in flight (button spinner / disabled). */
		testing: boolean;
		/** Member-select in flight for this group. */
		selecting: boolean;
		/** Время последнего теста этой группы (hh:mm) — «· last hh:mm». */
		lastTestAt?: string;
		onEdit: (tag: string) => void;
		onDelete: (tag: string) => void;
		onTest: (tag: string) => void;
		onSelect: (group: string, member: string) => void;
	}

	let {
		outbound,
		outbounds,
		outboundOptions,
		subscriptions,
		proxyGroups,
		live,
		testDelays,
		testing,
		selecting,
		lastTestAt,
		onEdit,
		onDelete,
		onTest,
		onSelect,
	}: Props = $props();

	// Shared sb-router resolution: active member + ordered member tags/labels.
	const view = $derived(
		resolveCompositeOutboundView(
			outbound.tag,
			outbounds,
			outboundOptions,
			subscriptions,
			live ? proxyGroups : [],
		),
	);

	// Full member tag list (config order). Composite outbounds carry their
	// members directly; subscription-sourced groups fall back to the resolved
	// view tags.
	const memberTags = $derived(
		outbound.outbounds?.length
			? outbound.outbounds
			: view
				? [view.activeMemberTag, ...view.otherMemberTags].filter(Boolean)
				: [],
	);

	const liveGroup = $derived(
		live ? proxyGroups.find((g) => g.tag === outbound.tag) : undefined,
	);

	// Active member tag: live `now` wins; otherwise the resolved view's choice.
	const activeTag = $derived(liveGroup?.now || view?.activeMemberTag || '');

	const groupTitle = $derived(view?.groupTitle || outbound.tag);
	const compositeType = $derived(view?.compositeType || outbound.type);

	// selector groups support an explicit active-member pick; urltest /
	// loadbalance choose automatically by latency, so no manual select.
	const canSelect = $derived(live && compositeType === 'selector');

	// Сабтайтл по типу группы (мокап): urltest → авто по задержке + интервал;
	// selector → ручной выбор; loadbalance — честная формулировка.
	const subtitle = $derived(
		compositeType === 'urltest'
			? `auto by latency${outbound.interval ? ` · interval ${outbound.interval}` : ''}`
			: compositeType === 'selector'
				? 'ручной выбор'
				: 'балансировка нагрузки',
	);

	function memberLabel(tag: string): string {
		return resolveMemberLabel(tag, subscriptions, outboundOptions);
	}

	// Per-member delay: prefer the explicit test result, fall back to the live
	// group snapshot's lastDelay. `undefined` → untested.
	function memberDelay(tag: string): number | undefined {
		if (testDelays && tag in testDelays) return testDelays[tag];
		const m = liveGroup?.members.find((x) => x.tag === tag);
		return m?.lastDelay;
	}
</script>

<div class="oc">
	<div class="otop">
		<span class="ty" class:sel={compositeType === 'selector'}>{compositeType}</span>
		<button
			type="button"
			class="ib"
			onclick={() => onEdit(outbound.tag)}
			aria-label={`Редактировать outbound ${outbound.tag}`}
			title={`Редактировать «${groupTitle}»`}
		>
			<Edit3 size={14} aria-hidden="true" />
		</button>
		<button
			type="button"
			class="ib danger"
			onclick={() => onDelete(outbound.tag)}
			aria-label={`Удалить outbound ${outbound.tag}`}
			title={`Удалить «${groupTitle}»`}
		>
			<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
				<path d="M3 6h18" /><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" /><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" />
			</svg>
		</button>
		<span class="d" data-health={live ? 'ok' : 'unknown'} aria-hidden="true"></span>
	</div>

	<div class="nm">{groupTitle}</div>
	<div class="srv">{subtitle}</div>

	{#if memberTags.length === 0}
		<p class="empty">В группе нет участников.</p>
	{:else}
		<div class="members">
			{#each memberTags as tag (tag)}
				{@const isActive = live && tag === activeTag}
				{@const delay = live ? memberDelay(tag) : undefined}
				{@const health = delayHealth(delay)}
				{#if canSelect && !isActive}
					<button
						type="button"
						class="mem selectable"
						disabled={selecting}
						onclick={() => onSelect(outbound.tag, tag)}
						title="Сделать активным"
					>
						<span class="dot" data-health={health} aria-hidden="true"></span>
						<span class="mem-label">{memberLabel(tag)}</span>
						{#if delay !== undefined}
							<span class="mem-delay" data-health={health}>{formatDelay(delay)}</span>
						{/if}
					</button>
				{:else}
					<div class="mem" class:act={isActive}>
						<span class="dot" data-health={isActive ? 'ok' : health} aria-hidden="true"></span>
						<span class="mem-label">{memberLabel(tag)}</span>
						{#if isActive}
							<span class="badge">active</span>
						{/if}
						{#if delay !== undefined}
							<span class="mem-delay" data-health={health}>{formatDelay(delay)}</span>
						{/if}
					</div>
				{/if}
			{/each}
		</div>
		{#if !live}
			<p class="hint">
				Движок не активен — активный участник, задержки и управление
				недоступны. Список участников показан из конфигурации.
			</p>
		{/if}
	{/if}

	<div class="lat">
		<span class="lat-label"
			>members{#if live && lastTestAt}<span class="muted"> · last {lastTestAt}</span>{/if}</span
		>
		<button
			type="button"
			class="test"
			disabled={!live || testing || memberTags.length === 0}
			onclick={() => onTest(outbound.tag)}
		>
			<Gauge size={13} aria-hidden="true" />
			{testing ? 'тест…' : 'тест группы'}
		</button>
	</div>
</div>

<style>
	.oc {
		display: flex;
		flex-direction: column;
		height: 100%;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: var(--radius-md, 10px);
		padding: 0.875rem;
	}

	.otop {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}

	.ty {
		font-size: 0.6875rem;
		border-radius: 5px;
		padding: 0.125rem 0.4375rem;
		border: 1px solid var(--color-success, #22c55e);
		color: var(--color-success, #22c55e);
		font-family: var(--font-mono);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.ty.sel {
		color: var(--color-accent);
		border-color: var(--color-accent-border, var(--color-accent));
	}

	.ib {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0.25rem 0.375rem;
		color: var(--text-muted);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		background: transparent;
		cursor: pointer;
	}
	.ib:first-of-type {
		margin-left: auto;
	}
	.ib:hover {
		color: var(--text-primary);
		background: var(--bg-hover);
	}
	.ib.danger:hover {
		color: var(--color-error, #dc2626);
	}

	.d {
		width: 9px;
		height: 9px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--color-success, #22c55e);
	}
	.d[data-health='unknown'] {
		background: var(--text-muted);
	}

	.nm {
		color: var(--text-primary);
		font-size: 0.875rem;
		font-weight: 700;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.srv {
		color: var(--text-muted);
		font-size: 0.8125rem;
		margin: 0.125rem 0 0.5rem;
	}

	.members {
		display: flex;
		flex-direction: column;
	}

	.mem {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.8125rem;
		padding: 0.3125rem 0;
		color: var(--text-secondary);
		width: 100%;
		text-align: left;
		background: transparent;
		border: 0;
	}
	.mem.selectable {
		cursor: pointer;
		border-radius: var(--radius-sm);
	}
	.mem.selectable:hover:not(:disabled) {
		color: var(--text-primary);
	}
	.mem.selectable:disabled {
		opacity: 0.5;
		cursor: default;
	}
	.mem.act {
		color: var(--color-accent);
	}

	.dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--text-muted);
	}
	.dot[data-health='ok'] {
		background: var(--color-success, #22c55e);
	}
	.dot[data-health='down'] {
		background: var(--color-error, #dc2626);
	}

	.mem-delay {
		margin-left: auto;
		flex-shrink: 0;
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--text-muted);
	}
	.mem-delay[data-health='ok'] {
		color: var(--color-success, #22c55e);
	}
	.mem-delay[data-health='down'] {
		color: var(--color-error, #dc2626);
	}

	.mem-label {
		font-family: var(--font-mono);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.badge {
		margin-left: auto;
		font-size: 0.625rem;
		font-weight: 700;
		color: var(--color-accent-contrast, #0a0a0a);
		background: var(--color-accent);
		border-radius: 4px;
		padding: 0.0625rem 0.375rem;
	}

	.lat {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		margin-top: auto;
		border-top: 1px solid var(--border);
		padding-top: 0.5625rem;
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}
	.lat .muted {
		color: var(--text-muted);
	}

	.test {
		display: inline-flex;
		align-items: center;
		gap: 0.3125rem;
		color: var(--color-accent);
		border: 1px solid var(--color-accent-border, var(--border));
		border-radius: var(--radius-sm);
		padding: 0.1875rem 0.5rem;
		background: transparent;
		cursor: pointer;
		font-size: 0.8125rem;
		flex-shrink: 0;
	}
	.test:hover:not(:disabled) {
		background: var(--accent-soft);
	}
	.test:disabled {
		opacity: 0.5;
		cursor: default;
	}

	.empty,
	.hint {
		margin: 0.25rem 0 0;
		font-size: 0.8125rem;
		color: var(--text-muted);
	}
</style>
