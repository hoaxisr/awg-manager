<script lang="ts">
	import { protocols, MAX_SIGNATURE_CHARS, type ProtocolKey, type SignaturePackets } from '$lib/utils/protocols';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import type { ASCParams, ASCParamsExtended } from '$lib/types';
	import { isExtendedASCParams } from '$lib/utils/asc-validation';
	import { awgParamHints } from '$lib/utils/awgParamHints';
	import { notifications } from '$lib/stores/notifications';
	import { SettingsSectionLabel } from '$lib/components/settings';
	import { Badge, Button, Dropdown, FieldHint, type DropdownOption } from '$lib/components/ui';
	import { Fingerprint, Hash, MoveHorizontal, Shredder, ShieldCheck, Shuffle } from 'lucide-svelte';

	type GenerateMode = 'protocol' | 'domain';
	type SignatureModes = 'both' | 'domain' | 'none';

	type ASCErrorFields = Partial<Record<keyof ASCParamsExtended, string[]>>;

	let {
		params = $bindable(),
		extended = undefined,
		awg3 = false,
		awg3Limited = false,
		errors = {},
		hints = undefined,
		signatureModes = 'both',
		idPrefix = '',
		compact = false,
	}: {
		params: ASCParams;
		extended?: boolean;
		awg3?: boolean;
		// NativeWG through awg_proxy (firmware before 5.02.A.11) can only do header
		// protection + random trailers — not timers / content padding. Hide those when true.
		awg3Limited?: boolean;
		errors?: ASCErrorFields;
		hints?: Record<string, string>;
		signatureModes?: SignatureModes;
		idPrefix?: string;
		compact?: boolean;
	} = $props();

	const hintMap = $derived(hints ?? awgParamHints());
	const showExtended = $derived(extended ?? isExtendedASCParams(params));

	// AWG 3.0 device params (kernel mode only). label = shown name, hint key.
	const awg3RangeFields: { key: keyof ASCParamsExtended; label: string }[] = [
		{ key: 'rekeyAfterTime', label: 'RekeyAfterTime' },
		{ key: 'rekeyTimeout', label: 'RekeyTimeout' },
		{ key: 'rejectAfterTime', label: 'RejectAfterTime' },
		{ key: 'keepaliveTimeout', label: 'KeepaliveTimeout' },
		{ key: 'maxHandshakeAttempts', label: 'MaxHandshakeAttempts' },
		{ key: 'contentPaddingAddition', label: 'ContentPaddingAddition' },
	];

	// AWG 3.1 device flags. Read-only: they arrive with an imported .conf and
	// switching RandomTrailers off on one end alone kills the tunnel.
	const AWG31_FLAGS: { key: 'randomTrailers' | 'disableCookies'; label: string }[] = [
		{
			key: 'randomTrailers',
			label: 'RandomTrailers',
		},
		{
			key: 'disableCookies',
			label: 'DisableCookies',
		},
	];
	const ext31Flags = $derived(
		AWG31_FLAGS.filter((f) => (params as ASCParamsExtended)[f.key]),
	);

	let selectedProtocol = $state<ProtocolKey>('quic_initial');
	let generateMode = $state<GenerateMode>('protocol');
	let domainInput = $state('');
	let capturing = $state(false);
	let generating = $state(false);
	/** Предупреждение/текст ошибки от бэкенда; общий текст сбоя выводится из `captureFailed`. */
	let captureError = $state('');
	let captureFailed = $state(false);
	const captureErrorText = $derived(captureError || (captureFailed ? m.asc_capture_failed() : ''));
	let captureSource = $state('');

	let totalChars = $derived.by(() => {
		if (!showExtended) return 0;
		const ext = params as ASCParamsExtended;
		return (
			String(ext.i1 || '') +
			String(ext.i2 || '') +
			String(ext.i3 || '') +
			String(ext.i4 || '') +
			String(ext.i5 || '')
		).length;
	});

	let overLimit = $derived(totalChars > MAX_SIGNATURE_CHARS);

	function fieldId(name: string): string {
		return `${idPrefix}${name}`;
	}

	function applySignaturePackets(packets: SignaturePackets) {
		params = {
			...params,
			i1: packets.i1,
			i2: packets.i2,
			i3: packets.i3,
			i4: packets.i4,
			i5: packets.i5,
		} as ASCParams;
	}

	async function handleGenerate() {
		if (!showExtended) return;
		generating = true;
		try {
			const res = await api.generateSignature(selectedProtocol);
			applySignaturePackets(res.packets);
			notifications.success(m.asc_signature_generated({ name: protocols[selectedProtocol].name }));
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.asc_generate_failed());
		} finally {
			generating = false;
		}
	}

	async function handleCapture() {
		if (!showExtended) {
			notifications.error(m.asc_signature_unavailable());
			return;
		}
		if (!domainInput.trim()) return;

		capturing = true;
		captureError = '';
		captureFailed = false;
		captureSource = '';
		try {
			const result = await api.captureSignature(domainInput.trim());
			applySignaturePackets({
				i1: result.packets.i1 || '',
				i2: result.packets.i2 || '',
				i3: result.packets.i3 || '',
				i4: result.packets.i4 || '',
				i5: result.packets.i5 || '',
			});
			captureSource = result.source;
			if (result.warning) {
				captureError = result.warning;
			} else {
				notifications.success(m.asc_capture_done());
			}
		} catch (e: unknown) {
			captureError = e instanceof Error ? e.message : '';
			captureFailed = true;
			notifications.error(captureErrorText);
		} finally {
			capturing = false;
		}
	}
