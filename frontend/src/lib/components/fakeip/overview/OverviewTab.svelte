<!--
  Контейнер вкладки «Обзор» по мокапу dash3. Держит обзор-специфичные деривации
  (deviceCount, активные composite-выборы, engineLive-гейт, ярлык статуса) и
  компонует дашборд: 3 карточки (движок · устройства · composite) → панель
  «Трафик · live» с бар-графиком → грид «Настройки движка». Сегменты-доставка
  живёт в каркасе (FakeIPPageShell), здесь не дублируется.

  ИСТОЧНИКИ/ЧЕСТНОСТЬ — см. под-компоненты: composite — proxies/list через
  sb-router helper; трафик — агрегат singboxTraffic (скорость = дельта байт).
  RAM (/memory), opkgtun-индекс и fakeip-пул в DTO отсутствуют — опущены.
-->
<script lang="ts">
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxProxies } from '$lib/stores/singboxProxies';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import type { FakeIPEngineState } from '../engineState';
	import OverviewCards from './OverviewCards.svelte';
	import TrafficPanel from './TrafficPanel.svelte';
	import EngineSettingsCard from './EngineSettingsCard.svelte';
	import SegmentsDelivery from './SegmentsDelivery.svelte';
	import { activeCompositeRows } from './activeComposites';

	interface Props {
		/** Дериватив состояния движка — гейтит живые блоки (1E.3). */
		engineState: FakeIPEngineState;
		/** EngineSettingsCard props (settings-производные). */
		engineOn: boolean;
		wanAutoDetect: boolean;
		wanInterface?: string;
		snifferEnabled: boolean;
		toggleBusy: boolean;
		onToggleEngine: (turnOn: boolean) => void;
		onRestart: () => void;
	}

	let {
		engineState,
		engineOn,
		wanAutoDetect,
		wanInterface,
		snifferEnabled,
		toggleBusy,
		onToggleEngine,
		onRestart,
	}: Props = $props();

	// engineLive — gate для живых блоков (composite-выборы, live-трафик).
	const engineLive = $derived(engineState !== 'stopped' && engineState !== 'clash-down');
	const notLiveReason = $derived<'stopped' | 'clash-down' | undefined>(
		engineState === 'clash-down' ? 'clash-down' : engineState === 'stopped' ? 'stopped' : undefined,
	);

	// Ярлык статуса движка для карточки (честно из engineState).
	const engineLabel = $derived(
		engineState === 'live'
			? 'работает'
			: engineState === 'clash-down'
				? 'clash ↯'
				: 'остановлен',
	);

	const status = singboxRouter.status;
	const outbounds = singboxRouter.outbounds;
	const options = singboxRouter.options;

	const deviceCount = $derived($status?.deviceCount ?? 0);

	// Активные composite-выборы: config outbounds + live proxies/list + подписки,
	// через общий sb-router helper. singboxProxies — reference-counted polling
	// store: подписка ($singboxProxies) сама стартует/останавливает опрос.
	const composites = $derived(
		activeCompositeRows({
			outbounds: $outbounds,
			outboundOptions: $options,
			subscriptions: $subscriptionsStore.data,
			proxyGroups: $singboxProxies.data ?? [],
		}),
	);
</script>

<section class="overview">
	<OverviewCards {engineLive} {engineLabel} {deviceCount} {notLiveReason} {composites} />

	<TrafficPanel {engineLive} {notLiveReason} />

	<EngineSettingsCard
		{engineOn}
		{wanAutoDetect}
		{wanInterface}
		{snifferEnabled}
		{toggleBusy}
		{onToggleEngine}
		{onRestart}
	/>

	<!-- «Доставка DNS · сегменты» — внизу Обзора (мокап dash3). -->
	<SegmentsDelivery />
</section>

<style>
	.overview {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
</style>
