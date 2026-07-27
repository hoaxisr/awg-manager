<script lang="ts">
	// Скелет страницы «Серверы»: рейл + detail-карточка. Каркас .layout
	// продублирован из routes/awg/servers/+page.svelte (scoped-стили страницы
	// из компонента недостижимы) — включая мобильный брейкпоинт 768px.
	interface Props {
		count?: number;
	}
	let { count = 2 }: Props = $props();
</script>

<div class="layout" aria-hidden="true">
	<!-- Мобильный (<768px) реальный рейл прячется за компакт-селектором — зеркалим -->
	<div class="skeleton mobile-bar"></div>
	<aside class="rail">
		<div class="skeleton" style="height: 0.875rem; width: 60%; margin-bottom: 12px;"></div>
		{#each Array.from({ length: count }) as _, i (i)}
			<div class="rail-item">
				<span class="skeleton-circle dot"></span>
				<span class="skeleton" style="height: 0.75rem; width: 70%"></span>
			</div>
		{/each}
	</aside>
	<main class="detail">
		<div class="card">
			<div class="skeleton" style="height: 1.125rem; width: 40%"></div>
			<div class="skeleton" style="height: 0.75rem; width: 65%"></div>
			<div class="skeleton" style="height: 0.75rem; width: 55%"></div>
			<div class="skeleton" style="height: 8rem; width: 100%; margin-top: 8px;"></div>
		</div>
	</main>
</div>

<style>
	.layout {
		display: flex;
		gap: 1rem;
		align-items: flex-start;
	}
	.rail {
		/* Геометрия реального ServerRail: 240px + карточный chrome (ревью T4) */
		width: 240px;
		flex: none;
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 8px;
		border: 1px solid var(--color-border);
		border-radius: 12px;
		background: var(--color-bg-secondary);
	}
	.mobile-bar {
		display: none;
		height: 2.5rem;
		width: 100%;
	}
	.rail-item {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.dot {
		width: 10px;
		height: 10px;
		flex: none;
	}
	.detail {
		flex: 1;
		min-width: 0;
	}
	.card {
		display: flex;
		flex-direction: column;
		gap: 12px;
		border: 1px solid var(--color-border);
		border-radius: 12px;
		padding: 16px;
	}
	@media (max-width: 768px) {
		.layout {
			flex-direction: column;
			gap: 0.75rem;
		}
		.rail {
			display: none;
		}
		.mobile-bar {
			display: block;
		}
		.detail {
			width: 100%;
		}
	}
</style>
