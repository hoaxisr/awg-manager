<script lang="ts">
	// Тонкий слой группы Sing-box — ТОЛЬКО данные для бейджей сайдбара.
	//
	// Зачем отдельный слой. Сайдбар раскрывает группу на ЛЮБОМ её пункте
	// (SideNav.svelte), то есть бейдж режима и счётчик соединений видны и на
	// /sb/tunnels, /sb/awg3, /sb/subscriptions, /sb/geodata. Эти четыре страницы
	// в 5D1 не трогаем, поэтому слой, который их накрывает, обязан быть
	// поведенчески нейтральным: он ничего не рендерит кроме слота и ничего не
	// гейтит. Всё, что видно глазами (баннер черновика, хосты модалок, гейт
	// «sing-box не установлен»), живёт слоем ниже — в (engine)/+layout.svelte.
	//
	// Прайминг делится со страницами: здесь — ровно то, что нужно бейджам
	// (routingMode из настроек, счётчики из статуса, соединения из Clash-WS),
	// а не полный loadAll: сваливать в общий слой всё подряд означало бы десять
	// ручек на каждый вход в любую страницу группы.
	import { onMount } from 'svelte';
	import { get } from 'svelte/store';
	import type { Snippet } from 'svelte';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { bindLiveConnectionsStore } from '$lib/components/sb-router/liveConnectionsStore';

	let { children }: { children: Snippet } = $props();

	onMount(() => {
		// Идемпотентно и на весь процесс: сокет открывается только при запущенном
		// движке (внутри — подписка на status.enabled), закрывается при остановке.
		bindLiveConnectionsStore();
		// Layout монтируется один раз на весь заход в группу, поэтому это не
		// «запрос на каждую страницу». Если данные уже загружены страницей
		// движка — не перезапрашиваем: у бейджей всё есть.
		if (get(singboxRouter.initialized)) return;
		void singboxRouter.reloadStatus();
		void singboxRouter.reloadSettings();
	});
</script>

{@render children()}
