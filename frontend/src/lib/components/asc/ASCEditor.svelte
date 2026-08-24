<script lang="ts">
	import { protocols, getSignaturePackets, calcByteSize, type ProtocolKey, type SignaturePackets } from '$lib/utils/protocols';
	import { api } from '$lib/api/client';
	import type { ASCParams, ASCParamsExtended } from '$lib/types';
	import { isExtendedASCParams } from '$lib/utils/asc-validation';
	import { AWG_PARAM_HINTS } from '$lib/utils/awgParamHints';
	import { notifications } from '$lib/stores/notifications';
	import { SettingsSectionLabel } from '$lib/components/settings';
	import { Badge, Button, Dropdown, FieldHint, type DropdownOption } from '$lib/components/ui';
	import { Fingerprint, Hash, MoveHorizontal, Shredder, ShieldCheck, Shuffle } from 'lucide-svelte';

	const MAX_SIGNATURE_BYTES = 4096;

	type GenerateMode = 'protocol' | 'domain';
	type SignatureModes = 'both' | 'domain';

	type ASCErrorFields = Partial<Record<keyof ASCParamsExtended, string[]>>;

	let {
		params = $bindable(),
		extended = undefined,
		awg3 = false,
		awg3Limited = false,
		mtu = 1280,
		errors = {},
		hints = AWG_PARAM_HINTS,
		signatureModes = 'both',
		idPrefix = '',
		compact = false,
	}: {
		params: ASCParams;
		extended?: boolean;
		awg3?: boolean;
		// NativeWG (awg_proxy) can only do header protection + random trailers —
		// not the kernel-only timers / content padding. Hide those when true.
		awg3Limited?: boolean;
		mtu?: number;
		errors?: ASCErrorFields;
		hints?: Record<string, string>;
		signatureModes?: SignatureModes;
		idPrefix?: string;
		compact?: boolean;
	} = $props();

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
	const AWG31_FLAGS: { key: 'randomTrailers' | 'disableCookies'; label: string; note: string }[] = [
		{
			key: 'randomTrailers',
			label: 'RandomTrailers',
			note: 'Добивает пакеты случайным числом лишних байт. Согласования по проводу нет: параметр обязан стоять на обоих концах, иначе туннель молча не поднимется.',
		},
		{
			key: 'disableCookies',
			label: 'DisableCookies',
			note: 'Не отвечать служебным пакетом-подтверждением под нагрузкой. Односторонний, совместимость не ломает.',
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
	let captureError = $state('');
	let captureSource = $state('');

	let totalBytes = $derived.by(() => {
		if (!showExtended) return 0;
		const ext = params as ASCParamsExtended;
		return (
			calcByteSize(String(ext.i1 || '')) +
			calcByteSize(String(ext.i2 || '')) +
			calcByteSize(String(ext.i3 || '')) +
			calcByteSize(String(ext.i4 || '')) +
			calcByteSize(String(ext.i5 || ''))
		);
	});

	let overLimit = $derived(totalBytes > MAX_SIGNATURE_BYTES);

	function fieldId(name: string): string {
		return `${idPrefix}${name}`;
	}

	function generationErrorMessage(e: unknown): string {
		const msg = e instanceof Error ? e.message : String(e);
		if (/getRandomValues|crypto/i.test(msg)) {
			return 'Генерация недоступна: откройте интерфейс по HTTPS или через localhost';
		}
		return msg || 'Ошибка генерации пакетов';
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

	function handleGenerate() {
		if (!showExtended) {
			notifications.error('Signature-пакеты (I1–I5) недоступны на этом устройстве');
			return;
		}

		generating = true;
		try {
			const packets = getSignaturePackets(selectedProtocol, mtu);
			const size =
				calcByteSize(packets.i1) +
				calcByteSize(packets.i2) +
				calcByteSize(packets.i3) +
				calcByteSize(packets.i4) +
				calcByteSize(packets.i5);
			if (size > MAX_SIGNATURE_BYTES) {
				notifications.error(
					`Суммарный размер (${size} байт) превышает лимит ${MAX_SIGNATURE_BYTES}`,
				);
				return;
			}

			applySignaturePackets(packets);
			const protoName = protocols[selectedProtocol]?.name ?? selectedProtocol;
			notifications.success(`Signature-пакеты сгенерированы (${protoName})`);
		} catch (e: unknown) {
			notifications.error(generationErrorMessage(e));
		} finally {
			generating = false;
		}
	}

	async function handleCapture() {
		if (!showExtended) {
			notifications.error('Signature-пакеты (I1–I5) недоступны на этом устройстве');
			return;
		}
		if (!domainInput.trim()) return;

		capturing = true;
		captureError = '';
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
				notifications.success('Signature-пакеты захвачены');
			}
		} catch (e: unknown) {
			captureError = e instanceof Error ? e.message : 'Ошибка захвата';
			notifications.error(captureError);
		} finally {
			capturing = false;
		}
	}
</script>

{#snippet paramLabel(id: string, name: string)}
	<label class="field-label param-field-label" for={fieldId(id)}>
		{name}
		{#if hints[id]}
			<FieldHint text={hints[id]} ariaLabel={`Подсказка: ${name}`} />
		{/if}
	</label>
{/snippet}

<div class="asc-editor" class:compact>
	<section class="card param-section">
		<SettingsSectionLabel label="Junk пакеты" icon={Shredder} tone="orange" header />
		<p class="group-desc">Фейковые пакеты перед handshake — ломают анализ трафика DPI</p>
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
		<p class="group-desc">Дополнительные байты в handshake — меняют размер пакетов WireGuard</p>
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
		<SettingsSectionLabel label="Заголовки (H1-H4)" icon={Hash} tone="indigo" header />
		<p class="group-desc">Подмена типов пакетов WireGuard на произвольные значения</p>
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

	{#if showExtended}
		{@const ext = params as ASCParamsExtended}
		<section class="card param-section">
			<SettingsSectionLabel label="Signature пакеты (I1-I5)" icon={Fingerprint} tone="green" header />
			<p class="group-desc">Имитация протоколов — DPI видит знакомый трафик вместо WireGuard</p>

			{#if signatureModes === 'both'}
				<div class="mode-options">
					<div class="mode-options-radios">
						<label class="mode-option">
							<input type="radio" value="protocol" bind:group={generateMode} />
							<span>Протокол</span>
						</label>
						<label class="mode-option">
							<input type="radio" value="domain" bind:group={generateMode} />
							<span>По домену</span>
						</label>
					</div>
					{#if captureSource && !captureError}
						<span class="capture-badge">{captureSource.toUpperCase()}</span>
					{/if}
				</div>
			{:else if captureSource && !captureError}
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
						{capturing ? 'Захват...' : 'Захватить'}
					</Button>
				</div>
				{#if captureError}
					<p class="capture-info" class:capture-warning={!!captureSource}>{captureError}</p>
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
						{generating ? 'Генерация...' : 'Сгенерировать'}
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
							placeholder={field.toUpperCase() + (idx === 0 ? ' (обязательный)' : '')}
						/>
						{#if errors[field as keyof ASCParamsExtended]}
							<p class="field-error">{errors[field as keyof ASCParamsExtended]}</p>
						{/if}
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
	{/if}

	{#if awg3}
		{@const ext = params as ASCParamsExtended}
		<section class="card param-section">
			<SettingsSectionLabel label="AmneziaWG 3.0" icon={ShieldCheck} tone="purple" header />
			<p class="group-desc">
				{#if awg3Limited}
					Через NativeWG (awg_proxy) доступны защита заголовков и RandomTrailers (ниже).
					Таймеры и content-padding работают лишь в режиме kernel.
				{:else}
					Параметры ядра AWG 3.0 (только режим kernel). Таймеры — число или диапазон
					<code>min-max</code> в секундах; пусто = значение по умолчанию.
				{/if}
			</p>

			<div class="form-group">
				{@render paramLabel('headerProtectionKey', 'HeaderProtectionKey')}
				<input
					type="text"
					id={fieldId('headerProtectionKey')}
					class="field-input"
					bind:value={ext.headerProtectionKey}
					placeholder="base64-ключ шифрования заголовков (необязательно)"
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
							placeholder="напр. 120 или 120-150"
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
				Параметры версии 3.1 включены в конфигурационном файле туннеля. Отсюда их не
				поменять: они требуют модуля ядра 3.1 и на этом роутере, и на стороне сервера,
				а снятие RandomTrailers на одной стороне рвёт туннель.
			</p>

			<div class="toggle-stack">
				{#each ext31Flags as f}
					<div>
						<div class="flag-row">
							<Badge variant="purple" size="xs" mono>{f.label}</Badge>
							<span class="flag-state">включён</span>
						</div>
						<p class="toggle-note">{f.note}</p>
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
