<script lang="ts">
	// Страница «Sing-box · Маршрутизация» — вынесена из вкладок /routing
	// (навигация v3). Хостит ОБЕ взаимоисключающие поверхности sing-box:
	// TProxy (sb-router) и FakeIP. Активная определяется settings.routingMode,
	// неактивная доступна через локальный переключатель ?view= и рисуется
	// приглушённой — ровно как это делали вкладки контейнера.
	import { onMount } from 'svelte';
	import { get } from 'svelte/store';
	import { beforeNavigate, goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { PageContainer, EmptyState } from '$lib/components/layout';
	import { Tabs, Button, Modal } from '$lib/components/ui';
	import { systemInfo } from '$lib/stores/system';
	import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
	import { modeSwitch, modeSwitchBusy } from '$lib/stores/modeSwitch';
	import { SingboxRouterRedesignPage } from '$lib/components/sb-router';
	import FakeIPTab from '$lib/components/fakeip/FakeIPTab.svelte';
	import ModeSwitchHost from '$lib/components/routing/ModeSwitchHost.svelte';

	type View = 'tproxy' | 'fakeip';

	const settings = singboxRouterStore.settings;
	const status = singboxRouterStore.status;

	onMount(() => {
		// Статус и настройки нужны сразу: по routingMode выбирается поверхность
		// по умолчанию, а mute-XOR переключателя читает `enabled && routingMode`
		// (issue #420). Без прайминга неактивный режим выглядел бы активным.
		void singboxRouterStore.reloadStatus();
		void singboxRouterStore.reloadSettings();
	});

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

	let singboxInstalled = $derived($systemInfo.data?.singbox?.installed ?? false);
	let systemKnown = $derived($systemInfo.lastFetchedAt > 0 || $systemInfo.status === 'error');

	let ruleCount = $derived($status?.ruleCount ?? 0);
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
	let leaving = false;

	function hasDraft(): boolean {
		return get(singboxRouterStore.staging)?.hasDraft ?? false;
	}

	function requestView(id: string): void {
		if (modeSwitchBusy(get(modeSwitch))) return;
		// Как и вкладки контейнера: спрашиваем при уходе С поверхности TProxy —
		// черновик правит именно её конфигурацию.
		if (view === 'tproxy' && id !== 'tproxy' && hasDraft()) {
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
	<title>Маршрутизация sing-box - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	{#if systemKnown && !singboxInstalled}
		<EmptyState
			title="Sing-box не установлен"
			description="Маршрутизация sing-box доступна после установки пакета — откройте «Настройки»."
		/>
	{:else}
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
	{/if}
	<ModeSwitchHost />
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
