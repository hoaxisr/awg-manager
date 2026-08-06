<script lang="ts">
	// Пикер режима захвата: выбор одного из ТРЁХ режимов.
	//
	// ПОЧЕМУ ЭТО НЕ ConfirmSwitch. `ConfirmSwitch` подтверждает конкретную пару
	// «откуда → куда» и перечисляет последствия перехода — он получает готовый
	// target. Выбирать target было негде: до nav-v3 выбор делали две карточки
	// `OutboundOption` в шторке движка (tproxy и policy-tun), а FakeIP включался
	// тумблером на своей вкладке. Вкладки не стало, шторки тоже — значит выбор
	// из трёх нужен здесь, а подтверждение и прогресс остаются за
	// `ModeSwitchHost` в layout группы.
	//
	// ЛОКАЛЬНОГО ВЫБОРА НЕТ НАМЕРЕННО. Соблазн «запомнить цель и включить ею по
	// кнопке» разводит два утверждения о режиме на одной странице: пилюля шапки
	// читает persisted `settings.routingMode`, а пикер помнил бы своё. Поэтому
	// клик по режиму сразу уходит в `modeSwitch.request` — при выключенном
	// движке это и есть «включить вот этим», переход 'off' → выбранный режим.
	//
	// Отметка «текущий» — тот же нормализованный persisted-режим, что показывает
	// пилюля шапки, поэтому расходиться им не с чем.
	import { Modal, Button } from '$lib/components/ui';
	import OutboundOption from '$lib/components/sb-router/OutboundOption.svelte';
	import { captureModeOptions } from './captureModes';
	import type { EngineRoutingMode } from './engineRunState';

	interface Props {
		open: boolean;
		/** Текущий (при выключенном движке — persisted) режим. */
		current: EngineRoutingMode;
		/** Движок включён: от этого зависит смысл выбора, а не сам выбор. */
		enabled: boolean;
		onpick: (mode: EngineRoutingMode) => void;
		onclose: () => void;
	}

	let { open, current, enabled, onpick, onclose }: Props = $props();

	const options = captureModeOptions();
</script>

<Modal {open} title="Режим захвата трафика" size="lg" {onclose}>
	<p class="lead">
		{#if enabled}
			Смена режима перезапускает sing-box: захват снимается и поднимается заново. Правила,
			выходы и DNS общие для всех режимов — они сохранятся.
		{:else}
			Движок выключен. Выбор режима сразу включит его в этом режиме.
		{/if}
	</p>

	<div class="grid">
		{#each options as opt (opt.mode)}
			<OutboundOption
				label={opt.label}
				sub={opt.summary}
				count={opt.tech}
				tone="accent"
				selected={opt.mode === current}
				onclick={() => onpick(opt.mode)}
			/>
		{/each}
	</div>

	<p class="note">
		{#if enabled}
			Отмеченный режим работает сейчас.
		{:else}
			Отмечен режим, в котором движок работал в прошлый раз.
		{/if}
	</p>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onclose}>Отмена</Button>
	{/snippet}
</Modal>

<style>
	.lead {
		margin: 0 0 0.75rem;
		font-size: 13px;
		line-height: 1.5;
		color: var(--text-secondary);
	}

	/* Три карточки в ряд на десктопе, в столбец — на узком экране. */
	.grid {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 8px;
	}

	@media (max-width: 720px) {
		.grid {
			grid-template-columns: 1fr;
		}
	}

	.note {
		margin: 0.75rem 0 0;
		font-size: 11.5px;
		color: var(--text-muted);
	}
</style>
