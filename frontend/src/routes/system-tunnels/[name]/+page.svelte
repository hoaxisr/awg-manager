<script lang="ts">
	import { page } from '$app/stores';
	import { api } from '$lib/api/client';
	import { goto } from '$app/navigation';
	import { notifications } from '$lib/stores/notifications';
	import type { SystemTunnel, ASCParams, ASCParamsExtended } from '$lib/types';
	import { PageContainer } from '$lib/components/layout';
	import { formatBytes } from '$lib/utils/format';
	import { protocols, getSignaturePackets, calcByteSize, calcTotalSize, type ProtocolKey } from '$lib/utils/protocols';

	const name = $page.params.name!;

	let tunnel = $state<SystemTunnel | null>(null);
	let ascParams = $state<ASCParams | null>(null);
	let saving = $state(false);
	let loading = $state(true);
	let error = $state<string | null>(null);

	const isExtended = $derived(ascParams !== null && 's3' in ascParams);

	// Signature generator
	const MAX_SIGNATURE_BYTES = 4096;
	type GenerateMode = 'protocol' | 'domain';
	let selectedProtocol = $state<ProtocolKey>('quic_initial');
	let generateMode = $state<GenerateMode>('protocol');
	let domainInput = $state('');
	let capturing = $state(false);
	let captureError = $state('');
	let captureSource = $state('');

	let totalBytes = $derived.by(() => {
		if (!ascParams || !isExtended) return 0;
		const ext = ascParams as ASCParamsExtended;
		return calcByteSize(String(ext.i1 || '')) + calcByteSize(String(ext.i2 || '')) +
			calcByteSize(String(ext.i3 || '')) + calcByteSize(String(ext.i4 || '')) +
			calcByteSize(String(ext.i5 || ''));
	});

	let overLimit = $derived(totalBytes > MAX_SIGNATURE_BYTES);

	function handleGenerate() {
		if (!ascParams || !isExtended) return;
		const ext = ascParams as ASCParamsExtended;
		const packets = getSignaturePackets(selectedProtocol, tunnel?.mtu ?? 1280);
		const size = calcTotalSize(packets);
		if (size > MAX_SIGNATURE_BYTES) return;
		ext.i1 = packets.i1;
		ext.i2 = packets.i2;
		ext.i3 = packets.i3;
		ext.i4 = packets.i4;
		ext.i5 = packets.i5;
	}

	async function handleCapture() {
		if (!domainInput.trim() || !ascParams || !isExtended) return;
		const ext = ascParams as ASCParamsExtended;
		capturing = true;
		captureError = '';
		captureSource = '';
		try {
			const result = await api.captureSignature(domainInput.trim());
			ext.i1 = result.packets.i1 || '';
			ext.i2 = result.packets.i2 || '';
			ext.i3 = result.packets.i3 || '';
			ext.i4 = result.packets.i4 || '';
			ext.i5 = result.packets.i5 || '';
			captureSource = result.source;
			if (result.warning) {
				captureError = result.warning;
			}
		} catch (e: unknown) {
			captureError = e instanceof Error ? e.message : 'Ошибка захвата';
		} finally {
			capturing = false;
		}
	}

	$effect(() => {
		loadData();
	});

	async function loadData() {
		loading = true;
		error = null;
		try {
			const [t, asc] = await Promise.all([
				api.getSystemTunnel(name),
				api.getASCParams(name)
			]);
			tunnel = t;
			ascParams = asc;
		} catch (e) {
			error = e instanceof Error ? e.message : 'Не удалось загрузить данные';
		} finally {
			loading = false;
		}
	}

	async function handleSave() {
		if (!ascParams) return;
		saving = true;
		try {
			await api.setASCParams(name, ascParams);
			notifications.success('Параметры обфускации сохранены');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения');
		} finally {
			saving = false;
		}
	}
</script>

<svelte:head>
	<title>{tunnel?.description || name} — AWG Manager</title>
</svelte:head>

