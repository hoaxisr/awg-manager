<script lang="ts">
	import { m } from '$lib/i18n';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { AlertTriangle } from 'lucide-svelte';
	import { Tabs } from '$lib/components/ui';
	import FileManager from './FileManager.svelte';
	import ServicesPanel from './ServicesPanel.svelte';
	import PackagesPanel from './PackagesPanel.svelte';
	import SystemTerminal from './SystemTerminal.svelte';
	import PortsPanel from './PortsPanel.svelte';
	import ProcessesPanel from './ProcessesPanel.svelte';
	import PinkPoniesPanel from './PinkPoniesPanel.svelte';
	import AIAssistantPanel from './AIAssistantPanel.svelte';
	import { poniesUnlocked } from '$lib/stores/poniesUnlocked';

	type SystemView = 'ai' | 'files' | 'services' | 'packages' | 'terminal' | 'ports' | 'processes' | 'ponies';

	const baseViews: { id: SystemView; label: string }[] = $derived([
		{ id: 'ai', label: 'ИИ-помощник' },
		{ id: 'files', label: m.system_tab_files() },
		{ id: 'services', label: m.system_tab_services() },
		{ id: 'packages', label: m.system_tab_packages() },
		{ id: 'terminal', label: m.nav_terminal() },
		{ id: 'ports', label: m.system_tab_ports() },
		{ id: 'processes', label: m.system_tab_processes() },
	]);

	const views = $derived.by(() => {
		if ($poniesUnlocked || $page.url.searchParams.get('view') === 'ponies') {
			return [...baseViews, { id: 'ponies' as SystemView, label: m.system_tab_ponies() }];
		}
		return baseViews;
	});

	function initialView(): SystemView {
		const v = $page.url.searchParams.get('view');
		if (v === 'ai' || v === 'services' || v === 'packages' || v === 'terminal' || v === 'ports' || v === 'processes' || v === 'ponies') return v as SystemView;
		return 'ai';
	}

	let activeView = $state<SystemView>(initialView());

	$effect(() => {
		const v = $page.url.searchParams.get('view');
		if (v === 'ponies') {
			if ($poniesUnlocked) {
				activeView = 'ponies';
			} else {
				activeView = 'ai';
			}
		} else if (v === 'ai' || v === 'files' || v === 'services' || v === 'packages' || v === 'terminal' || v === 'ports' || v === 'processes') {
			activeView = v;
		} else if (!$page.url.searchParams.has('view')) {
			activeView = 'ai';
		}
	});

	function setView(id: SystemView) {
		activeView = id;
		const url = new URL($page.url);
		url.searchParams.set('view', id);
		void goto(url.pathname + url.search + url.hash, {
			replaceState: true,
			keepFocus: true,
			noScroll: true,
		});
	}
</script>

<div class="system-tools">
	<div class="expert-disclaimer" role="note">
		<AlertTriangle size={16} aria-hidden="true" />
		<span>
			<strong>{m.system_tab_expert_title()}</strong>
			{m.system_tab_expert_before_code()} <code>root</code>
			{m.system_tab_expert_after_code()}
		</span>
	</div>

	<Tabs tabs={views} active={activeView} onchange={(id) => setView(id as SystemView)} />

	<div class="panel">
		{#if activeView === 'ai'}
			<AIAssistantPanel />
		{:else if activeView === 'files'}
			<FileManager />
		{:else if activeView === 'services'}
			<ServicesPanel />
		{:else if activeView === 'packages'}
			<PackagesPanel />
		{:else if activeView === 'ports'}
			<PortsPanel />
		{:else if activeView === 'processes'}
			<ProcessesPanel />
		{:else if activeView === 'ponies'}
			<PinkPoniesPanel onhide={() => setView('processes')} />
		{:else}
			<SystemTerminal compact={true} />
		{/if}
	</div>
</div>

<style>
	.expert-disclaimer {
		display: flex;
		align-items: flex-start;
		gap: 0.6rem;
		padding: 0.55rem 0.75rem;
		background: color-mix(in srgb, var(--warning, #f59e0b) 12%, transparent);
		border: 1px solid color-mix(in srgb, var(--warning, #f59e0b) 45%, transparent);
		border-radius: var(--radius-sm, 6px);
		color: var(--color-text-primary, #e5e7eb);
		font-size: 0.82rem;
		line-height: 1.4;
	}
	.expert-disclaimer :global(svg) {
		flex-shrink: 0;
		margin-top: 0.1rem;
		color: var(--warning, #f59e0b);
	}
	.expert-disclaimer code {
		padding: 0 0.25rem;
		background: rgba(0, 0, 0, 0.2);
		border-radius: 3px;
		font-size: 0.78rem;
	}
	.system-tools {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
	.panel {
		min-height: 420px;
	}
</style>
