<script lang="ts">
	import { page } from '$app/stores';
</script>

<div class="error-page">
	<p class="error-code">{$page.status}</p>
	<h1 class="error-title">
		{$page.status === 404 ? 'Раздел не найден' : 'Что-то пошло не так'}
	</h1>
	{#if $page.status === 404}
		<p class="error-hint">Навигация изменилась в v3.0 — нужный раздел ищите в меню слева.</p>
	{/if}
	<a class="error-link" href="/">На обзор</a>
</div>

<style>
	.error-page {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 0.5rem;
		padding: 3rem 1rem;
		text-align: center;
	}

	.error-code {
		font-family: var(--font-mono);
		font-size: 2.5rem;
		font-weight: 700;
		color: var(--color-text-muted);
		margin: 0;
	}

	.error-title {
		font-size: 1.25rem;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}

	.error-hint {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		margin: 0;
	}

	.error-link {
		margin-top: 0.75rem;
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--color-accent);
		text-decoration: none;
	}

	.error-link:hover {
		color: var(--color-accent-hover);
	}
</style>
