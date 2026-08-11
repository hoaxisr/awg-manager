<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		children: Snippet;
	}

	let { children }: Props = $props();
</script>

<div class="strip">
	{@render children()}
</div>

<style>
	.strip {
		display: grid;
		grid-template-columns: repeat(4, minmax(0, 1fr));
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		overflow: hidden;
	}

	.strip > :global(:not(:last-child)) {
		border-right: 1px solid var(--color-border);
	}

	@media (max-width: 960px) {
		.strip {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}

		.strip > :global(:nth-child(2n)) {
			border-right: none;
		}

		.strip > :global(:nth-child(-n + 2)) {
			border-bottom: 1px solid var(--color-border);
		}
	}
</style>
