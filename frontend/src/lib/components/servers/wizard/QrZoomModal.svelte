<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Modal } from '$lib/components/ui';

	interface Props {
		open: boolean;
		dataUrl: string;
	}

	let { open = $bindable(false), dataUrl }: Props = $props();
</script>

<Modal
	bind:open
	title={m.servers_wizard_result_qr_title()}
	onclose={() => (open = false)}
	size="sm"
>
	{#if dataUrl}
		<div class="qr-zoom-body">
			<div class="qr-zoom-frame">
				<img src={dataUrl} alt={m.servers_wizard_result_qr_zoom_alt()} class="qr-zoom-img" />
			</div>
			<p class="qr-zoom-note">{m.servers_wizard_result_qr_zoom_note()}</p>
		</div>
	{/if}
	{#snippet actions()}
		<Button variant="secondary" onclick={() => (open = false)}>{m.common_close()}</Button>
	{/snippet}
</Modal>

<style>
	.qr-zoom-body {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.75rem;
	}

	.qr-zoom-frame {
		padding: 16px;
		background: #ffffff;
		border-radius: var(--radius);
		border: 1px solid var(--color-border);
	}

	.qr-zoom-img {
		width: min(360px, 78vw);
		height: auto;
		aspect-ratio: 1 / 1;
		image-rendering: crisp-edges;
		display: block;
	}

	.qr-zoom-note {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		text-align: center;
		margin: 0;
	}
</style>
