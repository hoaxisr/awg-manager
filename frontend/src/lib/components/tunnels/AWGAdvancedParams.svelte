<script lang="ts">
	import { type ProtocolKey, protocols } from '$lib/utils/protocols';

	interface AWGFormFields {
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

	let {
		form = $bindable(),
		errors,
		hints = undefined,
		selectedProtocol = $bindable<ProtocolKey>('quic'),
		onGenerate,
		showSignatureControls = true,
		showCPSHints = false,
		compact = false
	}: {
		form: AWGFormFields;
		errors: AWGErrorFields;
		hints?: Record<string, string>;
		selectedProtocol: ProtocolKey;
		onGenerate: () => void;
		showSignatureControls?: boolean;
		showCPSHints?: boolean;
		compact?: boolean;
	} = $props();
</script>

<div class="awg-params" class:compact>
	<h3 class="subsection-title">Junk пакеты</h3>
	<p class="group-desc">Фейковые пакеты перед handshake — сбивают анализ трафика</p>
	<div class="form-row form-row-3">
		<div class="form-group">
			<label class="label" for="jc">Jc {#if hints}<span class="hint" title={hints.jc}>?</span>{/if}</label>
			<input type="number" id="jc" class="input" bind:value={form.jc} />
			{#if errors.jc}<p class="field-error">{errors.jc}</p>{/if}
		</div>
		<div class="form-group">
			<label class="label" for="jmin">Jmin {#if hints}<span class="hint" title={hints.jmin}>?</span>{/if}</label>
			<input type="number" id="jmin" class="input" bind:value={form.jmin} />
			{#if errors.jmin}<p class="field-error">{errors.jmin}</p>{/if}
		</div>
		<div class="form-group">
			<label class="label" for="jmax">Jmax {#if hints}<span class="hint" title={hints.jmax}>?</span>{/if}</label>
			<input type="number" id="jmax" class="input" bind:value={form.jmax} />
			{#if errors.jmax}<p class="field-error">{errors.jmax}</p>{/if}
		</div>
	</div>

	<h3 class="subsection-title">Padding (S1-S4)</h3>
	<p class="group-desc">Дополнительные байты в handshake — меняют размер пакетов WireGuard</p>
	<div class="form-row form-row-compact">
		<div class="form-group">
			<label class="label" for="s1">S1 {#if hints}<span class="hint" title={hints.s1}>?</span>{/if}</label>
			<input type="number" id="s1" class="input" bind:value={form.s1} />
			{#if errors.s1}<p class="field-error">{errors.s1}</p>{/if}
		</div>
		<div class="form-group">
			<label class="label" for="s2">S2 {#if hints}<span class="hint" title={hints.s2}>?</span>{/if}</label>
			<input type="number" id="s2" class="input" bind:value={form.s2} />
			{#if errors.s2}<p class="field-error">{errors.s2}</p>{/if}
		</div>
		<div class="form-group">
			<label class="label" for="s3">S3 {#if hints}<span class="hint" title={hints.s3}>?</span>{/if}</label>
			<input type="number" id="s3" class="input" bind:value={form.s3} />
			{#if errors.s3}<p class="field-error">{errors.s3}</p>{/if}
		</div>
		<div class="form-group">
			<label class="label" for="s4">S4 {#if hints}<span class="hint" title={hints.s4}>?</span>{/if}</label>
			<input type="number" id="s4" class="input" bind:value={form.s4} />
			{#if errors.s4}<p class="field-error">{errors.s4}</p>{/if}
		</div>
	</div>

	<h3 class="subsection-title">Заголовки (H1-H4)</h3>
	<p class="group-desc">Подмена типов пакетов WireGuard на произвольные значения</p>
	<div class="form-row form-row-compact">
		<div class="form-group">
			<label class="label" for="h1">H1 {#if hints}<span class="hint" title={hints.h1}>?</span>{/if}</label>
			<input type="text" id="h1" class="input" bind:value={form.h1} placeholder="123-456" />
			{#if errors.h1}<p class="field-error">{errors.h1}</p>{/if}
		</div>
		<div class="form-group">
			<label class="label" for="h2">H2 {#if hints}<span class="hint" title={hints.h2}>?</span>{/if}</label>
			<input type="text" id="h2" class="input" bind:value={form.h2} placeholder="123-456" />
			{#if errors.h2}<p class="field-error">{errors.h2}</p>{/if}
		</div>
		<div class="form-group">
			<label class="label" for="h3">H3 {#if hints}<span class="hint" title={hints.h3}>?</span>{/if}</label>
			<input type="text" id="h3" class="input" bind:value={form.h3} placeholder="123-456" />
			{#if errors.h3}<p class="field-error">{errors.h3}</p>{/if}
		</div>
		<div class="form-group">
			<label class="label" for="h4">H4 {#if hints}<span class="hint" title={hints.h4}>?</span>{/if}</label>
			<input type="text" id="h4" class="input" bind:value={form.h4} placeholder="123-456" />
			{#if errors.h4}<p class="field-error">{errors.h4}</p>{/if}
		</div>
	</div>

	{#if showSignatureControls}
		<div class="signature-header">
			<h3 class="subsection-title">Signature пакеты (I1-I5)</h3>
			<div class="generate-controls">
				<select class="protocol-select" bind:value={selectedProtocol}>
					{#each Object.entries(protocols) as [key, proto]}
						<option value={key}>{proto.name}</option>
					{/each}
				</select>
				<button type="button" class="btn-generate" onclick={onGenerate}>
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"/>
					</svg>
					Сгенерировать
				</button>
			</div>
		</div>
		<p class="protocol-hint">{protocols[selectedProtocol].description}</p>
	{:else}
		<h3 class="subsection-title">Signature пакеты (I1-I5)</h3>
		<p class="group-desc">Заполняются генератором слева или вручную</p>
	{/if}

	<div class="signature-fields">
		{#each ['i1', 'i2', 'i3', 'i4', 'i5'] as field, idx}
			<div class="form-group">
				<label class="label" for={field}>{field.toUpperCase()}</label>
				<input type="text" id={field} class="input" bind:value={form[field]} placeholder={idx === 0 ? 'обязательный' : ''} />
				{#if errors[field]}<p class="field-error">{errors[field]}</p>{/if}
			</div>
		{/each}
	</div>

	{#if showCPSHints}
		<div class="hint-box">
			<div class="hint-header">
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<circle cx="12" cy="12" r="10"/>
					<line x1="12" y1="16" x2="12" y2="12"/>
					<line x1="12" y1="8" x2="12.01" y2="8"/>
				</svg>
				<span>Доступные теги CPS</span>
			</div>
			<div class="hint-content">
				<div class="hint-row"><code>&lt;r X&gt;</code> <span>X случайных байт</span></div>
				<div class="hint-row"><code>&lt;c&gt;</code> <span>Счётчик пакетов</span></div>
				<div class="hint-row"><code>&lt;t&gt;</code> <span>Timestamp UNIX</span></div>
				<div class="hint-row"><code>&lt;b 0xXX&gt;</code> <span>Статические байты</span></div>
			</div>
		</div>
	{/if}
</div>

<style>
	.awg-params {
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

	.field-error {
		font-size: 11px;
		color: var(--error);
	}

	.label {
		font-size: 13px;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.hint {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 14px;
		height: 14px;
		font-size: 10px;
		background: var(--bg-tertiary);
		border-radius: 50%;
		color: var(--text-muted);
		cursor: help;
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

	.form-row {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 12px;
	}

	.form-row-3 {
		grid-template-columns: repeat(3, 1fr);
	}

	/* In compact mode (right column), use 2-col for S/H fields */
	.form-row-compact {
		grid-template-columns: repeat(2, 1fr);
	}

	/* In non-compact mode (full width), use 4-col for S/H fields */
	:not(.compact) > .form-row-compact {
		grid-template-columns: repeat(4, 1fr);
	}

	:not(.compact) > .form-row-3 {
		grid-template-columns: repeat(3, 1fr);
	}

	/* Signature fields: always one per row (long values) */
	.signature-fields {
		display: flex;
		flex-direction: column;
	}

	.signature-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 8px;
		margin-top: 16px;
	}

	.signature-header .subsection-title {
		margin: 0;
	}

	.generate-controls {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.protocol-select {
		padding: 6px 10px;
		font-size: 12px;
		color: var(--text-primary);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		cursor: pointer;
	}

	.btn-generate {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 6px 12px;
		font-size: 12px;
		font-weight: 500;
		color: var(--accent);
		background: transparent;
		border: 1px solid var(--accent);
		border-radius: 6px;
		cursor: pointer;
		transition: all 0.15s;
	}

	.btn-generate:hover {
		background: var(--accent);
		color: white;
	}

	.protocol-hint {
		font-size: 12px;
		color: var(--text-muted);
		margin: 4px 0 12px 0;
		font-style: italic;
	}

	.hint-box {
		margin-top: 16px;
		padding: 12px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		font-size: 12px;
	}

	.hint-header {
		display: flex;
		align-items: center;
		gap: 6px;
		color: var(--text-secondary);
		font-weight: 500;
		margin-bottom: 8px;
	}

	.hint-content {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 4px 16px;
	}

	.hint-row {
		display: flex;
		align-items: center;
		gap: 8px;
		color: var(--text-muted);
	}

	.hint-row code {
		font-family: monospace;
		color: var(--accent);
		background: var(--bg-tertiary);
		padding: 1px 4px;
		border-radius: 3px;
		font-size: 11px;
	}

	@media (max-width: 640px) {
		.form-row,
		.form-row-3,
		.form-row-compact {
			grid-template-columns: 1fr;
		}

		.hint-content {
			grid-template-columns: 1fr;
		}
	}
</style>
