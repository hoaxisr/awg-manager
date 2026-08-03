<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/stores';
	import { Network, Server, Route, Wrench, Settings, ChevronRight } from 'lucide-svelte';
	import { usageLevel } from '$lib/stores/settings';
	import { systemInfo } from '$lib/stores/system';
	import { hydrarouteStatus } from '$lib/stores/hydraroute';
	import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
	import { compactLayout, isCompactLayoutActive } from '$lib/stores/compactLayout';
	import {
		buildNavTree,
		isNavItemActive,
		type NavGroup,
		type NavLeaf,
		type NavSection,
	} from '$lib/nav/navTree';

	interface Props {
		mobileOpen?: boolean;
	}

	let { mobileOpen = $bindable(false) }: Props = $props();

	const pathname = $derived($page.url.pathname);
	const tab = $derived($page.url.searchParams.get('tab'));

	const isOS5 = $derived($systemInfo.data?.isOS5 ?? false);
	const singboxInstalled = $derived($systemInfo.data?.singbox?.installed ?? false);
	const hydrarouteInstalled = $derived($hydrarouteStatus.data?.installed ?? false);
	const singboxSettings = singboxRouterStore.settings;
	const fakeipModeActive = $derived($singboxSettings?.routingMode === 'fakeip-tun');

	const navTree = $derived(
		buildNavTree({
			usageLevel: $usageLevel,
			isOS5,
			singboxInstalled,
			hydrarouteInstalled,
			fakeipModeActive,
		}),
	);

	const compact = $derived(isCompactLayoutActive($usageLevel, $compactLayout));

	/** Sing-box groups stay collapsed until the user opens them. */
	let openGroupIds = $state(new Set<string>());
	/** Same key scheme as Tabs.svelte — shared last-child memory. */
	const MENU_MEMORY_PREFIX = 'ui.tabs.menuLast:';
	let menuMemory = $state<Record<string, string>>({});

	function closeMobile() {
		mobileOpen = false;
	}

	function sectionIcon(icon: NavSection['icon']) {
		switch (icon) {
			case 'tunnels':
				return Network;
			case 'servers':
				return Server;
			case 'routing':
				return Route;
			case 'diagnostics':
				return Wrench;
			case 'settings':
				return Settings;
		}
	}

	function leafActive(leaf: NavLeaf): boolean {
		return leaf.match(pathname, tab);
	}

	function groupActive(group: NavGroup): boolean {
		return isNavItemActive(group, pathname, tab);
	}

	function sectionActive(section: NavSection): boolean {
		return isNavItemActive(section, pathname, tab);
	}

	function groupMemoryKey(group: NavGroup): string {
		return `${group.label}:${group.children
			.map((c) => c.id)
			.slice()
			.sort()
			.join(',')}`;
	}

	function readStoredChild(group: NavGroup): string | null {
		if (typeof localStorage === 'undefined') return null;
		try {
			const raw = localStorage.getItem(MENU_MEMORY_PREFIX + groupMemoryKey(group));
			if (raw && group.children.some((c) => c.id === raw)) return raw;
		} catch {
			/* private mode */
		}
		return null;
	}

	function rememberChild(group: NavGroup, childId: string) {
		if (!group.children.some((c) => c.id === childId)) return;
		const key = groupMemoryKey(group);
		if (menuMemory[key] === childId) return;
		menuMemory = { ...menuMemory, [key]: childId };
		if (typeof localStorage === 'undefined') return;
		try {
			localStorage.setItem(MENU_MEMORY_PREFIX + key, childId);
		} catch {
			/* private mode */
		}
	}

	function resolvedGroupChild(group: NavGroup): NavLeaf {
		const key = groupMemoryKey(group);
		const remembered = menuMemory[key] ?? readStoredChild(group);
		return group.children.find((c) => c.id === remembered) ?? group.children[0];
	}

	function isGroupOpen(group: NavGroup): boolean {
		return openGroupIds.has(group.id);
	}

	function openGroup(group: NavGroup) {
		if (openGroupIds.has(group.id)) return;
		const next = new Set(openGroupIds);
		next.add(group.id);
		openGroupIds = next;
	}

	/** Collapse Sing-box groups only after the route leaves them (not on open-click). */
	$effect(() => {
		const path = pathname;
		const t = tab;
		const tree = navTree;
		const current = untrack(() => openGroupIds);
		const next = new Set(current);
		let changed = false;
		for (const section of tree) {
			for (const child of section.children) {
				if (child.kind !== 'group') continue;
				if (next.has(child.id) && !isNavItemActive(child, path, t)) {
					next.delete(child.id);
					changed = true;
				}
			}
		}
		if (changed) openGroupIds = next;
	});

	function onGroupClick(group: NavGroup) {
		openGroup(group);
		const leaf = resolvedGroupChild(group);
		rememberChild(group, leaf.id);
		closeMobile();
	}

	function onGroupLeafClick(group: NavGroup, leaf: NavLeaf) {
		rememberChild(group, leaf.id);
		openGroup(group);
		closeMobile();
	}
