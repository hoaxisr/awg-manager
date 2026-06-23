<!--
  Атомарный egress — карточка одного прокси-выхода из пула (AWG/NWG-туннель
  или прокси подписки) в секции ATOMIC чипа «Outbounds» (мокап
  page-outbounds-v3). Это те же сущности, что появляются участниками composite-
  групп («DE Frankfurt (awg0)», «de01.demo.example»…).

    - top-left: тип-бейдж (протокол: vless / hysteria2 / awg…);
    - top-right: статус-точка (по последней задержке) + ссылка-выход к месту
      управления этим выходом (read-only здесь);
    - body: имя (bold, с флагом — как у участников групп), «server:port»,
      строка sni (если есть), строка transport/flow (если есть);
    - footer: «задержка <N ms|таймаут|—>» + кнопка «тест» (gauge).

  READ-ONLY (решение пользователя): выходы создаются/правятся на страницах
  Туннели / Подписки, не здесь. Поэтому НЕТ pencil/delete — вместо них
  ненавязчивая ссылка-выход к их странице.

  ЧЕСТНОСТЬ (FE-spec §4): показываем только поля, что РЕАЛЬНО есть у выхода —
  sni/transport рендерим лишь когда они присутствуют. Задержка — единственное
  per-egress число, только по запросу; никакого throughput.

  ЖИВЫЕ ДАННЫЕ vs КОНФИГ (§12.1): карточка (пул) видна всегда. Статус-точка и
  задержка — runtime: когда движок не live, тест недоступен, точка нейтральна.
-->
<script lang="ts">
	import { Gauge, ArrowUpRight } from 'lucide-svelte';
	import type { AtomicEgress } from './atomicEgress';
	import { delayHealth, formatDelay } from './formatDelay';

	interface Props {
		egress: AtomicEgress;
		/** Runtime live? Gates the status dot + delay test (FE-spec §12.1). */
		live: boolean;
		/** Last measured delay (ms); 0 = timeout, undefined = untested. */
		delay: number | undefined;
		/** Delay test in flight for this egress (button spinner / disabled). */
		testing: boolean;
		/** Run an on-demand delay test for this egress tag. */
		onTest: (tag: string) => void;
	}

	let { egress, live, delay, testing, onTest }: Props = $props();

	const hasDelay = $derived(live && delay !== undefined);
	const health = $derived(live ? delayHealth(delay) : 'unknown');

	// Read-only link-out to where this egress is managed.
	const manageHref = $derived(
		egress.source === 'subscription' && egress.subscriptionId
			? `/subscriptions/${egress.subscriptionId}`
			: egress.source === 'tunnel'
				? `/singbox/${encodeURIComponent(egress.tag)}`
				: '/',
	);
	const manageLabel = $derived(
		egress.source === 'subscription'
			? 'Открыть подписку'
			: egress.source === 'tunnel'
				? 'Открыть туннель'
				: 'Открыть Туннели',
	);
</script>

<div class="oc">
	<div class="otop">
		<span class="proto">{egress.proto}</span>
		<a
			class="ib link"
			href={manageHref}
			aria-label={`${manageLabel}: ${egress.name}`}
			title={manageLabel}
		>
			<ArrowUpRight size={14} aria-hidden="true" />
		</a>
		<span class="d" data-health={health} aria-hidden="true"></span>
	</div>

	<div class="nm" title={egress.name}>{egress.name}</div>

	<div class="srv">
		{#if egress.endpoint}
			{egress.endpoint}
		{:else}
			—
		{/if}
	</div>

	{#if egress.sni || egress.transport}
		<div class="meta">
			{#if egress.sni}<div class="meta-line">sni: {egress.sni}</div>{/if}
			{#if egress.transport}<div class="meta-line">{egress.transport}</div>{/if}
		</div>
	{/if}

	<div class="lat">
		<span class="lat-label"
			>задержка
			{#if hasDelay}
				<span class="ms" data-health={health}>{formatDelay(delay)}</span>
			{:else}
				<span class="ms" data-health="muted">—</span>
			{/if}
		</span>
		<button
			type="button"
			class="test"
			disabled={!live || testing}
			onclick={() => onTest(egress.tag)}
		>
			<Gauge size={13} aria-hidden="true" />
			{testing ? 'тест…' : 'тест'}
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

	.proto {
		font-size: 0.6875rem;
		color: var(--color-accent);
		border: 1px solid var(--color-accent-border, var(--border));
		border-radius: 5px;
		padding: 0.125rem 0.4375rem;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		font-family: var(--font-mono);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 60%;
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
	.ib.link {
		margin-left: auto;
		text-decoration: none;
	}
	.ib:hover {
		color: var(--text-primary);
		background: var(--bg-hover);
	}

	.d {
		width: 9px;
		height: 9px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--color-success, #22c55e);
	}
	.d[data-health='down'] {
		background: var(--color-error, #dc2626);
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
		margin: 0.125rem 0 0.375rem;
		font-family: var(--font-mono);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.meta {
		color: var(--text-secondary);
		font-size: 0.8125rem;
		line-height: 1.5;
		margin-bottom: 0.5rem;
	}
	.meta-line {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
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

	.ms {
		font-weight: 700;
		font-family: var(--font-mono);
	}
	.ms[data-health='ok'] {
		color: var(--color-success, #22c55e);
	}
	.ms[data-health='down'] {
		color: var(--color-error, #dc2626);
	}
	.ms[data-health='muted'],
	.ms[data-health='unknown'] {
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
</style>
