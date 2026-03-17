<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import QRCode from 'qrcode';

	interface Props {
		open: boolean;
		pubkey: string;
		peerName: string;
		onclose: () => void;
	}

	let { open = $bindable(false), pubkey, peerName, onclose }: Props = $props();

	let conf = $state('');
	let loading = $state(false);
	let showQR = $state(false);
	let qrDataUrl = $state('');
	let qrGenerating = $state(false);

	$effect(() => {
		if (open && pubkey) {
			showQR = false;
			qrDataUrl = '';
			loadConf();
		}
	});

	async function loadConf() {
		loading = true;
		try {
			conf = await api.getManagedPeerConf(pubkey);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка загрузки');
			conf = '';
		} finally {
			loading = false;
		}
	}

	async function toggleQR() {
		if (showQR) {
			showQR = false;
			return;
		}
		if (!qrDataUrl) {
			qrGenerating = true;
			try {
				qrDataUrl = await QRCode.toDataURL(conf, {
					width: 360,
					margin: 2,
					color: { dark: '#000000', light: '#ffffff' }
				});
			} catch (e) {
				notifications.error('Ошибка генерации QR-кода');
				return;
			} finally {
				qrGenerating = false;
			}
		}
		showQR = true;
	}

	function downloadConf() {
		const name = peerName || 'peer';
		const safeName = name.replace(/[^a-zA-Z0-9а-яА-Я_-]/g, '_');
		const blob = new Blob([conf], { type: 'text/plain' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = `${safeName}.conf`;
		a.click();
		URL.revokeObjectURL(url);
	}

	function copyConf() {
		navigator.clipboard.writeText(conf);
		notifications.success('Скопировано');
	}
</script>

<Modal {open} title="Конфигурация клиента" size="md" {onclose}>
	{#if loading}
		<div class="loading">Загрузка...</div>
	{:else if conf}
		{#if showQR && qrDataUrl}
			<div class="qr-container">
				<img src={qrDataUrl} alt="QR-код конфигурации" class="qr-image" />
				<span class="qr-hint">Отсканируйте в AmneziaWG / WireGuard</span>
			</div>
		{:else}
			<pre class="conf-preview">{conf}</pre>
		{/if}
	{:else}
		<div class="loading">Нет данных</div>
	{/if}

	{#snippet actions()}
		<button class="btn btn-ghost" onclick={toggleQR} disabled={!conf || qrGenerating}>
			{#if qrGenerating}
				Генерация...
			{:else if showQR}
				Конфиг
			{:else}
				QR-код
			{/if}
		</button>
		<button class="btn btn-ghost" onclick={copyConf} disabled={!conf}>
			Копировать
		</button>
		<button class="btn btn-primary" onclick={downloadConf} disabled={!conf}>
			Скачать .conf
		</button>
	{/snippet}
</Modal>

<style>
	.conf-preview {
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 1rem;
		font-size: 0.75rem;
		font-family: var(--font-mono, monospace);
		white-space: pre-wrap;
		word-break: break-all;
		max-height: 400px;
		overflow-y: auto;
		color: var(--text-primary);
		margin: 0;
	}

	.loading {
		padding: 2rem;
		text-align: center;
		color: var(--text-muted);
	}

	.qr-container {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.75rem;
		padding: 1.5rem;
	}

	.qr-image {
		width: 360px;
		height: 360px;
		border-radius: 8px;
		image-rendering: pixelated;
	}

	.qr-hint {
		font-size: 0.75rem;
		color: var(--text-muted);
	}
</style>