</script>

{#snippet paramLabel(id: string, name: string)}
	<label class="field-label param-field-label" for={fieldId(id)}>
		{name}
		{#if hintMap[id]}
			<FieldHint text={hintMap[id]} ariaLabel={m.proxy_detail_hint_aria({ title: name })} />
		{/if}
	</label>
{/snippet}

<div class="asc-editor" class:compact>
	<section class="card param-section">
		<SettingsSectionLabel label={m.asc_junk_title()} icon={Shredder} tone="orange" header />
		<p class="group-desc">{m.asc_junk_desc()}</p>
		<div class="inline-row inline-row-3">
			{@render paramLabel('jc', 'Jc')}
			<input type="number" id={fieldId('jc')} class="field-input" bind:value={params.jc} />
			{@render paramLabel('jmin', 'Jmin')}
			<input type="number" id={fieldId('jmin')} class="field-input" bind:value={params.jmin} />
			{@render paramLabel('jmax', 'Jmax')}
			<input type="number" id={fieldId('jmax')} class="field-input" bind:value={params.jmax} />
		</div>
	</section>

	<section class="card param-section">
		<SettingsSectionLabel
			label={showExtended ? 'Padding (S1-S4)' : 'Padding (S1-S2)'}
			icon={MoveHorizontal}
			tone="teal"
			header
		/>
		<p class="group-desc">{m.asc_padding_desc()}</p>
		<div class="inline-row inline-row-2">
			{@render paramLabel('s1', 'S1')}
			<input type="number" id={fieldId('s1')} class="field-input" bind:value={params.s1} />
			{@render paramLabel('s2', 'S2')}
			<input type="number" id={fieldId('s2')} class="field-input" bind:value={params.s2} />
			{#if showExtended}
				{@const ext = params as ASCParamsExtended}
				{@render paramLabel('s3', 'S3')}
				<input type="number" id={fieldId('s3')} class="field-input" bind:value={ext.s3} />
				{@render paramLabel('s4', 'S4')}
				<input type="number" id={fieldId('s4')} class="field-input" bind:value={ext.s4} />
			{/if}
		</div>
	</section>

	<section class="card param-section">
		<SettingsSectionLabel label={m.asc_headers_title()} icon={Hash} tone="indigo" header />
		<p class="group-desc">{m.asc_headers_desc()}</p>
		<div class="inline-row inline-row-2">
			{@render paramLabel('h1', 'H1')}
			<input type="text" id={fieldId('h1')} class="field-input" bind:value={params.h1} />
			{@render paramLabel('h2', 'H2')}
			<input type="text" id={fieldId('h2')} class="field-input" bind:value={params.h2} />
			{@render paramLabel('h3', 'H3')}
			<input type="text" id={fieldId('h3')} class="field-input" bind:value={params.h3} />
			{@render paramLabel('h4', 'H4')}
			<input type="text" id={fieldId('h4')} class="field-input" bind:value={params.h4} />
		</div>
	</section>

	{#if showExtended && signatureModes !== 'none'}
		{@const ext = params as ASCParamsExtended}
		<section class="card param-section">
			<SettingsSectionLabel label={m.asc_signature_title()} icon={Fingerprint} tone="green" header />
			<p class="group-desc">{m.asc_signature_desc()}</p>

			{#if signatureModes === 'both'}
				<div class="mode-options">
					<div class="mode-options-radios">
						<label class="mode-option">
							<input type="radio" value="protocol" bind:group={generateMode} />
							<span>{m.asc_mode_protocol()}</span>
						</label>
						<label class="mode-option">
							<input type="radio" value="domain" bind:group={generateMode} />
							<span>{m.asc_mode_domain()}</span>
						</label>
					</div>
					{#if captureSource && !captureErrorText}
						<span class="capture-badge">{captureSource.toUpperCase()}</span>
					{/if}
				</div>
			{:else if captureSource && !captureErrorText}
				<div class="mode-options mode-options-badge-only">
					<span class="capture-badge">{captureSource.toUpperCase()}</span>
				</div>
			{/if}

			{#if signatureModes === 'domain' || generateMode === 'domain'}
				<div class="generate-row">
					<input
						type="text"
						class="field-input"
						bind:value={domainInput}
						placeholder="example.com"
						disabled={capturing}
						onkeydown={(e) => {
							if (e.key === 'Enter') {
								e.preventDefault();
								handleCapture();
							}
						}}
					/>
					<Button
						variant="secondary"
						size="sm"
						onclick={handleCapture}
						disabled={capturing || !domainInput.trim()}
						loading={capturing}
					>
						{capturing ? m.asc_capturing() : m.asc_capture()}
					</Button>
				</div>
				{#if captureErrorText}
					<p class="capture-info" class:capture-warning={!!captureSource}>{captureErrorText}</p>
				{/if}
			{:else}
				{@const protocolOpts: DropdownOption<ProtocolKey>[] = Object.entries(protocols).map(
					([key, proto]) => ({
						value: key as ProtocolKey,
						label: proto.name,
						description: proto.description,
					}),
				)}
				<div class="generate-row">
					<div class="protocol-select">
						<Dropdown bind:value={selectedProtocol} options={protocolOpts} fullWidth />
					</div>
					<Button
						variant="secondary"
						size="sm"
						onclick={handleGenerate}
						disabled={generating || capturing}
						loading={generating}
					>
						{generating ? m.asc_generating() : m.common_generate()}
					</Button>
				</div>
			{/if}

			<div class="signature-fields">
				{#each ['i1', 'i2', 'i3', 'i4', 'i5'] as field, idx}
					<div class="form-group">
						{@render paramLabel(field, field.toUpperCase())}
						<input
							type="text"
							id={fieldId(field)}
							class="field-input"
							bind:value={ext[field as keyof ASCParamsExtended]}
							placeholder={idx === 0 ? m.asc_signature_required({ field: field.toUpperCase() }) : field.toUpperCase()}
						/>
						{#if errors[field as keyof ASCParamsExtended]}
							<p class="field-error">{errors[field as keyof ASCParamsExtended]}</p>
						{/if}
					</div>
				{/each}
			</div>

			<div class="size-indicator" class:over-limit={overLimit}>
				{m.asc_size_chars({ total: totalChars, max: MAX_SIGNATURE_CHARS })}
				{#if overLimit}
					<span class="size-error">{m.asc_size_over_limit()}</span>
				{/if}
			</div>
		</section>
	{/if}

	{#if awg3}
		{@const ext = params as ASCParamsExtended}
		<section class="card param-section">
			<SettingsSectionLabel label="AmneziaWG 3.0" icon={ShieldCheck} tone="purple" header />
			<p class="group-desc">
				{#if awg3Limited}
					{m.asc_awg3_limited_note()}
				{:else}
					{m.asc_awg3_full_note_before()}
					<code>min-max</code> {m.asc_awg3_full_note_after()}
				{/if}
			</p>

			<div class="form-group">
				{@render paramLabel('headerProtectionKey', 'HeaderProtectionKey')}
				<input
					type="text"
					id={fieldId('headerProtectionKey')}
					class="field-input"
					bind:value={ext.headerProtectionKey}
					placeholder={m.asc_header_key_placeholder()}
				/>
				{#if errors['headerProtectionKey' as keyof ASCParamsExtended]}
					<p class="field-error">{errors['headerProtectionKey' as keyof ASCParamsExtended]}</p>
				{/if}
			</div>

			{#if !awg3Limited}
				<div class="inline-row inline-row-2">
					{#each awg3RangeFields as f}
						{@render paramLabel(f.key, f.label)}
						<input
							type="text"
							id={fieldId(f.key)}
							class="field-input"
							bind:value={ext[f.key]}
							placeholder={m.asc_timer_placeholder()}
						/>
					{/each}
				</div>
				{#each awg3RangeFields as f}
					{#if errors[f.key]}
						<p class="field-error">{f.label}: {errors[f.key]}</p>
					{/if}
				{/each}
			{/if}
		</section>
	{/if}

	{#if ext31Flags.length > 0}
		<section class="card param-section">
			<SettingsSectionLabel label="AmneziaWG 3.1" icon={Shuffle} tone="purple" header />
			<p class="group-desc">
				{m.asc_awg31_note()}
			</p>

			<div class="toggle-stack">
				{#each ext31Flags as f}
					<div>
						<div class="flag-row">
							<Badge variant="purple" size="xs" mono>{f.label}</Badge>
							<span class="flag-state">{m.asc_flag_enabled()}</span>
						</div>
						<p class="toggle-note">{f.key === 'randomTrailers' ? m.asc_note_random_trailers() : m.asc_note_disable_cookies()}</p>
					</div>
				{/each}
			</div>
		</section>
	{/if}
</div>

<style>
	.asc-editor {
		display: flex;
		flex-direction: column;
		gap: var(--settings-gap);
	}

	.param-section {
		background: var(--color-settings-surface-bg);
		overflow: visible;
	}

	.toggle-stack {
		display: flex;
		flex-direction: column;
		gap: 1.25rem;
	}

	.flag-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.flag-state {
		font-size: 0.8125rem;
		color: var(--color-text-muted, #6b7280);
	}

	.toggle-note {
		margin: 0.35rem 0 0;
		font-size: 0.8125rem;
		line-height: 1.45;
		color: var(--color-text-muted, #6b7280);
	}

	.param-section :global(.settings-section-label.header) {
		margin-bottom: 0.5rem;
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

	.param-field-label {
		display: inline-flex;
		align-items: center;
		gap: 0.15rem;
		white-space: nowrap;
	}

	.field-error {
		font-size: 11px;
		color: var(--color-error);
	}

	.group-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		margin: 0 0 12px 0;
		line-height: 1.4;
	}

	.signature-fields {
		display: flex;
		flex-direction: column;
	}

	.mode-options {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem 1rem;
		margin-bottom: 12px;
	}

	.mode-options-radios {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 1rem;
	}

	.mode-options-badge-only {
		justify-content: flex-start;
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

	.mode-option input[type='radio'] {
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
		display: inline-flex;
		align-items: center;
		flex-shrink: 0;
		font-size: 11px;
		font-weight: 600;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-tertiary);
		color: var(--color-accent);
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
			align-items: stretch;
			gap: 0.5rem;
		}

		.mode-options-radios {
			flex-direction: column;
			align-items: flex-start;
		}

		.capture-badge {
			align-self: flex-start;
		}
	}
</style>
