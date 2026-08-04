<script lang="ts">
	// Скелет страницы «Серверы»: полоса-селектор сверху + detail-карточка.
	// Геометрия продублирована из ServerStrip и страницы (scoped-стили из
	// компонента недостижимы): плашка 240px × 56px, зазор 0.625rem.
	interface Props {
		count?: number;
	}
	let { count = 2 }: Props = $props();
</script>

<div class="layout" aria-hidden="true">
	<div class="strip">
		{#each Array.from({ length: count }) as _, i (i)}
			<div class="strip-item">
				<span class="skeleton-circle dot"></span>
				<span class="skeleton" style="height: 0.75rem; width: 60%"></span>
			</div>
		{/each}
	</div>
	<div class="card">
		<div class="skeleton" style="height: 1.125rem; width: 40%"></div>
		<div class="skeleton" style="height: 0.75rem; width: 65%"></div>
		<div class="skeleton" style="height: 0.75rem; width: 55%"></div>
		<div class="skeleton" style="height: 8rem; width: 100%; margin-top: 8px;"></div>
	</div>
</div>

<style>
	.layout {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.strip {
		display: flex;
		flex-wrap: wrap;
		gap: 0.625rem;
	}

	.strip-item {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		box-sizing: border-box;
		width: 240px;
		height: 56px;
		padding: 0.625rem 1rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
	}

	.dot {
		width: 8px;
		height: 8px;
		flex: none;
	}

	.card {
		display: flex;
		flex-direction: column;
		gap: 12px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 16px;
	}

	@media (max-width: 640px) {
		.strip-item {
			width: 100%;
		}
	}
</style>
