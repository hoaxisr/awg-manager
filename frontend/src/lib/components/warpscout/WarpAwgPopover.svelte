<script lang="ts">
	import { Shield } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { signatureHostPresetOptions } from '$lib/utils/protocols';
	import { Badge, Button, Dropdown, SegmentedControl, SideDrawer } from '$lib/components/ui';
	import type { DropdownOption, SegmentedOption } from '$lib/components/ui';
	import WarpChipButton from './WarpChipButton.svelte';
	import WarpFindJunkPanel from './WarpFindJunkPanel.svelte';
	import type {
		WarpAWGParams,
		WarpFindJunkStatus,
		WarpObfuscationSource,
		WarpSignatureHostPreset,
	} from '$lib/types/warpscout';
	import {
		LIMITS,
		SIGNATURE_PROFILES,
		awg3SummaryTag,
		awgVersionBadge,
		isValidContentPadding,
		isValidDomainListInput,
		signatureProfileLabel,
	} from './warpscoutUi';

	interface Props {
		awg: WarpAWGParams;
		obfuscationSources?: WarpObfuscationSource[];
		dirty: boolean;
		saving: boolean;
		findJunk?: WarpFindJunkStatus | null;
		engineAvailable?: boolean;
		hasAccount?: boolean;
		scanRunning?: boolean;
		scanInterface?: string;
		supportsAsc3?: boolean;
		supportsKernelAwg31?: boolean;
		/** Сохранить параметры AWG; true — сохранено, поповер можно закрыть. */
		onsave: () => Promise<boolean>;
		onreset: () => void;
		onfindjunkapplied?: (awg: WarpAWGParams) => void | Promise<void>;
		onfindjunkchanged?: () => void | Promise<void>;
	}

	let {
		awg = $bindable(),
		obfuscationSources = [],
		dirty,
		saving,
		findJunk = null,
		engineAvailable = false,
		hasAccount = false,
		scanRunning = false,
		scanInterface = '',
		supportsAsc3 = false,
		supportsKernelAwg31 = false,
		onsave,
		onreset,
		onfindjunkapplied,
		onfindjunkchanged,
	}: Props = $props();

	let open = $state(false);
	let chipEl = $state<HTMLButtonElement | null>(null);
	let awgTab = $state<'params' | 'findjunk'>('params');

	const awgTabOptions = $derived<SegmentedOption<'params' | 'findjunk'>[]>([
		{ value: 'params', label: m.warpscout_awg_tab_params() },
		{ value: 'findjunk', label: m.warpscout_awg_tab_findjunk() },
	]);

	function closeDrawer(): void {
		open = false;
		chipEl?.focus();
	}

	let cpFieldOpen = $state(false);
	const junkRunning = $derived(findJunk?.running ?? false);
	// Не $state: хранит предыдущее значение для детекции фронта false -> true без повторного запуска $effect.
	let prevJunkRunning = false;
	$effect(() => {
		if (junkRunning && !prevJunkRunning) awgTab = 'findjunk';
		prevJunkRunning = junkRunning;
	});
	const cpValid = $derived(isValidContentPadding(awg.contentPadding));
	const customDomainValid = $derived(
		awg.signatureProfile === 'custom' ||
			awg.signatureProfile === 'none' ||
			awg.signatureHostPreset !== 'custom' ||
			isValidDomainListInput(awg.signatureDomain),
	);
	const chipVerBadge = $derived(awgVersionBadge(awg));
	const chipAwg3Tag = $derived(awg3SummaryTag(awg));
	const showDomainPool = $derived(awg.signatureProfile !== 'custom' && awg.signatureProfile !== 'none');

	const domainPresetOptions = $derived<DropdownOption[]>(
		signatureHostPresetOptions().map((o) => ({
			value: o.value,
			label: o.label,
			description: o.description,
		})),
	);

	const signatureOptions = $derived.by<DropdownOption[]>(() => {
		const opts: DropdownOption[] = SIGNATURE_PROFILES.map((s) => ({
			value: s,
			label: s === 'none' ? m.warpscout_sig_none() : s,
		}));
		if (awg.signatureProfile === 'custom') {
			opts.unshift({ value: 'custom', label: m.warpscout_sig_custom() });
		}
		return opts;
	});

	const copySourceOptions = $derived<DropdownOption[]>([
		{ value: '', label: m.warpscout_awg_copy_placeholder() },
		...obfuscationSources.map((s) => {
			const tag = awg3SummaryTag(s.awg);
			return {
				value: `${s.mode}:${s.id}`,
				label: `${s.name}${s.iface ? ` (${s.iface})` : ''} · Jc ${s.awg.jc}${tag ? ` · ${tag}` : ''}`,
			};
		}),
	]);

	// Список — действие «скопировать», а не выбор: после копирования он
	// возвращается к заглушке, иначе тот же туннель нельзя выбрать повторно
	// (Dropdown зовёт onchange только при смене значения).
	let copySource = $state('');

	function applyObfSource(key: string): void {
		if (!key) return;
		const src = obfuscationSources.find((s) => `${s.mode}:${s.id}` === key);
		if (src) {
			awg = { ...src.awg };
			cpFieldOpen = Boolean(src.awg.contentPadding);
		}
		copySource = '';
	}

	function changeSignatureProfile(v: string): void {
		const next = { ...awg, signatureProfile: v };
		if (v !== 'custom') {
			// Ключи убираются, а не обнуляются: сервер пустые I1..I5 не
			// присылает, и с пустыми строками черновик не сравнялся бы с
			// сохранённой конфигурацией («не сохранено» без изменений).
			delete next.i1;
			delete next.i2;
			delete next.i3;
			delete next.i4;
			delete next.i5;
		}
		if (v === 'custom' || v === 'none') {
			delete next.signatureHostPreset;
			delete next.signatureDomain;
		}
		awg = next;
	}

	function changeSignatureHostPreset(v: string): void {
		const preset = v as WarpSignatureHostPreset;
		const next = { ...awg };
		if (preset === 'default') {
			delete next.signatureHostPreset;
			delete next.signatureDomain;
		} else {
			next.signatureHostPreset = preset;
			if (preset !== 'custom') delete next.signatureDomain;
		}
		awg = next;
	}

	function onSignatureDomainInput(val: string): void {
		awg = { ...awg, signatureDomain: val };
	}

	function setTimingJitter(checked: boolean): void {
		const next = { ...awg };
		if (checked) next.timingJitter = true;
		else delete next.timingJitter;
		awg = next;
	}

	function toggleContentPadding(checked: boolean): void {
		cpFieldOpen = checked;
		const next = { ...awg };
		if (checked) next.contentPadding = (next.contentPadding ?? '').trim() || '10-100';
		else delete next.contentPadding;
		awg = next;
	}

	function onContentPaddingInput(val: string): void {
		if (val.trim()) cpFieldOpen = true;
		awg = { ...awg, contentPadding: val };
	}

	function setDisableCookies(checked: boolean): void {
		const next = { ...awg };
		if (checked) next.disableCookies = true;
		else delete next.disableCookies;
		awg = next;
	}

	function setRandomTrailers(checked: boolean): void {
		const next = { ...awg };
		if (checked) next.randomTrailers = true;
		else delete next.randomTrailers;
		awg = next;
	}
