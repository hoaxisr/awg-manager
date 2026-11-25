<script lang="ts">
	import { onMount, tick, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { api } from '$lib/api/client';
	import { LoadingSpinner, PageContainer, PageHeader } from '$lib/components/layout';
	import {
		PukhososPatrol,
		SettingsAccessTab,
		SettingsComponentsTab,
		SettingsFooter,
		SettingsGeneralTab,
		SettingsLogsTab,
		SettingsSystemTab,
		SettingsUpdatesTab,
	} from '$lib/components/settings';
	import { providePageState } from '$lib/components/settings/pageState.svelte';
	import { SettingsEditor } from '$lib/components/settings/settingsEditor.svelte';
	import { Tabs } from '$lib/components/ui';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { setSettings as setGlobalSettings } from '$lib/stores/settings';
	import {
		resolveSettingsLink,
		revealSettingsTarget,
		settingsTabHref,
		type SettingsLink,
		type SettingsTab,
	} from '$lib/utils/settingsLink';

	providePageState();

	let editor = $state.raw<SettingsEditor | null>(null);
	let loading = $state(true);
	let footerPatrolWidth = $state(0);

	const link = $derived($page.url.pathname === '/settings' ? resolveSettingsLink($page.url) : null);
	let activeTab = $state<SettingsTab>(resolveSettingsLink($page.url)?.tab ?? 'general');
	const tabs = $derived([
		{ id: 'general', label: m.settings_tab_general() },
		{ id: 'updates', label: m.settings_tab_updates() },
		{ id: 'components', label: m.settings_tab_components() },
		{ id: 'access', label: m.settings_tab_access() },
		{ id: 'logs', label: m.settings_tab_logs() },
		{ id: 'system', label: m.settings_tab_system() },
	] satisfies Array<{ id: SettingsTab; label: string }>);

	/** Куда привела глубокая ссылка; до ручной смены вкладки. */
	let target = $state.raw<SettingsLink | null>(null);
	let confirmedToken = '';

	// Глубокая ссылка — разовое событие: открыли вкладку, запомнили блок и
	// убрали её из адреса, иначе она возвращала бы на свою вкладку при каждом
	// переключении. Токен подтверждения адреса панели обязан уйти на бэкенд с
	// любой вкладки и до загрузки настроек: не дойдёт — адрес откатится.
	$effect(() => {
		if (!link) return;
		const opened = link;
		untrack(() => followLink(opened));
	});

	function followLink(link: SettingsLink) {
		if (link.listenConfirm && link.listenConfirm !== confirmedToken) {
			confirmedToken = link.listenConfirm;
			void confirmListenChange(link.listenConfirm);
		}
		activeTab = link.tab;
		target = link;
		void goto(settingsTabHref(link.tab), { replaceState: true, noScroll: true, keepFocus: true });
	}

	async function confirmListenChange(token: string) {
		try {
			await api.serverListenConfirm(token);
			notifications.success(m.settings_http_confirmed_saved());
		} catch (e) {
			notifications.error(
				m.settings_http_confirm_failed({ error: e instanceof Error ? e.message : String(e) }),
			);
		}
	}

	$effect(() => {
		const anchor = target?.anchor;
		if (!editor || !anchor) return;
		void tick().then(() => requestAnimationFrame(() => revealSettingsTarget(anchor)));
	});

	function selectTab(id: string) {
		activeTab = id as SettingsTab;
		target = null;
	}

	onMount(async () => {
		try {
			const settings = await api.getSettings();
			setGlobalSettings(settings);
			editor = new SettingsEditor(settings);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_load_failed());
		} finally {
			loading = false;
		}
	});
</script>

<svelte:head>
	<title>{m.settings_page_document_title()}</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title={m.nav_settings()} />
	<!-- Пока в адресе ссылка, Tabs его не читает и не пишет: иначе прежняя вкладка
	     вернулась бы в адрес наперегонки с её разбором. -->
	<Tabs {tabs} active={activeTab} onchange={selectTab} urlParam={link ? undefined : 'tab'} defaultTab="general" />
	{#if loading}
		<div class="flex justify-center py-8">
			<LoadingSpinner size="md" />
		</div>
	{:else if editor}
		<div class="settings-layout">
			{#if activeTab === 'general'}
				<SettingsGeneralTab {editor} expandLevel={target?.anchor === 'usage-level'} />
			{:else if activeTab === 'updates'}
				<SettingsUpdatesTab {editor} />
			{:else if activeTab === 'components'}
				<SettingsComponentsTab {editor} />
			{:else if activeTab === 'access'}
				<SettingsAccessTab {editor} />
			{:else if activeTab === 'logs'}
				<SettingsLogsTab {editor} />
			{:else if activeTab === 'system'}
				<SettingsSystemTab {editor} />
			{/if}

			<div id="settings-footer-block" class="settings-footer-patrol-host" bind:clientWidth={footerPatrolWidth}>
				<PukhososPatrol trackWidth={footerPatrolWidth} />
				<SettingsFooter />
			</div>
		</div>
	{/if}
</PageContainer>

<style>
	.settings-footer-patrol-host {
		position: relative;
		overflow: visible;
	}
</style>
