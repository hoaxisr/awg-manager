<script lang="ts">
	import { ChevronDown, ChevronRight, Folder, RefreshCw } from 'lucide-svelte';
	import type { TreeDir } from './types';

	interface Props {
		nodes: TreeDir[];
		currentPath: string;
		onToggle: (node: TreeDir) => void;
		onNavigate: (path: string) => void;
	}

	let { nodes, currentPath, onToggle, onNavigate }: Props = $props();
</script>

<aside class="side-tree">
	<div class="tree-head">Структура /opt</div>
	<ul class="tree-root">
		{#each nodes as node (node.path)}
			<li>
				<button type="button" class="tree-item" class:active={currentPath === node.path} onclick={() => onToggle(node)}>
					{#if node.loading}
						<RefreshCw size={13} class="spin" />
					{:else if node.expanded}
						<ChevronDown size={13} />
					{:else}
						<ChevronRight size={13} />
					{/if}
					<Folder size={13} class="tree-folder-icon" />
					<span>{node.name}</span>
				</button>
				{#if node.expanded && node.children.length > 0}
					<ul class="tree-nested">
						{#each node.children as child (child.path)}
							<li>
								<button
									type="button"
									class="tree-item"
									class:active={currentPath === child.path}
									onclick={() => onNavigate(child.path)}
								>
									<Folder size={13} class="tree-folder-icon" />
									<span>{child.name}</span>
								</button>
							</li>
						{/each}
					</ul>
				{/if}
			</li>
		{/each}
	</ul>
</aside>

<style>
	.side-tree {
		border-right: 1px solid var(--color-border);
		padding-right: 0.5rem;
		overflow-y: auto;
		max-height: 520px;
	}
	.tree-head {
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		color: var(--color-text-muted);
		margin-bottom: 0.4rem;
	}
	.tree-root, .tree-nested {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.tree-nested {
		padding-left: 0.75rem;
	}
	.tree-item {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		width: 100%;
		padding: 0.25rem 0.35rem;
		border-radius: 4px;
		background: none;
		border: none;
		color: var(--color-text-secondary);
		font-size: 0.8rem;
		text-align: left;
		cursor: pointer;
	}
	.tree-item:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.tree-item.active {
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.18));
		color: var(--color-accent);
		font-weight: 600;
	}
	:global(.tree-folder-icon) {
		color: #60a5fa;
		flex-shrink: 0;
	}

	@media (max-width: 900px) {
		.side-tree {
			display: none;
		}
	}
</style>