</script>

<WarpChipButton bind:buttonEl={chipEl} {open} title={m.warpscout_awg_chip_tip()} onclick={() => (open = !open)}>
	<Shield size={13} class="chip-ic" />
	<span class="chip-dim hide-sm">{m.warpscout_awg_chip_label()}</span>
	<span class="mono chip-strong">{signatureProfileLabel(awg.signatureProfile)}</span>
	{#if awg.generatedDomain && showDomainPool}
		<span class="mono chip-dim hide-sm">· {awg.generatedDomain}</span>
	{/if}
	<span class="mono chip-dim">· Jc {awg.jc} · {awg.jmin}–{awg.jmax}</span>
	{#if chipVerBadge}
		<span class="chip-awg3" title={chipAwg3Tag}>
			<Badge variant="info" size="xs">{chipVerBadge}</Badge>
		</span>
	{/if}
	{#if junkRunning}
		<span class="mono chip-dim">· {Math.max(1, findJunk?.attempt ?? 0)}/{findJunk?.maxAttempts ?? LIMITS.findJunkAttempts.def}</span>
	{/if}
	{#if dirty}<span class="unsaved-dot" title={m.warpscout_unsaved_tip()}></span>{/if}
</WarpChipButton>

<SideDrawer {open} title={m.warpscout_awg_pop_title()} width={460} onClose={closeDrawer}>
	<div class="awg-drawer-content">
		<div class="awg-drawer-tabs">
			<SegmentedControl
				value={awgTab}
				options={awgTabOptions}
				ariaLabel={m.warpscout_awg_tabs_aria()}
				onchange={(v) => (awgTab = v)}
			/>
		</div>

		<div class="awg-pop-body" class:tab-hidden={awgTab !== 'params'}>
			{#if obfuscationSources.length > 0}
				<Dropdown
					label={m.warpscout_awg_copy_label()}
					bind:value={copySource}
					options={copySourceOptions}
					fullWidth
					onchange={applyObfSource}
				/>
			{/if}

			<Dropdown
				label={m.warpscout_awg_sig_label()}
				value={awg.signatureProfile}
				options={signatureOptions}
				fullWidth
				onchange={changeSignatureProfile}
			/>

			{#if showDomainPool}
				<div class="sig-domain-box">
					<Dropdown
						label={m.warpscout_awg_domain_label()}
						value={awg.signatureHostPreset ?? 'default'}
						options={domainPresetOptions}
						fullWidth
						onchange={changeSignatureHostPreset}
					/>
					{#if awg.signatureHostPreset === 'custom'}
						<input
							class="field-input mono"
							class:input-invalid={!customDomainValid}
							type="text"
							placeholder={m.warpscout_awg_domain_custom_placeholder()}
							spellcheck="false"
							autocomplete="off"
							value={awg.signatureDomain ?? ''}
							oninput={(e) => onSignatureDomainInput((e.currentTarget as HTMLInputElement).value)}
						/>
						<p class="pop-hint">{m.warpscout_awg_domain_custom_hint()}</p>
						{#if !customDomainValid}
							<p class="fj-err">{m.warpscout_awg_domain_invalid()}</p>
						{/if}
					{/if}
					{#if awg.generatedDomain}
						<p class="pop-hint mono">{m.warpscout_awg_domain_generated({ domain: awg.generatedDomain })}</p>
					{/if}
				</div>
			{/if}

			<div class="awg-nums">
				<label class="num-field">
					<span>Jc</span>
					<input class="field-input mono" type="number" min={LIMITS.jc.min} max={LIMITS.jc.max} bind:value={awg.jc} />
				</label>
				<label class="num-field">
					<span>Jmin</span>
					<input class="field-input mono" type="number" min={LIMITS.jmin.min} max={LIMITS.jmin.max} bind:value={awg.jmin} />
				</label>
				<label class="num-field">
					<span>Jmax</span>
					<input class="field-input mono" type="number" min={LIMITS.jmax.min} max={LIMITS.jmax.max} bind:value={awg.jmax} />
				</label>
			</div>

			<div class="awg3-box">
				<div class="awg3-title">{m.warpscout_awg3_section()}</div>
				<label class="awg3-check">
					<input
						type="checkbox"
						checked={!!awg.timingJitter}
						onchange={(e) => setTimingJitter((e.currentTarget as HTMLInputElement).checked)}
					/>
					<span>{m.warpscout_awg3_jitter_label()}</span>
				</label>
				<div class="awg3-pad-row">
					<label class="awg3-check">
						<input
							type="checkbox"
							checked={cpFieldOpen || !!(awg.contentPadding ?? '').trim()}
							onchange={(e) => toggleContentPadding((e.currentTarget as HTMLInputElement).checked)}
						/>
						<span>{m.warpscout_awg3_padding_label()}</span>
					</label>
					{#if cpFieldOpen || !!(awg.contentPadding ?? '').trim()}
						<input
							class="field-input awg3-pad-input mono"
							class:input-invalid={!cpValid}
							type="text"
							maxlength={11}
							placeholder={m.warpscout_awg3_padding_placeholder()}
							spellcheck="false"
							autocomplete="off"
							value={awg.contentPadding ?? ''}
							oninput={(e) => onContentPaddingInput((e.currentTarget as HTMLInputElement).value)}
						/>
					{/if}
				</div>
				{#if !cpValid}
					<p class="fj-err">{m.warpscout_awg3_padding_invalid()}</p>
				{/if}
				{#if !supportsAsc3 && (awg.timingJitter || !!(awg.contentPadding ?? '').trim())}
					<p class="pop-hint">{m.warpscout_awg3_fw_hint()}</p>
				{/if}
			</div>

			<div class="awg3-box">
				<div class="awg3-title">{m.warpscout_awg31_section()}</div>
				<label class="awg3-check">
					<input
						type="checkbox"
						checked={!!awg.disableCookies}
						onchange={(e) => setDisableCookies((e.currentTarget as HTMLInputElement).checked)}
					/>
					<span>{m.warpscout_awg31_cookies_label()}</span>
				</label>
				<label class="awg3-check">
					<input
						type="checkbox"
						checked={!!awg.randomTrailers}
						onchange={(e) => setRandomTrailers((e.currentTarget as HTMLInputElement).checked)}
					/>
					<span>{m.warpscout_awg31_trailers_label()}</span>
				</label>
				{#if awg.randomTrailers}
					<p class="fj-warn">{m.warpscout_awg31_warp_warn()}</p>
				{/if}
				{#if (awg.randomTrailers || awg.disableCookies) && (!supportsAsc3 || !supportsKernelAwg31)}
					<p class="pop-hint">
						{#if !supportsAsc3 && !supportsKernelAwg31}
							{m.warpscout_awg31_compat_none()}
						{:else if supportsKernelAwg31}
							{m.warpscout_awg31_compat_kernel_only()}
						{:else}
							{m.warpscout_awg31_compat_native_only()}
						{/if}
					</p>
				{/if}
			</div>

			<p class="pop-hint">
				{awg.signatureProfile === 'custom' ? m.warpscout_awg_custom_hint() : m.warpscout_awg_hint()}
			</p>
		</div>

		<div class:tab-hidden={awgTab !== 'findjunk'}>
			<WarpFindJunkPanel
				{awg}
				{findJunk}
				{engineAvailable}
				{hasAccount}
				{scanRunning}
				{scanInterface}
				{cpValid}
				{domainPresetOptions}
				{onfindjunkapplied}
				{onfindjunkchanged}
			/>
		</div>
	</div>

	{#snippet footer()}
		<div class="awg-drawer-footer">
			<span class="awg-sync-status" class:is-dirty={dirty}>
				{dirty ? m.warpscout_awg_unsaved() : m.warpscout_awg_synced()}
			</span>
			<div class="awg-footer-actions">
				{#if dirty}
					<Button size="sm" variant="ghost" onclick={onreset} disabled={saving}>
						{m.warpscout_reset_btn()}
					</Button>
				{/if}
				<Button
					size="sm"
					variant="primary"
					loading={saving}
					disabled={!dirty || !cpValid || !customDomainValid}
					onclick={async () => {
						if (await onsave()) closeDrawer();
					}}
				>
					{m.common_save()}
				</Button>
			</div>
		</div>
	{/snippet}
</SideDrawer>

<style>
	.awg-drawer-content {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		min-width: 0;
		max-width: 100%;
		overflow-x: hidden;
	}
	.awg-drawer-tabs {
		display: flex;
		align-items: center;
	}
	.tab-hidden {
		display: none !important;
	}
	.awg-pop-body {
		display: flex;
		flex-direction: column;
		gap: 0.55rem;
	}
	.awg-nums {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 0.45rem;
	}
	.num-field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		font-size: 0.78rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.chip-awg3 {
		display: inline-flex;
		align-items: center;
	}
	.sig-domain-box,
	.awg3-box {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		padding: 0.65rem 0 0;
		border-radius: 0;
		border: 0;
		border-top: 1px solid var(--color-border);
		background: transparent;
	}
	.awg3-title {
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.awg3-check {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		font-size: 0.8rem;
		color: var(--color-text-primary);
		cursor: pointer;
	}
	.awg3-pad-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
	.awg3-pad-input {
		width: 7rem;
	}
	.input-invalid {
		border-color: var(--color-error);
	}
	.fj-warn {
		margin: 0;
		font-size: 0.76rem;
		line-height: 1.35;
		color: var(--color-warning);
	}
	.fj-err {
		margin: 0;
		font-size: 0.78rem;
		color: var(--color-error);
	}
	.awg-drawer-footer {
		width: 100%;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.65rem;
	}
	.awg-sync-status {
		font-size: 0.74rem;
		color: var(--color-text-muted);
	}
	.awg-sync-status.is-dirty {
		color: var(--color-warning);
		font-weight: 500;
	}
	.awg-footer-actions {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		margin-left: auto;
	}
	.mono {
		font-family: var(--font-mono);
	}

	@media (max-width: 768px) {
		.awg-drawer-footer {
			flex-direction: column;
			align-items: stretch;
		}
		.awg-footer-actions {
			margin-left: 0;
			justify-content: flex-end;
		}
	}
</style>
