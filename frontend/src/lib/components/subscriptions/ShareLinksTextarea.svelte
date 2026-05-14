<script lang="ts">
	import { highlightShareEditorContent } from '$lib/utils/shareEditorHighlight';
	import { tick } from 'svelte';

	interface Props {
		value?: string;
		rows?: number;
		disabled?: boolean;
		placeholder?: string;
		onpaste?: (e: ClipboardEvent & { currentTarget: HTMLTextAreaElement }) => void;
	}

	let {
		value = $bindable(''),
		rows = 6,
		disabled = false,
		placeholder = '',
		onpaste,
	}: Props = $props();

	let ta = $state<HTMLTextAreaElement | null>(null);
	let back = $state<HTMLPreElement | null>(null);

	let highlightHtml = $derived(highlightShareEditorContent(value));

	$effect(() => {
		value;
		void tick().then(syncScroll);
	});

	function syncScroll(): void {
		if (!ta || !back) return;
		back.scrollTop = ta.scrollTop;
		back.scrollLeft = ta.scrollLeft;
	}
</script>

<!-- Resize lives on the outer box so height can shrink below text; inner textarea scrolls. -->
<div
	class="share-links-editor"
	class:disabled
	style="--sl-rows: {rows}"
>
	<div class="share-links-stack">
		<pre
			class="share-links-back"
			aria-hidden="true"
			bind:this={back}
		>{@html highlightHtml}</pre>
		<textarea
			class="share-links-ta"
			bind:this={ta}
			bind:value={value}
			rows={1}
			{disabled}
			{placeholder}
			spellcheck="false"
			autocomplete="off"
			autocapitalize="off"
			onscroll={syncScroll}
			onpaste={onpaste}
		></textarea>
	</div>
</div>

<style>
	.share-links-editor {
		position: relative;
		box-sizing: border-box;
		display: flex;
		flex-direction: column;
		width: 100%;
		min-width: 0;
		min-height: 140px;
		height: calc(var(--sl-rows, 6) * 1.45em + 1rem);
		padding: 0.5rem 0.7rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 0.78rem;
		color: var(--color-text-primary);
		transition: border-color 120ms;
		resize: vertical;
		overflow: hidden;
	}
	.share-links-stack {
		position: relative;
		flex: 1 1 auto;
		min-height: 0;
		width: 100%;
		display: grid;
		grid-template: 1fr / 1fr;
		align-items: stretch;
	}
	.share-links-stack > * {
		grid-area: 1 / 1;
		min-height: 0;
		width: 100%;
		box-sizing: border-box;
	}
	.share-links-editor.disabled {
		opacity: 0.65;
		pointer-events: none;
	}
	.share-links-back {
		margin: 0;
		padding: 0;
		border: none;
		border-radius: inherit;
		font: inherit;
		line-height: 1.45;
		letter-spacing: inherit;
		white-space: pre-wrap;
		word-break: break-word;
		overflow: hidden;
		height: 100%;
		z-index: 0;
		pointer-events: none;
		color: var(--color-text-primary);
		background: transparent;
		scrollbar-width: none;
	}
	.share-links-back::-webkit-scrollbar {
		display: none;
	}
	.share-links-back :global(.share-link-proto) {
		font-weight: 600;
		color: var(--color-primary, #2563eb);
	}
	.share-links-back :global(.hl-json-str) {
		color: var(--hl-json-str, #16a34a);
	}
	.share-links-back :global(.hl-json-num) {
		color: var(--hl-json-num, #ea580c);
	}
	.share-links-back :global(.hl-json-lit) {
		color: var(--hl-json-lit, #9333ea);
	}
	.share-links-back :global(.hl-json-punct) {
		color: var(--hl-json-punct, var(--color-text-muted));
		opacity: 0.9;
	}
	.share-links-back :global(.hl-yaml-key) {
		color: var(--hl-yaml-key, #0284c7);
		font-weight: 600;
	}
	.share-links-back :global(.hl-yaml-comment) {
		color: var(--hl-yaml-comment, var(--color-text-muted));
		font-style: italic;
	}
	.share-links-back :global(.hl-yaml-str) {
		color: var(--hl-yaml-str, #15803d);
	}
	.share-links-back :global(.hl-yaml-punct) {
		color: var(--hl-yaml-punct, var(--color-text-muted));
	}
	.share-links-ta {
		margin: 0;
		padding: 0;
		position: relative;
		z-index: 1;
		height: 100%;
		resize: none;
		overflow: auto;
		font: inherit;
		line-height: 1.45;
		letter-spacing: inherit;
		border: none;
		border-radius: inherit;
		outline: none;
		background: transparent;
		color: transparent;
		-webkit-text-fill-color: transparent;
		caret-color: var(--color-text-primary);
	}
	.share-links-ta::placeholder {
		opacity: 1;
		color: var(--color-text-muted);
		-webkit-text-fill-color: var(--color-text-muted);
	}
	.share-links-ta:focus-visible {
		outline: none;
	}
	.share-links-ta::selection {
		background: color-mix(in srgb, var(--color-primary, #2563eb) 38%, transparent);
	}
	.share-links-editor:focus-within {
		border-color: var(--color-primary, #3b82f6);
	}
</style>
