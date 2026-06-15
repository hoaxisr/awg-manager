<!--
  Атомарный outbound (тип `direct`) — единственный неконтейнерный тип в
  SingboxRouterOutbound. Карточка показывает: имя/тег, протокол и цель
  (bind-интерфейс, если задан), действия edit/delete.

  ЧЕСТНОСТЬ (FE-spec §4): для `direct` нет сервера и нет осмысленной
  задержки (это локальный выход мимо VPN) — кнопку «тест задержки» НЕ
  показываем и ничего про скорость/throughput не выдумываем. Health-точка
  отражает только то, что direct — всегда доступный локальный маршрут.

  Имя outbound может содержать emoji-флаги из подписки — это данные, рендерим
  как есть; запрет emoji касается только UI-хрома.
-->
<script lang="ts">
	import type { SingboxRouterOutbound, Subscription } from '$lib/types';
	import { Badge } from '$lib/components/ui';
	import { Edit3, Trash2, ArrowRight } from 'lucide-svelte';
	import { outboundDisplay } from '$lib/components/sb-router/outboundLabel';

	interface Props {
		outbound: SingboxRouterOutbound;
		subscriptions: Subscription[];
		onEdit: (tag: string) => void;
		onDelete: (tag: string) => void;
	}

	let { outbound, subscriptions, onEdit, onDelete }: Props = $props();

	const display = $derived(outboundDisplay(outbound, subscriptions));
</script>

<div class="card">
	<div class="head">
		<span class="dot" aria-hidden="true"></span>
		<div class="title-wrap">
			<span class="title">{display.title}</span>
			<Badge variant="muted" size="xs">{outbound.type}</Badge>
		</div>
		<div class="actions">
			<button
				type="button"
				class="icon-btn"
				onclick={() => onEdit(outbound.tag)}
				aria-label={`Редактировать outbound ${outbound.tag}`}
				title={`Редактировать «${display.title}»`}
			>
				<Edit3 size={15} />
			</button>
			<button
				type="button"
				class="icon-btn danger"
				onclick={() => onDelete(outbound.tag)}
				aria-label={`Удалить outbound ${outbound.tag}`}
				title={`Удалить «${display.title}»`}
			>
				<Trash2 size={15} />
			</button>
		</div>
	</div>

	<div class="meta">
		{#if outbound.bind_interface}
			<span class="meta-item">
				<ArrowRight size={12} aria-hidden="true" />
				<span class="iface">{outbound.bind_interface}</span>
			</span>
		{:else}
			<span class="meta-item meta-muted">прямой выход (без VPN)</span>
		{/if}
	</div>
</div>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.75rem 0.875rem;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
	}

	.head {
		display: grid;
		grid-template-columns: 8px minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.5rem;
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--text-muted);
		flex-shrink: 0;
	}

	.title-wrap {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}

	.title {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		font-weight: 600;
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

	.meta {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding-left: calc(8px + 0.5rem);
		font-size: 0.75rem;
		color: var(--text-secondary);
	}

	.meta-item {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
	}

	.meta-muted {
		color: var(--text-muted);
	}

	.iface {
		font-family: var(--font-mono);
	}
</style>
