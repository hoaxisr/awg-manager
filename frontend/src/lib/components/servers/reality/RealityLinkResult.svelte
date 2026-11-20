<script lang="ts">
	import QRCode from 'qrcode';
	import { Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { Copy, AlertTriangle } from 'lucide-svelte';

	interface Props {
		link?: string;
		error?: string | null;
		title?: string;
	}

	let { link = '', error = null, title = 'Подключение через Reality (прямой вход)' }: Props = $props();

	let qr = $state('');

	$effect(() => {
		const l = link;
		if (!l) {
			qr = '';
			return;
		}
		QRCode.toDataURL(l, { width: 280, margin: 2, errorCorrectionLevel: 'L', color: { dark: '#000000', light: '#ffffff' } })
			.then((d: string) => (qr = d))
			.catch(() => (qr = ''));
	});

	async function copy() {
		if (link && (await copyToClipboard(link))) {
			notifications.success('Reality-ссылка скопирована');
		}
	}
</script>

<div class="rr">
	<div class="rr-title">{title}</div>
	{#if link}
		<div class="rr-body">
			{#if qr}
				<div class="rr-qr"><img src={qr} alt="QR-код Reality" /></div>
			{/if}
			<div class="rr-col">
				<div class="rr-link mono">{link}</div>
				<div class="rr-actions">
					<Button variant="primary" size="sm" onclick={copy}><Copy size={14} class="rr-btn-icon" /> Скопировать ссылку</Button>
				</div>
				<span class="rr-hint">
					Импортируйте в Happ / v2rayNG / Hiddify / Streisand / Mihomo. JSON и YAML — в окне «QR / Ссылка» устройства, маршрут «Reality».
				</span>
			</div>
		</div>
	{:else if error}
		<div class="rr-warn"><AlertTriangle size={15} class="rr-warn-icon" /><span>{error}</span></div>
	{:else}
		<span class="rr-hint">Получение ссылки Reality…</span>
	{/if}
</div>

<style>
	.rr {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.rr-title {
		font-size: 12px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-secondary);
	}
	.rr-body {
		display: flex;
		gap: 14px;
		align-items: flex-start;
	}
	@media (max-width: 640px) {
		.rr-body {
			flex-direction: column;
		}
	}
	.rr-qr {
		background: #fff;
		border: 1px solid var(--color-border);
		padding: 6px;
		border-radius: var(--radius);
		flex-shrink: 0;
	}
	.rr-qr img {
		display: block;
		width: 190px;
		height: 190px;
	}
	.rr-col {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}
	.rr-actions {
		display: flex;
		align-items: center;
	}
	.rr-actions :global(.rr-btn-icon) {
		margin-right: 4px;
		flex-shrink: 0;
	}
	.rr-link {
		font-size: 11px;
		word-break: break-all;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 8px 10px;
		color: var(--color-text-primary);
	}
	.mono {
		font-family: var(--font-mono);
	}
	.rr-hint {
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.4;
	}
	.rr-warn {
		display: flex;
		gap: 8px;
		align-items: flex-start;
		font-size: 12px;
		color: var(--color-text-primary);
		padding: 8px 10px;
		border: 1px solid var(--color-warning-border);
		background: var(--color-warning-tint);
		border-radius: var(--radius-sm);
	}
	.rr-warn :global(.rr-warn-icon) {
		color: var(--color-warning);
		flex-shrink: 0;
		margin-top: 1px;
	}
</style>
