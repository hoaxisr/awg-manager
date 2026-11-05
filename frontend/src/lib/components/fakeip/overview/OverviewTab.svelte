<!--
  Контейнер вкладки «Обзор» по мокапу dash3. Держит обзор-специфичные деривации
  (память sing-box, активные composite-выборы, engineLive-гейт, ярлык статуса) и
  компонует дашборд: 3 карточки (движок · память · composite) → панель
  «Трафик · live» с бар-графиком → грид «Настройки движка». Сегменты-доставка
  живёт в каркасе (FakeIPPageShell), здесь не дублируется.

  ИСТОЧНИКИ/ЧЕСТНОСТЬ — см. под-компоненты: composite — proxies/list через
  sb-router helper; трафик — агрегат singboxTraffic (скорость = дельта байт);
  память — Clash /connections WebSocket поле `memory` (singbox:memory SSE).
-->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { fakeipConfig } from '$lib/stores/fakeipConfig';
	import { tunStackLabel } from '$lib/components/sb-router/tunStack';
	import type { SingboxRouterSettings, SingboxRouterStatus, TunStack } from '$lib/types';
	import { singboxProxies } from '$lib/stores/singboxProxies';
	import { singboxMemory } from '$lib/stores/singboxMemory';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import type { FakeIPEngineState } from '../engineState';
	import OverviewCards from './OverviewCards.svelte';
	import TrafficPanel from './TrafficPanel.svelte';
	import EngineSettingsCard from './EngineSettingsCard.svelte';
	import SourcePreserveSection from '$lib/components/sb-router/SourcePreserveSection.svelte';
	import { mergeAndSaveSettings } from '$lib/components/sb-router/settingsActions';
	import { notifications } from '$lib/stores/notifications';
	import { activeCompositeRows } from './activeComposites';

	interface Props {
		/** Дериватив состояния движка — гейтит живые блоки (1E.3). */
		engineState: FakeIPEngineState;
		/** EngineSettingsCard props (settings-производные). */
		engineOn: boolean;
		wanAutoDetect: boolean;
		wanInterface?: string;
		snifferEnabled: boolean;
		fakeipStack?: TunStack;
		fakeipPool4?: string;
		fakeipPool6?: string;
		fakeipMtu?: number;
		fakeipIface?: string;
		fakeipDns?: string;
		/** Настройки и статус для блока «Сохранять адреса клиентов». */
		settings: SingboxRouterSettings | null;
		status: SingboxRouterStatus | null;
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
		fakeipStack,
		fakeipPool4,
		fakeipPool6,
		fakeipMtu,
		fakeipIface,
		fakeipDns,
		settings,
		status,
		toggleBusy,
		onToggleEngine,
		onRestart,
	}: Props = $props();

	async function patchPreserve(patch: Partial<SingboxRouterSettings>): Promise<void> {
		try {
			await mergeAndSaveSettings(patch);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.fakeip_engine_save_failed());
		}
	}

	// engineLive — gate для живых блоков (composite-выборы, live-трафик).
	const engineLive = $derived(engineState !== 'stopped' && engineState !== 'clash-down');
	const notLiveReason = $derived<'stopped' | 'clash-down' | undefined>(
		engineState === 'clash-down' ? 'clash-down' : engineState === 'stopped' ? 'stopped' : undefined,
	);

	// Ярлык статуса движка для карточки (честно из engineState).
	const engineLabel = $derived(
		engineState === 'live'
			? m.fakeip_overview_engine_running()
			: engineState === 'clash-down'
				? 'clash ↯'
				: m.fakeip_overview_engine_stopped(),
	);

	const outbounds = fakeipConfig.outbounds;
	const options = fakeipConfig.options;

	// Память процесса sing-box из Clash /connections WebSocket `memory` поля.
	const memoryBytes = $derived($singboxMemory);

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
	<OverviewCards {engineLive} {engineLabel} stackLabel={tunStackLabel(fakeipStack)} {memoryBytes} {notLiveReason} {composites} />

	<TrafficPanel {engineLive} {notLiveReason} />

	<EngineSettingsCard
		{engineOn}
		{wanAutoDetect}
		{wanInterface}
		{snifferEnabled}
		{fakeipStack}
		{fakeipPool4}
		{fakeipPool6}
		{fakeipMtu}
		{fakeipIface}
		{fakeipDns}
		{toggleBusy}
		{onToggleEngine}
		{onRestart}
	/>

	{#if settings}
		<div class="preserve">
			<SourcePreserveSection
				cfg={settings}
				{status}
				tunName={fakeipIface || m.sb_router_policy_tun_default_tun_name()}
				onPatch={patchPreserve}
			/>
		</div>
	{/if}
</section>

<style>
	.preserve {
		display: flex;
		flex-direction: column;
		gap: 10px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius, 12px);
		padding: 1rem;
	}

	.overview {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
</style>
