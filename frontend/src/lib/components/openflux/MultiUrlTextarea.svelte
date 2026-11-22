<script lang="ts">
	import { Badge } from '$lib/components/ui';
	import { laneCountLabel, splitTransportUrls } from './openfluxUrls';

	interface Props {
		label: string;
		value?: string;
		placeholder?: string;
		hint?: string;
		rows?: number;
		oninput?: () => void;
	}

	let {
		label,
		value = $bindable(''),
		placeholder = 'https://...\nhttps://...',
		hint = 'Можно указать несколько ссылок (по одной на строку или через запятую) — включится параллельный stripe-режим для ускорения. Список должен совпадать на сервере и клиенте.',
		rows = 3,
		oninput,
	}: Props = $props();

	const urlCount = $derived(splitTransportUrls(value).length);
</script>

<label class="multi-url-field">
	<span class="multi-url-head">
		<span class="multi-url-label">{label}</span>
		{#if urlCount >= 2}
			<Badge size="xs" variant="info">{laneCountLabel(urlCount)}</Badge>
		{/if}
	</span>
	<textarea
		class="multi-url-textarea"
		bind:value
		{placeholder}
		{rows}
		spellcheck="false"
		oninput={() => oninput?.()}
	></textarea>
	{#if hint}
		<span class="multi-url-hint">{hint}</span>
	{/if}
</label>

<style>
	.multi-url-field {
		grid-column: 1 / -1;
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.multi-url-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.multi-url-label {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.multi-url-textarea {
		width: 100%;
		min-height: 4.75rem;
		padding: 0.55rem 0.7rem;
		border-radius: var(--radius);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		line-height: 1.45;
		resize: vertical;
		box-sizing: border-box;
	}

	.multi-url-textarea:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.multi-url-hint {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		line-height: 1.4;
	}
</style>
