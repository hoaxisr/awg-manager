<script lang="ts">
	import type { WireguardServerConfig, WireguardServerPeerConfig, ASCParams } from '$lib/types';
	import { Modal } from '$lib/components/ui';

	interface Props {
		open: boolean;
		serverConfig: WireguardServerConfig;
		peer: WireguardServerPeerConfig;
		ascParams: ASCParams | null;
		wanIP: string;
		onclose: () => void;
	}

	let { open = $bindable(false), serverConfig, peer, ascParams, wanIP, onclose }: Props = $props();

	let privateKey = $state('');

	function generateConf(): string {
		const lines: string[] = [
			'[Interface]',
			`PrivateKey = ${privateKey}`,
			`Address = ${peer.address}/32`,
			`DNS = ${serverConfig.address}`,
			`MTU = ${serverConfig.mtu}`,
		];

		if (ascParams) {
			lines.push(`Jc = ${ascParams.jc}`);
			lines.push(`Jmin = ${ascParams.jmin}`);
			lines.push(`Jmax = ${ascParams.jmax}`);
			lines.push(`S1 = ${ascParams.s1}`);
			lines.push(`S2 = ${ascParams.s2}`);
			lines.push(`H1 = ${ascParams.h1}`);
			lines.push(`H2 = ${ascParams.h2}`);
			lines.push(`H3 = ${ascParams.h3}`);
			lines.push(`H4 = ${ascParams.h4}`);
			if ('s3' in ascParams) {
				lines.push(`S3 = ${ascParams.s3}`);
				lines.push(`S4 = ${ascParams.s4}`);
				lines.push(`I1 = ${ascParams.i1}`);
				lines.push(`I2 = ${ascParams.i2}`);
				lines.push(`I3 = ${ascParams.i3}`);
				lines.push(`I4 = ${ascParams.i4}`);
				lines.push(`I5 = ${ascParams.i5}`);
			}
		}

		lines.push('');
		lines.push('[Peer]');
		lines.push(`PublicKey = ${serverConfig.publicKey}`);
		if (peer.presharedKey) {
			lines.push(`PresharedKey = ${peer.presharedKey}`);
		}
		lines.push(`AllowedIPs = 0.0.0.0/0, ::/0`);
		lines.push(`Endpoint = ${wanIP}:${serverConfig.listenPort}`);
		lines.push(`PersistentKeepalive = 25`);

		return lines.join('\n') + '\n';
	}

	function download() {
		const conf = generateConf();
		const blob = new Blob([conf], { type: 'text/plain' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = `${peer.description || 'peer'}.conf`;
		a.click();
		URL.revokeObjectURL(url);
	}

	let preview = $derived(privateKey ? generateConf() : '');
</script>

<Modal {open} title="Генерация .conf — {peer.description || 'Пир'}" size="lg" {onclose}>
	<div class="conf-form">
		<div class="form-field">
			<label class="label" for="private-key">Private Key клиента</label>
			<input
				id="private-key"
				type="text"
				class="input"
				placeholder="Вставьте приватный ключ клиента"
				bind:value={privateKey}
				autocomplete="off"
				spellcheck="false"
			/>
			<p class="form-hint">Роутер хранит только публичный ключ. Приватный ключ необходимо ввести вручную.</p>
		</div>

		<div class="conf-info">
			<div class="info-row">
				<span class="info-label">Адрес клиента</span>
				<span class="info-value">{peer.address}/32</span>
			</div>
			<div class="info-row">
				<span class="info-label">Endpoint</span>
				<span class="info-value">{wanIP}:{serverConfig.listenPort}</span>
			</div>
			<div class="info-row">
				<span class="info-label">MTU</span>
				<span class="info-value">{serverConfig.mtu}</span>
			</div>
			<div class="info-row">
				<span class="info-label">PresharedKey</span>
				<span class="info-value">{peer.presharedKey ? 'Да' : 'Нет'}</span>
			</div>
			{#if ascParams}
				<div class="info-row">
					<span class="info-label">ASC (AWG)</span>
					<span class="info-value">Jc={ascParams.jc} Jmin={ascParams.jmin} Jmax={ascParams.jmax}</span>
				</div>
			{/if}
		</div>

		{#if preview}
			<div class="preview-section">
				<span class="label">Предпросмотр</span>
				<pre class="conf-preview">{preview}</pre>
			</div>
		{/if}
	</div>

	{#snippet actions()}
		<button class="btn btn-secondary" onclick={onclose}>Отмена</button>
		<button class="btn btn-primary" onclick={download} disabled={!privateKey.trim()}>
			Скачать .conf
		</button>
	{/snippet}
</Modal>

<style>
	.conf-form {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.form-field {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.conf-info {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
		padding: 0.75rem;
		background: var(--bg-tertiary);
		border-radius: var(--radius);
	}

	.info-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.8125rem;
	}

	.info-label {
		color: var(--text-muted);
	}

	.info-value {
		font-family: var(--font-mono, monospace);
		color: var(--text-secondary);
	}

	.preview-section {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.conf-preview {
		padding: 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		font-family: var(--font-mono, monospace);
		font-size: 0.75rem;
		line-height: 1.5;
		overflow-x: auto;
		white-space: pre;
		color: var(--text-secondary);
		margin: 0;
	}
</style>
