<script lang="ts">
	import { Button, Input } from '$lib/components/ui';

	interface Props {
		label?: string;
		value?: string;
		placeholder?: string;
		hint?: string;
		disabled?: boolean;
	}

	let {
		label = '',
		value = $bindable(''),
		placeholder = '',
		hint = '',
		disabled = false
	}: Props = $props();

	let revealed = $state(false);
</script>

<label class="sensitive-field">
	{#if label}
		<span class="sensitive-label">{label}</span>
	{/if}
	<div class="sensitive-row">
		<Input
			type={revealed ? 'text' : 'password'}
			bind:value
			{placeholder}
			{disabled}
		/>
		<Button variant="secondary" size="sm" {disabled} onclick={() => (revealed = !revealed)}>
			{revealed ? 'Скрыть' : 'Показать'}
		</Button>
	</div>
	{#if hint}
		<p class="sensitive-hint">{hint}</p>
	{/if}
</label>

<style>
	.sensitive-field {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}
	.sensitive-label {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}
	.sensitive-row {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: flex-start;
	}
	.sensitive-row :global(.input-wrap) {
		flex: 1 1 12rem;
		min-width: 0;
	}
	.sensitive-hint {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}
</style>
