<script lang="ts">
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import type { TunnelListItem } from '$lib/types';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { Tabs } from '$lib/components/ui';
	import { LogsTerminal } from '$lib/components/diagnostics';
	import ConnectionsTab from './ConnectionsTab.svelte';
	import ChecksTab from './ChecksTab.svelte';

	type ActiveTab = 'logs' | 'connections' | 'checks';

	let activeTab = $state<ActiveTab>('logs');
	let tunnels = $state<TunnelListItem[]>([]);

	const diagnosticsTabs = [
		{ id: 'logs', label: 'Журнал' },
		{ id: 'connections', label: 'Соединения' },
		{ id: 'checks', label: 'Проверки' },
	];

	// Deep-link: ?tab=logs|connections|checks. Legacy `tests`/`dnscheck`
	// (which used to render the health rail inside the logs tab) now map
	// to `checks` since the rail moved into its own tab.
	$effect(() => {
		const tabParam = $page.url.searchParams.get('tab');
		if (tabParam === 'connections') {
			activeTab = 'connections';
		} else if (tabParam === 'checks' || tabParam === 'tests' || tabParam === 'dnscheck') {
			activeTab = 'checks';
		} else {
			activeTab = 'logs';
		}
	});

	onMount(async () => {
		try {
			tunnels = await api.listTunnels();
		} catch {
			tunnels = [];
		}
	});

	const pageTitle = $derived(
		activeTab === 'connections' ? 'Соединения · Диагностика' :
		activeTab === 'checks' ? 'Проверки · Диагностика' :
		'Журнал · Диагностика',
	);
</script>

<svelte:head>
	<title>{pageTitle} - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="Диагностика" />

	<Tabs
		tabs={diagnosticsTabs}
		active={activeTab}
		onchange={(id) => (activeTab = id as ActiveTab)}
	/>

	{#if activeTab === 'logs'}
		<LogsTerminal />
	{:else if activeTab === 'connections'}
		<ConnectionsTab />
	{:else if activeTab === 'checks'}
		<ChecksTab {tunnels} />
	{/if}
</PageContainer>
