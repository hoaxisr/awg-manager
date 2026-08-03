<script lang="ts">
	import { Button } from '$lib/components/ui';
	import QrZoomImage from '$lib/components/ui/QrZoomImage.svelte';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import QRCode from 'qrcode';

	interface Props {
		linkWdtt?: string;
		linkQwdtt?: string;
	}

	let { linkWdtt = '', linkQwdtt = '' }: Props = $props();

	let showQRWdtt = $state(false);
	let showQRQwdtt = $state(false);
	let qrWdtt = $state('');
	let qrQwdtt = $state('');
	let qrGeneratingWdtt = $state(false);
	let qrGeneratingQwdtt = $state(false);

	async function copyLink(link: string, label: string) {
		if (!link) return;
		if (await copyToClipboard(link)) notifications.success(`${label} скопирована`);
	}

	async function toggleQR(kind: 'wdtt' | 'qwdtt') {
		const link = kind === 'wdtt' ? linkWdtt : linkQwdtt;
		const show = kind === 'wdtt' ? showQRWdtt : showQRQwdtt;
		if (show) {
			if (kind === 'wdtt') showQRWdtt = false;
			else showQRQwdtt = false;
			return;
		}
		const cached = kind === 'wdtt' ? qrWdtt : qrQwdtt;
		if (cached) {
			if (kind === 'wdtt') showQRWdtt = true;
			else showQRQwdtt = true;
			return;
		}
		if (kind === 'wdtt') qrGeneratingWdtt = true;
		else qrGeneratingQwdtt = true;
		try {
			const dataUrl = await QRCode.toDataURL(link, {
				width: 512,
				margin: 2,
				errorCorrectionLevel: 'L',
				color: { dark: '#000000', light: '#ffffff' }
			});
			if (kind === 'wdtt') {
				qrWdtt = dataUrl;
				showQRWdtt = true;
			} else {
				qrQwdtt = dataUrl;
				showQRQwdtt = true;
			}
		} catch {
			const size = new Blob([link]).size;
			if (size > 2900) {
				notifications.error(
					`Ссылка слишком длинная для QR (${size} байт). Скопируйте текст вручную.`,
					8000
				);
			} else {
				notifications.error('Не удалось сгенерировать QR-код');
			}
		} finally {
			if (kind === 'wdtt') qrGeneratingWdtt = false;
			else qrGeneratingQwdtt = false;
		}
	}

	$effect(() => {
		linkWdtt;
		linkQwdtt;
		showQRWdtt = false;
		showQRQwdtt = false;
		qrWdtt = '';
		qrQwdtt = '';
	});
</script>

{#if linkQwdtt}
	<div class="wdtt-link-share">
		<p class="wdtt-link-label">qwdtt:// — для приложения на телефоне</p>
		<div class="wdtt-link-box">{linkQwdtt}</div>
		<div class="wdtt-link-actions">
			<Button variant="secondary" size="sm" onclick={() => copyLink(linkQwdtt, 'qwdtt://')}>
				Копировать
			</Button>
			<Button variant="secondary" size="sm" loading={qrGeneratingQwdtt} onclick={() => toggleQR('qwdtt')}>
				{showQRQwdtt ? 'Скрыть QR' : 'QR для приложения'}
			</Button>
		</div>
		{#if showQRQwdtt && qrQwdtt}
			<div class="wdtt-qr-wrap">
				<QrZoomImage src={qrQwdtt} alt="QR-код qwdtt://" />
				<p class="wdtt-qr-hint">Отсканируйте в приложении qwdtt (Android). Нажмите QR для увеличения.</p>
			</div>
		{/if}
	</div>
{/if}

{#if linkWdtt}
	<div class="wdtt-link-share">
		<p class="wdtt-link-label">wdtt:// — для клиента на роутере</p>
		<div class="wdtt-link-box">{linkWdtt}</div>
		<div class="wdtt-link-actions">
			<Button variant="secondary" size="sm" onclick={() => copyLink(linkWdtt, 'wdtt://')}>
				Копировать
			</Button>
			<Button variant="secondary" size="sm" loading={qrGeneratingWdtt} onclick={() => toggleQR('wdtt')}>
				{showQRWdtt ? 'Скрыть QR' : 'QR'}
			</Button>
		</div>
		{#if showQRWdtt && qrWdtt}
			<div class="wdtt-qr-wrap">
				<QrZoomImage src={qrWdtt} alt="QR-код wdtt://" />
				<p class="wdtt-qr-hint">Импорт в WDTT «Клиент» на роутере. Нажмите QR для увеличения.</p>
			</div>
		{/if}
	</div>
{/if}

<style>
	.wdtt-link-share {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.wdtt-link-label {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.wdtt-link-box {
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

	.wdtt-link-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.wdtt-qr-wrap {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.375rem;
		padding: 0.75rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}

	.wdtt-qr-hint {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		text-align: center;
	}
</style>
