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
	import { Button, Modal } from '$lib/components/ui';
	import { JsonConfigDrawer } from '$lib/components/singbox-routing';
	import { TracePanel, traceOpen, openTrace, closeTrace } from '$lib/components/sb-router';
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
		/** TCP/IP-стек fakeip-tun (gvisor/system) — первый факт субтайтла. */
		fakeipStack?: 'gvisor' | 'system';
		/** Активный fakeip tun-интерфейс из статуса (e.g. «opkgtun0»); опционально. */
		fakeipIface?: string;
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
		fakeipStack = 'gvisor',
		fakeipIface,
		onRestart,
		restartEnabled = true,
		createButton,
	}: Props = $props();

	let restarting = $state(false);

	// config.json — самодостаточный JsonConfigDrawer (грузит /singbox/config-preview).
	// Инспектор — существующий route-инспектор TracePanel (openTrace/closeTrace,
	// api.singboxRouterInspectRoute). DNS-ветку из page-inspector-v2 НЕ строим —
	// в мокапе она помечена «(проектируется)»; здесь только реальный route-трейс.
	let configOpen = $state(false);

	const engineFact = $derived(
		engineState === 'not-fakeip'
			? 'движок выключен'
			: engineState === 'stopped'
				? 'движок остановлен'
				: engineState === 'clash-down'
					? 'clash-runtime недоступен'
					: 'движок работает', // 'live'
	);

	// Честный субтайтл: стек (· iface, если провижен) · WAN · состояние.
	const stackFact = $derived(fakeipIface ? `${fakeipStack} · ${fakeipIface}` : fakeipStack);
	const wanFact = $derived(
		wanAutoDetect ? 'WAN авто' : wanInterface ? `WAN ${wanInterface}` : '',
	);
	const facts = $derived([stackFact, wanFact, engineFact].filter(Boolean).join(' · '));

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
		<Button
			variant="secondary"
			size="sm"
			title="Сгенерированный конфиг sing-box"
			onclick={() => (configOpen = true)}
		>
			{#snippet iconBefore()}<FileJson size={14} />{/snippet}
			config.json
		</Button>

		<Button
			variant="secondary"
			size="sm"
			title="Инспектор маршрутов — куда поедет домен/IP"
			onclick={() => openTrace()}
		>
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

<!-- config.json — drawer с конфигом sing-box (copy/download внутри). -->
<JsonConfigDrawer open={configOpen} onClose={() => (configOpen = false)} />

<!-- Инспектор маршрутов — route-трейс в модале (✕ и «← Назад» внутри закрывают). -->
<Modal
	open={$traceOpen}
	title="Инспектор маршрутов"
	size="wide"
	bodyLayout="fill"
	onclose={closeTrace}
>
	<TracePanel embedded />
</Modal>

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
		font-size: 0.75rem;
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
		font-size: 0.8125rem;
	}

	.btns {
		display: flex;
		gap: var(--sp-2, 0.5rem);
		flex-wrap: wrap;
		align-items: center;
	}
</style>
