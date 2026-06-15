<!--
  Контейнер вкладки «Обзор» (Slice 3.1). Держит обзор-специфичные деривации
  (hero-сводка, deviceCount, активные composite-выборы, engineLive-гейт) и
  компонует дашборд: hero-сводка + operational-карточки + live-трафик над
  составным readiness/движком (1E.7) и сегментами. Страница остаётся тонким
  оркестратором — обзорная логика живёт здесь.

  ИСТОЧНИКИ/ЧЕСТНОСТЬ — см. под-компоненты: hero — config-счётчики (Status +
  DNS sub-stores), composite — proxies/list через sb-router helper, live-трафик
  — агрегат singboxTraffic. RAM (/memory) опущен (нет реального store).
-->
<script lang="ts">
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxProxies } from '$lib/stores/singboxProxies';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import type { FakeIPEngineState } from '../engineState';
	import ReadinessPanel from './ReadinessPanel.svelte';
	import EngineSettingsCard from './EngineSettingsCard.svelte';
	import SegmentsDelivery from './SegmentsDelivery.svelte';
	import OverviewSummary from './OverviewSummary.svelte';
	import OperationalCards from './OperationalCards.svelte';
	import LiveTraffic from './LiveTraffic.svelte';
	import { overviewSummary } from './overviewSummary';
	import { activeCompositeRows } from './activeComposites';

	interface Props {
		/** Живые сигналы движка (из Status DTO / singboxStatus). */
		running: boolean;
		active: boolean;
		sourcePreserved?: boolean;
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
		running,
		active,
		sourcePreserved,
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

	const status = singboxRouter.status;
	const dnsServers = singboxRouter.dnsServers;
	const dnsRules = singboxRouter.dnsRules;
	const outbounds = singboxRouter.outbounds;
	const options = singboxRouter.options;

	const summary = $derived(
		overviewSummary({
			status: $status,
			dnsServerCount: $dnsServers.length,
			dnsRuleCount: $dnsRules.length,
		}),
	);

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
	<div class="overview-wide">
		<OverviewSummary {summary} />
		<OperationalCards {running} {active} {deviceCount} {engineLive} {notLiveReason} {composites} />
		<LiveTraffic {engineLive} {notLiveReason} />
	</div>

	<div class="overview-cols">
		<ReadinessPanel {running} {active} {sourcePreserved} />
		<EngineSettingsCard
			{engineOn}
			{wanAutoDetect}
			{wanInterface}
			{snifferEnabled}
			{toggleBusy}
			{onToggleEngine}
			{onRestart}
		/>
	</div>

	<SegmentsDelivery />
</section>

<style>
	.overview {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	/* Широкие блоки на всю ширину: hero-сводка, operational-карточки, live-трафик. */
	.overview-wide {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	/* Составной readiness + карта движка — две колонки (как в 1E.7 MVP). */
	.overview-cols {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
		gap: 1rem;
		align-items: start;
	}

	@media (max-width: 720px) {
		.overview-cols {
			grid-template-columns: 1fr;
		}
	}
</style>
