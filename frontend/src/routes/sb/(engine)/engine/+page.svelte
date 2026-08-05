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
	// Гард черновика переедет в layout группы (Task 5) — пока живёт здесь, как
	// жил на /sb/routing. ModeSwitchHost, баннер черновика, окно пересборки ipset
	// и гейт «Sing-box не установлен» уже переехали в (engine)/+layout.svelte.
	// Статус и настройки движка (routingMode для поверхности по умолчанию и
	// mute-XOR переключателя, issue #420) праймит /sb/+layout.svelte — тем же
	// парным reloadStatus+reloadSettings, что стоял здесь.
	import { get } from 'svelte/store';
	import { beforeNavigate, goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { PageContainer } from '$lib/components/layout';
	import { Tabs, Button, Modal } from '$lib/components/ui';
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

	// ── Гард несохранённого черновика sing-box ────────────────────────────────
	// Две двери: локальная смена поверхности (?view=) и уход СО СТРАНИЦЫ
	// (сайдбар, крошки, back/forward) — последнее ловится beforeNavigate.
	let pending = $state<{ kind: 'view'; view: View } | { kind: 'nav'; href: string } | null>(null);
	// Повторный goto после подтверждения не должен снова упереться в гард.
	let leaving = $state(false);

	function hasDraft(): boolean {
		return get(singboxRouterStore.staging)?.hasDraft ?? false;
	}

	function requestView(id: string): void {
		if (modeSwitchBusy(get(modeSwitch))) return;
		// Черновик — один на обе поверхности: после 5D0 правила, наборы и
		// outbound'ы лежат в ОБЩЕМ слоте, и правка с FakeIP-табов копится в том
		// же pending, что с TProxy. Поэтому спрашиваем при любой смене
		// поверхности, а не только при уходе с TProxy.
		if (id !== view && hasDraft()) {
			pending = { kind: 'view', view: id as View };
			return;
		}
		chosenView = id as View;
	}

	beforeNavigate((nav) => {
		if (leaving) return;
		// `to === null` — уход из приложения (закрытие вкладки, внешняя ссылка);
		// willUnload — полная перезагрузка документа. Там работает нативный
		// диалог браузера, наша модалка отрисоваться уже не успеет.
		if (!nav.to || nav.willUnload) return;
		// Навигации в пределах страницы — смена ?view=, визард ?add/?edit,
		// ?sub/?chip/?mode/?trace/?q и writeUrl из Tabs — пропускаем: это не уход.
		if (nav.to.url.pathname === nav.from?.url.pathname) return;
		if (!hasDraft()) return;
		nav.cancel();
		pending = { kind: 'nav', href: nav.to.url.href };
	});

	function confirmLeave(): void {
		const target = pending;
		pending = null;
		if (!target) return;
		if (target.kind === 'view') {
			chosenView = target.view;
			return;
		}
		leaving = true;
		void goto(target.href).finally(() => {
			leaving = false;
		});
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

<Modal
	open={pending !== null}
	title="Несохранённые правки маршрутизации"
	size="sm"
	onclose={() => (pending = null)}
>
	<p>Правки sing-box сохранены как черновик, но <strong>ещё не применены</strong>. Если уйти отсюда — маршрутизация не изменится, пока вы не нажмёте «Применить».</p>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (pending = null)}>Остаться</Button>
		<Button variant="primary" size="md" onclick={confirmLeave}>Уйти всё равно</Button>
	{/snippet}
</Modal>
