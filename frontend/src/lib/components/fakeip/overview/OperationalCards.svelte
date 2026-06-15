<!--
  Операционные карточки «Обзора» (FE-spec §5.1):
    1. Состояние движка — переиспользуем readiness-сигнал (running + active),
       НЕ дублируем тумблер (он в EngineSettingsCard). Только индикатор + текст.
    2. Активные устройства — Status.deviceCount (config-счётчик).
    3. «Активные выборы (composite)» — для каждого selector/urltest/loadbalance
       его текущий активный участник (из proxies/list через общий sb-router
       helper resolveCompositeOutboundView, переданный пропом-строками).

  ЧЕСТНОСТЬ (§4): не показываем per-outbound throughput / latency. «Активные
  выборы» — живой блок: если движок не live, показываем честный empty-state
  вместо устаревшего/выдуманного снимка.

  Презентационный: получает все значения пропами, своих подписок не делает.
-->
<script lang="ts">
	import { Card, Badge } from '$lib/components/ui';
	import { Activity, MonitorSmartphone, Shuffle } from 'lucide-svelte';
	import type { ActiveCompositeRow } from './activeComposites';

	interface Props {
		/** Движок запущен (singboxStatus.running). */
		running: boolean;
		/** NDMS auto-маршрут пула присутствует (Status.active). */
		active: boolean;
		/** Управляемых устройств (Status.deviceCount). */
		deviceCount: number;
		/** Живой ли движок сейчас (gate для composite-блока): движок запущен и
		 *  clash-runtime доступен. Когда false — показываем empty-state. */
		engineLive: boolean;
		/** Причина не-live состояния для текста empty-state. */
		notLiveReason?: 'stopped' | 'clash-down';
		/** Строки активных composite-выборов (из proxies/list, уже разрешены). */
		composites: ActiveCompositeRow[];
	}

	let {
		running,
		active,
		deviceCount,
		engineLive,
		notLiveReason,
		composites,
	}: Props = $props();

	// Состояние движка из двух живых сигналов. active == маршрут пула присутствует.
	const engineTone = $derived(running && active ? 'success' : running ? 'warning' : 'error');
	const engineText = $derived(
		running && active
			? 'запущен, маршрут активен'
			: running
				? 'запущен, маршрут отсутствует'
				: 'остановлен',
	);

	const notLiveText = $derived(
		notLiveReason === 'clash-down'
			? 'Clash-runtime недоступен — активные выборы временно недоступны.'
			: 'Движок остановлен — активные выборы недоступны.',
	);
</script>

<div class="op-grid">
	<Card padding="md">
		<div class="op-card">
			<div class="op-head">
				<Activity size={16} class="op-icon" data-tone={engineTone} />
				<span class="op-label">Состояние движка</span>
			</div>
			<div class="op-body">
				<span class="op-dot" data-tone={engineTone} aria-hidden="true"></span>
				<span class="op-text">{engineText}</span>
			</div>
		</div>
	</Card>

	<Card padding="md">
		<div class="op-card">
			<div class="op-head">
				<MonitorSmartphone size={16} class="op-icon" />
				<span class="op-label">Активные устройства</span>
			</div>
			<div class="op-body">
				<span class="op-count">{deviceCount}</span>
				<span class="op-text op-muted">под управлением политики</span>
			</div>
		</div>
	</Card>

	<Card padding="md">
		<div class="op-card op-card-wide">
			<div class="op-head">
				<Shuffle size={16} class="op-icon" />
				<span class="op-label">Активные выборы (composite)</span>
			</div>

			{#if !engineLive}
				<p class="op-empty">{notLiveText}</p>
			{:else if composites.length === 0}
				<p class="op-empty">Composite-outbounds не настроены.</p>
			{:else}
				<ul class="comp-rows">
					{#each composites as row (row.tag)}
						<li class="comp-row">
							<div class="comp-info">
								<span class="comp-title">{row.groupTitle}</span>
								<Badge variant="muted" size="xs">{row.compositeType}</Badge>
							</div>
							<div class="comp-active">
								<span class="comp-member">{row.activeMemberLabel || '—'}</span>
								{#if row.otherCount > 0}
									<Badge variant="default" size="xs" compact>+{row.otherCount}</Badge>
								{/if}
							</div>
						</li>
					{/each}
				</ul>
			{/if}
		</div>
	</Card>
</div>

<style>
	.op-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
		gap: 1rem;
		align-items: start;
	}

	/* Composite-карточка занимает всю ширину строки сетки. */
	.op-card-wide {
		grid-column: 1 / -1;
	}

	.op-card {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.op-head {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.op-head :global(.op-icon) {
		color: var(--color-accent);
		flex-shrink: 0;
	}

	.op-head :global(.op-icon[data-tone='success']) {
		color: var(--color-success);
	}
	.op-head :global(.op-icon[data-tone='warning']) {
		color: var(--color-warning);
	}
	.op-head :global(.op-icon[data-tone='error']) {
		color: var(--color-error);
	}

	.op-label {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--text-secondary);
	}

	.op-body {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
	}

	.op-dot {
		width: 0.625rem;
		height: 0.625rem;
		border-radius: 50%;
		flex-shrink: 0;
		align-self: center;
	}
	.op-dot[data-tone='success'] {
		background: var(--color-success);
	}
	.op-dot[data-tone='warning'] {
		background: var(--color-warning);
	}
	.op-dot[data-tone='error'] {
		background: var(--color-error);
	}

	.op-count {
		font: 700 1.5rem/1 var(--font-sans);
		letter-spacing: -0.02em;
		color: var(--text-primary);
	}

	.op-text {
		font-size: 0.875rem;
		color: var(--text-primary);
	}

	.op-muted {
		color: var(--text-muted);
		font-size: 0.8125rem;
	}

	.op-empty {
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-muted);
	}

	.comp-rows {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.comp-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.375rem 0;
		border-bottom: 1px solid var(--color-border);
	}

	.comp-row:last-child {
		border-bottom: none;
	}

	.comp-info {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}

	.comp-title {
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.comp-active {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		flex-shrink: 0;
	}

	.comp-member {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		color: var(--color-accent);
	}
</style>
