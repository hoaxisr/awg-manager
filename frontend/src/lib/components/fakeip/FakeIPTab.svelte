<script lang="ts">
	import { onMount } from 'svelte';
	import { get } from 'svelte/store';
	import { PageContainer } from '$lib/components/layout';
	import { Button } from '$lib/components/ui';
	import { Plus } from 'lucide-svelte';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxStatus } from '$lib/stores/singbox';
	import {
		NotEnabledScreen,
		OverviewTab,
		InboundsTab,
		OutboundsTab,
		DnsTab,
		RuleSetsTab,
		RoutesTab,
		DevicesTab,
		FakeIPPageShell,
		deriveFakeIPEngineState,
		type ShellChip,
	} from '$lib/components/fakeip';
	import { ConnectionsSubTab } from '$lib/components/routing/singboxRouter';
	import { LogsTerminal } from '$lib/components/diagnostics';
	import { modeSwitch, modeSwitchBusy } from '$lib/stores/modeSwitch';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';

	onMount(() => {
		// routingMode comes from the router SETTINGS, which are only fetched by
		// loadAll(). On direct navigation to /fakeip the store may still be cold
		// (settings === null → routingMode undefined → 'not-fakeip'), so prime it
		// once. Idempotent; refreshes status too.
		if (!get(singboxRouter.initialized)) void singboxRouter.loadAll();
	});

	// FE-spec §3: fixed order + labels of the 9 FakeIP sub-pages. Real chip
	// counters (badge) are wired inside FakeIPPageShell from the live Status DTO
	// + DNS sub-stores + Clash connections WS. `live` marks chips whose content
	// depends on the running engine / Clash runtime (FE-spec §12.1): they show
	// the "движок остановлен" empty-state or a clash-down banner. Config-oriented
	// chips render regardless of state.
	const CHIPS: ShellChip[] = [
		{ id: 'overview', label: 'Обзор', live: false },
		{ id: 'inbounds', label: 'Inbounds', live: false },
		{ id: 'outbounds', label: 'Outbounds', live: false },
		{ id: 'rulesets', label: 'Rule sets', live: false },
		{ id: 'dns', label: 'DNS', live: false },
		{ id: 'routes', label: 'Маршруты', live: false },
		{ id: 'devices', label: 'Устройства', live: false },
		{ id: 'connections', label: 'Соединения', live: true },
		{ id: 'logs', label: 'Журнал', live: true }
	];

	let activeTab = $state('overview');

	let activeChip = $derived(CHIPS.find((c) => c.id === activeTab) ?? CHIPS[0]);

	// Hero-title по активному чипу (мокап: «FakeIP Router» на Обзоре, имя раздела
	// на остальных — ср. page-outbounds-v3 title «Outbounds»).
	const pageTitle = $derived(activeTab === 'overview' ? 'FakeIP Router' : activeChip.label);

	// singboxRouter is a composite store: `settings` and `status` are exposed as
	// separate sub-stores, not a single subscribe value. routingMode lives in
	// SETTINGS, not status (verified against backend). Absent on legacy payloads
	// → 'tproxy' default, handled inside the pure helper.
	const settings = singboxRouter.settings;
	const status = singboxRouter.status;
	const routingMode = $derived($settings?.routingMode);
	const running = $derived($singboxStatus.data?.running ?? false);

	// Engine toggle ON-state: persisted fakeip-tun AND enabled. Drives the card
	// toggle; flipping it requests a mode switch via the shared modeSwitch store.
	const engineOn = $derived(
		$settings?.enabled === true && $settings?.routingMode === 'fakeip-tun',
	);

	// TODO(1E.7/slice3): derive from live-block fetch errors. Live blocks are
	// still stubs, so there is no robust Clash-reachability signal yet — assume
	// reachable rather than fabricate a probe.
	const clashReachable = true;

	const engineState = $derived(
		deriveFakeIPEngineState({
			routingMode,
			enabled: $settings?.enabled === true,
			running,
			clashReachable,
		}),
	);

	// Routing-mode switch is owned by the shared `modeSwitch` store + the
	// page-level <ModeSwitchHost> (confirm + progress). The tab only requests a
	// target mode; `switchBusy` mirrors the in-flight state for the toggle.
	function handleEnableRequested(): void {
		modeSwitch.request('fakeip-tun');
	}

	// Engine-card toggle: ON→OFF requests a switch to 'off'; OFF→ON re-enables
	// fakeip-tun. The shared host drives confirm + progress.
	function handleToggleEngine(turnOn: boolean): void {
		modeSwitch.request(turnOn ? 'fakeip-tun' : 'off');
	}

	const switchBusy = $derived(modeSwitchBusy($modeSwitch));

	async function handleRestart(): Promise<void> {
		try {
			await api.singboxControl('restart');
			notifications.success('Перезапуск sing-box запущен');
		} catch (e) {
			const msg = e instanceof Error ? e.message : 'Не удалось перезапустить sing-box';
			notifications.error(msg);
		}
	}
