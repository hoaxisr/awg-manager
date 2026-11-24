<script lang="ts" module>
	import type { SignatureHostPreset } from '$lib/utils/protocols';

	export interface FindJunkForm {
		profile: string;
		hostPreset: SignatureHostPreset;
		domains: string;
		maxAttempts: number;
		thresholdPct: number;
		selectedAttempt: number | null;
	}
</script>

<script lang="ts">
	import { Sparkles, Square } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { Badge, Button, Dropdown } from '$lib/components/ui';
	import type { DropdownOption } from '$lib/components/ui';
	import type { WarpAWGParams, WarpFindJunkStatus } from '$lib/types/warpscout';
	import {
		FIND_JUNK_PROFILES,
		LIMITS,
		canApplyFindJunk,
		isAttemptAlreadyApplied,
		isUsableJunkAttempt,
		isValidDomainListInput,
		resolveSelectedJunkAttempt,
		sanitizeInt,
		signatureProfileLabel,
		warpErrorMessage,
	} from './warpscoutUi';

	interface Props {
		/** Поля формы: живут у родителя, потому что панель размонтируется при закрытии. */
		form: FindJunkForm;
		awg: WarpAWGParams;
		findJunk: WarpFindJunkStatus | null;
		engineAvailable: boolean;
		hasAccount: boolean;
		scanRunning: boolean;
		scanInterface: string;
		cpValid: boolean;
		domainPresetOptions: DropdownOption[];
		onfindjunkapplied?: (awg: WarpAWGParams) => void | Promise<void>;
		onfindjunkchanged?: () => void | Promise<void>;
	}

	let {
		form = $bindable(),
		awg,
		findJunk,
		engineAvailable,
		hasAccount,
		scanRunning,
		scanInterface,
		cpValid,
		domainPresetOptions,
		onfindjunkapplied,
		onfindjunkchanged,
	}: Props = $props();

	let startingJunk = $state(false);
	let stoppingJunk = $state(false);
	let applyingJunk = $state(false);

	const junkRunning = $derived(findJunk?.running ?? false);
	const junkAttempts = $derived(findJunk?.attempts ?? []);
	const activeAttempt = $derived(resolveSelectedJunkAttempt(findJunk, form.selectedAttempt));
	const junkCanApply = $derived(canApplyFindJunk(findJunk, form.selectedAttempt));
	const selectedIsApplied = $derived(isAttemptAlreadyApplied(findJunk, activeAttempt));
	const junkCustomDomainsValid = $derived(
		form.profile === 'none' || form.hostPreset !== 'custom' || isValidDomainListInput(form.domains),
	);

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

	async function startFindJunk(): Promise<void> {
		if (!junkCustomDomainsValid) {
			notifications.error(m.warpscout_awg_domain_invalid());
			return;
		}
		form.maxAttempts = sanitizeInt(form.maxAttempts, LIMITS.findJunkAttempts);
		form.thresholdPct = sanitizeInt(form.thresholdPct, LIMITS.findJunkThreshold);
		form.selectedAttempt = null;
		startingJunk = true;
		try {
			await api.warpscoutFindJunkStart({
				i1Profile: form.profile,
				hostPreset: form.profile !== 'none' && form.hostPreset !== 'default' ? form.hostPreset : undefined,
				domains: form.profile !== 'none' && form.hostPreset === 'custom' ? form.domains.trim() : undefined,
				maxAttempts: form.maxAttempts,
				thresholdPct: form.thresholdPct,
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
				value={form.profile}
				options={findJunkProfileOptions}
				fullWidth
				disabled={junkRunning}
				onchange={(v) => (form.profile = v)}
			/>
		</div>
		<label class="num-field">
			<span>{m.warpscout_findjunk_attempts_label()}</span>
			<input
				class="field-input mono"
				type="number"
				min={LIMITS.findJunkAttempts.min}
				max={LIMITS.findJunkAttempts.max}
				bind:value={form.maxAttempts}
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
				bind:value={form.thresholdPct}
				disabled={junkRunning}
			/>
		</label>
	</div>

	{#if form.profile !== 'none'}
		<div class="sig-domain-box">
			<Dropdown
				label={m.warpscout_findjunk_domain_preset_label()}
				value={form.hostPreset}
				options={domainPresetOptions}
				fullWidth
				disabled={junkRunning}
				onchange={(v) => (form.hostPreset = v as SignatureHostPreset)}
			/>
			{#if form.hostPreset === 'custom'}
				<input
					class="field-input mono"
					class:input-invalid={!junkCustomDomainsValid}
					type="text"
					placeholder={m.warpscout_awg_domain_custom_placeholder()}
					spellcheck="false"
					autocomplete="off"
					bind:value={form.domains}
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
					max: findJunk?.maxAttempts ?? form.maxAttempts,
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
							if (usable) form.selectedAttempt = att.attempt;
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

<style>
	.findjunk-box {
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
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
	.num-field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		font-size: 0.78rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.sig-domain-box {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		padding: 0.65rem 0 0;
		border-radius: 0;
		border: 0;
		border-top: 1px solid var(--color-border);
		background: transparent;
	}
	.input-invalid {
		border-color: var(--color-error);
	}
	.fj-err {
		margin: 0;
		font-size: 0.78rem;
		color: var(--color-error);
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
