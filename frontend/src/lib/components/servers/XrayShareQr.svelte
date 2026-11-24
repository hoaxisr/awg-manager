<script lang="ts">
	import { Modal, Button } from '$lib/components/ui';
	import { m } from '$lib/i18n';

	interface Props {
		qrDataUrl: string;
		generating: boolean;
		/** Ссылка и QR относятся к транспорту FiniteProto v2. */
		finite: boolean;
	}

	let { qrDataUrl, generating, finite }: Props = $props();

	let zoomOpen = $state(false);

	// Пропал QR (закрыли окно, сменили клиента) — увеличенный вид тоже закрыт.
	$effect(() => {
		if (!qrDataUrl) zoomOpen = false;
	});
</script>

<div class="qr-col">
	{#if generating}
		<div class="qr-placeholder">{m.servers_xray_share_qr_generating()}</div>
	{:else if qrDataUrl}
		<button
			type="button"
			class="qr-wrapper qr-zoom-btn"
			onclick={() => (zoomOpen = true)}
			title={m.servers_xray_share_qr_zoom_title()}
		>
			<img src={qrDataUrl} alt={m.servers_xray_share_qr_alt()} class="qr-img" />
		</button>
		<div class="qr-hint">
			{finite ? m.servers_xray_share_qr_hint_finite() : m.servers_xray_share_qr_hint_scan()}
		</div>
	{:else}
		<div class="qr-placeholder">{m.servers_xray_share_qr_unavailable()}</div>
	{/if}
</div>

<Modal
	bind:open={zoomOpen}
	title={m.servers_xray_share_qr_zoom_modal_title()}
	onclose={() => (zoomOpen = false)}
	size="sm"
>
	{#if qrDataUrl}
		<div class="qr-zoom-body">
			<div class="qr-zoom-frame">
				<img src={qrDataUrl} alt={m.servers_xray_share_qr_zoom_alt()} class="qr-zoom-img" />
			</div>
		</div>
	{/if}
	{#snippet actions()}
		<Button variant="secondary" onclick={() => (zoomOpen = false)}>{m.common_close()}</Button>
	{/snippet}
</Modal>

<style>
	.qr-col {
		display: flex;
		flex-direction: column;
		align-items: center;
		width: 264px;
		flex-shrink: 0;
	}

	.qr-wrapper {
		background: #ffffff;
		padding: 10px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.qr-zoom-btn {
		cursor: zoom-in;
		transition: transform var(--t-fast) ease, box-shadow var(--t-fast) ease;
	}

	.qr-zoom-btn:hover {
		transform: scale(1.02);
		box-shadow: 0 6px 16px rgba(0, 0, 0, 0.2);
	}

	.qr-img {
		width: 240px;
		height: 240px;
		image-rendering: crisp-edges;
		display: block;
	}

	.qr-zoom-body {
		display: flex;
		justify-content: center;
	}

	.qr-zoom-frame {
		background: #ffffff;
		padding: 16px;
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

	.qr-placeholder {
		width: 240px;
		height: 240px;
		background: var(--color-bg-secondary);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius);
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.qr-hint {
		margin-top: 10px;
		font-size: 11px;
		color: var(--color-text-muted);
		text-align: center;
		line-height: 1.4;
	}

	@media (max-width: 640px) {
		.qr-col {
			width: 100%;
		}
	}
</style>
