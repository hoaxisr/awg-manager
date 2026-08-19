<script lang="ts">
	import { Button, Card } from '$lib/components/ui';
	import { RefreshCw, Plus, Search, Layers } from 'lucide-svelte';

	interface Props {
		count: number;
		loading: boolean;
		searchQuery: string;
		onRefresh: () => void;
		onCreate: () => void;
	}

	let { count, loading, searchQuery = $bindable(''), onRefresh, onCreate }: Props = $props();
</script>

<Card padding="sm">
	<div class="head-toolbar">
		<div class="toolbar-left">
			<div class="panel-title-wrap">
				<Layers size={18} class="text-accent" />
				<h3>Службы Entware (init.d)</h3>
				<span class="count-badge">{count}</span>
			</div>

			<Button size="sm" variant="primary" onclick={onCreate}>
				{#snippet iconBefore()}<Plus size={14} />{/snippet}
				Добавить службу
			</Button>
		</div>

		<div class="toolbar-right">
			<div class="search-box">
				<Search size={13} class="search-icon" />
				<input
					type="text"
					placeholder="Фильтр по имени или пути…"
					bind:value={searchQuery}
				/>
			</div>

			<Button size="sm" variant="ghost" onclick={onRefresh} disabled={loading}>
				{#snippet iconBefore()}<RefreshCw size={14} class={loading ? 'spin' : ''} />{/snippet}
				Обновить
			</Button>
		</div>
	</div>
</Card>

<style>
	.head-toolbar {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.65rem;
	}

	.toolbar-left, .toolbar-right {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		flex-wrap: wrap;
	}

	.panel-title-wrap {
		display: flex;
		align-items: center;
		gap: 0.45rem;
	}
	.panel-title-wrap h3 {
		margin: 0;
		font-size: 0.95rem;
		font-weight: 700;
	}

	.count-badge {
		font-size: 0.72rem;
		font-weight: 700;
		padding: 0.08rem 0.45rem;
		border-radius: 999px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}

	.search-box {
		position: relative;
		display: flex;
		align-items: center;
	}

	:global(.search-icon) {
		position: absolute;
		left: 0.55rem;
		color: var(--color-text-muted);
	}

	.search-box input {
		padding: 0.3rem 0.55rem 0.3rem 1.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.8rem;
		width: 220px;
	}

	.search-box input:focus {
		border-color: var(--color-accent);
		outline: none;
	}
</style>
