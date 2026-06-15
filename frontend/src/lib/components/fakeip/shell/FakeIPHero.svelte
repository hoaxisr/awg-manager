<!--
  FakeIPHero — шапка страницы FakeIP (мокап `.hero`): kick-eyebrow + крупный
  title + hsub-факты + панель действий.

  ЧЕСТНОСТЬ субтайтла: показываем только реально известные факты. «gvisor»
  фиксирован для fakeip-tun (см. EngineSettingsCard), WAN — из settings, состояние
  движка — из engineState. Имя opkgtun-интерфейса и fakeip-пул backend в
  settings/status НЕ отдаёт (DefaultFakeIPTunParams не в DTO) — НЕ выдумываем их,
  как и EngineSettingsCard показывает пул «по умолчанию».

  Панель действий:
    - config.json / Инспектор маршрутов — отложенные фичи (просмотрщик конфига
      и инспектор 10.2). Рендерим кнопки disabled с подсказкой «скоро», НЕ
      строим фейковый просмотрщик. TODO ниже.
    - Перезагрузить — реальный api.singboxControl('restart') через onRestart.
    - createButton — Snippet-слот: страница вставляет контекстную кнопку
      «+ Outbound»/«+ Правило» под активный чип.
-->
<script lang="ts" module>
	import type { Snippet } from 'svelte';
</script>

<script lang="ts">
	import { Button } from '$lib/components/ui';
	import { FileJson, Search, RotateCw } from 'lucide-svelte';
	import type { FakeIPEngineState } from '../engineState';

	interface Props {
		/** Заголовок страницы (мокап htitle): «FakeIP Router» / «Outbounds» / … */
		title: string;
		/** Состояние движка — формирует честный хвост субтайтла. */
		engineState: FakeIPEngineState;
		/** WAN: авто-детект интерфейса. */
		wanAutoDetect?: boolean;
		/** WAN: явный системный интерфейс (когда не авто). */
		wanInterface?: string;
		/** Перезапуск sing-box (страница зовёт api.singboxControl('restart')). */
		onRestart: () => void | Promise<void>;
		/** Доступна ли кнопка «Перезагрузить» (движок должен быть запущен). */
		restartEnabled?: boolean;
		/** Контекстная create-кнопка под активный чип. */
		createButton?: Snippet;
	}

	let {
		title,
		engineState,
		wanAutoDetect = true,
		wanInterface,
		onRestart,
		restartEnabled = true,
		createButton,
	}: Props = $props();

	let restarting = $state(false);

	const engineFact = $derived(
		engineState === 'not-fakeip'
			? 'движок выключен'
			: engineState === 'stopped'
				? 'движок остановлен'
				: engineState === 'clash-down'
					? 'clash-runtime недоступен'
					: 'движок работает', // 'live'
	);

	// Честный субтайтл: gvisor (фиксирован для fakeip-tun) · WAN · состояние.
	const wanFact = $derived(
		wanAutoDetect ? 'WAN авто' : wanInterface ? `WAN ${wanInterface}` : '',
	);
	const facts = $derived(['gvisor', wanFact, engineFact].filter(Boolean).join(' · '));

	async function handleRestart(): Promise<void> {
		if (restarting) return;
		restarting = true;
		try {
			await onRestart();
		} finally {
			restarting = false;
		}
	}
</script>

<div class="hero">
	<div class="hero-titles">
		<div class="kick">SING-BOX · FAKEIP + TUN ROUTER</div>
		<h1 class="htitle">{title}</h1>
		<div class="hsub">{facts}</div>
	</div>

	<div class="btns">
		<!--
			TODO(10.x): просмотрщик config.json. Доставляется отдельной задачей —
			до неё кнопка disabled с подсказкой, НЕ открываем фейковый JSON.
		-->
		<Button variant="secondary" size="sm" disabled title="Скоро — просмотрщик конфигурации">
			{#snippet iconBefore()}<FileJson size={14} />{/snippet}
			config.json
		</Button>

		<!--
			TODO(10.2): инспектор маршрутов. До задачи — disabled c подсказкой.
		-->
		<Button variant="secondary" size="sm" disabled title="Скоро — инспектор маршрутов">
			{#snippet iconBefore()}<Search size={14} />{/snippet}
			Инспектор маршрутов
		</Button>

		<Button
			variant="secondary"
			size="sm"
			loading={restarting}
			disabled={restarting || !restartEnabled}
			onclick={handleRestart}
		>
			{#snippet iconBefore()}<RotateCw size={14} />{/snippet}
			Перезагрузить
		</Button>

		{#if createButton}{@render createButton()}{/if}
	</div>
</div>

<style>
	.hero {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: var(--sp-3, 0.75rem);
		flex-wrap: wrap;
		margin-bottom: var(--sp-4, 1rem);
	}

	.hero-titles {
		min-width: 0;
	}

	.kick {
		color: var(--color-accent);
		font-size: 10px;
		letter-spacing: 0.12em;
		text-transform: uppercase;
	}

	.htitle {
		margin: 2px 0;
		color: var(--text-primary);
		font-size: 1.5rem;
		font-weight: 800;
		letter-spacing: -0.3px;
		line-height: 1.15;
	}

	.hsub {
		color: var(--text-muted);
		font-size: 0.75rem;
	}

	.btns {
		display: flex;
		gap: var(--sp-2, 0.5rem);
		flex-wrap: wrap;
		align-items: center;
	}
</style>
