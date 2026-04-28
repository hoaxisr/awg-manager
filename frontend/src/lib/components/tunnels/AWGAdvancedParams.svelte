<script lang="ts">
	import { protocols, getSignaturePackets, calcByteSize, calcTotalSize, type ProtocolKey } from '$lib/utils/protocols';
	import { api } from '$lib/api/client';
	import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';

	interface AWGFormFields {
		mtu: number;
		jc: number;
		jmin: number;
		jmax: number;
		s1: number;
		s2: number;
		s3: number;
		s4: number;
		h1: string;
		h2: string;
		h3: string;
		h4: string;
		i1: string;
		i2: string;
		i3: string;
		i4: string;
		i5: string;
		[key: string]: unknown;
	}

	interface AWGErrorFields {
		jc?: string[];
		jmin?: string[];
		jmax?: string[];
		s1?: string[];
		s2?: string[];
		s3?: string[];
		s4?: string[];
		h1?: string[];
		h2?: string[];
		h3?: string[];
		h4?: string[];
		i1?: string[];
		i2?: string[];
		i3?: string[];
		i4?: string[];
		i5?: string[];
		[key: string]: string[] | undefined;
	}

	const MAX_SIGNATURE_BYTES = 4096;

	type GenerateMode = 'protocol' | 'domain';

	let {
		form = $bindable(),
		errors,
		hints = undefined,
		compact = false
	}: {
		form: AWGFormFields;
		errors: AWGErrorFields;
		hints?: Record<string, string>;
		compact?: boolean;
	} = $props();

	let selectedProtocol = $state<ProtocolKey>('quic_initial');
	let generateMode = $state<GenerateMode>('protocol');
	let domainInput = $state('');
	let capturing = $state(false);
	let captureError = $state('');
	let captureSource = $state('');

	let totalBytes = $derived(
		calcByteSize(String(form.i1 || '')) + calcByteSize(String(form.i2 || '')) +
		calcByteSize(String(form.i3 || '')) + calcByteSize(String(form.i4 || '')) +
		calcByteSize(String(form.i5 || ''))
	);

	let overLimit = $derived(totalBytes > MAX_SIGNATURE_BYTES);

	function handleGenerate() {
		const packets = getSignaturePackets(selectedProtocol, form.mtu);
		const size = calcTotalSize(packets);
		if (size > MAX_SIGNATURE_BYTES) return;
		form.i1 = packets.i1;
		form.i2 = packets.i2;
		form.i3 = packets.i3;
		form.i4 = packets.i4;
		form.i5 = packets.i5;
	}

	async function handleCapture() {
		if (!domainInput.trim()) return;
		capturing = true;
		captureError = '';
		captureSource = '';
		try {
			const result = await api.captureSignature(domainInput.trim());
			form.i1 = result.packets.i1 || '';
			form.i2 = result.packets.i2 || '';
			form.i3 = result.packets.i3 || '';
			form.i4 = result.packets.i4 || '';
			form.i5 = result.packets.i5 || '';
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
</script>

<div class="awg-params" class:compact>
	<section class="param-section">
		<h3 class="section-title">Junk пакеты</h3>
		<p class="group-desc">Фейковые пакеты перед handshake — сбивают анализ трафика</p>
		<div class="inline-row inline-row-3">
			<label class="field-label" for="jc">Jc {#if hints}<span class="hint" title={hints.jc}>?</span>{/if}</label>
			<input type="number" id="jc" class="field-input" bind:value={form.jc} />
			<label class="field-label" for="jmin">Jmin {#if hints}<span class="hint" title={hints.jmin}>?</span>{/if}</label>
			<input type="number" id="jmin" class="field-input" bind:value={form.jmin} />
			<label class="field-label" for="jmax">Jmax {#if hints}<span class="hint" title={hints.jmax}>?</span>{/if}</label>
			<input type="number" id="jmax" class="field-input" bind:value={form.jmax} />
		</div>
	</section>

	<section class="param-section">
		<h3 class="section-title">Padding (S1-S4)</h3>
		<p class="group-desc">Дополнительные байты в handshake — меняют размер пакетов WireGuard</p>
		<div class="inline-row inline-row-2">
			<label class="field-label" for="s1">S1 {#if hints}<span class="hint" title={hints.s1}>?</span>{/if}</label>
			<input type="number" id="s1" class="field-input" bind:value={form.s1} />
			<label class="field-label" for="s2">S2 {#if hints}<span class="hint" title={hints.s2}>?</span>{/if}</label>
			<input type="number" id="s2" class="field-input" bind:value={form.s2} />
			<label class="field-label" for="s3">S3 {#if hints}<span class="hint" title={hints.s3}>?</span>{/if}</label>
			<input type="number" id="s3" class="field-input" bind:value={form.s3} />
			<label class="field-label" for="s4">S4 {#if hints}<span class="hint" title={hints.s4}>?</span>{/if}</label>
			<input type="number" id="s4" class="field-input" bind:value={form.s4} />
		</div>
	</section>

	<section class="param-section">
		<h3 class="section-title">Заголовки (H1-H4)</h3>
		<p class="group-desc">Подмена типов пакетов WireGuard на произвольные значения</p>
		<div class="inline-row inline-row-2">
			<label class="field-label" for="h1">H1 {#if hints}<span class="hint" title={hints.h1}>?</span>{/if}</label>
			<input type="text" id="h1" class="field-input" bind:value={form.h1} />
			<label class="field-label" for="h2">H2 {#if hints}<span class="hint" title={hints.h2}>?</span>{/if}</label>
			<input type="text" id="h2" class="field-input" bind:value={form.h2} />
			<label class="field-label" for="h3">H3 {#if hints}<span class="hint" title={hints.h3}>?</span>{/if}</label>
			<input type="text" id="h3" class="field-input" bind:value={form.h3} />
			<label class="field-label" for="h4">H4 {#if hints}<span class="hint" title={hints.h4}>?</span>{/if}</label>
			<input type="text" id="h4" class="field-input" bind:value={form.h4} />
		</div>
	</section>

	<section class="param-section">
		<h3 class="section-title">Signature пакеты (I1-I5)</h3>
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
			{@const protocolOpts: DropdownOption<ProtocolKey>[] = Object.entries(protocols).map(([key, proto]) => ({
				value: key as ProtocolKey,
				label: proto.name,
				description: proto.description,
			}))}
			<div class="generate-row">
				<div class="protocol-select">
					<Dropdown bind:value={selectedProtocol} options={protocolOpts} fullWidth />
				</div>
				<Button variant="secondary" size="sm" onclick={handleGenerate}>
					Сгенерировать
				</Button>
			</div>
		{:else}
			<div class="generate-row">
				<input
					type="text"
					class="field-input"
					bind:value={domainInput}
					placeholder="example.com"
					disabled={capturing}
					onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); handleCapture(); } }}
				/>
				<Button
					variant="secondary"
					size="sm"
					onclick={handleCapture}
					disabled={capturing || !domainInput.trim()}
				>
					{capturing ? 'Захват...' : 'Захватить'}
				</Button>
			</div>
			{#if captureError}
				<p class="capture-info" class:capture-warning={!!captureSource}>{captureError}</p>
			{/if}
			{#if captureSource && !captureError}
				<span class="capture-badge">{captureSource.toUpperCase()}</span>
			{/if}
		{/if}

		<div class="signature-fields">
			{#each ['i1', 'i2', 'i3', 'i4', 'i5'] as field, idx}
				<div class="form-group">
					<input type="text" id={field} class="field-input" bind:value={form[field]} placeholder={field.toUpperCase() + (idx === 0 ? ' (обязательный)' : '')} />
					{#if errors[field]}<p class="field-error">{errors[field]}</p>{/if}
				</div>
			{/each}
		</div>

		<div class="size-indicator" class:over-limit={overLimit}>
			{totalBytes} / {MAX_SIGNATURE_BYTES} байт
			{#if overLimit}
				<span class="size-error">— превышен лимит!</span>
			{/if}
		</div>
	</section>
</div>

<style>
	/* Layout-only. Form-control visuals come from .field-input / .field-select / .field-label in app.css. */
	.awg-params {
		display: flex;
		flex-direction: column;
		gap: 20px;
	}

	.param-section {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 16px;
	}

	.section-title {
		font-size: 14px;
		font-weight: 600;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--color-border);
		margin-bottom: 12px;
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

	.inline-row {
		display: grid;
		align-items: center;
		gap: 8px;
	}

	.inline-row-2 {
		grid-template-columns: auto 1fr auto 1fr;
	}

	.inline-row-3 {
		grid-template-columns: auto 1fr auto 1fr auto 1fr;
	}

	.field-error {
		font-size: 11px;
		color: var(--color-error);
	}

	/* "?" help marker next to a field label */
	.hint {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 14px;
		height: 14px;
		font-size: 10px;
		background: var(--color-bg-tertiary);
		border-radius: 50%;
		color: var(--color-text-muted);
		cursor: help;
	}

	.group-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		margin: 0 0 10px 0;
		line-height: 1.4;
	}

	.signature-fields {
		display: flex;
		flex-direction: column;
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
		color: var(--color-text-primary);
		cursor: pointer;
		white-space: nowrap;
	}

	.mode-option input[type="radio"] {
		accent-color: var(--color-accent);
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
		color: var(--color-text-muted);
		margin-top: 4px;
	}

	.size-indicator.over-limit {
		color: var(--color-error);
		font-weight: 500;
	}

	.size-error {
		font-weight: 600;
	}

	.capture-info {
		font-size: 11px;
		color: var(--color-error);
		margin-top: 4px;
	}

	.capture-info.capture-warning {
		color: var(--color-text-muted);
	}

	.capture-badge {
		display: inline-block;
		font-size: 11px;
		font-weight: 600;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-tertiary);
		color: var(--color-accent);
		margin-top: 4px;
	}

	@media (max-width: 640px) {
		.inline-row-2,
		.inline-row-3 {
			grid-template-columns: auto 1fr;
		}
	}

	@media (max-width: 480px) {
		.mode-options {
			flex-direction: column;
			gap: 0.5rem;
			align-items: flex-start;
		}
	}
</style>
