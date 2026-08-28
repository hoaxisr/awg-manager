<script lang="ts">
	import { ArrowUp } from 'lucide-svelte';

	interface Props {
		currentPath: string;
		onNavigate: (path: string) => void;
	}

	let { currentPath, onNavigate }: Props = $props();

	const breadcrumbParts = $derived.by(() => {
		if (!currentPath || currentPath === '/') return [{ name: '/', path: '/' }];
		const parts = currentPath.split('/').filter(Boolean);
		const res: { name: string; path: string }[] = [{ name: '/', path: '/' }];
		let acc = '';
		for (const p of parts) {
			acc += '/' + p;
			res.push({ name: p, path: acc });
		}
		return res;
	});

	const parent = $derived(parentPath(currentPath));

	function parentPath(p: string): string | null {
		if (!p || p === '/') return null;
		const clean = p.replace(/\/$/, '');
		const i = clean.lastIndexOf('/');
		return i <= 0 ? '/' : clean.slice(0, i);
	}
</script>

<div class="nav-bar">
	<div class="breadcrumbs">
		{#if parent}
			<button type="button" class="btn-up" onclick={() => onNavigate(parent!)} title="Наверх">
				<ArrowUp size={13} />
			</button>
		{/if}
		{#each breadcrumbParts as part, i}
			{#if i > 0}<span class="bc-sep">/</span>{/if}
			<button
				type="button"
				class="bc-item"
				class:active={i === breadcrumbParts.length - 1}
				onclick={() => onNavigate(part.path)}
			>
				{part.name}
			</button>
		{/each}
	</div>
</div>

<style>
	.nav-bar {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-top: 0.5rem;
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border);
	}
	.breadcrumbs {
		display: flex;
		align-items: center;
		gap: 0.2rem;
		flex-wrap: wrap;
		font-size: 0.82rem;
	}
	.btn-up {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
		padding: 0.2rem 0.4rem;
		border-radius: 4px;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
	}
	.btn-up:hover {
		background: var(--color-bg-tertiary);
	}
	.bc-item {
		background: none;
		border: none;
		padding: 0.15rem 0.35rem;
		border-radius: 4px;
		font-family: var(--font-mono, monospace);
		font-size: 0.8rem;
		color: var(--color-text-secondary);
		cursor: pointer;
	}
	.bc-item:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.bc-item.active {
		font-weight: 700;
		color: var(--color-text-primary);
	}
	.bc-sep {
		color: var(--color-text-muted);
		font-size: 0.75rem;
	}
</style>