</script>

<PageContainer>
	<!--
		Шапка (PageHeader) заменена на FakeIPHero внутри FakeIPPageShell — мокап
		fakeip-page-layout-v2: kick + title + hsub + панель действий.
	-->

	{#if engineState === 'not-fakeip'}
		<NotEnabledScreen onEnableRequested={handleEnableRequested} />
	{:else}
		<!--
			Каркас под мокап fakeip-page-layout-v2: hero (title + панель действий) +
			чипы-счётчики + полоса «Доставка DNS · сегменты». Контент под-страницы
			остаётся прежним (переработка по странице — следующие задачи).
		-->
		<FakeIPPageShell
			title={pageTitle}
			chips={CHIPS}
			activeChip={activeTab}
			onChipChange={(id) => (activeTab = id)}
			{engineState}
			wanAutoDetect={$settings?.wanAutoDetect ?? true}
			wanInterface={$settings?.wanInterface}
			fakeipStack={$settings?.fakeipStack ?? 'gvisor'}
			fakeipIface={$status?.fakeipIface}
			onRestart={handleRestart}
		>
			{#snippet createButton()}
				{#if activeTab === 'outbounds'}
					<!-- TODO(Slice 3.2): открыть мастер создания outbound. -->
					<Button variant="primary" size="sm" disabled title="Скоро">
						{#snippet iconBefore()}<Plus size={14} />{/snippet}
						Outbound
					</Button>
				{/if}
			{/snippet}

			{#if activeTab === 'overview'}
			<!--
				Обзор по мокапу dash3: 3 карточки (движок / устройства / composite) +
				панель «Трафик · live» с бар-графиком + грид «Настройки движка».
				Счётчики живут в чипах, сегменты-доставка — в каркасе. Живые блоки
				(composite, трафик) гейтятся engineLive (1E.3).
			-->
			<OverviewTab
				{engineState}
				{engineOn}
				wanAutoDetect={$settings?.wanAutoDetect ?? true}
				wanInterface={$settings?.wanInterface}
				snifferEnabled={$settings?.snifferEnabled ?? false}
				fakeipStack={$settings?.fakeipStack ?? 'gvisor'}
				fakeipPool4={$settings?.fakeipPool4}
				fakeipPool6={$settings?.fakeipPool6}
				fakeipMtu={$settings?.fakeipMtu}
				fakeipIface={$status?.fakeipIface}
				fakeipDns={$status?.fakeipDns}
				toggleBusy={switchBusy}
				onToggleEngine={handleToggleEngine}
				onRestart={handleRestart}
			/>
		{:else if activeTab === 'inbounds'}
			<!--
				Inbounds-чип по мокапу page-inbounds-v2: tun-in (read-only,
				управляется движком — interface/address/стек·MTU/DNS из бэкенда)
				+ SOCKS/HTTP-входы (переиспользуют фичу device-proxy: list/runtime/
				listen-choices + InboundSettingsDrawer для правки, тумблер enabled).
				Конфиг-инстансы видны всегда; статус-точки деградируют по engineState.
			-->
			<InboundsTab {engineState} />
		{:else if activeTab === 'outbounds'}
			<!--
				Outbounds (Slice 3.2): atomic-карточки + composite-группы
				(активный участник / тест группы / select), переиспользование
				каталога outbounds и компонентов sb-router. Конфиг виден всегда;
				живые сигналы деградируют по engineState (FE-spec §12.1).
			-->
			<OutboundsTab {engineState} />
		{:else if activeTab === 'dns'}
			<!--
				DNS-чип по мокапу page-dns-v3: серверы / правила / перезаписи в
				3-блочной сетке. Всё конфиг (нет live-рантайма) → доступно при любом
				состоянии движка; переиспользует эдит-модалы + CRUD sb-router.
			-->
			<DnsTab />
		{:else if activeTab === 'rulesets'}
			<!--
				Rule sets-чип по мокапу page-rulesets-v3: одна карточка на всю ширину
				(заголовок + описание + тип-фильтр-чипы + таблица). Конфиг (нет
				live-рантайма) → доступно при любом состоянии движка; переиспользует
				RuleSetAddModal + dat-каталог + CRUD sb-router.
			-->
			<RuleSetsTab />
		{:else if activeTab === 'routes'}
			<!--
				«Маршруты»-чип по мокапу page-routes: одна карточка на всю ширину —
				плотная таблица route-правил (grip | # | match | → outbound | действия)
				+ read-only «final»-строка. first-match, порядок drag'ом. Конфиг (нет
				live-рантайма) → доступно при любом состоянии движка; переиспользует
				reorderDrag + MatcherChip/RuleOutboundAction + RuleEditModal + каталог
				наборов sb-router.
			-->
			<RoutesTab />
		{:else if activeTab === 'devices'}
			<!--
				«Устройства»-чип по мокапу page-devices: ОДНА карточка — таблица
				NDMS-устройств (hotspot) + персональная привязка (route по
				source_ip_cidr → outbound). Список информационный (NDMS) → виден при
				любом состоянии движка; счётчик соединений — живой сигнал, деградирует
				по engineState.
			-->
			<DevicesTab {engineState} />
		{:else if activeTab === 'connections'}
			<!--
				«Соединения»-чип по мокапу page-connections = ВЕРБАТИМ текущий
				sb-router connections-вью (футер мокапа: «как в текущем sb-router-вью»).
				Переиспользуем ConnectionsSubTab — свой Clash WS, totals, разбивка по
				outbound/host/client, фильтры/поиск, таблица с kill. Живой блок:
				при остановленном движке / clash-down — стандартная заглушка.
			-->
			{#if engineState === 'live'}
				<ConnectionsSubTab />
			{:else if engineState === 'clash-down'}
				<section class="chip-stub">
					<h2 class="chip-stub-title">{activeChip.label}</h2>
					<p class="chip-stub-note chip-stub-error">
						Clash-runtime недоступен — живые соединения временно не работают.
					</p>
				</section>
			{:else}
				<section class="chip-stub">
					<h2 class="chip-stub-title">{activeChip.label}</h2>
					<p class="chip-stub-note chip-stub-empty">
						Движок остановлен — живые соединения недоступны.
					</p>
				</section>
			{/if}
		{:else if activeTab === 'logs'}
			<!--
				«Журнал»-чип по мокапу page-log = sing-box-bucket общего лог-вью
				приложения. Переиспользуем diagnostics LogsTerminal с lockBucket="singbox"
				(level-фильтр / subgroup-чипы inbound/outbound/dns/router/runtime / поиск /
				пауза / очистить — всё внутри; переключатель app/singbox скрыт). Живой
				блок: при остановленном движке / clash-down — заглушка.
			-->
			{#if engineState === 'live'}
				<LogsTerminal lockBucket="singbox" />
			{:else if engineState === 'clash-down'}
				<section class="chip-stub">
					<h2 class="chip-stub-title">{activeChip.label}</h2>
					<p class="chip-stub-note chip-stub-error">
						Clash-runtime недоступен — живой журнал sing-box временно не работает.
					</p>
				</section>
			{:else}
				<section class="chip-stub">
					<h2 class="chip-stub-title">{activeChip.label}</h2>
					<p class="chip-stub-note chip-stub-empty">
						Движок остановлен — живой журнал sing-box недоступен.
					</p>
				</section>
			{/if}
		{:else}
			<section class="chip-stub">
				<h2 class="chip-stub-title">{activeChip.label}</h2>

				{#if activeChip.live && engineState === 'stopped'}
					<p class="chip-stub-note chip-stub-empty">
						Движок остановлен — живые данные недоступны.
					</p>
				{:else if activeChip.live && engineState === 'clash-down'}
					<p class="chip-stub-note chip-stub-error">
						Clash-runtime недоступен — живые блоки временно не работают.
						Конфигурация по-прежнему доступна.
					</p>
				{:else}
					<p class="chip-stub-note">Раздел в разработке (Slice 1E+)</p>
				{/if}
			</section>
		{/if}
		</FakeIPPageShell>
	{/if}
</PageContainer>

<style>
	.chip-stub {
		padding: 2rem;
		border: 1px dashed var(--border);
		border-radius: var(--radius);
		text-align: center;
	}

	.chip-stub-title {
		margin: 0 0 0.5rem;
		font-size: 1rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.chip-stub-note {
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-muted);
	}

	.chip-stub-error {
		color: var(--color-error, var(--text-primary));
	}
</style>
