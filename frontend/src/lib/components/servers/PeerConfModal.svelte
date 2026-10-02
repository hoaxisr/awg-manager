<script lang="ts">
	import { m } from '$lib/i18n';
	import { Modal, Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import QRCode from 'qrcode';

	interface Props {
		open: boolean;
		serverId: string;
		pubkey: string;
		peerName: string;
		kind?: 'managed' | 'system';
		onclose: () => void;
	}

	let { open = $bindable(false), serverId, pubkey, peerName, kind = 'managed', onclose }: Props = $props();

	let conf = $state('');
	let loading = $state(false);
	let showQR = $state(false);
	let qrDataUrl = $state('');
	let qrGenerating = $state(false);
	let loadedForKey = $state('');

	function confLoadKey(): string {
		return `${serverId}\0${pubkey}\0${kind}`;
	}

	// Load once per open cycle / peer — polling updates on the servers page
	// re-run parent effects and would otherwise flash "Загрузка..." and reset QR.
	$effect(() => {
		if (!open || !pubkey) {
			if (!open) loadedForKey = '';
			return;
		}
		const key = confLoadKey();
		if (loadedForKey === key) return;
		loadedForKey = key;
		showQR = false;
		qrDataUrl = '';
		void loadConf();
	});

	async function loadConf() {
		loading = true;
		try {
			conf = kind === 'system'
				? await api.getSystemServerPeerConf(serverId, pubkey)
				: await api.getManagedPeerConf(serverId, pubkey);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_conf_load_failed());
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
					width: 512,
					margin: 2,
					errorCorrectionLevel: 'L',
					color: { dark: '#000000', light: '#ffffff' }
				});
			} catch (e) {
				const size = new Blob([conf]).size;
				if (size > 2900) {
					notifications.error(m.servers_conf_qr_too_large({ size }), 8000);
				} else {
					notifications.error(m.servers_conf_qr_failed());
				}
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

	async function copyConf() {
		if (await copyToClipboard(conf)) {
			notifications.success(m.servers_conf_copied());
		} else {
			notifications.error(m.diag_about_copy_failed());
		}
	}
</script>

<Modal {open} title={m.servers_conf_title()} size="md" {onclose}>
	{#if loading}
		<div class="loading">{m.tunnels_loading()}</div>
	{:else if conf}
		{#if showQR && qrDataUrl}
			<div class="qr-container">
				<img src={qrDataUrl} alt={m.servers_conf_qr_alt()} class="qr-image" />
				<span class="qr-hint">{m.servers_conf_qr_hint()}</span>
			</div>
		{:else}
			<pre class="conf-preview">{conf}</pre>
		{/if}
	{:else}
		<div class="loading">{m.servers_conf_no_data()}</div>
	{/if}

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={toggleQR} disabled={!conf} loading={qrGenerating}>
			{showQR ? m.servers_conf_show_conf() : m.servers_conf_show_qr()}
		</Button>
		<Button variant="ghost" size="md" onclick={copyConf} disabled={!conf}>
			{m.diag_logs_copy()}
		</Button>
		<Button variant="primary" size="md" onclick={downloadConf} disabled={!conf}>
			{m.servers_confgen_download()}
		</Button>
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
		width: min(360px, 100%);
		aspect-ratio: 1 / 1;
		height: auto;
		object-fit: contain;
		border-radius: 8px;
		image-rendering: pixelated;
	}

	@media (max-width: 640px) {
		.qr-container {
			padding: 1rem;
		}

		.qr-image {
			width: min(320px, 100%);
		}
	}

	.qr-hint {
		font-size: 0.75rem;
		color: var(--text-muted);
	}
</style>
