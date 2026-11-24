<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Badge } from '$lib/components/ui';
	import {
		Plus,
		Upload,
		Download,
		Search,
		ChevronsDownUp,
		ChevronsUpDown,
		Shield,
		LayoutGrid,
		Copy,
		Edit3,
	} from 'lucide-svelte';

	type GroupView = 'grid' | 'list';

	interface Props {
		groupsCount: number;
		netfilterDirty: boolean;
		searchQuery: string;
		groupView: GroupView;
		anyExpanded: boolean;
		proxyGroupsCount: number;
		groupSelectMode: boolean;
		onsetView: (view: GroupView) => void;
		ontoggleExpandAll: () => void;
		onimportFile: (e: Event) => void;
		onexport: () => void;
		oncopyDomains: () => void;
		onopenProxyGroups: () => void;
		onopenCatalog: () => void;
		ontoggleSelectMode: () => void;
		oncreateGroup: () => void;
	}

	let {
		groupsCount,
		netfilterDirty,
		searchQuery = $bindable(),
		groupView,
		anyExpanded,
		proxyGroupsCount,
		groupSelectMode,
		onsetView,
		ontoggleExpandAll,
		onimportFile,
		onexport,
		oncopyDomains,
		onopenProxyGroups,
		onopenCatalog,
		ontoggleSelectMode,
		oncreateGroup,
	}: Props = $props();

	let importFileInput = $state<HTMLInputElement | null>(null);
</script>

<!-- Top Toolbar (styled like Sing-box / AWG Manager toolbar) -->
<div class="groups-toolbar">
	<div class="toolbar-left">
		<span class="groups-count">
			{m.firc_groups_count_label()} <strong>{groupsCount}</strong>
			{#if netfilterDirty}
				<Badge variant="warning" size="sm">{m.firc_groups_netfilter_dirty()}</Badge>
			{/if}
		</span>
		<div class="search-box">
			<Search size={14} class="search-icon" />
			<input
				type="search"
				class="inp search-inp"
				placeholder={m.firc_groups_search_placeholder()}
				bind:value={searchQuery}
			/>
		</div>
	</div>

	<div class="toolbar-actions">
		<!-- Компактные чипы, а не SegmentedControl: широкий контрол выталкивал поиск на вторую строку. -->
		<div class="group-view-switch" role="group" aria-label={m.firc_groups_view_aria()}>
			<button type="button" class:active={groupView === 'grid'} aria-pressed={groupView === 'grid'} onclick={() => onsetView('grid')}>{m.firc_groups_view_grid()}</button>
			<button type="button" class:active={groupView === 'list'} aria-pressed={groupView === 'list'} onclick={() => onsetView('list')}>{m.firc_groups_view_list()}</button>
		</div>
		<button
			type="button"
			class="route-action-btn"
			hidden={groupView === 'grid'}
			title={anyExpanded ? m.firc_groups_collapse_all() : m.firc_groups_expand_all()}
			onclick={ontoggleExpandAll}
		>
			{#if anyExpanded}
				<ChevronsDownUp size={15} />
			{:else}
				<ChevronsUpDown size={15} />
			{/if}
		</button>

		<input
			type="file"
			accept=".firc,.mtrickle,.json"
			style="display: none"
			bind:this={importFileInput}
			onchange={onimportFile}
		/>
		<Button variant="ghost" size="sm" onclick={() => importFileInput?.click()}>
			{#snippet iconBefore()}<Download size={14} />{/snippet}
			{m.firc_groups_import()}
		</Button>
		<Button variant="ghost" size="sm" onclick={onexport}>
			{#snippet iconBefore()}<Upload size={14} />{/snippet}
			{m.common_export()}
		</Button>
		<Button
			variant="ghost"
			size="sm"
			title={m.firc_groups_copy_domains_title()}
			onclick={oncopyDomains}
		>
			{#snippet iconBefore()}<Copy size={14} />{/snippet}
			{m.firc_groups_copy_domains()}
		</Button>
		<Button variant="secondary" size="sm" onclick={onopenProxyGroups}>
			{#snippet iconBefore()}<Shield size={14} />{/snippet}
			{m.firc_groups_proxy_groups_btn({ count: proxyGroupsCount })}
		</Button>
		<Button variant="secondary" size="sm" onclick={onopenCatalog}>
			{#snippet iconBefore()}<LayoutGrid size={14} />{/snippet}
			{m.firc_groups_service_catalog()}
		</Button>
		<Button
			variant={groupSelectMode ? 'primary' : 'ghost'}
			size="sm"
			onclick={ontoggleSelectMode}
		>
			{#snippet iconBefore()}<Edit3 size={14} />{/snippet}
			{groupSelectMode ? m.firc_bulk_select_groups_done() : m.firc_bulk_select_groups_btn()}
		</Button>
		<Button variant="secondary" size="sm" onclick={oncreateGroup}>
			{#snippet iconBefore()}<Plus size={14} />{/snippet}
			{m.firc_groups_add_group()}
		</Button>
	</div>
</div>

<style>
	.groups-toolbar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 10px 14px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius);
	}

	.toolbar-left {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 12px;
		flex: 1 1 auto;
		min-width: 0;
	}

	.groups-count {
		font-size: 13px;
		color: var(--text-secondary);
		display: inline-flex;
		align-items: center;
		gap: 8px;
		white-space: nowrap;
	}

	.search-box {
		position: relative;
		display: flex;
		align-items: center;
		flex: 1 1 220px;
		max-width: 340px;
	}

	.search-box :global(.search-icon) {
		position: absolute;
		left: 10px;
		color: var(--text-muted);
		pointer-events: none;
	}

	.search-inp {
		width: 100%;
		padding-left: 30px !important;
	}

	.toolbar-actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 6px;
	}

	.group-view-switch { display: inline-flex; gap: .2rem; }
	.route-action-btn[hidden] { display: none; }
	.group-view-switch button { padding: .35rem .6rem; border: 1px solid var(--border); background: var(--bg-secondary); color: var(--text-secondary); border-radius: 6px; cursor: pointer; font: inherit; font-size: .75rem; }
	.group-view-switch button.active { border-color: var(--accent); color: var(--text-primary); }
	.group-view-switch button:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }

	.inp {
		padding: 5px 9px;
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		color: var(--text-primary);
		font-size: 12.5px;
		font-family: inherit;
	}

	.inp:focus {
		outline: none;
		border-color: var(--accent);
	}
</style>
