<script lang="ts">
	import { m } from '$lib/i18n';
	import { Search, X } from 'lucide-svelte';

	interface Props {
		value: string;
	}

	let { value = $bindable() }: Props = $props();
</script>

<div class="dm-search-wrap">
	<Search size={13} class="dm-search-icon" />
	<input
		type="search"
		class="dm-search-inp"
		aria-label={m.firc_devices_search_placeholder()}
		placeholder={m.firc_devices_search_placeholder()}
		bind:value
	/>
	{#if value.trim() !== ''}
		<button
			type="button"
			class="dm-search-clear"
			aria-label={m.common_clear()}
			onclick={() => (value = '')}
		>
			<X size={12} />
		</button>
	{/if}
</div>

<style>
	.dm-search-wrap {
		position: relative;
		display: flex;
		align-items: center;
		width: min(280px, 100%);
		min-width: 170px;
	}

	:global(.dm-search-icon) {
		position: absolute;
		left: 9px;
		color: var(--color-text-muted);
		pointer-events: none;
	}

	.dm-search-inp {
		width: 100%;
		height: 30px;
		padding: 0 26px 0 29px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-size: 12px;
		font-family: inherit;
		outline: none;
		transition: border-color var(--t-fast);
	}

	.dm-search-inp:focus {
		border-color: var(--color-accent);
	}

	.dm-search-clear {
		position: absolute;
		right: 5px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 20px;
		height: 20px;
		border: 0;
		border-radius: 4px;
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
	}

	.dm-search-clear:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-hover);
	}

	@media (max-width: 768px) {
		.dm-search-wrap {
			width: 100%;
		}
	}
</style>
