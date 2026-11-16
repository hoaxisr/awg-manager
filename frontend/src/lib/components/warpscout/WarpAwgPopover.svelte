<script lang="ts">
	import { Shield, Sparkles, Square } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { signatureHostPresetOptions, type SignatureHostPreset } from '$lib/utils/protocols';
	import { Badge, Button, Dropdown } from '$lib/components/ui';
	import type { DropdownOption } from '$lib/components/ui';
	import type {
		WarpAWGParams,
		WarpFindJunkStatus,
		WarpObfuscationSource,
		WarpSignatureHostPreset,
	} from '$lib/types/warpscout';
	import WarpChipPopover from './WarpChipPopover.svelte';
	import {
		FIND_JUNK_PROFILES,
		LIMITS,
		SIGNATURE_PROFILES,
		awg3SummaryTag,
		awgVersionBadge,
		canApplyFindJunk,
		isAttemptAlreadyApplied,
		isUsableJunkAttempt,
		isValidContentPadding,
		isValidDomainListInput,
		resolveSelectedJunkAttempt,
		sanitizeInt,
		signatureProfileLabel,
		warpErrorMessage,
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

	let junkProfile = $state<string>('random');
	let junkHostPreset = $state<SignatureHostPreset>('default');
	let junkDomains = $state<string>('');
	let junkMaxAttempts = $state<number>(LIMITS.findJunkAttempts.def);
	let junkThresholdPct = $state<number>(LIMITS.findJunkThreshold.def);
	let startingJunk = $state(false);
	let stoppingJunk = $state(false);
	let applyingJunk = $state(false);
	let selectedAttempt = $state<number | null>(null);
	let cpFieldOpen = $state(false);
	let lastRunId = 0;

	$effect(() => {
		const rid = findJunk?.runId ?? 0;
		if (rid !== lastRunId) {
			lastRunId = rid;
			selectedAttempt = null;
		}
	});

	const junkRunning = $derived(findJunk?.running ?? false);
	const junkAttempts = $derived(findJunk?.attempts ?? []);
	const activeAttempt = $derived(resolveSelectedJunkAttempt(findJunk, selectedAttempt));
	const junkCanApply = $derived(canApplyFindJunk(findJunk, selectedAttempt));
	const selectedIsApplied = $derived(isAttemptAlreadyApplied(findJunk, activeAttempt));
	const cpValid = $derived(isValidContentPadding(awg.contentPadding));
	const customDomainValid = $derived(
		awg.signatureProfile === 'custom' ||
			awg.signatureProfile === 'none' ||
			awg.signatureHostPreset !== 'custom' ||
			isValidDomainListInput(awg.signatureDomain),
	);
	const junkCustomDomainsValid = $derived(
		junkProfile === 'none' || junkHostPreset !== 'custom' || isValidDomainListInput(junkDomains),
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

	const findJunkProfileOptions = $derived<DropdownOption[]>(
		FIND_JUNK_PROFILES.map((s) => ({
			value: s,
			label:
				s === 'random'
					? m.warpscout_findjunk_sig_random()
					: s === 'none'
						? m.warpscout_sig_none()
						: s,
		})),
	);

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

	async function startFindJunk(): Promise<void> {
		if (!junkCustomDomainsValid) {
			notifications.error(m.warpscout_awg_domain_invalid());
			return;
		}
		junkMaxAttempts = sanitizeInt(junkMaxAttempts, LIMITS.findJunkAttempts);
		junkThresholdPct = sanitizeInt(junkThresholdPct, LIMITS.findJunkThreshold);
		selectedAttempt = null;
		startingJunk = true;
		try {
			await api.warpscoutFindJunkStart({
				i1Profile: junkProfile,
				hostPreset: junkProfile !== 'none' && junkHostPreset !== 'default' ? junkHostPreset : undefined,
				domains: junkProfile !== 'none' && junkHostPreset === 'custom' ? junkDomains.trim() : undefined,
				maxAttempts: junkMaxAttempts,
				thresholdPct: junkThresholdPct,
				ifname: scanInterface.trim() || undefined,
			});
			await onfindjunkchanged?.();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			startingJunk = false;
		}
	}

	async function stopFindJunk(): Promise<void> {
		stoppingJunk = true;
		try {
			await api.warpscoutFindJunkStop();
			await onfindjunkchanged?.();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			stoppingJunk = false;
		}
	}

	async function applySelectedJunk(): Promise<void> {
		if (!cpValid) {
			notifications.error(m.warpscout_awg3_padding_invalid());
			return;
		}
		applyingJunk = true;
		try {
			const nextAwg = await api.warpscoutFindJunkApply({
				attempt: activeAttempt?.attempt,
				timingJitter: Boolean(awg.timingJitter),
				contentPadding: (awg.contentPadding ?? '').trim(),
				randomTrailers: Boolean(awg.randomTrailers),
				disableCookies: Boolean(awg.disableCookies),
			});
			await onfindjunkapplied?.(nextAwg);
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			applyingJunk = false;
		}
	}
</script>

<WarpChipPopover
	title={m.warpscout_awg_pop_title()}
	ariaLabel={m.warpscout_awg_pop_aria()}
	chipTitle={m.warpscout_awg_chip_tip()}
	width="min(31rem, calc(100vw - 2rem))"
>
	{#snippet chip()}
		<Shield size={13} class="chip-ic" />
		<span class="chip-dim hide-sm">{m.warpscout_awg_chip_label()}</span>
		<span class="mono chip-strong">{signatureProfileLabel(awg.signatureProfile)}</span>
		{#if awg.generatedDomain && showDomainPool}
			<span class="mono chip-dim">· {awg.generatedDomain}</span>
		{/if}
		<span class="mono chip-dim">· Jc {awg.jc} · {awg.jmin}–{awg.jmax}</span>
		{#if chipVerBadge}
			<span class="chip-awg3" title={chipAwg3Tag}>
				<Badge variant="info" size="xs">{chipVerBadge}</Badge>
			</span>
		{/if}
		{#if junkRunning}
			<span class="mono chip-dim">· {Math.max(1, findJunk?.attempt ?? 0)}/{findJunk?.maxAttempts ?? junkMaxAttempts}</span>
		{/if}
		{#if dirty}<span class="unsaved-dot" title={m.warpscout_unsaved_tip()}></span>{/if}
	{/snippet}

	{#snippet children(close)}
		<div class="awg-pop-body">
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

			<div class="findjunk-box">
				<div class="fj-head">
					<span class="fj-title">{m.warpscout_findjunk_title()}</span>
					{#if junkRunning}
						<Button size="sm" variant="danger" loading={stoppingJunk} onclick={stopFindJunk}>
							{#snippet iconBefore()}<Square size={12} />{/snippet}
							{m.common_stop()}
						</Button>
					{:else}
						<Button
							size="sm"
							variant="secondary"
							loading={startingJunk}
							disabled={!engineAvailable || !hasAccount || scanRunning || !junkCustomDomainsValid}
							onclick={startFindJunk}
						>
							{#snippet iconBefore()}<Sparkles size={12} />{/snippet}
							{m.warpscout_findjunk_start_btn()}
						</Button>
					{/if}
				</div>

				<p class="pop-hint">{m.warpscout_findjunk_hint()}</p>

				<div class="fj-controls">
					<div class="fj-prof">
						<Dropdown
							label={m.warpscout_findjunk_profile_label()}
							value={junkProfile}
							options={findJunkProfileOptions}
							fullWidth
							disabled={junkRunning}
							onchange={(v) => (junkProfile = v)}
						/>
					</div>
					<label class="num-field">
						<span>{m.warpscout_findjunk_attempts_label()}</span>
						<input
							class="field-input mono"
							type="number"
							min={LIMITS.findJunkAttempts.min}
							max={LIMITS.findJunkAttempts.max}
							bind:value={junkMaxAttempts}
							disabled={junkRunning}
						/>
					</label>
					<label class="num-field">
						<span>{m.warpscout_findjunk_threshold_label()}</span>
						<input
							class="field-input mono"
							type="number"
							min={LIMITS.findJunkThreshold.min}
							max={LIMITS.findJunkThreshold.max}
							bind:value={junkThresholdPct}
							disabled={junkRunning}
						/>
					</label>
				</div>

				{#if junkProfile !== 'none'}
					<div class="sig-domain-box">
						<Dropdown
							label={m.warpscout_findjunk_domain_preset_label()}
							value={junkHostPreset}
							options={domainPresetOptions}
							fullWidth
							disabled={junkRunning}
							onchange={(v) => (junkHostPreset = v as SignatureHostPreset)}
						/>
						{#if junkHostPreset === 'custom'}
							<input
								class="field-input mono"
								class:input-invalid={!junkCustomDomainsValid}
								type="text"
								placeholder={m.warpscout_awg_domain_custom_placeholder()}
								spellcheck="false"
								autocomplete="off"
								bind:value={junkDomains}
								disabled={junkRunning}
							/>
							{#if !junkCustomDomainsValid}
								<p class="fj-err">{m.warpscout_awg_domain_invalid()}</p>
							{/if}
						{/if}
					</div>
				{/if}

				{#if junkRunning || junkAttempts.length > 0 || findJunk?.error}
					{#if junkRunning}
						<div class="fj-status mono">
							{m.warpscout_findjunk_running({
								cur: Math.max(1, findJunk?.attempt ?? 1),
								max: findJunk?.maxAttempts ?? junkMaxAttempts,
							})}
						</div>
					{/if}

					{#if junkAttempts.length > 0}
						{#if !junkRunning && junkAttempts.length > 1}
							<p class="pop-hint">{m.warpscout_findjunk_select_hint()}</p>
						{/if}
						<div class="junk-attempts">
							{#each junkAttempts as att (att.attempt)}
								{@const usable = isUsableJunkAttempt(att)}
								{@const isBest = !att.incomplete && att.working > 0 && findJunk?.best?.attempt === att.attempt}
								{@const isSel = activeAttempt?.attempt === att.attempt}
								{@const isAttApplied = isAttemptAlreadyApplied(findJunk, att)}
								<button
									type="button"
									class="junk-row"
									class:best={isBest}
									class:selected={isSel}
									disabled={junkRunning || !usable}
									aria-pressed={isSel}
									onclick={() => {
										if (usable) selectedAttempt = att.attempt;
									}}
								>
									<span class="mono">#{att.attempt}</span>
									<span class="mono">{signatureProfileLabel(att.profile)}</span>
									{#if att.domain}
										<span class="mono fj-params">· {att.domain}</span>
									{/if}
									<span class="mono fj-params">Jc {att.awg.jc} ({att.awg.jmin}–{att.awg.jmax})</span>
									<span class="mono fj-score">{att.working}/{att.total} ({att.pct}%)</span>
									{#if att.incomplete}
										<Badge variant="warning" size="xs">{m.warpscout_findjunk_incomplete()}</Badge>
									{:else if isAttApplied}
										<Badge variant="success" size="xs">{m.warpscout_findjunk_applied_badge()}</Badge>
									{/if}
								</button>
							{/each}
						</div>
					{/if}

					{#if !junkRunning}
						{#if activeAttempt}
							{@const isBestSelected = findJunk?.best?.attempt === activeAttempt.attempt}
							<div class="fj-foot">
								<span class="fj-best mono">
									{#if isBestSelected}
										{m.warpscout_findjunk_best_label({
											attempt: activeAttempt.attempt,
											profile: signatureProfileLabel(activeAttempt.profile),
											jc: activeAttempt.awg.jc,
											jmin: activeAttempt.awg.jmin,
											jmax: activeAttempt.awg.jmax,
											working: activeAttempt.working,
											total: activeAttempt.total,
											pct: activeAttempt.pct,
										})}
									{:else}
										{m.warpscout_findjunk_selected_label({ attempt: activeAttempt.attempt })} ·
										{signatureProfileLabel(activeAttempt.profile)} ·
										Jc {activeAttempt.awg.jc} ({activeAttempt.awg.jmin}–{activeAttempt.awg.jmax}) ·
										{activeAttempt.working}/{activeAttempt.total} ({activeAttempt.pct}%)
									{/if}
								</span>
								{#if selectedIsApplied}
									<Badge variant="success" size="xs">{m.warpscout_findjunk_applied_badge()}</Badge>
								{:else}
									<Button
										size="sm"
										variant="secondary"
										loading={applyingJunk}
										disabled={!junkCanApply || !cpValid}
										onclick={applySelectedJunk}
									>
										{isBestSelected
											? m.warpscout_findjunk_apply_btn()
											: m.warpscout_findjunk_apply_selected_btn({ attempt: activeAttempt.attempt })}
									</Button>
								{/if}
							</div>
						{:else if junkAttempts.length > 0 && !findJunk?.error}
							<p class="fj-empty">{m.warpscout_findjunk_no_working()}</p>
						{/if}
					{/if}

					{#if findJunk?.error}
						<p class="fj-err">{findJunk.error}</p>
					{/if}
				{/if}
			</div>
		</div>

		<div class="pop-foot end">
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
					if (await onsave()) close();
				}}
			>
				{m.common_save()}
			</Button>
		</div>
	{/snippet}
</WarpChipPopover>

<style>
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
	.findjunk-box {
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
		padding: 0.75rem 0 0.1rem;
		border-top: 1px solid var(--color-border);
	}
	.fj-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 0.6rem;
	}
	.fj-title {
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.fj-controls {
		display: grid;
		grid-template-columns: minmax(0, 1.8fr) minmax(0, 0.75fr) minmax(0, 0.75fr);
		gap: 0.5rem;
		align-items: end;
	}
	.fj-prof {
		min-width: 0;
	}
	.fj-status {
		font-size: 0.78rem;
		color: var(--color-info);
	}
	.junk-attempts {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		max-height: 12.5rem;
		overflow-y: auto;
		padding: 0.45rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}
	.junk-row {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		width: 100%;
		padding: 0.34rem 0.5rem;
		border: 1px solid transparent;
		border-radius: var(--radius-sm);
		background: transparent;
		font: inherit;
		font-size: 0.8125rem;
		line-height: 1.35;
		color: var(--color-text-secondary);
		text-align: left;
		cursor: pointer;
		flex-wrap: wrap;
		transition: background 0.12s, border-color 0.12s;
	}
	.junk-row:hover:not(:disabled) {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.junk-row:disabled {
		opacity: 0.55;
		cursor: not-allowed;
	}
	.junk-row.best {
		background: var(--color-success-tint);
		color: var(--color-text-primary);
	}
	.junk-row.selected {
		border-color: var(--color-accent);
		background: color-mix(in srgb, var(--color-accent) 12%, transparent);
		color: var(--color-text-primary);
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
		padding: 0.5rem 0.6rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-bg-tertiary) 45%, transparent);
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
	.fj-params {
		color: var(--color-text-muted);
	}
	.fj-score {
		margin-left: auto;
		font-weight: 600;
		white-space: nowrap;
		flex-shrink: 0;
	}
	.fj-foot {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.55rem;
		padding-top: 0.15rem;
		flex-wrap: wrap;
	}
	.fj-best {
		font-size: 0.8125rem;
		line-height: 1.35;
		color: var(--color-text-primary);
	}
	.fj-empty {
		margin: 0;
		font-size: 0.78rem;
		color: var(--color-text-muted);
	}
	.fj-err {
		margin: 0;
		font-size: 0.78rem;
		color: var(--color-error);
	}
	.mono {
		font-family: var(--font-mono);
	}

	@media (max-width: 480px) {
		.fj-controls {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
		.fj-prof {
			grid-column: 1 / -1;
		}
	}
</style>
