<script lang="ts">
	// Страница «AWG3 туннели» — вынесена из вкладки главной
	// (routes/+page.svelte, навигация v3): срез состояния вкладки переехал сюда
	// целиком, главная сохранила только dashboard-потребление стора awg3.
	import { onMount, onDestroy } from 'svelte';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { Awg3ImportModal, Awg3TunnelsSection } from '$lib/components/awg3';
	import { awg3Tunnels } from '$lib/stores/awg3';
	import {
		SINGBOX_LAYOUT_STORAGE_KEY,
		parseSingboxLayoutMode,
		readTunnelMobileLayout,
		resolveTunnelRenderMode,
		subscribeTunnelMobileLayout,
		type SingboxLayoutMode,
	} from '$lib/constants/singboxLayout';

	const AWG3_TUNNELS_LAYOUT_STORAGE_KEY = 'awg3_tunnels_layout_mode';

	// Polling-store subscription: первая подписка запускает опрос,
	// последняя отписка его останавливает.
	let unsubAwg3Tunnels: (() => void) | undefined;
	onMount(() => {
		unsubAwg3Tunnels = awg3Tunnels.subscribe(() => {});
	});
	onDestroy(() => unsubAwg3Tunnels?.());

	let awg3List = $derived($awg3Tunnels.data ?? []);

	let isMobile = $state(readTunnelMobileLayout());
	onMount(() =>
		subscribeTunnelMobileLayout((mobile) => {
			isMobile = mobile;
		}),
	);

	let layoutMode = $state<SingboxLayoutMode>('compact');
	let layoutReady = false;
	onMount(() => {
		// Обратная совместимость: без ключа вкладки берём старый общий sing-box-ключ.
		const stored =
			localStorage.getItem(AWG3_TUNNELS_LAYOUT_STORAGE_KEY) ??
			localStorage.getItem(SINGBOX_LAYOUT_STORAGE_KEY);
		const parsed = parseSingboxLayoutMode(stored);
		if (parsed) layoutMode = parsed;
		layoutReady = true;
	});
	$effect(() => {
		if (!layoutReady) return;
		localStorage.setItem(AWG3_TUNNELS_LAYOUT_STORAGE_KEY, layoutMode);
	});
	let renderMode = $derived(resolveTunnelRenderMode(isMobile, layoutMode));

	let searchQuery = $state('');

	// Импорт .conf — единственный владелец Escape держит модалку на странице.
	let importOpen = $state(false);

	// Авто-проверка delay: на главной она перезапускалась при входе на вкладку,
	// здесь «вход на поверхность» — само монтирование страницы (lastAutoCheckKey
	// начинается пустым), поэтому отдельный entry-nonce не нужен.
	// AWG3-эндпоинты — обычные sing-box outbound'ы без on/off: все они пробуемы.
	let autoDelayCheckNonce = $state(0);
	let lastAutoCheckKey = '';
	$effect(() => {
		const tags = awg3List
			.map((t) => t.tag)
			.sort()
			.join(',');
		if (!tags || tags === lastAutoCheckKey) return;
		lastAutoCheckKey = tags;
		autoDelayCheckNonce += 1;
	});
</script>

<svelte:head>
	<title>Endpoints - AWGM</title>
</svelte:head>

<PageContainer>
	<PageHeader title="Endpoints" />
	<Awg3TunnelsSection
		tunnels={awg3List}
		{renderMode}
		layout={layoutMode}
		showGridListToggle={true}
		{autoDelayCheckNonce}
		onImport={() => (importOpen = true)}
		bind:searchQuery
		bind:layoutMode
	/>
</PageContainer>

<Awg3ImportModal
	open={importOpen}
	onclose={() => (importOpen = false)}
	onimported={() => void awg3Tunnels.refetch()}
/>
