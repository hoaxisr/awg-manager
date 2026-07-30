<script lang="ts">
	interface Props {
		src: string;
		alt: string;
		class?: string;
	}

	let { src, alt, class: className = '' }: Props = $props();

	let enlarged = $state(false);

	function open() {
		enlarged = true;
	}

	function close() {
		enlarged = false;
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape' && enlarged) close();
	}

	function portal(node: HTMLElement) {
		document.body.appendChild(node);
		return {
			destroy() {
				node.remove();
			}
		};
	}
</script>

<svelte:window onkeydown={handleKeydown} />

<button type="button" class="qr-zoom-trigger {className}" onclick={open} title="Нажмите для увеличения">
	<img {src} {alt} class="qr-zoom-thumb" />
	<span class="qr-zoom-badge" aria-hidden="true">⤢</span>
</button>

{#if enlarged}
	<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<div
		use:portal
		class="qr-zoom-lightbox"
		role="dialog"
		aria-modal="true"
		aria-label={alt}
		tabindex="-1"
		onclick={close}
	>
		<img {src} {alt} class="qr-zoom-enlarged" onclick={(e) => e.stopPropagation()} />
		<p class="qr-zoom-caption">Нажмите вне QR или Esc, чтобы закрыть</p>
	</div>
{/if}

<style>
	.qr-zoom-trigger {
		position: relative;
		display: block;
		padding: 0;
		border: none;
		background: transparent;
		cursor: zoom-in;
		border-radius: var(--radius-sm);
	}

	.qr-zoom-trigger:focus-visible {
		outline: 2px solid var(--color-accent, var(--accent));
		outline-offset: 2px;
	}

	.qr-zoom-thumb {
		display: block;
		width: min(100%, 14rem);
		height: auto;
		image-rendering: pixelated;
	}

	.qr-zoom-badge {
		position: absolute;
		right: 0.25rem;
		bottom: 0.25rem;
		font-size: 0.75rem;
		line-height: 1;
		padding: 0.2rem 0.35rem;
		border-radius: var(--radius-sm);
		background: rgba(0, 0, 0, 0.55);
		color: #fff;
		pointer-events: none;
	}

	.qr-zoom-lightbox {
		position: fixed;
		inset: 0;
		z-index: var(--z-modal);
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 0.75rem;
		padding: 1rem;
		background: rgba(0, 0, 0, 0.72);
		cursor: zoom-out;
	}

	.qr-zoom-enlarged {
		width: min(92vw, 92vh, 28rem);
		height: auto;
		max-height: calc(100dvh - 4rem);
		image-rendering: pixelated;
		background: #fff;
		padding: 0.75rem;
		border-radius: var(--radius-sm);
		cursor: default;
		box-shadow: 0 8px 32px rgba(0, 0, 0, 0.35);
	}

	.qr-zoom-caption {
		margin: 0;
		font-size: 0.8125rem;
		color: rgba(255, 255, 255, 0.85);
		text-align: center;
	}
</style>
