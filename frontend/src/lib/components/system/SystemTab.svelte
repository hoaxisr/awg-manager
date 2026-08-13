<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { Tabs } from '$lib/components/ui';
	import FileManager from './FileManager.svelte';
	import ServicesPanel from './ServicesPanel.svelte';
	import PackagesPanel from './PackagesPanel.svelte';
	import SystemTerminal from './SystemTerminal.svelte';

	type SystemView = 'files' | 'services' | 'packages' | 'terminal';

	const views: { id: SystemView; label: string }[] = [
		{ id: 'files', label: 'Файлы' },
		{ id: 'services', label: 'Службы' },
		{ id: 'packages', label: 'Пакеты opkg' },
		{ id: 'terminal', label: 'Терминал' },
	];

	function initialView(): SystemView {
		const v = $page.url.searchParams.get('view');
		if (v === 'services' || v === 'packages' || v === 'terminal') return v;
		return 'files';
	}

	let activeView = $state<SystemView>(initialView());

	$effect(() => {
		const v = $page.url.searchParams.get('view');
		if (v === 'files' || v === 'services' || v === 'packages' || v === 'terminal') {
			activeView = v;
		} else if (!$page.url.searchParams.has('view')) {
			activeView = 'files';
		}
	});

	function setView(id: SystemView) {
		activeView = id;
		const url = new URL($page.url);
		if (id === 'files') url.searchParams.delete('view');
		else url.searchParams.set('view', id);
		void goto(url.pathname + url.search + url.hash, {
			replaceState: true,
			keepFocus: true,
			noScroll: true,
		});
	}
</script>

<div class="system-tools">
	<Tabs tabs={views} active={activeView} onchange={(id) => setView(id as SystemView)} />

	<div class="panel">
		{#if activeView === 'files'}
			<FileManager />
		{:else if activeView === 'services'}
			<ServicesPanel />
		{:else if activeView === 'packages'}
			<PackagesPanel />
		{:else}
			<SystemTerminal />
		{/if}
	</div>
</div>

<style>
	.system-tools {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
	.panel {
		min-height: 420px;
	}
</style>