</script>

{#if mobileOpen}
	<button
		type="button"
		class="backdrop"
		aria-label="Закрыть меню"
		onclick={closeMobile}
	></button>
{/if}

<aside
	class="sidebar"
	class:mobile-open={mobileOpen}
	class:compact
	aria-label="Основная навигация"
>
	<nav class="nav">
		{#each navTree as section (section.id)}
			{@const Icon = sectionIcon(section.icon)}
			{@const hasChildren = section.children.length > 0}
			{@const active = sectionActive(section)}

			{#if !hasChildren}
				<a
					href={section.href}
					class="section-link"
					class:active
					aria-current={active ? 'page' : undefined}
					onclick={closeMobile}
				>
					<span class="section-icon" aria-hidden="true">
						<Icon size={18} strokeWidth={2} />
					</span>
					<span class="section-label">{section.label}</span>
				</a>
			{:else}
				{@const childActive = section.children.some((c) => isNavItemActive(c, pathname, tab))}
				{@const firstChild = section.children[0]}
				{@const sectionLandingHref =
					firstChild.kind === 'group'
						? resolvedGroupChild(firstChild).href
						: firstChild.href}
				<div class="section-block">
					<a
						href={sectionLandingHref}
						class="section-link"
						class:in-section={childActive}
						onclick={() => {
							if (firstChild.kind === 'group') {
								onGroupClick(firstChild);
							} else {
								closeMobile();
							}
						}}
					>
						<span class="section-icon" aria-hidden="true">
							<Icon size={18} strokeWidth={2} />
						</span>
						<span class="section-label">{section.label}</span>
					</a>

					<div class="section-children">
						{#each section.children as child (child.id)}
							{#if child.kind === 'leaf'}
								{@const leafIsActive = leafActive(child)}
								<a
									href={child.href}
									class="nav-leaf"
									class:active={leafIsActive}
									aria-current={leafIsActive ? 'page' : undefined}
									onclick={closeMobile}
								>
									<span class="nav-leaf-label">{child.label}</span>
									{#if child.badge != null && child.badge > 0}
										<span class="nav-badge">{child.badge}</span>
									{/if}
								</a>
							{:else}
								{@const groupOpen = isGroupOpen(child)}
								{@const groupIsActive = groupActive(child)}
								{@const groupHref = resolvedGroupChild(child).href}
								<div class="nav-group" class:open={groupOpen}>
									<a
										href={groupHref}
										class="nav-group-link"
										class:accent={groupIsActive}
										aria-expanded={groupOpen}
										onclick={() => onGroupClick(child)}
									>
										<span class="nav-leaf-label">{child.label}</span>
										<span class="group-chevron" class:open={groupOpen} aria-hidden="true">
											<ChevronRight size={14} strokeWidth={2} />
										</span>
									</a>
									{#if groupOpen}
										<div class="group-children">
											{#each child.children as leaf (leaf.id)}
												{@const leafIsActive = leafActive(leaf)}
												<a
													href={leaf.href}
													class="nav-leaf nav-leaf--nested"
													class:active={leafIsActive}
													aria-current={leafIsActive ? 'page' : undefined}
													onclick={() => onGroupLeafClick(child, leaf)}
												>
													<span class="nav-leaf-label">{leaf.label}</span>
													{#if leaf.badge != null && leaf.badge > 0}
														<span class="nav-badge">{leaf.badge}</span>
													{/if}
												</a>
											{/each}
										</div>
									{/if}
								</div>
							{/if}
						{/each}
					</div>
				</div>
			{/if}
		{/each}
	</nav>
</aside>

<style>
	.sidebar {
		width: var(--sidebar-width, 240px);
		flex-shrink: 0;
		align-self: stretch;
		height: auto;
		max-height: 100%;
		background: var(--color-bg-secondary);
		border-right: 1px solid var(--color-border);
		overflow-y: auto;
		overflow-x: hidden;
		padding: 0.75rem 0.5rem;
		isolation: isolate;
	}

	.sidebar.compact {
		padding: 0.5rem 0.375rem;
	}

	.nav {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.section-block {
		border-radius: var(--radius-sm);
	}

	.section-link,
	.nav-leaf,
	.nav-group-link {
		display: flex;
		align-items: center;
		box-sizing: border-box;
		width: 100%;
		min-width: 0;
		border-radius: var(--radius-sm);
		text-decoration: none;
		box-shadow: inset 3px 0 0 transparent;
		transition:
			background var(--t-fast) ease,
			color var(--t-fast) ease,
			box-shadow var(--t-fast) ease;
	}

	.section-link {
		gap: 0.625rem;
		height: 40px;
		min-height: 40px;
		padding: 0 0.625rem;
		color: var(--color-text-primary);
		font-size: 13px;
		font-weight: 600;
		line-height: 1;
	}

	.section-link.in-section .section-icon {
		color: var(--color-accent);
	}

	.section-link:hover,
	.nav-leaf:hover,
	.nav-group-link:hover {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.section-link.active,
	.nav-leaf.active {
		color: var(--color-accent);
		/* Opaque mix — transparent tint ghosts over the page while scrolling. */
		background: color-mix(in srgb, var(--color-accent) 18%, var(--color-bg-secondary));
		box-shadow: inset 3px 0 0 var(--color-accent);
	}

	.section-icon {
		display: flex;
		flex: 0 0 18px;
		align-items: center;
		justify-content: center;
		width: 18px;
		height: 18px;
		margin: 0;
		padding: 0;
		line-height: 0;
		color: var(--color-text-muted);
		pointer-events: none;
	}

	.section-icon :global(svg) {
		display: block;
		width: 18px;
		height: 18px;
		margin: 0;
		flex-shrink: 0;
		overflow: visible;
	}

	.section-link.active .section-icon {
		color: var(--color-accent);
	}

	.section-label {
		flex: 1;
		min-width: 0;
		margin: 0;
		padding: 0;
		font-size: 13px;
		font-weight: 600;
		line-height: 1;
		letter-spacing: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.nav-leaf-label {
		display: block;
		flex: 1;
		min-width: 0;
		line-height: 16px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.section-children {
		display: flex;
		flex-direction: column;
		gap: 1px;
		padding: 1px 0 6px;
	}

	.nav-leaf {
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.4375rem 0.625rem 0.4375rem 2.125rem;
		color: var(--color-text-secondary);
		font-size: 12px;
		font-weight: 500;
		line-height: 1;
		min-height: 30px;
	}

	.nav-leaf--nested {
		padding-left: 2.75rem;
	}

	.nav-badge {
		flex-shrink: 0;
		min-width: 1.125rem;
		padding: 0 0.3125rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		font-size: 10px;
		font-weight: 600;
		line-height: 1.4;
		text-align: center;
	}

	.nav-leaf.active .nav-badge {
		background: color-mix(in srgb, var(--color-accent) 22%, var(--color-bg-secondary));
		color: var(--color-accent);
	}

	.nav-group {
		display: flex;
		flex-direction: column;
		gap: 1px;
	}

	.nav-group-link {
		justify-content: space-between;
		gap: 0.375rem;
		padding: 0.375rem 0.5rem 0.375rem 2.125rem;
		color: var(--color-text-secondary);
		font-size: 12px;
		font-weight: 600;
		line-height: 1;
		min-height: 30px;
	}

	.nav-group-link.accent {
		color: var(--color-accent);
		background: transparent;
		box-shadow: none;
	}

	.nav-group-link.accent .group-chevron {
		color: var(--color-accent);
	}

	.group-chevron {
		display: inline-flex;
		flex-shrink: 0;
		color: var(--color-text-muted);
		transition: transform var(--t-fast) ease;
	}

	.group-chevron.open {
		transform: rotate(90deg);
	}

	.group-children {
		display: flex;
		flex-direction: column;
		gap: 1px;
	}

	.backdrop {
		display: none;
	}

	@media (max-width: 1050px) {
		.sidebar {
			position: fixed;
			top: 56px;
			left: 0;
			bottom: 0;
			height: auto;
			max-height: none;
			z-index: var(--z-drawer);
			transform: translateX(-100%);
			transition: transform 0.22s ease;
			box-shadow: 4px 0 24px rgba(0, 0, 0, 0.18);
		}

		.sidebar.mobile-open {
			transform: translateX(0);
		}

		.backdrop {
			display: block;
			position: fixed;
			inset: 56px 0 0 0;
			border: none;
			padding: 0;
			background: rgba(0, 0, 0, 0.4);
			z-index: var(--z-drawer-backdrop);
			cursor: pointer;
		}
	}
</style>
