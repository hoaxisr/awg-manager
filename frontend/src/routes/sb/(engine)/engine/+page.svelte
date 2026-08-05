<script lang="ts">
	// Страница «Движок».
	//
	// ВРЕМЕННО ДО 5D2a: здесь целиком лежит сегодняшнее содержимое /sb/routing —
	// обе взаимоисключающие поверхности sing-box (TProxy и FakeIP) с локальным
	// переключателем ?view=. Причина: весь UI движка смонтирован ровно в одном
	// месте, и если 5D1 заменит /sb/routing редиректом, а «Движок» сделает
	// заглушкой, приложение разом потеряет редакторы правил, эксперт-панели и
	// fakeip-табы — а вместе с ними и способ проверить ворота подэтапа (черновик
	// создаётся только правкой в редакторе, modeSwitch.request зовут только
	// FakeIPTab и StatusDrawer).
	//
	// Волна 5D2a заменит это на страницу по разделу 3 спеки
	// docs/superpowers/specs/2026-08-04-nav-v3-5d-singbox-design.md.
	//
	// ModeSwitchHost, баннер черновика, окно пересборки ipset, гейт «Sing-box не
	// установлен» и напоминание о непринятом черновике переехали в
	// (engine)/+layout.svelte. Статус и настройки движка (routingMode для
	// поверхности по умолчанию и mute-XOR переключателя, issue #420) праймит
	// /sb/+layout.svelte — тем же парным reloadStatus+reloadSettings, что стоял
	// здесь.
	import { get } from 'svelte/store';
	import { page } from '$app/stores';
	import { PageContainer } from '$lib/components/layout';
	import { Tabs } from '$lib/components/ui';
	import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
	import { modeSwitch, modeSwitchBusy } from '$lib/stores/modeSwitch';
	import { SingboxRouterRedesignPage } from '$lib/components/sb-router';
	import FakeIPTab from '$lib/components/fakeip/FakeIPTab.svelte';

	type View = 'tproxy' | 'fakeip';

	const settings = singboxRouterStore.settings;
	const status = singboxRouterStore.status;

	// Поверхность по умолчанию = текущий режим маршрутизации. Это замена
	// бывшего one-shot автоселекта вкладки fakeip на /routing.
	let modeDefault = $derived<View>($settings?.routingMode === 'fakeip-tun' ? 'fakeip' : 'tproxy');

	// Явный ?view= из URL забираем синхронно при монтировании: settings приходят
	// асинхронно, и без этого поздний modeDefault перебил бы deep-link.
	const initialView = $page.url.searchParams.get('view');
	let chosenView = $state<View | null>(
		initialView === 'fakeip' || initialView === 'tproxy' ? initialView : null,
	);
	let view = $derived<View>(chosenView ?? modeDefault);

	// Без `?? 0`: при недоступном бэкенде статуса нет, и бейдж «0» соврал бы про
	// пустую конфигурацию. undefined Tabs просто не рисует — так же, как чипы
	// FakeIP-каркаса (badge: st?.ruleCount).
	let ruleCount = $derived($status?.ruleCount);
	let engineEnabled = $derived(!!$status?.enabled);
	let routingMode = $derived($settings?.routingMode);

	let tabItems = $derived([
		{ id: 'tproxy', label: 'TProxy', badge: ruleCount, muted: engineEnabled && routingMode === 'fakeip-tun' },
		{ id: 'fakeip', label: 'FakeIP', muted: engineEnabled && routingMode === 'tproxy' },
	]);

	function requestView(id: string): void {
		if (modeSwitchBusy(get(modeSwitch))) return;
		// Про непринятый черновик здесь не спрашиваем: обе поверхности делят один
		// серверный слот, баннер черновика висит над обеими, и смена поверхности
		// — это переход внутри группы. Напоминание даёт слой группы, и только на
		// выходе наружу.
		chosenView = id as View;
	}
</script>

<svelte:head>
	<title>Движок sing-box - AWGM</title>
</svelte:head>

<PageContainer>
	<Tabs
		tabs={tabItems}
		active={view}
		onchange={requestView}
		urlParam="view"
		defaultTab={modeDefault}
	/>

	{#if view === 'fakeip'}
		<FakeIPTab />
	{:else}
		<SingboxRouterRedesignPage />
	{/if}
</PageContainer>
