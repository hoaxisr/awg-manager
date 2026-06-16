<!--
  Атомарный outbound (тип `direct`) — единственный неконтейнерный тип в
  SingboxRouterOutbound. Карточка по мокапу page-outbounds-v3 (секция ATOMIC):
    - top-left: тип-бейдж (протокол) из конфига outbound;
    - top-right: pencil (edit) + статус-точка;
    - body: имя (bold), «server:port» / bind-интерфейс, деталь-строка;
    - footer: «задержка <…>» + (для типов с реальным замером) кнопка «тест».

  ЧЕСТНОСТЬ (FE-spec §4): SingboxRouterOutbound.type для атомарного outbound —
  всегда `direct` (это локальный выход мимо VPN). У него НЕТ сервера, sni, flow
  и осмысленной per-outbound задержки, поэтому кнопку «тест задержки» и число
  задержки для direct НЕ показываем и ничего про throughput не выдумываем.
  Статус-точка direct = всегда доступный локальный маршрут (нейтральная).

  Тип-бейдж и тон протокола берём из общих sb-router-хелперов
  (outboundDisplay / typeSubtitle), чтобы не хардкодить маппинг.

  Имя outbound может содержать emoji-флаги из подписки — это данные, рендерим
  как есть; запрет emoji касается только UI-хрома.
-->
<script lang="ts">
	import type { SingboxRouterOutbound, Subscription } from '$lib/types';
	import { Edit3, Gauge, ArrowRight } from 'lucide-svelte';
	import { outboundDisplay } from '$lib/components/sb-router/outboundLabel';
	import { delayHealth, formatDelay } from './formatDelay';

	interface Props {
		outbound: SingboxRouterOutbound;
		subscriptions: Subscription[];
		onEdit: (tag: string) => void;
		onDelete: (tag: string) => void;
	}

	let { outbound, subscriptions, onEdit, onDelete }: Props = $props();

	const display = $derived(outboundDisplay(outbound, subscriptions));

	// Тип-бейдж протокола. Для router-каталога это всегда `direct` — честная
	// метка; bind_interface уходит в деталь-строку, а не в бейдж.
	const protoLabel = $derived(outbound.type);

	// direct — локальный выход без замера задержки и без теста (см. шапку).
	const hasDelay = $derived(false);
	const delay = $derived<number | undefined>(undefined);
	const health = $derived(delayHealth(delay));
</script>

<div class="oc">
	<div class="otop">
		<span class="proto">{protoLabel}</span>
		<button
			type="button"
			class="ib"
			onclick={() => onEdit(outbound.tag)}
			aria-label={`Редактировать outbound ${outbound.tag}`}
			title={`Редактировать «${display.title}»`}
		>
			<Edit3 size={14} aria-hidden="true" />
		</button>
		<button
			type="button"
			class="ib danger"
			onclick={() => onDelete(outbound.tag)}
			aria-label={`Удалить outbound ${outbound.tag}`}
			title={`Удалить «${display.title}»`}
		>
			<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
				<path d="M3 6h18" /><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" /><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" />
			</svg>
		</button>
		<span class="d" data-health={health} aria-hidden="true"></span>
	</div>

	<div class="nm">{display.title}</div>

	<div class="srv">
		{#if outbound.bind_interface}
			<span class="iface-line"
				><ArrowRight size={11} aria-hidden="true" />{outbound.bind_interface}</span
			>
		{:else}
			прямой выход (без VPN)
		{/if}
	</div>

	<div class="lat">
		<span class="lat-label"
			>задержка
			{#if hasDelay}
				<span class="ms" data-health={health}>{formatDelay(delay)}</span>
			{:else}
				<span class="ms" data-health="muted">—</span>
			{/if}
		</span>
		{#if hasDelay}
			<button type="button" class="test">
				<Gauge size={13} aria-hidden="true" /> тест
			</button>
		{/if}
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
		margin: 0.125rem 0 0.5625rem;
		font-family: var(--font-mono);
	}

	.iface-line {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
	}

	.lat {
		display: flex;
		align-items: center;
		justify-content: space-between;
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
	}
	.test:hover {
		background: var(--accent-soft);
	}
</style>
