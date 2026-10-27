<script lang="ts">
	interface Props {
		value: string;
		label: string;
		sub?: string;
	}

	let { value, label, sub }: Props = $props();
</script>

<div class="stat">
	<span class="value">{value}</span>
	<span class="stat-label">{label}</span>
	{#if sub}
		<span class="sub">{sub}</span>
	{/if}
</div>

<style>
	.stat {
		display: flex;
		flex-direction: column;
		gap: 0.1875rem;
		padding: 0.75rem 0.875rem;
		min-width: 0;
	}

	.value {
		font: 700 1.125rem/1 var(--font-sans);
		letter-spacing: -0.02em;
		color: var(--color-accent);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.stat-label {
		font: 600 0.6875rem/1.2 var(--font-sans);
		letter-spacing: 0.05em;
		text-transform: uppercase;
		color: var(--color-text-secondary);
	}

	.sub {
		font: 400 0.6875rem/1.3 var(--font-mono);
		color: var(--color-text-muted);
		overflow-wrap: anywhere;
	}

	@media (max-width: 760px) {
		.stat {
			padding: 0.5rem 0.625rem;
			gap: 0.125rem;
		}

		.value {
			font-size: 0.9375rem;
		}

		.stat-label {
			font-size: 0.625rem;
			letter-spacing: 0.04em;
		}

		.sub {
			font-size: 0.625rem;
			line-height: 1.25;
		}
	}
</style>
