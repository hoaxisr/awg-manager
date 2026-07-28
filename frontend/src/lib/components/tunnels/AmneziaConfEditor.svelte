<script lang="ts">
	import { highlightAmneziaConfContent } from '$lib/utils/amneziaConfHighlight';
	import { tick } from 'svelte';

	type Variant = 'page' | 'modal' | 'preview' | 'modal-preview';

	interface Props {
		value?: string;
		placeholder?: string;
		disabled?: boolean;
		readonly?: boolean;
		/** page: /tunnels/new; modal: replace modal paste; preview: vpn:// decode on new tunnel; modal-preview: decode in modal */
		variant?: Variant;
	}

	let {
		value = $bindable(''),
		placeholder = '',
		disabled = false,
		readonly = false,
		variant = 'page',
	}: Props = $props();

	let ta = $state<HTMLTextAreaElement | null>(null);
	let back = $state<HTMLPreElement | null>(null);

	let highlightHtml = $derived(highlightAmneziaConfContent(value));

	/** Default editor block height (like ShareLinksTextarea --sl-rows); user can resize vertically. */
	function outerHeightVar(): string {
		switch (variant) {
			case 'page':
				return 'calc(var(--ace-rows, 16) * 1.45em + 1.75rem)';
			case 'modal':
				return 'calc(var(--ace-rows, 12) * 1.45em + 1.5rem)';
			case 'preview':
				return 'calc(var(--ace-rows, 12) * 1.45em + 1.75rem)';
			case 'modal-preview':
				return 'calc(var(--ace-rows, 8) * 1.45em + 1.5rem)';
			default:
				return 'calc(14 * 1.45em + 1.75rem)';
		}
	}

	/** Textarea scrollbar eats width/height; <pre> has none — reflow drifts vs transparent text (often ~2ch). */
	function syncGutter(): void {
		if (!ta || !back) return;
		const gw = ta.offsetWidth - ta.clientWidth;
		const gh = ta.offsetHeight - ta.clientHeight;
		back.style.paddingRight = gw > 0 ? `${gw}px` : '';
		back.style.paddingBottom = gh > 0 ? `${gh}px` : '';
	}

	function syncScroll(): void {
		syncGutter();
		if (!ta || !back) return;
		back.scrollTop = ta.scrollTop;
		back.scrollLeft = ta.scrollLeft;
	}

	$effect(() => {
		value;
		void tick().then(syncScroll);
	});

	$effect(() => {
		if (!ta || typeof ResizeObserver === 'undefined') return;
		const ro = new ResizeObserver(() => {
			syncScroll();
		});
		ro.observe(ta);
		return () => ro.disconnect();
	});
</script>

<!-- Same stacking model as ShareLinksTextarea: fixed outer height, inner textarea scrolls, sync pre. -->
<div
	class="ace-root"
	class:ace-page={variant === 'page'}
	class:ace-modal={variant === 'modal'}
	class:ace-preview={variant === 'preview'}
	class:ace-modal-preview={variant === 'modal-preview'}
	class:ace-disabled={disabled}
	style="--ace-outer-h: {outerHeightVar()};"
>
	<div class="ace-stack">
		<pre class="ace-back" aria-hidden="true" bind:this={back}>{@html highlightHtml}</pre>
		<textarea
			class="ace-ta"
			bind:this={ta}
			bind:value
			rows={1}
			{placeholder}
			{disabled}
			{readonly}
			spellcheck="false"
			autocomplete="off"
			autocapitalize="off"
			onscroll={syncScroll}
			oninput={syncScroll}
		></textarea>
	</div>
</div>

<style>
	/* Mirror ShareLinksTextarea: mono + pre-wrap + inner scroll; highlight spans must NOT change advance width. */
	.ace-root {
		position: relative;
		box-sizing: border-box;
		display: flex;
		flex-direction: column;
		width: 100%;
		min-width: 0;
		height: var(--ace-outer-h);
		min-height: 0;
		padding: 0;
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: inherit;
		line-height: 1.45;
		letter-spacing: inherit;
		tab-size: 4;
		color: var(--color-text-primary, var(--text-primary));
		transition: border-color 0.15s;
		resize: vertical;
		overflow: hidden;
	}

	.ace-page {
		min-height: 300px;
		padding: 14px;
		font-size: 13px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 8px;
	}

	.ace-modal {
		min-height: 180px;
		padding: 12px;
		font-size: 0.75rem;
		color: var(--text-primary);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.ace-preview {
		min-height: 200px;
		margin-top: 12px;
		padding: 14px;
		font-size: 13px;
		background: var(--color-bg-secondary, var(--bg-primary));
		border: 1px solid var(--color-border, var(--border));
		border-radius: 8px;
		opacity: 0.92;
	}

	.ace-modal-preview {
		min-height: 120px;
		margin-top: 8px;
		padding: 12px;
		font-size: 0.75rem;
		color: var(--text-primary);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 8px;
		opacity: 0.85;
	}

	.ace-disabled {
		opacity: 0.65;
		pointer-events: none;
	}

	.ace-stack {
		position: relative;
		flex: 1 1 auto;
		min-height: 0;
		width: 100%;
		display: grid;
		grid-template: 1fr / 1fr;
		align-items: stretch;
	}

	.ace-stack > * {
		grid-area: 1 / 1;
		min-height: 0;
		width: 100%;
		box-sizing: border-box;
	}

	.ace-back {
		margin: 0;
		padding: 0;
		border: none;
		border-radius: inherit;
		font: inherit;
		line-height: inherit;
		letter-spacing: inherit;
		tab-size: inherit;
		white-space: pre-wrap;
		word-break: break-word;
		overflow: hidden;
		height: 100%;
		z-index: 0;
		pointer-events: none;
		color: var(--color-text-primary, var(--text-primary));
		background: transparent;
		scrollbar-width: none;
	}

	.ace-back::-webkit-scrollbar {
		display: none;
	}

	/* Critical: bold/italic in monospace changes glyph widths vs transparent textarea — only color may differ. */
	.ace-back :global(span) {
		font-weight: 400;
		font-style: normal;
	}

	.ace-back :global(.ace-section) {
		color: var(--color-accent);
	}
	.ace-back :global(.ace-comment) {
		color: var(--color-text-muted, var(--text-muted));
	}

	.ace-ta {
		margin: 0;
		padding: 0;
		position: relative;
		z-index: 1;
		height: 100%;
		resize: none;
		overflow: auto;
		-webkit-appearance: none;
		appearance: none;
		font: inherit;
		line-height: inherit;
		letter-spacing: inherit;
		tab-size: inherit;
		white-space: pre-wrap;
		word-break: break-word;
		border: none;
		border-radius: inherit;
		outline: none;
		background: transparent;
		color: transparent;
		-webkit-text-fill-color: transparent;
		caret-color: var(--color-text-primary, var(--text-primary));
		font-weight: 400;
	}

	.ace-ta::placeholder {
		opacity: 1;
		color: var(--color-text-muted, var(--text-muted));
		-webkit-text-fill-color: var(--color-text-muted, var(--text-muted));
	}

	.ace-ta:focus-visible {
		outline: none;
	}

	.ace-page:focus-within {
		border-color: var(--color-accent);
	}
	.ace-modal:focus-within {
		border-color: var(--accent);
	}
	.ace-preview:focus-within {
		border-color: var(--color-accent, var(--accent));
	}

	.ace-modal-preview:focus-within {
		border-color: var(--accent);
	}

	.ace-ta::selection {
		background: color-mix(in srgb, var(--color-accent) 38%, transparent);
	}
</style>
