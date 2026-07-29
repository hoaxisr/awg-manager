<script lang="ts">
	import { Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import QRCode from 'qrcode';
	import LinkParamsSummary from './LinkParamsSummary.svelte';
	import type { FreeTurnLinkPayload } from '$lib/types';

	interface Props {
		link: string;
		peer?: string;
		payload?: FreeTurnLinkPayload | null;
		onCopy: (text: string) => void;
	}

	let { link, peer = '', payload = null, onCopy }: Props = $props();

	let showQR = $state(false);
	let qrDataUrl = $state('');
	let qrGenerating = $state(false);

	async function toggleQR() {
		if (showQR) {
			showQR = false;
			return;
		}
		if (!qrDataUrl) {
			qrGenerating = true;
			try {
				qrDataUrl = await QRCode.toDataURL(link, {
					width: 512,
					margin: 2,
					errorCorrectionLevel: 'L',
					color: { dark: '#000000', light: '#ffffff' }
				});
			} catch {
				const size = new Blob([link]).size;
				if (size > 2900) {
					notifications.error(
						`Ссылка слишком длинная для QR (${size} байт). Скопируйте текст или уберите WG из ссылки.`,
						8000
					);
				} else {
					notifications.error('Не удалось сгенерировать QR-код');
				}
				return;
			} finally {
				qrGenerating = false;
			}
		}
		showQR = true;
	}
</script>

<div class="ft-link-share">
	<div class="ft-link-box">{link}</div>
	<div class="ft-link-actions">
		<Button variant="ghost" size="sm" onclick={() => onCopy(link)}>Скопировать</Button>
		<Button variant="ghost" size="sm" loading={qrGenerating} onclick={toggleQR}>
			{showQR ? 'Скрыть QR' : 'QR для Android'}
		</Button>
	</div>
	{#if showQR && qrDataUrl}
		<div class="ft-qr-wrap">
			<img src={qrDataUrl} alt="QR-код freeturn://" class="ft-qr-image" />
			<p class="ft-qr-hint">Отсканируйте в приложении FreeTurn (Android)</p>
		</div>
	{/if}
	<LinkParamsSummary {payload} {peer} />
</div>

<style>
	.ft-link-share {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}
	.ft-link-box {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		word-break: break-all;
		padding: 0.5rem 0.625rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		max-height: 6rem;
		overflow: auto;
	}
	.ft-link-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}
	.ft-qr-wrap {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.375rem;
		padding: 0.75rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}
	.ft-qr-image {
		width: min(100%, 14rem);
		height: auto;
		image-rendering: pixelated;
	}
	.ft-qr-hint {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		text-align: center;
	}
</style>