<PageContainer>
	<div class="sticky-header">
		<div class="header-left">
			<button class="btn btn-ghost btn-sm" onclick={() => goto('/')}>
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M19 12H5M12 19l-7-7 7-7"/>
				</svg>
				Назад
			</button>
			<h1 class="page-title">{tunnel?.description || name}</h1>
			<span class="badge-system">Системный</span>
		</div>
		<button
			class="btn btn-primary"
			onclick={handleSave}
			disabled={saving || !ascParams}
		>
			{saving ? 'Сохранение...' : 'Сохранить'}
		</button>
	</div>

	{#if loading}
		<div class="py-12 text-center text-surface-400">Загрузка...</div>
	{:else if error}
		<div class="py-12 text-center text-error-500">{error}</div>
	{:else if tunnel && ascParams}
		<!-- Info (read-only) -->
		<div class="section">
			<h2 class="section-title">Информация</h2>
			<div class="info-grid">
				<div class="info-item">
					<span class="info-label">Статус</span>
					<span class="info-value">{tunnel.status === 'up' ? 'Активен' : 'Неактивен'}</span>
				</div>
				<div class="info-item">
					<span class="info-label">Интерфейс</span>
					<span class="info-value font-mono">{tunnel.interfaceName}</span>
				</div>
				<div class="info-item">
					<span class="info-label">MTU</span>
					<span class="info-value">{tunnel.mtu}</span>
				</div>
				{#if tunnel.peer?.endpoint}
					<div class="info-item">
						<span class="info-label">Endpoint</span>
						<span class="info-value font-mono">{tunnel.peer.endpoint}</span>
					</div>
				{/if}
				{#if tunnel.peer?.publicKey}
					<div class="info-item">
						<span class="info-label">Public Key</span>
						<span class="info-value font-mono text-xs">{tunnel.peer.publicKey}</span>
					</div>
				{/if}
				{#if tunnel.peer}
					<div class="info-item">
						<span class="info-label">Трафик</span>
						<span class="info-value">RX: {formatBytes(tunnel.peer.rxBytes)} / TX: {formatBytes(tunnel.peer.txBytes)}</span>
					</div>
				{/if}
			</div>
		</div>

		<!-- ASC Parameters -->
		<div class="section">
			<h2 class="section-title">Параметры обфускации (ASC)</h2>

			<h3 class="subsection-title">Junk пакеты</h3>
			<p class="group-desc">Фейковые пакеты перед handshake — сбивают анализ трафика</p>
			<div class="inline-row inline-row-3">
				<label class="label" for="jc">Jc</label>
				<input type="number" id="jc" class="input" bind:value={ascParams.jc} />
				<label class="label" for="jmin">Jmin</label>
				<input type="number" id="jmin" class="input" bind:value={ascParams.jmin} />
				<label class="label" for="jmax">Jmax</label>
				<input type="number" id="jmax" class="input" bind:value={ascParams.jmax} />
			</div>

			<h3 class="subsection-title">Padding (S1-S2)</h3>
			<p class="group-desc">Дополнительные байты в handshake — меняют размер пакетов WireGuard</p>
			<div class="inline-row inline-row-2">
				<label class="label" for="s1">S1</label>
				<input type="number" id="s1" class="input" bind:value={ascParams.s1} />
				<label class="label" for="s2">S2</label>
				<input type="number" id="s2" class="input" bind:value={ascParams.s2} />
			</div>

			<h3 class="subsection-title">Заголовки (H1-H4)</h3>
			<p class="group-desc">Подмена типов пакетов WireGuard на произвольные значения</p>
			<div class="inline-row inline-row-2">
				<label class="label" for="h1">H1</label>
				<input type="text" id="h1" class="input" bind:value={ascParams.h1} />
				<label class="label" for="h2">H2</label>
				<input type="text" id="h2" class="input" bind:value={ascParams.h2} />
				<label class="label" for="h3">H3</label>
				<input type="text" id="h3" class="input" bind:value={ascParams.h3} />
				<label class="label" for="h4">H4</label>
				<input type="text" id="h4" class="input" bind:value={ascParams.h4} />
			</div>

			{#if isExtended}
				{@const ext = ascParams as ASCParamsExtended}
				<h3 class="subsection-title">Padding (S3-S4)</h3>
				<p class="group-desc">Дополнительные байты в handshake — расширенный режим</p>
				<div class="inline-row inline-row-2">
					<label class="label" for="s3">S3</label>
					<input type="number" id="s3" class="input" bind:value={ext.s3} />
					<label class="label" for="s4">S4</label>
					<input type="number" id="s4" class="input" bind:value={ext.s4} />
				</div>

				<h3 class="subsection-title">Signature пакеты (I1-I5)</h3>
				<p class="group-desc">Имитация протоколов — DPI видит знакомый трафик вместо WireGuard</p>

				<div class="mode-options">
					<label class="mode-option">
						<input type="radio" value="protocol" bind:group={generateMode} />
						<span>Протокол</span>
					</label>
					<label class="mode-option">
						<input type="radio" value="domain" bind:group={generateMode} />
						<span>По домену</span>
					</label>
				</div>

				{#if generateMode === 'protocol'}
					<div class="generate-row">
						<select bind:value={selectedProtocol} class="input protocol-select">
							{#each Object.entries(protocols) as [key, proto]}
								<option value={key}>{proto.name} — {proto.description}</option>
							{/each}
						</select>
						<button class="btn btn-secondary btn-sm" onclick={handleGenerate}>
							Сгенерировать
						</button>
					</div>
				{:else}
					<div class="generate-row">
						<input
							type="text"
							class="input"
							bind:value={domainInput}
							placeholder="example.com"
							disabled={capturing}
							onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); handleCapture(); } }}
						/>
						<button
							class="btn btn-secondary btn-sm"
							onclick={handleCapture}
							disabled={capturing || !domainInput.trim()}
						>
							{capturing ? 'Захват...' : 'Захватить'}
						</button>
					</div>
					{#if captureError}
						<p class="capture-info" class:capture-warning={!!captureSource}>{captureError}</p>
					{/if}
					{#if captureSource && !captureError}
						<span class="capture-badge">{captureSource.toUpperCase()}</span>
					{/if}
				{/if}

				<div class="signature-fields">
					<div class="form-group">
						<input type="text" id="i1" class="input" bind:value={ext.i1} placeholder="I1 (обязательный)" />
					</div>
					<div class="form-group">
						<input type="text" id="i2" class="input" bind:value={ext.i2} placeholder="I2" />
					</div>
					<div class="form-group">
						<input type="text" id="i3" class="input" bind:value={ext.i3} placeholder="I3" />
					</div>
					<div class="form-group">
						<input type="text" id="i4" class="input" bind:value={ext.i4} placeholder="I4" />
					</div>
					<div class="form-group">
						<input type="text" id="i5" class="input" bind:value={ext.i5} placeholder="I5" />
					</div>
				</div>

				<div class="size-indicator" class:over-limit={overLimit}>
					{totalBytes} / {MAX_SIGNATURE_BYTES} байт
					{#if overLimit}
						<span class="size-error">— превышен лимит!</span>
					{/if}
				</div>
			{/if}
		</div>
	{/if}
</PageContainer>

<style>
	.sticky-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		position: sticky;
		top: 0;
		z-index: 10;
		background: var(--bg-primary);
		padding: 0.75rem 0;
		margin-bottom: 1rem;
		border-bottom: 1px solid var(--border);
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.page-title {
		font-size: 1.25rem;
		font-weight: 600;
		margin: 0;
	}

	.badge-system {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 0.6875rem;
		font-weight: 500;
		border-radius: 9999px;
		background: rgba(148, 163, 184, 0.15);
		color: var(--text-muted);
	}

	.section {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 1.25rem;
		margin-bottom: 1rem;
	}

	.section-title {
		font-size: 1rem;
		font-weight: 600;
		margin: 0 0 1rem;
	}

	.info-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.75rem;
	}

	.info-item {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.info-label {
		font-size: 0.6875rem;
		text-transform: uppercase;
		color: var(--text-muted);
	}

	.info-value {
		font-size: 0.875rem;
		color: var(--text-primary);
	}

	.subsection-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-secondary);
		margin: 16px 0 4px;
	}

	.subsection-title:first-child {
		margin-top: 0;
	}

	.group-desc {
		font-size: 11px;
		color: var(--text-muted);
		margin: 0 0 10px 0;
		line-height: 1.4;
	}

	.inline-row {
		display: grid;
		align-items: center;
		gap: 8px;
		margin-bottom: 12px;
	}

	.inline-row-2 {
		grid-template-columns: auto 1fr auto 1fr;
	}

	.inline-row-3 {
		grid-template-columns: auto 1fr auto 1fr auto 1fr;
	}

	.label {
		font-size: 13px;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.input {
		padding: 8px 12px;
		font-size: 13px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
		transition: border-color 0.15s;
	}

	.input:focus {
		outline: none;
		border-color: var(--accent);
	}

	.input[type="number"] {
		-moz-appearance: textfield;
		appearance: textfield;
	}

	.input[type="number"]::-webkit-outer-spin-button,
	.input[type="number"]::-webkit-inner-spin-button {
		-webkit-appearance: none;
		margin: 0;
	}

	.signature-fields {
		display: flex;
		flex-direction: column;
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-bottom: 12px;
	}

	.form-group:last-child {
		margin-bottom: 0;
	}

	.mode-options {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 1rem;
		margin-bottom: 12px;
	}

	.mode-option {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-size: 13px;
		color: var(--text-primary);
		cursor: pointer;
		white-space: nowrap;
	}

	.mode-option input[type="radio"] {
		accent-color: var(--accent);
	}

	.generate-row {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		margin-bottom: 12px;
	}

	.protocol-select {
		width: 100%;
	}

	.size-indicator {
		font-size: 12px;
		color: var(--text-muted);
		margin-top: 4px;
	}

	.size-indicator.over-limit {
		color: var(--error);
		font-weight: 500;
	}

	.size-error {
		font-weight: 600;
	}

	.capture-info {
		font-size: 11px;
		color: var(--error);
		margin-top: 4px;
	}

	.capture-info.capture-warning {
		color: var(--text-muted);
	}

	.capture-badge {
		display: inline-block;
		font-size: 11px;
		font-weight: 600;
		padding: 2px 8px;
		border-radius: 4px;
		background: var(--bg-tertiary);
		color: var(--accent);
		margin-top: 4px;
	}


	@media (max-width: 640px) {
		.info-grid {
			grid-template-columns: 1fr;
		}

		.inline-row-2,
		.inline-row-3 {
			grid-template-columns: auto 1fr;
		}

		.sticky-header {
			flex-direction: column;
			gap: 0.75rem;
			align-items: stretch;
		}

		.header-left {
			flex-wrap: wrap;
		}

		.mode-options {
			flex-direction: column;
			gap: 0.5rem;
			align-items: flex-start;
		}
	}
</style>
