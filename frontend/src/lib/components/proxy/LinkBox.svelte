<script lang="ts">
	// Одна ссылка: текст, «Копировать» (LK-09), QR с увеличением (LK-10) и
	// отказ QR (LK-11 / LK-12). Показанный QR сбрасывается при смене ссылки.
	import { m } from '$lib/i18n';
	import QRCode from 'qrcode';
	import { Button } from '$lib/components/ui';
	import QrZoomImage from '$lib/components/ui/QrZoomImage.svelte';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { groupDigits } from './serverClients';

	interface Props {
		link: string;
		/** LK-07 / LK-08; у FreeTurn подзаголовка нет. */
		title?: string;
		/** LK-12 вместо LK-11: у FreeTurn длину лечит отказ от WG в ссылке. */
		freeturn?: boolean;
	}

	let { link, title = '', freeturn = false }: Props = $props();

	let showQR = $state(false);
	let qrDataUrl = $state('');
	let qrBusy = $state(false);
	// Размер ссылки при отказе QR; текст собирается при показе и следует за языком.
	let qrErrorSize = $state<number | null>(null);
	const qrError = $derived(
		qrErrorSize === null
			? ''
			: freeturn
				? // LK-12
					m.proxy_linkbox_qr_too_long_wg()
				: // LK-11
					m.proxy_linkbox_qr_too_long({ size: groupDigits(qrErrorSize) }),
	);
	let shownFor = $state('');

	// Смена ссылки снимает показанный QR: иначе рядом с новой ссылкой висел бы
	// старый код (ia.md §3.3).
	$effect(() => {
		if (link === shownFor) return;
		shownFor = link;
		showQR = false;
		qrDataUrl = '';
		qrErrorSize = null;
	});

	async function copy() {
		if (!link) return;
		// TS-12
		if (await copyToClipboard(link)) notifications.success(m.proxy_linkbox_copied());
	}

	async function toggleQR() {
		if (showQR) {
			showQR = false;
			return;
		}
		if (qrDataUrl) {
			showQR = true;
			return;
		}
		qrBusy = true;
		qrErrorSize = null;
		try {
			qrDataUrl = await QRCode.toDataURL(link, {
				width: 512,
				margin: 2,
				errorCorrectionLevel: 'L',
				color: { dark: '#000000', light: '#ffffff' },
			});
			showQR = true;
		} catch {
			qrErrorSize = new Blob([link]).size;
		} finally {
			qrBusy = false;
		}
	}
</script>

<div class="link-box">
	{#if title}
		<p class="link-title">{title}</p>
	{/if}
	<div class="link-text">{link}</div>
	<div class="btn-row">
		<Button variant="secondary" size="sm" onclick={copy}>{m.common_copy()}</Button>
		<Button variant="secondary" size="sm" loading={qrBusy} onclick={toggleQR}>{m.proxy_linkbox_qr()}</Button>
	</div>
	{#if qrError}
		<p class="link-error">{qrError}</p>
	{/if}
	{#if showQR && qrDataUrl}
		<div class="qr-wrap">
			<QrZoomImage src={qrDataUrl} alt={m.proxy_linkbox_qr()} />
		</div>
	{/if}
</div>

<style>
	.link-box {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.link-title {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.link-text {
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

	.link-error {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-error);
	}

	.qr-wrap {
		display: flex;
		justify-content: center;
		padding: 0.75rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}
</style>
