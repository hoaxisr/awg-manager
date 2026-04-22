<script lang="ts">
	import { api } from '$lib/api/client';
	import { goto } from '$app/navigation';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import type { ASCParams, ASCParamsExtended, SystemInfo } from '$lib/types';
	import { PageContainer } from '$lib/components/layout';
	import { calcByteSize, calcTotalSize } from '$lib/utils/protocols';
	import { generateASCParams } from '$lib/utils/asc-generator';

	let ascParams = $state<ASCParams | null>(null);
	let systemInfo = $state<SystemInfo | null>(null);
	let saving = $state(false);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let generating = $state(false);

	const isExtended = $derived(ascParams !== null && 's3' in ascParams);

	// Signature capture
	const MAX_SIGNATURE_BYTES = 4096;
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

	/** Generate ASC parameters: Jc/Jmin/Jmax, S1-S4, H1-H4. I1-I5 set separately via capture. */
	async function handleGenerateAll() {
		if (!ascParams) return;
		generating = true;
		try {
			const extended = isExtended && (systemInfo?.supportsExtendedASC ?? false);
			const hRanges = systemInfo?.supportsHRanges ?? false;

			// Generate numeric params + headers
			const params = generateASCParams({ extended, hRanges });
			ascParams.jc = params.jc;
			ascParams.jmin = params.jmin;
			ascParams.jmax = params.jmax;
			ascParams.s1 = params.s1;
			ascParams.s2 = params.s2;
			ascParams.h1 = params.h1;
			ascParams.h2 = params.h2;
			ascParams.h3 = params.h3;
			ascParams.h4 = params.h4;

			if (extended && isExtended) {
				const ext = ascParams as ASCParamsExtended;
				ext.s3 = params.s3!;
				ext.s4 = params.s4!;
			}

			notifications.success('Параметры сгенерированы');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка генерации');
		} finally {
			generating = false;
		}
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
			[ascParams, systemInfo] = await Promise.all([
				api.getManagedServerASC(),
				api.getSystemInfo()
			]);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Не удалось загрузить параметры';
		} finally {
			loading = false;
		}
	}

	async function handleSave() {
		if (!ascParams) return;
		saving = true;
		try {
			const fresh = await api.setManagedServerASC(ascParams);
			servers.applyMutationResponse(fresh);
			notifications.success('Параметры обфускации сохранены');
			// Warn about existing peers needing reconfiguration
			try {
				const server = await api.getManagedServer();
				if (server && server.peers.length > 0) {
					const n = server.peers.length;
					notifications.warning(
						`Клиенты (${n}) должны быть переконфигурированы — скачайте новые конфигурации`,
						12000
					);
				}
			} catch {
				// Non-critical — don't block on peer count fetch
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения');
		} finally {
			saving = false;
		}
	}
</script>

<svelte:head>
	<title>Обфускация — AWG Manager</title>
</svelte:head>

<PageContainer>
	<div class="sticky-header">
		<div class="header-left">
			<button class="btn btn-ghost btn-sm" onclick={() => goto('/servers')}>
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M19 12H5M12 19l-7-7 7-7"/>
				</svg>
				Назад
			</button>
			<h1 class="page-title">Обфускация (ASC)</h1>
			<span class="badge-managed">Управляемый сервер</span>
		</div>
		<div class="header-actions">
			<button
				class="btn btn-secondary"
				onclick={handleGenerateAll}
				disabled={generating || !ascParams}
			>
				{generating ? 'Генерация...' : 'Сгенерировать параметры'}
			</button>
			<button
				class="btn btn-primary"
				onclick={handleSave}
				disabled={saving || generating || capturing || !ascParams}
			>
				{saving ? 'Сохранение...' : 'Сохранить'}
			</button>
		</div>
	</div>

	{#if loading}
		<div class="py-12 text-center text-surface-400">Загрузка...</div>
	{:else if error}
		<div class="py-12 text-center text-error-500">{error}</div>
	{:else if ascParams}
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

	.header-actions {
		display: flex;
		gap: 0.5rem;
	}

	.page-title {
		font-size: 1.25rem;
		font-weight: 600;
		margin: 0;
	}

	.badge-managed {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 0.6875rem;
		font-weight: 500;
		border-radius: 9999px;
		background: rgba(59, 130, 246, 0.15);
		color: var(--accent);
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

	.generate-row {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		margin-bottom: 12px;
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
	}
</style>
