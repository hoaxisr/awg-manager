<!--
  FakeIPPageShell — каркас всех под-страниц FakeIP по мокапу
  `fakeip-page-layout-v2.html`:

    FakeIPHero (title + панель действий)
      → stat-row (Snippet-слот, опционально)
      → чип-стрип С СЧЁТЧИКАМИ (канонический ui/Tabs с badge на чип)
      → контент под-страницы (Snippet-слот).

  СЧЁТЧИКИ ЧИПОВ — реальные источники (честно):
    - Inbounds      — tun-in (1, движок-управляемый) + длина deviceProxyInstances.
    - Outbounds     — Status.outboundAwgCount + outboundCompositeCount → «5 + 3».
    - Rule sets     — Status.ruleSetCount.
    - DNS           — длина fakeipConfig.dnsRules.
    - Маршруты      — Status.ruleCount.
    - Устройства    — Status.deviceCount.
    - Соединения    — liveConnectionsSnapshot.connectionsTotal (Clash WS),
                      тот же источник, что у sb-router LiveConnectionsChip.

  Live-WS соединений биндится здесь (bindLiveConnectionsStore — идемпотентно),
  поток открыт только при запущенном движке.
-->
<script lang="ts" module>
	import type { Snippet } from 'svelte';

	export interface ShellChip {
		id: string;
		label: string;
		/** Зависит ли контент от живого движка / Clash-runtime (гейт пустого экрана). */
		live: boolean;
	}
</script>

<script lang="ts">
	import { onMount } from 'svelte';
	import { Tabs } from '$lib/components/ui';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { fakeipConfig } from '$lib/stores/fakeipConfig';
	import { deviceProxyInstances } from '$lib/stores/deviceproxy';
	import {
		bindLiveConnectionsStore,
		liveConnectionsSnapshot,
	} from '$lib/components/sb-router/liveConnectionsStore';
	import { formatCompactCount } from './formatCount';
	import { buildAtomicEgresses } from '../outbounds/atomicEgress';
	import { partitionOutbounds } from '../outbounds/partitionOutbounds';
	import FakeIPHero from './FakeIPHero.svelte';
	import type { FakeIPEngineState } from '../engineState';

	interface Props {
		/** Заголовок страницы для hero. */
		title: string;
		/** Чипы под-страниц (порядок/лейблы фиксированы FE-spec §3). */
		chips: ShellChip[];
		/** Активный чип. */
		activeChip: string;
		/** Смена чипа (страница пишет в свой $state и URL через Tabs). */
		onChipChange: (id: string) => void;
		/** Состояние движка (hero-факты + restart-доступность). */
		engineState: FakeIPEngineState;
		wanAutoDetect?: boolean;
		wanInterface?: string;
		/** TCP/IP-стек fakeip-tun (gvisor/system) — hero-факт. */
		fakeipStack?: 'gvisor' | 'system';
		/** Активный fakeip tun-интерфейс из статуса (e.g. «opkgtun0»). */
		fakeipIface?: string;
		onRestart: () => void | Promise<void>;
		/** Контекстная create-кнопка под активный чип (в hero). */
		createButton?: Snippet;
		/** Стат-строка тайлов (опционально — рендерится между hero и чипами). */
		statRow?: Snippet;
		/** Контент активной под-страницы. */
		children: Snippet;
	}

	let {
		title,
		chips,
		activeChip,
		onChipChange,
		engineState,
		wanAutoDetect = true,
		wanInterface,
		fakeipStack = 'gvisor',
		fakeipIface,
		onRestart,
		createButton,
		statRow,
		children,
	}: Props = $props();

	onMount(() => {
		// Тот же WS, что у sb-router LiveConnectionsChip — для счётчика «Соединения».
		bindLiveConnectionsStore();
	});

	const status = singboxRouter.status;
	const dnsRules = fakeipConfig.dnsRules;
	const options = fakeipConfig.options;
	const outbounds = fakeipConfig.outbounds;
	const connSnapshot = liveConnectionsSnapshot;
	// Inbounds badge: tun-in (always 1 in fakeip mode) + device-proxy instances.
	// Polling store auto-fetches on first subscribe ($-access below).
	const dpInstances = deviceProxyInstances;

	// Доступность restart — движок запущен (live) или Clash недоступен, но демон
	// жив (clash-down). При 'stopped'/'not-fakeip' перезапускать нечего.
	const restartEnabled = $derived(engineState === 'live' || engineState === 'clash-down');

	// Реальные счётчики чипов из Status DTO + sub-stores + Clash WS.
	const counts = $derived.by<Record<string, number | string | undefined>>(() => {
		const st = $status;
		// Outbounds: тот же счёт, что на странице — atomic-пул (прокси-эгрессы:
		// туннели+подписки) + composite-группы. Членство atomic не зависит от
		// enrichment, поэтому tunnels/subscriptions тут не нужны (null).
		const atomic = buildAtomicEgresses($options, null, null).length;
		const composite = partitionOutbounds($outbounds).composite.length;
		// Inbounds = tun-in (1, движок-управляемый) + device-proxy-инстансы.
		const dpCount = $dpInstances.data?.length ?? 0;
		return {
			inbounds: 1 + dpCount,
			outbounds: composite > 0 ? `${atomic} + ${composite}` : atomic,
			rulesets: st?.ruleSetCount,
			dns: $dnsRules.length,
			routes: st?.ruleCount,
			devices: st?.deviceCount,
			connections: formatCompactCount($connSnapshot.connectionsTotal),
		};
	});

	// Чипы → tabs канонического ui/Tabs (badge = реальный счётчик).
	const tabs = $derived(
		chips.map((c) => ({
			id: c.id,
			label: c.label,
			badge: counts[c.id],
		})),
	);
</script>

<div class="fakeip-shell">
	<FakeIPHero
		{title}
		{engineState}
		{wanAutoDetect}
		{wanInterface}
		{fakeipStack}
		{fakeipIface}
		{onRestart}
		{restartEnabled}
		{createButton}
	/>

	{#if statRow}
		<div class="stat-row">{@render statRow()}</div>
	{/if}

	<Tabs
		{tabs}
		active={activeChip}
		onchange={onChipChange}
		urlParam="chip"
		defaultTab={chips[0]?.id}
	/>

	<div class="shell-body">
		{@render children()}
	</div>
</div>

<style>
	.fakeip-shell {
		display: flex;
		flex-direction: column;
		gap: var(--sp-3, 0.75rem);
	}

	/* Стат-строка тайлов: грид с тонкими разделителями (мокап `.stats`). */
	.stat-row {
		display: grid;
		grid-template-columns: repeat(5, minmax(0, 1fr));
		gap: 1px;
		background: var(--color-border);
		border: 1px solid var(--color-border);
		border-radius: var(--radius, 10px);
		overflow: hidden;
	}

	.shell-body {
		margin-top: var(--sp-2, 0.5rem);
	}

	@media (max-width: 760px) {
		.stat-row {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}
</style>
