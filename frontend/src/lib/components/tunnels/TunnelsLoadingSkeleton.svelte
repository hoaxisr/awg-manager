<script lang="ts">
	// Скелетон списка туннелей на первую загрузку. Выделено из
	// routes/+page.svelte при расщеплении навигации v3 — тот же скелетон
	// показывают страница /awg/tunnels и дашборд главной.
	// Память формы (число карточек прошлого визита) — в tunnelsSkeletonCount.
	import { tunnelsSkeletonCount } from '$lib/stores/skeletonCounts';
	import TunnelCardSkeleton from './TunnelCardSkeleton.svelte';

	interface Props {
		/** Десктопный «список» рисуется строками таблицы, а не карточками. */
		table?: boolean;
		dense?: boolean;
		compact?: boolean;
	}

	let { table = false, dense = false, compact = false }: Props = $props();
</script>

<div aria-hidden="true">
	{#if table}
		<div class="skel-table">
			{#each Array.from({ length: $tunnelsSkeletonCount }) as _, i (i)}
				<div class="skel-row">
					{#each ['28%', '18%', '14%', '12%', '10%'] as w, ci (ci)}
						<span class="skeleton" style="height: 0.75rem; width: {w}"></span>
					{/each}
				</div>
			{/each}
		</div>
	{:else}
		<div class="tunnel-grid" class:tunnel-grid--dense={dense} class:tunnel-grid--compact={compact}>
			{#each Array.from({ length: $tunnelsSkeletonCount }) as _, i (i)}
				<TunnelCardSkeleton {compact} />
			{/each}
		</div>
	{/if}
</div>

<style>
	.skel-table {
		display: flex;
		flex-direction: column;
		gap: 12px;
		padding: 8px 4px;
	}
	.skel-row {
		display: flex;
		gap: 16px;
		align-items: center;
	}
</style>
