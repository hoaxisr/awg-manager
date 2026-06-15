<!--
  Композитный outbound (selector / urltest / loadbalance) — группа с
  участниками, активным участником и групповым тестом (FE-spec §5.3).

  ПЕРЕИСПОЛЬЗОВАНИЕ:
  - resolveCompositeOutboundView (sb-router) — активный участник (clash `now`
    → подписка → первый) + метки участников. Тот же helper, что и обзор.
  - outboundDisplay — заголовок группы.

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
	import { Badge, Button } from '$lib/components/ui';
	import { Edit3, Trash2, Gauge, Check } from 'lucide-svelte';
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

<div class="card">
	<div class="head">
		<div class="title-wrap">
			<span class="title">{groupTitle}</span>
			<Badge variant="accent" size="xs">{compositeType}</Badge>
			{#if view?.isSubscription}
				<Badge variant="muted" size="xs">subscription</Badge>
			{/if}
		</div>
		<div class="actions">
			<Button
				variant="secondary"
				size="sm"
				disabled={!live || testing || memberTags.length === 0}
				loading={testing}
				onclick={() => onTest(outbound.tag)}
			>
				{#snippet iconBefore()}
					<Gauge size={14} aria-hidden="true" />
				{/snippet}
				Тест группы
			</Button>
			<button
				type="button"
				class="icon-btn"
				onclick={() => onEdit(outbound.tag)}
				aria-label={`Редактировать outbound ${outbound.tag}`}
				title={`Редактировать «${groupTitle}»`}
			>
				<Edit3 size={15} />
			</button>
			<button
				type="button"
				class="icon-btn danger"
				onclick={() => onDelete(outbound.tag)}
				aria-label={`Удалить outbound ${outbound.tag}`}
				title={`Удалить «${groupTitle}»`}
			>
				<Trash2 size={15} />
			</button>
		</div>
	</div>

	{#if memberTags.length === 0}
		<p class="empty">В группе нет участников.</p>
	{:else}
		<ul class="members">
			{#each memberTags as tag (tag)}
				{@const isActive = live && tag === activeTag}
				{@const delay = live ? memberDelay(tag) : undefined}
				{@const health = delayHealth(delay)}
				<li class="member" class:active={isActive}>
					<span class="m-dot" data-health={health} aria-hidden="true"></span>
					<span class="m-label">{memberLabel(tag)}</span>
					{#if isActive}
						<Badge variant="success" size="xs" compact>активен</Badge>
					{/if}
					<span class="m-spacer"></span>
					{#if live}
						<span class="m-delay" data-health={health}>{formatDelay(delay)}</span>
					{/if}
					{#if canSelect && !isActive}
						<button
							type="button"
							class="m-select"
							disabled={selecting}
							onclick={() => onSelect(outbound.tag, tag)}
							aria-label={`Выбрать участника ${tag}`}
							title="Сделать активным"
						>
							<Check size={13} aria-hidden="true" />
						</button>
					{/if}
				</li>
			{/each}
		</ul>
		{#if !live}
			<p class="hint">
				Движок не активен — активный участник, задержки и управление
				недоступны. Список участников показан из конфигурации.
			</p>
		{/if}
	{/if}
</div>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
		padding: 0.75rem 0.875rem;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.title-wrap {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}

	.title {
		font-weight: 600;
		font-size: 0.9375rem;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.actions {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
	}

	.icon-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0.25rem;
		border: 0;
		background: transparent;
		color: var(--text-muted);
		border-radius: var(--radius-sm);
		cursor: pointer;
	}
	.icon-btn:hover {
		background: var(--bg-hover);
		color: var(--text-primary);
	}
	.icon-btn.danger:hover {
		color: var(--color-error, #dc2626);
	}

	.members {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.member {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.3125rem 0.375rem;
		border-radius: var(--radius-sm);
	}
	.member.active {
		background: color-mix(in srgb, var(--accent) 8%, transparent);
	}

	.m-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--text-muted);
	}
	.m-dot[data-health='ok'] {
		background: var(--color-success, #22c55e);
	}
	.m-dot[data-health='down'] {
		background: var(--color-error, #dc2626);
	}

	.m-label {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.m-spacer {
		flex: 1;
	}

	.m-delay {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		color: var(--text-muted);
		flex-shrink: 0;
	}
	.m-delay[data-health='ok'] {
		color: var(--text-secondary);
	}
	.m-delay[data-health='down'] {
		color: var(--color-error, #dc2626);
	}

	.m-select {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0.1875rem;
		border: 1px solid var(--border);
		background: transparent;
		color: var(--text-muted);
		border-radius: var(--radius-sm);
		cursor: pointer;
		flex-shrink: 0;
	}
	.m-select:hover:not(:disabled) {
		border-color: var(--accent);
		color: var(--accent);
	}
	.m-select:disabled {
		opacity: 0.5;
		cursor: default;
	}

	.empty,
	.hint {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--text-muted);
	}
</style>
